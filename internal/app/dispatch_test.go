package app

// WP1.5 单测：Dispatch 故障转移语义（对齐 channel.go 真实 API）。

import (
	"context"
	"errors"
	"testing"
)

func newDispatchBalancer(t *testing.T) *Balancer {
	t.Helper()
	b := NewBalancer()
	for _, id := range []string{"d1", "d2", "d3", "d4"} {
		b.UpsertChannel(Channel{ID: id, Name: id, Provider: "openai-compatible", APIKey: "k-" + id})
	}
	b.UpsertGroup(ModelGroup{Name: "g-dispatch", Members: []string{"d1", "d2", "d3", "d4"}, Strategy: "round_robin"})
	return b
}

func TestDispatchSuccessFirstTry(t *testing.T) {
	b := newDispatchBalancer(t)
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		if ch.ID != "d1" {
			t.Fatalf("首轮应选 d1（rr 起点），得 %s", ch.ID)
		}
		return 200, nil
	})
	if res.Err != nil || res.Attempts != 1 || res.Failover {
		t.Fatalf("res=%+v", res)
	}
}

func TestDispatchFailoverOn5xx(t *testing.T) {
	b := newDispatchBalancer(t)
	tried := []string{}
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		tried = append(tried, ch.ID)
		if ch.ID == "d1" {
			return 500, nil
		}
		return 200, nil
	})
	if res.Err != nil || !res.Failover || res.Attempts != 2 {
		t.Fatalf("res=%+v tried=%v", res, tried)
	}
	if res.Channel.ID != "d2" {
		t.Fatalf("应切到 d2，得 %s", res.Channel.ID)
	}
}

func TestDispatch400NoRetry(t *testing.T) {
	b := newDispatchBalancer(t)
	calls := 0
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		calls++
		return 400, nil
	})
	if res.Status != 400 || calls != 1 || res.Failover {
		t.Fatalf("400 不应重试: res=%+v calls=%d", res, calls)
	}
}

func TestDispatchAuthFailureSkips(t *testing.T) {
	b := newDispatchBalancer(t)
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		if ch.ID == "d1" {
			return 401, nil
		}
		return 200, nil
	})
	if res.Err != nil || res.Channel.ID == "d1" {
		t.Fatalf("401 应触发换渠道: res=%+v", res)
	}
}

func TestDispatchAllFailExhausted(t *testing.T) {
	b := newDispatchBalancer(t)
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		return 503, nil
	})
	if res.Attempts != 4 || !res.Retryable || res.Status != 503 {
		t.Fatalf("res=%+v", res)
	}
}

func TestDispatchEmptyGroup(t *testing.T) {
	b := NewBalancer()
	b.UpsertGroup(ModelGroup{Name: "g-empty", Members: []string{}, Strategy: "round_robin"})
	res := b.Dispatch(context.Background(), "g-empty", "s1", 0, func(ch *Channel) (int, error) {
		return 200, nil
	})
	if !errors.Is(res.Err, ErrNoChannel) {
		t.Fatalf("空组应 ErrNoChannel: %+v", res)
	}
}

func TestDispatchAttemptPanicRecovered(t *testing.T) {
	b := newDispatchBalancer(t)
	res := b.Dispatch(context.Background(), "g-dispatch", "s1", 0, func(ch *Channel) (int, error) {
		if ch.ID == "d1" {
			panic("upstream panic")
		}
		return 200, nil
	})
	if res.Err != nil || res.Channel.ID != "d2" {
		t.Fatalf("panic 应按传输错误处理并 failover: %+v", res)
	}
}

func TestDispatchMaxAttemptsClamp(t *testing.T) {
	b := newDispatchBalancer(t)
	calls := 0
	b.Dispatch(context.Background(), "g-dispatch", "s1", 99, func(ch *Channel) (int, error) {
		calls++
		return 500, nil
	})
	if calls != MaxDispatchAttempts {
		t.Fatalf("maxAttempts 应钳制为 %d，得 %d", MaxDispatchAttempts, calls)
	}
}
