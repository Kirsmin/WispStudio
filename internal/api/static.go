package api

import (
	"html"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// spaHandler 托管前端构建产物；非静态文件回落到 index.html，
// 使刷新页面时仍由前端接管路由。未知 /api 路径不回落。
// index.html 免鉴权返回，并向 <head> 注入访问令牌 meta。
func spaHandler(root, token string) http.Handler {
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
		if name != "/" && name != "/index.html" {
			if info, err := files.Open(name); err == nil {
				_ = info.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		raw, err := os.ReadFile(filepath.Join(root, "index.html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(injectTokenMeta(string(raw), token)))
	})
}

// injectTokenMeta 在 <head> 后插入令牌 meta；令牌为空或找不到 head 时原样返回。
func injectTokenMeta(page, token string) string {
	if token == "" {
		return page
	}
	meta := `<meta name="wisp-token" content="` + html.EscapeString(token) + `">`
	return strings.Replace(page, "<head>", "<head>\n    "+meta, 1)
}
