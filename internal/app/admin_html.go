package app

// WP0.5：管理后台多文件化（//go:embed），替代原单 const 字符串。
// 资源：internal/app/admin/web/{index.html,app.js,style.css}
// 设计：静态资源放行（无敏感数据），/admin/api/* 由 adminAuth 保护。

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed admin/web
var adminWebFS embed.FS

// adminStaticHandler 以 /admin/ 前缀提供嵌入的静态资源。
func adminStaticHandler(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(adminWebFS, "admin/web")
	if err != nil {
		http.Error(w, "admin assets unavailable", http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/admin/", http.FileServerFS(sub)).ServeHTTP(w, r)
}
