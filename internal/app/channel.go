package app

// M1 WP1.1/WP1.2：渠道数据模型与模型组轮询器。
// Channel = 一个上游凭据；ModelGroup = 多个 Channel 组成一个对外模型名。
// 熔断器在 WP1.3 接入 ReportResult 的失败计数。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Channel 上游渠道：一个可独立调用的凭据与模型映射。
type Channel struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Provider string            `json:"provider"` // provider.Registry 中的名字
	APIKey   string            `json:"api_key"`
	BaseURL  string            `json:"base_url,omitempty"`
	Weight   int               `json:"weight"`               // weighted 策略，<=0 视为 1
	ModelMap map[string]string `json:"model_map,omitempty"`  // 对外模型名 → 上游模型名
	Disabled bool              `json:"disabled,omitempty"`
}

// channelRuntime 运行态，不落盘。
type channelRuntime struct {
	inflight       atomic.Int64
	totalRequests  atomic.Int64
	totalLatencyMS atomic.Int64
	totalFails     atomic.Int64 // WP1.3 熔断计数
}

// 熔断参数（WP1.3）：连续失败阈值 + 指数退避冷却 + 探测成功复位。
const (
	breakerAllowedFails = 3
	breakerBaseCooldown = 5 * time.Second
	breakerMaxCooldown  = 10 * time.Minute
)

// breaker 单渠道熔断状态。
type breaker struct {
	fails        int       // 当前窗口连续失败数
	openUntil    time.Time // >now 表示熔断中
	openCount    int       // 冷却指数退避基数（探测成功归零）
}

// open 进入熔断：冷却 = base << openCount，封顶 max。
func (br *breaker) open(now time.Time) time.Time {
	cooldown := breakerBaseCooldown << min(br.openCount, 20)
	cooldown = min(cooldown, breakerMaxCooldown)
	br.openUntil = now.Add(cooldown)
	br.openCount++
	br.fails = 0
	return br.openUntil
}

// available 是否可被 Pick。
func (br *breaker) available(now time.Time) bool {
	return now.After(br.openUntil) || br.openUntil.IsZero()
}

func (rt *channelRuntime) avgLatencyMS() float64 {
	n := rt.totalRequests.Load()
	if n == 0 {
		return 0
	}
	return float64(rt.totalLatencyMS.Load()) / float64(n)
}

// ModelGroup 对外模型组：一个组名（对外即模型名）挂多个渠道成员。
type ModelGroup struct {
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Strategy string   `json:"strategy"` // round_robin|weighted|least_used|lowest_latency
}

// stickyEntry 会话粘性记录。
type stickyEntry struct {
	channelID string
	until     time.Time
}

// stickyTTL 会话粘性时长。
const stickyTTL = 60 * time.Minute

// errNoChannel 组内无可用渠道。
var errNoChannel = errors.New("no available channel in group")

// Balancer 渠道组轮询器（并发安全）。
type Balancer struct {
	mu       sync.Mutex
	channels map[string]*Channel
	groups   map[string]*ModelGroup
	rt       map[string]*channelRuntime
	breakers map[string]*breaker
	rrIdx    map[string]int
	sticky   map[string]stickyEntry
}

// NewBalancer 创建空轮询器。
func NewBalancer() *Balancer {
	return &Balancer{
		channels: map[string]*Channel{},
		groups:   map[string]*ModelGroup{},
		rt:       map[string]*channelRuntime{},
		breakers: map[string]*breaker{},
		rrIdx:    map[string]int{},
		sticky:   map[string]stickyEntry{},
	}
}

// UpsertChannel 注册或更新渠道。
func (b *Balancer) UpsertChannel(c Channel) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := c
	b.channels[c.ID] = &cp
	if _, ok := b.rt[c.ID]; !ok {
		b.rt[c.ID] = &channelRuntime{}
	}
}

