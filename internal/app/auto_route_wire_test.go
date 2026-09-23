package app

// M2 WP2.3 基础层单测：auto 候选计算（AutoCandidates）与按序调度（DispatchOrdered）。
//
// 覆盖两类关键语义：
//  1. 能力过滤：明确标注「不支持」的渠道被硬过滤；未标注渠道不被误杀（宽松兜底）
//  2. 按序调度：严格按候选顺序（非轮询），且粘性/熔断/禁用语义与 Dispatch 一致

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// TestAutoCandidatesTierOrder tier 降序排序，未标注排最后。
func TestAutoCandidatesTierOrder(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "c-tier", Name: "C", Caps: &ModelCaps{Tier: "C"}})
	b.UpsertChannel(Channel{ID: "plain", Name: "P"}) // 未标注
	b.UpsertChannel(Channel{ID: "s-tier", Name: "S", Caps: &ModelCaps{Tier: "S"}})
	b.UpsertChannel(Channel{ID: "a-tier", Name: "A", Caps: &ModelCaps{Tier: "A"}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"c-tier", "plain", "s-tier", "a-tier"}})

	got, err := b.AutoCandidates("auto", RequestFeatures{})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	want := []string{"s-tier", "a-tier", "c-tier", "plain"}
	if !slices.Equal(got, want) {
		t.Errorf("got=%v, 期望 %v（tier 降序，未标注末位）", got, want)
	}
}

// TestAutoCandidatesToolFilterKeepsUnannotated 核心语义：
// 明确标注 ToolCall=false 的渠道被过滤；**未标注渠道必须保留**，
// 否则「配了 auto 组但没标注能力」会让所有带 tools 的编码请求静默失效。
func TestAutoCandidatesToolFilterKeepsUnannotated(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "no-tool", Name: "no", Caps: &ModelCaps{ToolCall: false}})
	b.UpsertChannel(Channel{ID: "has-tool", Name: "yes", Caps: &ModelCaps{ToolCall: true}})
	b.UpsertChannel(Channel{ID: "plain", Name: "unknown"})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"no-tool", "has-tool", "plain"}})

	got, err := b.AutoCandidates("auto", RequestFeatures{HasTools: true})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if slices.Contains(got, "no-tool") {
		t.Errorf("got=%v, 明确不支持 tools 的渠道应被过滤", got)
	}
	if !slices.Contains(got, "has-tool") || !slices.Contains(got, "plain") {
		t.Errorf("got=%v, 应保留「支持」与「未标注」两类渠道", got)
	}
}

// TestAutoCandidatesVisionFilter 带图请求过滤 Vision=false。
func TestAutoCandidatesVisionFilter(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "no-vision", Caps: &ModelCaps{ToolCall: true, Vision: false}})
	b.UpsertChannel(Channel{ID: "vision", Caps: &ModelCaps{ToolCall: true, Vision: true}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"no-vision", "vision"}})

	got, err := b.AutoCandidates("auto", RequestFeatures{HasImage: true})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if !slices.Equal(got, []string{"vision"}) {
		t.Errorf("got=%v, 期望仅 [vision]", got)
	}
}

// TestAutoCandidatesContextWindow 上下文超出窗口的渠道被过滤；窗口未标注（0）视为不限。
func TestAutoCandidatesContextWindow(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "small", Caps: &ModelCaps{ContextWindow: 8192}})
	b.UpsertChannel(Channel{ID: "big", Caps: &ModelCaps{ContextWindow: 200000}})
	b.UpsertChannel(Channel{ID: "unlimited"}) // 未标注 → 不限
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"small", "big", "unlimited"}})

	got, err := b.AutoCandidates("auto", RequestFeatures{EstContextTok: 50000})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if slices.Contains(got, "small") {
		t.Errorf("got=%v, 窗口 8k 的渠道不应承接 50k 上下文", got)
	}
	if !slices.Contains(got, "big") || !slices.Contains(got, "unlimited") {
		t.Errorf("got=%v, 应保留大窗口与未标注渠道", got)
	}
}

// TestAutoCandidatesSkipsDisabled 禁用渠道不进候选。
func TestAutoCandidatesSkipsDisabled(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "off", Disabled: true, Caps: &ModelCaps{Tier: "S"}})
	b.UpsertChannel(Channel{ID: "on", Caps: &ModelCaps{Tier: "B"}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"off", "on"}})

	got, err := b.AutoCandidates("auto", RequestFeatures{})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if !slices.Equal(got, []string{"on"}) {
		t.Errorf("got=%v, 禁用渠道应被排除", got)
	}
}

