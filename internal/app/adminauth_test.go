package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// WP0.1 验收：鉴权中间件单测。
// 注意：adminTokenValue 是进程级 sync.OnceValue，本测试进程内首次触发即固化；
// 因此用 t.Chdir 隔离文件生成，用 env 提供确定 token。

func TestAdminAuthMiddleware(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CLINE_PROXY_ADMIN_TOKEN", "test-secret-123")
	h := adminAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name   string
		url    string
		auth   string
		expect int
	}{
		{"no token", "/admin/api/accounts", "", http.StatusUnauthorized},
		{"wrong bearer", "/admin/api/accounts", "Bearer nope", http.StatusUnauthorized},
		{"ok bearer", "/admin/api/accounts", "Bearer test-secret-123", http.StatusOK},
		{"bootstrap query", "/admin/api/accounts?token=test-secret-123", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			h(rec, req)
			if rec.Code != tc.expect {
				t.Fatalf("expect %d, got %d, body=%s", tc.expect, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestResolveAdminTokenEnvPriority(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CLINE_PROXY_ADMIN_TOKEN", "from-env")
	tok, fresh := resolveAdminToken()
	if tok != "from-env" || fresh {
		t.Fatalf("env should win, got %q fresh=%v", tok, fresh)
	}
}

func TestResolveAdminTokenGeneratesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("CLINE_PROXY_ADMIN_TOKEN", "")
	tok1, fresh := resolveAdminToken()
	if !fresh || len(tok1) != 48 {
		t.Fatalf("expect fresh 48-hex token, got %q fresh=%v", tok1, fresh)
	}
	// 再次解析：走文件，fresh=false
	tok2, fresh2 := resolveAdminToken()
	if fresh2 || tok2 != tok1 {
		t.Fatalf("expect stable file token, got %q fresh=%v", tok2, fresh2)
	}
	if _, err := os.Stat(filepath.Join(dir, adminTokenFile)); err != nil {
		t.Fatalf("token file should exist: %v", err)
	}
}
