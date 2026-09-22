package app

// M1 WP1.4/WP1.5：组内故障转移调度。
// Dispatch 把 Pick → attempt → 错误分类上报 → 换渠道重试 串成闭环。
// 重试只发生在"首字节之前"（attempt 语义约定：流式请求在收到首个字节前失败才算 attempt 失败）。

import (
	"context"
	"errors"
	"time"
)

// MaxDispatchAttempts 单次请求最多尝试的渠道数（默认组内全部）。
const MaxDispatchAttempts = 4

// DispatchResult 一次调度结果。
type DispatchResult struct {
	Channel   *Channel
	Status    int    // 最终 attempt 的 HTTP 状态码（成功时为上游真实状态）
	Err       error  // 全部尝试失败时的最后错误
	Attempts  int    // 实际尝试次数
	Failover  bool   // 是否发生过换渠道重试
	Retryable bool   // 建议调用方对客户端返回可重试语义（5xx/429/网络错）
}

// channelPicker 把「选渠道」从调度骨架中解耦：
// groupPicker 走组策略（Pick/nextUntried），orderedPicker 走显式候选顺序（auto 路由）。
type channelPicker interface {
	first() (*Channel, error)            // 首个候选（尊重粘性）
	next(tried map[string]bool) *Channel // 重试时的下一个候选
	aliveCount() int                     // 当前可用候选数（用于计算最大尝试次数）
}

// groupPicker 按 ModelGroup 策略挑选（M1 语义，Dispatch 使用）。
type groupPicker struct {
	b          *Balancer
	groupName  string
	sessionKey string
}

func (p *groupPicker) first() (*Channel, error) { return p.b.Pick(p.groupName, p.sessionKey) }

func (p *groupPicker) next(tried map[string]bool) *Channel {
	return p.b.nextUntried(p.groupName, tried)
}

func (p *groupPicker) aliveCount() int {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()
	g, ok := p.b.groups[p.groupName]
	if !ok {
		return 0
	}
	return len(p.b.aliveMembers(g))
}

// orderedPicker 按显式候选顺序挑选（M2 auto 路由，DispatchOrdered 使用）。
type orderedPicker struct {
	b          *Balancer
	candidates []string
	sessionKey string
}

func (p *orderedPicker) first() (*Channel, error) {
	return p.b.pickOrdered(p.candidates, p.sessionKey)
}

func (p *orderedPicker) next(tried map[string]bool) *Channel {
	return p.b.nextUntriedOrdered(p.candidates, tried)
}

func (p *orderedPicker) aliveCount() int { return p.b.aliveInCandidates(p.candidates) }

// Dispatch 在 groupName 组内执行 attempt：
//   - attempt 收到选中渠道，返回 (httpStatus, err)；err==nil 且 2xx 视为成功
//   - 429/5xx/传输错误 → 计熔断并换下一渠道
//   - 401/403 → 计熔断（key 失效）并换下一渠道
//   - 400 → ReportSoftFailure，立即返回（坏请求换渠道无意义）
//
// ctx 取消立即终止。maxAttempts<=0 时取组内可用数与 MaxDispatchAttempts 的较小值。
func (b *Balancer) Dispatch(ctx context.Context, groupName, sessionKey string, maxAttempts int, attempt func(ch *Channel) (int, error)) DispatchResult {
	return b.dispatch(ctx, &groupPicker{b: b, groupName: groupName, sessionKey: sessionKey}, maxAttempts, attempt)
}

// DispatchOrdered 按显式候选顺序调度（M2 WP2.3，auto 路由用），其余语义与 Dispatch 完全一致：
// 尊重粘性/熔断/禁用，失败按错误分类换下一个候选，400/422 不换渠道。
// candidates 通常来自 AutoCandidates（已按能力过滤 + tier 排序）。
func (b *Balancer) DispatchOrdered(ctx context.Context, candidates []string, sessionKey string, maxAttempts int, attempt func(ch *Channel) (int, error)) DispatchResult {
	return b.dispatch(ctx, &orderedPicker{b: b, candidates: candidates, sessionKey: sessionKey}, maxAttempts, attempt)
}

