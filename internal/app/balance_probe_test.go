package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeDeepSeek(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" || r.Header.Get("Authorization") != "Bearer sk-x" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.50"}]}`))
	}))
	defer srv.Close()
	bal, err := ProbeBalance(context.Background(), "deepseek", "sk-x", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Currency != "CNY" || bal.Total != 110.50 || bal.ProbedAt.IsZero() {
		t.Fatalf("bal=%+v", bal)
	}
}

func TestProbeOpenRouter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"total_credits":10.0,"total_usage":2.5}}`))
	}))
	defer srv.Close()
	bal, err := ProbeBalance(context.Background(), "openrouter", "sk-or", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Total != 10.0 || bal.Used != 2.5 || bal.Currency != "USD" {
		t.Fatalf("bal=%+v", bal)
	}
}

func TestProbeNewAPIQuotaConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"quota":5000000,"used_quota":1000000}}`))
	}))
	defer srv.Close()
	bal, err := ProbeBalance(context.Background(), "newapi", "sess-abc", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Total != 10.0 || bal.Used != 2.0 {
		t.Fatalf("quota conversion broken: %+v", bal)
	}
}

func TestProbeUnknownProviderAndEmptyKey(t *testing.T) {
	if _, err := ProbeBalance(context.Background(), "nope", "k", ""); err == nil {
		t.Fatal("unknown provider should error")
	}
	if _, err := ProbeBalance(context.Background(), "deepseek", "", ""); err == nil {
		t.Fatal("empty key should error")
	}
}

func TestProbeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	if _, err := ProbeBalance(context.Background(), "moonshot", "bad", srv.URL); err == nil {
		t.Fatal("http 401 should error")
	}
}
