package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
)

// testEnv 是 API 测试环境：临时配置文件 + 内存事件日志 + 目录 + 服务。
type testEnv struct {
	store      *eventlog.Store
	catalog    *provider.Catalog
	srv        *Server
	token      string
	configPath string
}

// newTestEnv 用临时配置文件构造 API 服务；baseURL 是假 provider 地址。
func newTestEnv(t *testing.T, baseURL, token string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.FileName)
	text := fmt.Sprintf(`[server]
Port    = 8787
Token   = %q

[[Providers]]
ProviderID   = "fake"
ProviderName = "假服务商"
BaseURL      = %q
Secret       = "sk-test"
Models       = ["model-a"]
`, token, baseURL)
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatalf("解析测试配置失败: %v", err)
	}
	catalog := provider.NewCatalog(cfg, configPath)
	store, err := eventlog.Open(":memory:")
	if err != nil {
		t.Fatalf("打开事件日志失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv, err := NewServer(Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: dir,
		Logf:          func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("构造 API 服务失败: %v", err)
	}
	return &testEnv{store: store, catalog: catalog, srv: srv, token: token, configPath: configPath}
}

// streamingTextServer 是固定返回一段正文的假 provider。
func streamingTextServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"恢复完成。"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}
