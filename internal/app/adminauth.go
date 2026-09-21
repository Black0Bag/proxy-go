package app

// WP0.1：admin API 鉴权（agent-governance Strict 级）。
// 设计决策：静态页面(/admin/ 下 HTML/JS/CSS)放行——不含敏感数据，便于前端引导输入 token；
// 所有 /admin/api/* 强制 Bearer，这是真正的数据边界（与 Grafana 等通行实践一致）。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

const adminTokenFile = ".admin-token"

// resolveAdminToken：环境变量优先，其次已有文件，否则生成并落盘(0600)。
func resolveAdminToken() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("CLINE_PROXY_ADMIN_TOKEN")); v != "" {
		return v, false
	}
	if b, err := os.ReadFile(adminTokenFile); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, false
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("generate admin token: %v", err))
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(adminTokenFile, []byte(token+"\n"), 0o600); err != nil {
		panic(fmt.Sprintf("persist admin token: %v", err))
	}
	return token, true
}

// adminTokenState 一次性解析结果。
type adminTokenState struct {
	token string
	fresh bool
}

var adminTokenValue = sync.OnceValue(func() adminTokenState {
	tok, fresh := resolveAdminToken()
	return adminTokenState{token: tok, fresh: fresh}
})

// adminTokenNow 供启动横幅与中间件统一取值。
func adminTokenNow() string { return adminTokenValue().token }

// adminAuth 包裹所有 /admin/api/* handler：Bearer 或 ?token= 首次引导。
func adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := adminTokenNow()
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || got == "" {
			got = r.URL.Query().Get("token")
		}
		if got == "" || got != want {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized: missing or invalid admin token"}`)
			return
		}
		next(w, r)
	}
}