// TestAutoCandidatesNoMatch 全部被过滤/组不存在 → ErrNoChannel。
func TestAutoCandidatesNoMatch(t *testing.T) {
	b := NewBalancer()
	if _, err := b.AutoCandidates("missing", RequestFeatures{}); !errors.Is(err, ErrNoChannel) {
		t.Errorf("组不存在: err=%v, 期望 ErrNoChannel", err)
	}

	b.UpsertChannel(Channel{ID: "no-tool", Caps: &ModelCaps{ToolCall: false}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"no-tool"}})
	if _, err := b.AutoCandidates("auto", RequestFeatures{HasTools: true}); !errors.Is(err, ErrNoChannel) {
		t.Errorf("全被过滤: err=%v, 期望 ErrNoChannel（含能力不满足说明）", err)
	}
}

// TestChannelCapsPersistence Caps 随渠道落盘并原样读回；未标注保持 nil（omitempty）。
func TestChannelCapsPersistence(t *testing.T) {
	t.Setenv(envMasterKey, hexKey('e')) // 隔离加密环境：不生成/不读取真实 .masterkey
	path := filepath.Join(t.TempDir(), "channels.json")
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "x", Name: "X", Provider: "deepseek", APIKey: "k",
		Caps: &ModelCaps{Tier: "S", ToolCall: true, ContextWindow: 128000, CostPer1MIn: 0.5}})
	b.UpsertChannel(Channel{ID: "y", Name: "Y", Provider: "openai-compatible", APIKey: "k2"})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"x", "y"}, Strategy: "auto"})
	if err := b.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	b2 := NewBalancer()
	if err := b2.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	cx, ok := b2.Channel("x")
	if !ok || cx.Caps == nil {
		t.Fatalf("x 的 Caps 未往返: ok=%v caps=%+v", ok, cx.Caps)
	}
	if cx.Caps.Tier != "S" || !cx.Caps.ToolCall || cx.Caps.ContextWindow != 128000 || cx.Caps.CostPer1MIn != 0.5 {
		t.Errorf("Caps 字段丢失: %+v", *cx.Caps)
	}
	cy, _ := b2.Channel("y")
	if cy.Caps != nil {
		t.Errorf("未标注渠道的 Caps 应为 nil, got %+v", *cy.Caps)
	}
	g, ok := b2.Group("auto")
	if !ok || g.Strategy != "auto" {
		t.Errorf("组策略未往返: ok=%v g=%+v", ok, g)
	}
}

// TestDispatchOrderedFollowsCandidateOrder 严格按候选顺序：首选可用则不试后续。
func TestDispatchOrderedFollowsCandidateOrder(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "good"})
	b.UpsertChannel(Channel{ID: "bad"})

	var tried []string
	res := b.DispatchOrdered(context.Background(), []string{"good", "bad"}, "", 0,
		func(ch *Channel) (int, error) {
			tried = append(tried, ch.ID)
			if ch.ID == "bad" {
				return 500, errors.New("boom")
			}
			return 200, nil
		})
	if res.Err != nil {
		t.Fatalf("res=%+v, 期望成功", res)
	}
	if res.Channel == nil || res.Channel.ID != "good" {
		t.Fatalf("选中 %v, 期望 good（候选首位）", res.Channel)
	}
	if !slices.Equal(tried, []string{"good"}) {
		t.Errorf("tried=%v, 首位成功不应再试其他候选", tried)
	}
}

// TestDispatchOrderedFailoverInOrder 首选失败 → 按候选顺序换下一个（非轮询）。
func TestDispatchOrderedFailoverInOrder(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "bad"})
	b.UpsertChannel(Channel{ID: "good"})

	var tried []string
	res := b.DispatchOrdered(context.Background(), []string{"bad", "good"}, "", 0,
		func(ch *Channel) (int, error) {
			tried = append(tried, ch.ID)
			if ch.ID == "bad" {
				return 429, errors.New("rate limited")
			}
			return 200, nil
		})
	if res.Err != nil {
		t.Fatalf("res=%+v, 期望换渠道后成功", res)
	}
	if !res.Failover {
		t.Errorf("res.Failover=false, 期望发生换渠道")
	}
	if !slices.Equal(tried, []string{"bad", "good"}) {
		t.Errorf("tried=%v, 期望按候选顺序依次尝试", tried)
	}
}

// TestDispatchOrderedSticky 会话粘性：候选顺序变化时仍锁定原渠道。
func TestDispatchOrderedSticky(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a"})
	b.UpsertChannel(Channel{ID: "b"})
	ok := func(ch *Channel) (int, error) { return 200, nil }

	r1 := b.DispatchOrdered(context.Background(), []string{"a", "b"}, "sess-1", 0, ok)
	if r1.Channel == nil || r1.Channel.ID != "a" {
		t.Fatalf("首次选中 %v, 期望 a", r1.Channel)
	}
	// 候选顺序反转：若无粘性会选 b；有粘性应仍为 a
	r2 := b.DispatchOrdered(context.Background(), []string{"b", "a"}, "sess-1", 0, ok)
	if r2.Channel == nil || r2.Channel.ID != "a" {
		t.Errorf("粘性失效: 选中 %v, 期望仍为 a", r2.Channel)
	}
	// 不同会话不受影响
	r3 := b.DispatchOrdered(context.Background(), []string{"b", "a"}, "sess-2", 0, ok)
	if r3.Channel == nil || r3.Channel.ID != "b" {
		t.Errorf("会话 sess-2 选中 %v, 期望 b（无粘性时取候选首位）", r3.Channel)
	}
}

