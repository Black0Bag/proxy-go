// Package provider 定义统一上游抽象（M0 WP0.4）。
// 设计原则：窄接口起步。Chat 转发接口在 M1 Channel 落地时扩展；
// ProbeBalance 返回 nil,nil 表示该上游无余额概念（如 Cline 账号池）。
package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ModelInfo 能力标签，M2 auto 路由的数据基础。
type ModelInfo struct {
	ID            string  `json:"id"`
	DisplayName   string  `json:"display_name,omitempty"`
	ContextWindow int     `json:"context_window,omitempty"`
	Vision        bool    `json:"vision,omitempty"`
	ToolCall      bool    `json:"tool_call,omitempty"`
	Reasoning     bool    `json:"reasoning,omitempty"`
	CostPer1MIn   float64 `json:"cost_per_1m_in,omitempty"`
	CostPer1MOut  float64 `json:"cost_per_1m_out,omitempty"`
	Tier          string  `json:"tier,omitempty"` // S/A/B/C，空为未分级
}

// Balance 上游余额快照。
type Balance struct {
	Currency string    `json:"currency"`
	Total    float64   `json:"total"`
	Used     float64   `json:"used"`
	ProbedAt time.Time `json:"probed_at"`
}

// Sentinel 错误：路由层按类型决定熔断策略（M1 WP1.4）。
var (
	ErrRateLimited = errors.New("upstream rate limited")
	ErrKeyInvalid  = errors.New("upstream key invalid")
	ErrUpstream    = errors.New("upstream error")
)

// RetryAfterer 由具体错误实现，暴露建议重试间隔。
type RetryAfterer interface {
	RetryAfter() time.Duration
}

// RetryAfter 提取建议间隔，无则回退 fallback。
func RetryAfter(err error, fallback time.Duration) time.Duration {
	var ra RetryAfterer
	if errors.As(err, &ra) {
		if d := ra.RetryAfter(); d > 0 {
			return d
		}
	}
	return fallback
}

// Checkiner 可选签到能力；实现方以类型断言探测。
type Checkiner interface {
	CheckIn(ctx context.Context) error
}

// Provider 统一上游抽象。
type Provider interface {
	Name() string
	ListModels(ctx context.Context) ([]ModelInfo, error)
	ProbeBalance(ctx context.Context) (*Balance, error)
}

// Registry 进程内 provider 注册表（并发安全）。
type Registry struct {
	mu   sync.Mutex
	m    map[string]Provider
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry { return &Registry{m: make(map[string]Provider)} }

// Register 注册或覆盖。
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.Name()] = p
}

// Get 按名取用。
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[name]
	return p, ok
}

// Names 返回全部已注册名。
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.m))
	for k := range r.m {
		names = append(names, k)
	}
	return names
}
