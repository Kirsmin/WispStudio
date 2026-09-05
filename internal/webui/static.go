package webui

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Handler 由 Go 直接托管 Vite 构建后的 web/dist。
// 前端技术栈仍然是 Vue + TypeScript + Vite；Go 只负责静态文件和 SPA fallback。
func Handler() http.Handler {
	root := locateDist()
	if root == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `<!doctype html><html><meta charset="utf-8"><title>Wisp</title><body style="font-family:system-ui;padding:32px">Wisp 前端尚未构建。请先执行 <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code>，然后重新启动 Go 服务。</body></html>`)
		})
	}

	fileSystem := os.DirFS(root)
	fileServer := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requestPath == "." || requestPath == "" {
			requestPath = "index.html"
		}
		if info, err := fs.Stat(fileSystem, requestPath); err == nil && !info.IsDir() {
			if strings.HasPrefix(requestPath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// Vue SPA 路由 fallback。
		indexPath := filepath.Join(root, "index.html")
		http.ServeFile(w, r, indexPath)
	})
}

func locateDist() string {
	candidates := make([]string, 0, 4)
	if configured := strings.TrimSpace(os.Getenv("WISP_WEB_DIR")); configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, filepath.Join("web", "dist"))
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "web", "dist"),
			filepath.Join(dir, "dist"),
		)
	}
	for _, candidate := range candidates {
		index := filepath.Join(candidate, "index.html")
		if info, err := os.Stat(index); err == nil && !info.IsDir() {
			absolute, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return absolute
			}
			return candidate
		}
	}
	return ""
}
