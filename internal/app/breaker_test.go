package app

import (
	"errors"
	"testing"
	"time"
)

// 白盒：直接操作 breaker 状态与冷却时间。

func TestBreakerOpensAfterAllowedFails(t *testing.T) {
	b := mkBalancer(t)
	// a 连败 3 次 → open
	for range breakerAllowedFails {
		b.ReportResult("a", 10, false)
	}
	if br := b.breakers["a"]; !br.openUntil.After(time.Now()) {
		t.Fatalf("a should be open, openUntil=%v", br.openUntil)
	}
	// Pick 只会返回 b
	for range 3 {
		c, err := b.Pick("auto", "")
		if err != nil {
			t.Fatalf("pick should fall to b: %v", err)
		}
		if c.ID != "b" {
			t.Fatalf("open channel a must be skipped, got %s", c.ID)
		}
	}
}

func TestBreakerAllOpen(t *testing.T) {
	b := mkBalancer(t)
	for _, id := range []string{"a", "b"} {
		for range breakerAllowedFails {
			b.ReportResult(id, 5, false)
		}
	}
	if _, err := b.Pick("auto", ""); !errors.Is(err, errNoChannel) {
		t.Fatalf("all channels open should yield errNoChannel, got %v", err)
	}
}

func TestBreakerExponentialBackoffAndRecovery(t *testing.T) {
	b := mkBalancer(t)
	// 第一次 open：base 5s
	for range breakerAllowedFails {
		b.ReportResult("a", 5, false)
	}
	br := b.breakers["a"]
	cd1 := time.Until(br.openUntil)
	if cd1 <= 0 {
		t.Fatalf("should be cooling down, got %v", cd1)
	}
	// 模拟冷却到期 → 探测又失败 → 冷却翻倍
	br.openUntil = time.Now().Add(-time.Millisecond)
	b.ReportResult("a", 5, false)
	br2 := b.breakers["a"]
	// fails 从 0 重新计（open 时清零），这次单次失败不应立即 open
	if br2.fails != 1 {
		t.Fatalf("after reopen probe fail, fails should be 1, got %d", br2.fails)
	}
	// 补满 3 次 → 再次 open，openCount=2 → 冷却 ≈ 20s
	b.ReportResult("a", 5, false)
	b.ReportResult("a", 5, false)
	cd2 := time.Until(b.breakers["a"].openUntil)
	if cd2 <= cd1 {
		t.Fatalf("cooldown should grow: cd1=%v cd2=%v", cd1, cd2)
	}
	// 探测成功 → 完全恢复
	b.breakers["a"].openUntil = time.Now().Add(-time.Millisecond)
	b.ReportResult("a", 5, true)
	if br := b.breakers["a"]; br.fails != 0 || br.openCount != 0 {
		t.Fatalf("success should fully reset breaker, got %+v", br)
	}
}

func TestSoftFailureDoesNotTripBreaker(t *testing.T) {
	b := mkBalancer(t)
	for range 10 {
		b.ReportSoftFailure("a", 20)
	}
	if br := b.breakers["a"]; br != nil && br.fails > 0 {
		t.Fatalf("soft failures must not trip breaker, fails=%d", br.fails)
	}
	c, err := b.Pick("auto", "")
	if err != nil || c.ID == "" {
		t.Fatalf("a should still be pickable: %v", err)
	}
}

func TestBreakerResetOnSuccess(t *testing.T) {
	b := mkBalancer(t)
	b.ReportResult("a", 5, false)
	b.ReportResult("a", 5, false)
	b.ReportResult("a", 5, true) // 窗口清零
	if br := b.breakers["a"]; br.fails != 0 {
		t.Fatalf("success should reset fail window, got %d", br.fails)
	}
	for range breakerAllowedFails - 1 {
		b.ReportResult("a", 5, false)
	}
	if br := b.breakers["a"]; !br.openUntil.IsZero() {
		t.Fatalf("2 fails after reset should not open, openUntil=%v", br.openUntil)
	}
}
