package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embeddedFS embed.FS

// Handler 返回处理前端 SPA 静态资源的 HTTP Handler。
// 规则：
// 1. 若请求路径在静态资源中存在且为文件，直接响应（带 mime 与缓存）。
// 2. 若请求路径不存在或为 SPA 客户端路由（如 /groupchat, /agents 等），统一 fallback 到 index.html。
func Handler() http.Handler {
	distFS, err := fs.Sub(embeddedFS, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "前端静态资源未正确定位", http.StatusInternalServerError)
		})
	}

	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "index.html" {
			serveIndexHTML(w, r, distFS)
			return
		}

		// 检查静态资源中是否存在该实体
		f, err := distFS.Open(path)
		if err == nil {
			stat, sErr := f.Stat()
			_ = f.Close()
			if sErr == nil && !stat.IsDir() {
				// 文件真实存在，交由 fileServer 正常处理
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// 文件不存在或为前端路由路径，SPA fallback 到 index.html
		serveIndexHTML(w, r, distFS)
	})
}

func serveIndexHTML(w http.ResponseWriter, r *http.Request, distFS fs.FS) {
	data, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