// UpsertGroup 注册或更新模型组。
func (b *Balancer) UpsertGroup(g ModelGroup) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := g
	b.groups[g.Name] = &cp
	if _, ok := b.rrIdx[g.Name]; !ok {
		b.rrIdx[g.Name] = 0
	}
}

// RemoveChannel 移除渠道并从所有组摘除、清理粘性与熔断状态。
func (b *Balancer) RemoveChannel(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.channels, id)
	delete(b.rt, id)
	delete(b.breakers, id)
	for _, g := range b.groups {
		g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return m == id })
	}
	for k, v := range b.sticky {
		if v.channelID == id {
			delete(b.sticky, k)
		}
	}
}

// Pick 按组策略选择渠道；sessionKey 非空时启用 60min 会话粘性。
func (b *Balancer) Pick(groupName, sessionKey string) (*Channel, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[groupName]
	if !ok || len(g.Members) == 0 {
		return nil, fmt.Errorf("%w %q", errNoChannel, groupName)
	}
	now := time.Now()
	for k, v := range b.sticky {
		if now.After(v.until) {
			delete(b.sticky, k)
		}
	}
	if sessionKey != "" {
		if s, ok := b.sticky[sessionKey]; ok {
			if slices.Contains(b.aliveMembers(g), s.channelID) {
				return b.channels[s.channelID], nil
			}
			delete(b.sticky, sessionKey)
		}
	}
	var picked *Channel
	switch g.Strategy {
	case "weighted":
		picked = b.pickWeighted(g)
	case "least_used":
		picked = b.pickLeastUsed(g)
	case "lowest_latency":
		picked = b.pickLowestLatency(g)
	default: // round_robin 及未知策略兜底
		picked = b.pickRoundRobin(g)
	}
	if picked == nil {
		return nil, fmt.Errorf("%w %q", errNoChannel, groupName)
	}
	if sessionKey != "" {
		b.sticky[sessionKey] = stickyEntry{channelID: picked.ID, until: now.Add(stickyTTL)}
	}
	return picked, nil
}

// usableMember 返回成员中可用（存在、启用）的渠道。
func (b *Balancer) usableMember(g *ModelGroup, id string) *Channel {
	if !slices.Contains(g.Members, id) {
		return nil
	}
	c := b.channels[id]
	if c == nil || c.Disabled {
		return nil
	}
	return c
}

