package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// spaHandler 托管前端构建产物；非静态文件回落到 index.html，
// 使刷新页面时仍由前端接管路由。未知 /api 路径不回落。
func spaHandler(root string) http.Handler {
	files := http.Dir(root)
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := path.Clean("/" + r.URL.Path)
		if name != "/" {
			if info, err := files.Open(name); err == nil {
				_ = info.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}
