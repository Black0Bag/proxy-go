package app

// M1 WP1.5-接线 单测：覆盖开关解析、状态码提取与账号→渠道同步。

import (
	"errors"
	"fmt"
	"testing"
)

// TestDispatchEnabled 开关默认必须为关闭，避免接线改变既有线上行为。
func TestDispatchEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"off", false},
		{"no", false},
		{"unexpected", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{" on ", true},
		{"yes", true},
	}
	for _, c := range cases {
		t.Setenv("CLINE_PROXY_DISPATCH", c.val)
		if got := dispatchEnabled(); got != c.want {
			t.Errorf("CLINE_PROXY_DISPATCH=%q: dispatchEnabled()=%v, 期望 %v", c.val, got, c.want)
		}
	}
}

// TestUpstreamStatusOf 状态码提取：类型化错误可取出、包装后仍可取出、其余按传输错误为 0。
func TestUpstreamStatusOf(t *testing.T) {
	typed := upstreamStatusError{Status: 429, Msg: "API 429: rate limited"}
	if got := upstreamStatusOf(typed); got != 429 {
		t.Errorf("类型化错误: got %d, 期望 429", got)
	}
	wrapped := fmt.Errorf("account a@b token failed: %w", upstreamStatusError{Status: 401, Msg: "API 401: bad"})
	if got := upstreamStatusOf(wrapped); got != 401 {
		t.Errorf("包装后: got %d, 期望 401", got)
	}
	if got := upstreamStatusOf(errors.New("dial tcp: connection refused")); got != 0 {
		t.Errorf("传输错误: got %d, 期望 0", got)
	}
	if got := upstreamStatusOf(nil); got != 0 {
		t.Errorf("nil: got %d, 期望 0", got)
	}
}

// TestUpstreamStatusErrorMessage 错误文本与接线前的 fmt.Errorf("API %d: %s") 一致，日志可读性不回退。
func TestUpstreamStatusErrorMessage(t *testing.T) {
	msg := fmt.Sprintf("API %d: %s", 429, "rate limited")
	err := upstreamStatusError{Status: 429, Msg: msg}
	if err.Error() != msg {
		t.Errorf("Error()=%q, 期望 %q", err.Error(), msg)
	}
}

// TestSyncAccountsToBalancer 仅 active 账号进入候选，且渠道映射不携带凭据。
func TestSyncAccountsToBalancer(t *testing.T) {
	saved := pool
	t.Cleanup(func() { pool = saved })

	b := NewBalancer()
	pool = &AccountPool{Accounts: []*Account{
		{AccountID: "a1", Email: "alpha@example.com", Status: "active"},
		{AccountID: "a2", Email: "beta@example.com", Status: "cooldown"},
		{AccountID: "a3", Email: "gamma@example.com", Status: "active"},
		{AccountID: "a4", Email: "delta@example.com", Status: "expired"},
	}}

	if n := syncAccountsToBalancerInto(b); n != 2 {
		t.Fatalf("候选数=%d, 期望 2（仅 active）", n)
	}
	g, ok := b.Group(dispatchGroupName)
	if !ok {
		t.Fatalf("未注册组 %s", dispatchGroupName)
	}
	if len(g.Members) != 2 {
		t.Errorf("Members=%v, 期望 2 个", g.Members)
	}
	for _, m := range g.Members {
		if m == "a2" || m == "a4" {
			t.Errorf("非 active 账号不应成为候选: %v", g.Members)
		}
	}

	ch, ok := b.Channel("a1")
	if !ok {
		t.Fatalf("渠道 a1 未注册")
	}
	if ch.ID != "a1" || ch.Provider != "cline" {
		t.Errorf("渠道映射错误: %+v", ch)
	}
	if ch.APIKey != "" || ch.BaseURL != "" {
		t.Errorf("渠道不应携带凭据或地址: %+v", ch)
	}

	// 账号状态降级后应移出候选
	pool.Accounts[0].Status = "expired"
	if n := syncAccountsToBalancerInto(b); n != 1 {
		t.Fatalf("降级后候选数=%d, 期望 1", n)
	}
	g, _ = b.Group(dispatchGroupName)
	for _, m := range g.Members {
		if m == "a1" {
			t.Errorf("expired 的 a1 仍在候选: %v", g.Members)
		}
	}
}

// TestSyncAccountsToBalancerEmpty 无可用账号时返回 0（调用方据此返回原有错误语义）。
func TestSyncAccountsToBalancerEmpty(t *testing.T) {
	saved := pool
	t.Cleanup(func() { pool = saved })

	b := NewBalancer()
	pool = &AccountPool{Accounts: []*Account{
		{AccountID: "a1", Email: "alpha@example.com", Status: "cooldown"},
	}}
	if n := syncAccountsToBalancerInto(b); n != 0 {
		t.Fatalf("候选数=%d, 期望 0", n)
	}
	if _, err := b.Pick(dispatchGroupName, ""); err == nil {
		t.Errorf("空组 Pick 应返回错误")
	}
}
