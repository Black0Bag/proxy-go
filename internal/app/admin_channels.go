package app

// M4 WP4.1/WP4.2：渠道/模型组管理 REST API + append-only 审计日志。
// 全部挂 adminAuth；写操作落审计（agent-governance: 审计链）。

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"cline-go-proxy/internal/provider"
)

// channelsFile 渠道配置落盘路径。
const channelsFile = "data/channels.json"

// globalBalancer 进程级渠道轮询器单例。
var (
	globalBalancerOnce sync.Once
	globalBalancer     *Balancer
)

// GlobalBalancer 惰性初始化并加载 .channels.json。
func GlobalBalancer() *Balancer {
	globalBalancerOnce.Do(func() {
		globalBalancer = NewBalancer()
		if err := globalBalancer.Load(channelsFile); err != nil {
			logWarnf("load %s: %v", channelsFile, err)
		}
	})
	return globalBalancer
}

// logWarnf 轻量告警（避免直接依赖 proxy 的日志初始化顺序）。
func logWarnf(format string, args ...any) {
	fmt.Printf("[warn] "+format+"\n", args...)
}

// auditRecord 审计条目（append-only JSONL）。
type auditRecord struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
}

// auditWrite 追加一条审计记录（JSONL）。
func auditWrite(action, target, detail string) {
	rec := auditRecord{
		Time:   time.Now().UTC().Format(time.RFC3339),
		Actor:  "admin",
		Action: action,
		Target: target,
		Detail: detail,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(auditFile), 0o755)
	f, err := os.OpenFile(auditFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// auditFile 审计日志路径。
const auditFile = "data/audit.jsonl"

// --- REST handlers ---

func handleChannelsList(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: GlobalBalancer().Snapshot()})
}

func handleChannelsUpsert(w http.ResponseWriter, r *http.Request) {
	var c Channel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "bad json: " + err.Error()})
		return
	}
	if c.ID == "" || c.Provider == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "id and provider required"})
		return
	}
	GlobalBalancer().UpsertChannel(c)
	_ = GlobalBalancer().Save(channelsFile)
	auditWrite("channel.upsert", c.ID, fmt.Sprintf("provider=%s weight=%d disabled=%v", c.Provider, c.Weight, c.Disabled))
	writeAPI(w, http.StatusOK, apiResponse{Success: true})
}

func handleChannelsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "id required"})
		return
	}
	GlobalBalancer().RemoveChannel(req.ID)
	_ = GlobalBalancer().Save(channelsFile)
	auditWrite("channel.delete", req.ID, "")
	writeAPI(w, http.StatusOK, apiResponse{Success: true})
}

func handleGroupsUpsert(w http.ResponseWriter, r *http.Request) {
	var g ModelGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "bad json: " + err.Error()})
		return
	}
	if g.Name == "" || len(g.Members) == 0 {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "name and members required"})
		return
	}
	switch g.Strategy {
	case "round_robin", "weighted", "least_used", "lowest_latency", "auto":
		// auto：按请求能力过滤 + tier 排序 + 会话粘性（M2 WP2.3）
	default:
		g.Strategy = "round_robin"
	}
	GlobalBalancer().UpsertGroup(g)
	_ = GlobalBalancer().Save(channelsFile)
	auditWrite("group.upsert", g.Name, fmt.Sprintf("strategy=%s members=%d", g.Strategy, len(g.Members)))
	writeAPI(w, http.StatusOK, apiResponse{Success: true})
}

func handleGroupsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "name required"})
		return
	}
	GlobalBalancer().RemoveGroup(req.Name)
	_ = GlobalBalancer().Save(channelsFile)
	auditWrite("group.delete", req.Name, "")
	writeAPI(w, http.StatusOK, apiResponse{Success: true})
}

func handleChannelsProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		BaseURL  string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "bad json"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	bal, err := ProbeBalance(ctx, req.Provider, req.APIKey, req.BaseURL)
	if err != nil {
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: err.Error()})
		return
	}
	auditWrite("balance.probe", req.Provider, fmt.Sprintf("total=%.2f %s", bal.Total, bal.Currency))
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: bal})
}

// --- Balancer 扩展（快照/删除组/遍历） ---

// VisitChannels 遍历渠道快照（fn 返回 false 提前终止）。
func (b *Balancer) VisitChannels(fn func(Channel) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(b.channels)) {
		if !fn(*b.channels[id]) {
			return
		}
	}
}

// RemoveGroup 删除模型组。
func (b *Balancer) RemoveGroup(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.groups, name)
	delete(b.rrIdx, name)
}

// Snapshot 全量配置快照（API 用）。
func (b *Balancer) Snapshot() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	chs := make([]Channel, 0, len(b.channels))
	for _, id := range slices.Sorted(maps.Keys(b.channels)) {
		chs = append(chs, *b.channels[id])
	}
	grps := make([]ModelGroup, 0, len(b.groups))
	for _, name := range slices.Sorted(maps.Keys(b.groups)) {
		grps = append(grps, *b.groups[name])
	}
	return map[string]any{"channels": chs, "groups": grps}
}

var _ = provider.ErrRateLimited // 保持 provider 依赖（调度层语义对接）
