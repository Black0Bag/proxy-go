package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func mkBalancer(t *testing.T) *Balancer {
	t.Helper()
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "a", Name: "A", Provider: "openai", APIKey: "ka", Weight: 1})
	b.UpsertChannel(Channel{ID: "b", Name: "B", Provider: "openai", APIKey: "kb", Weight: 1})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"a", "b"}, Strategy: "round_robin"})
	t.Cleanup(func() {})
	return b
}

func TestRoundRobinSequential(t *testing.T) {
	b := mkBalancer(t)
	want := []string{"a", "b", "a", "b", "a"}
	for i, w := range want {
		c, err := b.Pick("auto", "")
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if c.ID != w {
			t.Fatalf("step %d: expect %s got %s", i, w, c.ID)
		}
	}
}

func TestWeightedRatio(t *testing.T) {
	b := mkBalancer(t)
	b.UpsertChannel(Channel{ID: "a", Name: "A", Provider: "x", APIKey: "k", Weight: 3})
	b.UpsertChannel(Channel{ID: "b", Name: "B", Provider: "x", APIKey: "k", Weight: 1})
	b.UpsertGroup(ModelGroup{Name: "g", Members: []string{"a", "b"}, Strategy: "weighted"})
	count := map[string]int{}
	for range 40 {
		c, err := b.Pick("g", "")
		if err != nil {
			t.Fatal(err)
		}
		count[c.ID]++
	}
	// 3:1 期望 30:10，随机实现允许 ±10
	if count["a"] < 20 || count["a"] > 40 {
		t.Fatalf("weighted ratio out of range: %v", count)
	}
	if count["b"] == 0 {
		t.Fatalf("b never picked: %v", count)
	}
}

func TestLeastUsedPrefersFewestRequests(t *testing.T) {
	b := mkBalancer(t)
	b.UpsertGroup(ModelGroup{Name: "g", Members: []string{"a", "b"}, Strategy: "least_used"})
	for range 3 {
		c, _ := b.Pick("g", "")
		b.NoteInflight(c.ID)
		b.ReportResult(c.ID, 100, true)
	}
	// a,b 各 3 次；再 Report a 一次 → a 请求数更多，下次应选 b
	c, _ := b.Pick("g", "")
	if c.ID != "b" {
		t.Fatalf("expect b (fewest), got %s", c.ID)
	}
}

func TestLowestLatencyPrefersFastest(t *testing.T) {
	b := mkBalancer(t)
	b.UpsertGroup(ModelGroup{Name: "g", Members: []string{"a", "b"}, Strategy: "lowest_latency"})
	// 白盒注入样本：a 快（30ms），b 慢（800ms）
	ta := b.rt["a"]
	ta.totalRequests.Store(1)
	ta.totalLatencyMS.Store(30)
	tb := b.rt["b"]
	tb.totalRequests.Store(1)
	tb.totalLatencyMS.Store(800)
	c, err := b.Pick("g", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "a" {
		t.Fatalf("expect fast a (30ms), got %s", c.ID)
	}
	// 无样本渠道优先探测：新建一个 c 无样本，应优先于 a(30ms)
	b.UpsertChannel(Channel{ID: "c", Name: "C", Provider: "x", APIKey: "k"})
	b.UpsertGroup(ModelGroup{Name: "g", Members: []string{"a", "b", "c"}, Strategy: "lowest_latency"})
	c2, _ := b.Pick("g", "")
	if c2.ID != "c" {
		t.Fatalf("expect unsampled c probed first, got %s", c2.ID)
	}
}

func TestStickySession(t *testing.T) {
	b := mkBalancer(t)
	c1, _ := b.Pick("auto", "sess-1")
	c2, _ := b.Pick("auto", "sess-1")
	if c1.ID != c2.ID {
		t.Fatalf("sticky broken: %s vs %s", c1.ID, c2.ID)
	}
	// 不同 sessionKey 不共享粘性（round_robin 会轮转）
	c3, _ := b.Pick("auto", "sess-2")
	if c3.ID == c1.ID {
		t.Fatalf("expected rotation for new session, got same %s", c3.ID)
	}
}

func TestPickErrorsOnMissingOrEmpty(t *testing.T) {
	b := mkBalancer(t)
	if _, err := b.Pick("nope", ""); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("expect ErrNoChannel, got %v", err)
	}
	b.UpsertGroup(ModelGroup{Name: "empty", Members: []string{}})
	if _, err := b.Pick("empty", ""); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("expect ErrNoChannel for empty group, got %v", err)
	}
}

func TestDisabledChannelSkipped(t *testing.T) {
	b := mkBalancer(t)
	b.UpsertChannel(Channel{ID: "a", Name: "A", Provider: "x", APIKey: "k", Disabled: true})
	b.UpsertGroup(ModelGroup{Name: "g", Members: []string{"a", "b"}, Strategy: "round_robin"})
	for range 3 {
		c, _ := b.Pick("g", "")
		if c.ID != "b" {
			t.Fatalf("disabled a should be skipped, got %s", c.ID)
		}
	}
}

func TestRemoveChannelCleansGroups(t *testing.T) {
	b := mkBalancer(t)
	b.RemoveChannel("a")
	g, ok := b.Group("auto")
	if !ok || len(g.Members) != 1 || g.Members[0] != "b" {
		t.Fatalf("group not cleaned: %+v", g)
	}
	if _, err := b.Pick("auto", ""); err != nil {
		t.Fatalf("pick should still work: %v", err)
	}
}

func TestBalancerSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	b := mkBalancer(t)
	b.UpsertChannel(Channel{ID: "c", Name: "C", Provider: "deepseek", APIKey: "sk-secret", Weight: 2, ModelMap: map[string]string{"auto": "deepseek-chat"}})
	b.UpsertGroup(ModelGroup{Name: "g2", Members: []string{"c"}, Strategy: "lowest_latency"})
	if err := b.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	b2 := NewBalancer()
	if err := b2.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	cc, ok := b2.Channel("c")
	if !ok || cc.APIKey != "sk-secret" || cc.ModelMap["auto"] != "deepseek-chat" {
		t.Fatalf("channel roundtrip broken: %+v", cc)
	}
	if g, ok := b2.Group("g2"); !ok || len(g.Members) != 1 || g.Strategy != "lowest_latency" {
		t.Fatalf("group roundtrip broken: %+v", g)
	}
	// 运行态应清零：Load 后 Pick 正常工作
	c, err := b2.Pick("g2", "")
	if err != nil {
		t.Fatalf("pick after load: %v", err)
	}
	b2.NoteInflight(c.ID)
	b2.ReportResult(c.ID, 10, true)
	// 配置应完整：a/b 来自 mkBalancer，c 来自本测试
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := b2.Channel(id); !ok {
			t.Fatalf("channel %s missing after roundtrip", id)
		}
	}
	if _, ok := b2.Channel("nonexistent"); ok {
		t.Fatalf("phantom channel returned")
	}
}