// TestDispatchOrderedStickyDroppedWhenOutOfCandidates 粘性渠道已不在候选内 → 失效重选。
func TestDispatchOrderedStickyDroppedWhenOutOfCandidates(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a"})
	b.UpsertChannel(Channel{ID: "b"})
	ok := func(ch *Channel) (int, error) { return 200, nil }

	_ = b.DispatchOrdered(context.Background(), []string{"a", "b"}, "sess", 0, ok)
	// 本轮候选不含 a（如能力不再匹配）
	r := b.DispatchOrdered(context.Background(), []string{"b"}, "sess", 0, ok)
	if r.Channel == nil || r.Channel.ID != "b" {
		t.Errorf("选中 %v, 期望 b（粘性渠道不在候选内应失效）", r.Channel)
	}
}

// TestDispatchOrderedStickyDroppedWhenBreakerOpen 粘性渠道熔断 → 失效换其他候选。
func TestDispatchOrderedStickyDroppedWhenBreakerOpen(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a"})
	b.UpsertChannel(Channel{ID: "b"})
	ok := func(ch *Channel) (int, error) { return 200, nil }

	r1 := b.DispatchOrdered(context.Background(), []string{"a", "b"}, "sess", 0, ok)
	if r1.Channel == nil || r1.Channel.ID != "a" {
		t.Fatalf("首次选中 %v, 期望 a", r1.Channel)
	}
	// 连续 3 次失败使 a 熔断（阈值 breakerAllowedFails=3）
	for i := 0; i < breakerAllowedFails; i++ {
		b.ReportResult("a", 1, false)
	}
	r2 := b.DispatchOrdered(context.Background(), []string{"a", "b"}, "sess", 0, ok)
	if r2.Channel == nil || r2.Channel.ID != "b" {
		t.Errorf("选中 %v, 期望 b（粘性渠道熔断应换用其他候选）", r2.Channel)
	}
}

// TestDispatchOrderedNoRetryOn400 坏请求（400）不换渠道。
func TestDispatchOrderedNoRetryOn400(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a"})
	b.UpsertChannel(Channel{ID: "b"})

	var tried []string
	res := b.DispatchOrdered(context.Background(), []string{"a", "b"}, "", 0,
		func(ch *Channel) (int, error) {
			tried = append(tried, ch.ID)
			return 400, errors.New("bad request")
		})
	if res.Status != 400 || res.Err == nil {
		t.Fatalf("res=%+v, 期望携带 400 错误返回", res)
	}
	if !slices.Equal(tried, []string{"a"}) {
		t.Errorf("tried=%v, 400 不应换渠道", tried)
	}
}

// TestDispatchOrderedAllFail 全部候选失败 → 汇总错误 + Retryable + 尝试次数正确。
func TestDispatchOrderedAllFail(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a"})
	b.UpsertChannel(Channel{ID: "b"})

	res := b.DispatchOrdered(context.Background(), []string{"a", "b"}, "", 0,
		func(ch *Channel) (int, error) { return 500, errors.New("boom") })
	if res.Err == nil {
		t.Fatalf("res=%+v, 期望失败", res)
	}
	if !res.Retryable {
		t.Errorf("res.Retryable=false, 期望可重试语义")
	}
	if res.Attempts != 2 {
		t.Errorf("res.Attempts=%d, 期望 2", res.Attempts)
	}
}

// TestDispatchOrderedEmptyCandidates 空候选立即失败，不 panic。
func TestDispatchOrderedEmptyCandidates(t *testing.T) {
	b := NewBalancer()
	res := b.DispatchOrdered(context.Background(), nil, "", 0,
		func(ch *Channel) (int, error) { return 200, nil })
	if !errors.Is(res.Err, ErrNoChannel) {
		t.Errorf("err=%v, 期望 ErrNoChannel", res.Err)
	}
	if res.Attempts != 0 {
		t.Errorf("res.Attempts=%d, 期望 0", res.Attempts)
	}
}

// TestAnnotatedCaps 未标注 → 宽松 caps（不参与硬过滤，tier 空）。
func TestAnnotatedCaps(t *testing.T) {
	got := annotatedCaps(nil)
	if !got.ToolCall || !got.Vision {
		t.Errorf("未标注应为宽松 caps（ToolCall/Vision 视为支持）, got %+v", got)
	}
	if got.ContextWindow != 0 {
		t.Errorf("未标注上下文窗口应为 0（不限）, got %d", got.ContextWindow)
	}
	annotated := &ModelCaps{Tier: "A", ToolCall: false}
	if got := annotatedCaps(annotated); got.Tier != "A" || got.ToolCall {
		t.Errorf("已标注应原样返回, got %+v", got)
	}
}
