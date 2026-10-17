package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenAuth 验收 F5：/api/* 需要匹配的 X-Token，跨 Origin 一律拒绝，
// health 与预检放行。
func TestTokenAuth(t *testing.T) {
	env := newTestEnv(t, "http://127.0.0.1:9", "secret-token")
	h := env.srv.Handler()
	call := func(method, path, token, origin string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("X-Token", token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		origin string
		want   int
	}{
		{"无 token", http.MethodGet, "/api/sessions", "", "", http.StatusUnauthorized},
		{"错误 token", http.MethodGet, "/api/sessions", "wrong", "", http.StatusUnauthorized},
		{"正确 token", http.MethodGet, "/api/sessions", "secret-token", "", http.StatusOK},
		{"health 免鉴权", http.MethodGet, "/api/health", "", "", http.StatusOK},
		{"非本机 Origin 带 token", http.MethodGet, "/api/sessions", "secret-token", "http://evil.example", http.StatusForbidden},
		{"非本机 Origin 预检", http.MethodOptions, "/api/sessions", "", "http://evil.example", http.StatusForbidden},
		{"本机 Origin 预检", http.MethodOptions, "/api/sessions", "", "http://127.0.0.1:5173", http.StatusNoContent},
		{"本机 Origin 带 token", http.MethodGet, "/api/sessions", "secret-token", "http://127.0.0.1:5173", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(tc.method, tc.path, tc.token, tc.origin); got != tc.want {
				t.Fatalf("%s %s token=%q origin=%q = %d, 期望 %d",
					tc.method, tc.path, tc.token, tc.origin, got, tc.want)
			}
		})
	}
}

// TestSPAInjectsToken 验收 F5：静态页面免鉴权，且 index.html 注入 token meta。
func TestSPAInjectsToken(t *testing.T) {
	env := newTestEnv(t, "http://127.0.0.1:9", "abcdef")
	dist := t.TempDir()
	page := `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body></body></html>`
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatalf("写 index.html 失败: %v", err)
	}
	srv, err := NewServer(Config{
		Store:   env.store,
		Catalog: env.catalog,
		WebDist: dist,
		Token:   "abcdef",
	})
	if err != nil {
		t.Fatalf("构造 API 服务失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, 期望 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<meta name="wisp-token" content="abcdef">`) {
		t.Fatalf("index.html 未注入 token meta:\n%s", rec.Body.String())
	}
}