// dispatch 调度骨架：选渠道 → attempt → 错误分类 → 换渠道重试 的公共流程。
func (b *Balancer) dispatch(ctx context.Context, p channelPicker, maxAttempts int, attempt func(ch *Channel) (int, error)) DispatchResult {
	res := DispatchResult{}
	if err := ctx.Err(); err != nil {
		res.Err = err
		return res
	}
	alive := p.aliveCount()
	if alive == 0 {
		res.Err = ErrNoChannel
		return res
	}
	if maxAttempts <= 0 || maxAttempts > alive {
		maxAttempts = alive
	}
	if maxAttempts > MaxDispatchAttempts {
		maxAttempts = MaxDispatchAttempts
	}

	tried := map[string]bool{}
	var lastStatus int
	var lastErr error
	var ch *Channel
	for i := 0; i < maxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			res.Err = err
			return res
		}
		if i == 0 {
			// 首选：尊重粘性
			var err error
			ch, err = p.first()
			if err != nil {
				break // 无可用渠道（全熔断/空/候选全不可用）
			}
		} else {
			// 重试：确定性选取下一个未试过的可用渠道
			ch = p.next(tried)
			if ch == nil {
				break
			}
		}
		tried[ch.ID] = true
		res.Attempts++
		if i > 0 {
			res.Failover = true
		}
		b.NoteInflight(ch.ID)
		start := time.Now()
		status, aerr := func() (int, error) {
			defer func() { _ = recover() }() // attempt 崩溃按传输错误处理
			return attempt(ch)
		}()
		latency := time.Since(start).Milliseconds()

		switch {
		case aerr == nil && status >= 200 && status < 300:
			b.ReportResult(ch.ID, latency, true)
			res.Channel, res.Status = ch, status
			return res
		case status == 400 || status == 422:
			// 请求本身问题：不熔断、不换渠道
			b.ReportSoftFailure(ch.ID, latency)
			res.Channel, res.Status, res.Err = ch, status, aerr
			return res
		case status == 429 || status == 401 || status == 403 || status >= 500 || status == 0:
			b.ReportResult(ch.ID, latency, false)
			lastStatus, lastErr = status, aerr
			if aerr == nil {
				lastErr = statusError(status)
			}
			continue
		default:
			// 其他 3xx/4xx：按软失败返回，不重试
			b.ReportSoftFailure(ch.ID, latency)
			res.Channel, res.Status, res.Err = ch, status, aerr
			return res
		}
	}
	res.Status = lastStatus
	res.Err = lastErr
	if res.Err == nil {
		res.Err = ErrNoChannel
	}
	res.Retryable = true
	return res
}

// nextUntried 返回组内第一个未尝试过的可用渠道（确定性，尊重熔断/禁用）。
func (b *Balancer) nextUntried(groupName string, tried map[string]bool) *Channel {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[groupName]
	if !ok {
		return nil
	}
	for _, id := range b.aliveMembers(g) {
		if !tried[id] {
			return b.channels[id]
		}
	}
	return nil
}

// statusError 把非 2xx 状态码转成可 errors.Is 匹配的错误。
func statusError(status int) error {
	if status == 429 {
		return providerErr{kind: errKindRateLimited, msg: "upstream 429"}
	}
	if status == 401 || status == 403 {
		return providerErr{kind: errKindKeyInvalid, msg: "upstream auth failed"}
	}
	if status >= 500 {
		return providerErr{kind: errKindUpstream, msg: "upstream 5xx"}
	}
	return providerErr{kind: errKindUpstream, msg: "upstream error"}
}

// 错误分类（对接 internal/provider sentinel 语义）。
type errKind int

const (
	errKindRateLimited errKind = iota
	errKindKeyInvalid
	errKindUpstream
)

type providerErr struct {
	kind errKind
	msg  string
}

func (e providerErr) Error() string { return e.msg }

// NewTTFBContext 首字节超时辅助：attempt 内读首块前使用。
func NewTTFBContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// ErrTTFB 超时语义错误（attempt 返回它 → 按 0 状态码传输错误处理）。
var ErrTTFB = errors.New("ttfb timeout")