// aliveMembers 过滤出可用成员 ID（存在、启用、未熔断）。
func (b *Balancer) aliveMembers(g *ModelGroup) []string {
	now := time.Now()
	out := make([]string, 0, len(g.Members))
	for _, id := range g.Members {
		if b.usableMember(g, id) == nil {
			continue
		}
		if br := b.breakers[id]; br != nil && !br.available(now) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (b *Balancer) pickRoundRobin(g *ModelGroup) *Channel {
	alive := b.aliveMembers(g)
	if len(alive) == 0 {
		return nil
	}
	i := b.rrIdx[g.Name] % len(alive)
	b.rrIdx[g.Name] = (b.rrIdx[g.Name] + 1) % len(alive)
	return b.channels[alive[i]]
}

func (b *Balancer) pickWeighted(g *ModelGroup) *Channel {
	type entry struct {
		id    string
		weight int
	}
	expanded := make([]entry, 0, len(g.Members))
	for _, id := range b.aliveMembers(g) {
		w := max(b.channels[id].Weight, 1)
		for range w {
			expanded = append(expanded, entry{id: id})
		}
	}
	if len(expanded) == 0 {
		return nil
	}
	return b.channels[expanded[rand.IntN(len(expanded))].id]
}

func (b *Balancer) pickLeastUsed(g *ModelGroup) *Channel {
	alive := b.aliveMembers(g)
	if len(alive) == 0 {
		return nil
	}
	minID, minScore := "", int64(-1)
	for _, id := range alive {
		rt := b.rt[id]
		score := rt.inflight.Load()*1_000_000 + rt.totalRequests.Load()
		if minScore < 0 || score < minScore {
			minID, minScore = id, score
		}
	}
	return b.channels[minID]
}

func (b *Balancer) pickLowestLatency(g *ModelGroup) *Channel {
	alive := b.aliveMembers(g)
	if len(alive) == 0 {
		return nil
	}
	minID := ""
	minScore := -1.0 // -1 表示无样本，优先探测
	for _, id := range alive {
		rt := b.rt[id]
		score := -1.0
		if rt.totalRequests.Load() > 0 {
			score = rt.avgLatencyMS()
		}
		if minScore < 0 || score < minScore {
			minID, minScore = id, score
		}
		if minScore < 0 {
			break // 已找到无样本渠道，直接用
		}
	}
	return b.channels[minID]
}

// ReportResult 回报一次调用结果：驱动统计与熔断（success=false 计入熔断窗口）。
func (b *Balancer) ReportResult(channelID string, latencyMS int64, success bool) {
	b.mu.Lock()
	rt := b.rt[channelID]
	br := b.breakers[channelID]
	if br == nil {
		br = &breaker{}
		b.breakers[channelID] = br
	}
	b.mu.Unlock()
	if rt == nil {
		return
	}
	rt.inflight.Add(-1)
	rt.totalRequests.Add(1)
	rt.totalLatencyMS.Add(max(latencyMS, 0))
	if success {
		br.fails = 0
		br.openCount = 0 // 探测成功 → 完全恢复
		return
	}
	rt.totalFails.Add(1)
	if br.fails++; br.fails >= breakerAllowedFails {
		until := br.open(time.Now())
		log.Printf("breaker: channel %s OPEN for %s", channelID, time.Until(until).Round(time.Millisecond))
	}
}

// ReportSoftFailure 回报不计入熔断的失败（4xx 请求本身错误等）。
func (b *Balancer) ReportSoftFailure(channelID string, latencyMS int64) {
	b.mu.Lock()
	rt := b.rt[channelID]
	b.mu.Unlock()
	if rt == nil {
		return
	}
	rt.inflight.Add(-1)
	rt.totalRequests.Add(1)
	rt.totalLatencyMS.Add(max(latencyMS, 0))
}

// NoteInflight 标记一次请求开始（Pick 后调用）。
func (b *Balancer) NoteInflight(channelID string) {
	b.mu.Lock()
	rt := b.rt[channelID]
	b.mu.Unlock()
	if rt != nil {
		rt.inflight.Add(1)
	}
}

// balancerFile 落盘结构：仅配置，不含运行态与粘性表。
type balancerFile struct {
	Channels []Channel    `json:"channels"`
	Groups   []ModelGroup `json:"groups"`
}

// Save 配置落盘（0600，含 APIKey）。
func (b *Balancer) Save(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := balancerFile{}
	for _, id := range slices.Sorted(maps.Keys(b.channels)) {
		f.Channels = append(f.Channels, *b.channels[id])
	}
	for _, name := range slices.Sorted(maps.Keys(b.groups)) {
		f.Groups = append(f.Groups, *b.groups[name])
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal balancer: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load 从文件恢复配置（运行态清零）。
func (b *Balancer) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read balancer: %w", err)
	}
	var f balancerFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("unmarshal balancer: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range f.Channels {
		cp := c
		b.channels[c.ID] = &cp
		if _, ok := b.rt[c.ID]; !ok {
			b.rt[c.ID] = &channelRuntime{}
		}
	}
	for _, g := range f.Groups {
		cp := g
		b.groups[g.Name] = &cp
	}
	return nil
}

// Group 返回组快照（admin API 用）。
func (b *Balancer) Group(name string) (ModelGroup, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[name]
	if !ok {
		return ModelGroup{}, false
	}
	return *g, true
}

// Channel 返回渠道快照（admin API 用）。
func (b *Balancer) Channel(id string) (Channel, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.channels[id]
	if !ok {
		return Channel{}, false
	}
	return *c, true
}
