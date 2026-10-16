// 真实端点端到端测试：默认跳过，只有设置 WISP_LIVE_API_KEY 时才执行。
// 用真实模型跑完整个 HTTP API 链路：启动回合 → 事件流轮询 → 工具调用
// （read_file）→ 回合完成。
//
// 环境变量：WISP_LIVE_API_KEY（必填）、WISP_LIVE_BASE_URL、WISP_LIVE_MODEL。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
)

// TestLiveAPITurnWithRealModel 真实模型端到端（需要 WISP_LIVE_API_KEY）。
func TestLiveAPITurnWithRealModel(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("WISP_LIVE_API_KEY"))
	if key == "" {
		t.Skip("未设置 WISP_LIVE_API_KEY，跳过真实端点测试")
	}
	baseURL := envOrLive("WISP_LIVE_BASE_URL", "https://api.deepseek.com")
	modelID := envOrLive("WISP_LIVE_MODEL", "deepseek-flash")

	store, err := eventlog.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("打开事件日志失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello wisp\n"), 0o644); err != nil {
		t.Fatalf("准备工作区失败: %v", err)
	}

	cfg := &config.Config{Providers: []config.ProviderConfig{{
		ProviderID: "live",
		BaseURL:    baseURL,
		Secret:     key,
		Models:     []config.ModelConfig{{ModelID: modelID}},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	fullID := provider.Join("live", modelID)
	srv, err := NewServer(Config{
		Store:         store,
		Catalog:       provider.NewCatalog(cfg, ""),
		WorkspaceRoot: ws,
		DefaultModel:  fullID,
		TurnTimeout:   3 * time.Minute,
		Logf:          func(msg string, args ...any) { t.Logf(msg, args...) },
	})
	if err != nil {
		t.Fatalf("构造 API 服务失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	code, raw := request(t, http.MethodPost, ts.URL+"/api/turns", map[string]any{
		"text":  "你必须调用 read_file 工具读取 a.txt，不要凭猜测回答，也不要直接回答。",
		"model": fullID.String(),
	})
	if code != http.StatusCreated {
		t.Fatalf("启动回合失败: %d %s", code, raw)
	}
	var started startTurnWire
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatalf("解析启动响应失败: %v: %s", err, raw)
	}

	// 轮询回合状态直到进入终态
	deadline := time.Now().Add(3 * time.Minute)
	var st statusWire
	for time.Now().Before(deadline) {
		st = turnStatus(t, ts.URL, started.TurnID)
		if st.Ref == "completed" || strings.HasPrefix(st.Ref, "failed:") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if st.Ref != "completed" {
		t.Fatalf("回合未正常完成: ref=%q error=%q", st.Ref, st.Error)
	}
	if st.Model != fullID.String() {
		t.Fatalf("状态模型 = %q, 期望 %q", st.Model, fullID)
	}

	// 事件流里必须出现真实的工具调用往返
	evs := sessionEvents(t, ts.URL, started.SessionID)
	var calledName, toolResult string
	var sawRequested, sawResponded bool
	for _, ev := range evs.Events {
		switch ev.Type {
		case "tool.called":
			var attrs struct {
				Name       string `json:"name"`
				ProviderID string `json:"provider_id"`
			}
			_ = json.Unmarshal(ev.Attrs, &attrs)
			calledName = attrs.Name
			if attrs.ProviderID == "" {
				t.Fatalf("tool.called 缺少 provider_id: %s", ev.Attrs)
			}
		case "tool.returned":
			toolResult = ev.BlobText
		case "llm.requested":
			var attrs struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(ev.Attrs, &attrs)
			if attrs.Model != fullID.String() {
				t.Fatalf("llm.requested 模型 = %q, 期望 %q", attrs.Model, fullID)
			}
			sawRequested = true
		case "llm.responded":
			sawResponded = true
		}
	}
	if calledName != "read_file" {
		t.Fatalf("真实模型未调用 read_file: %q (事件 %d 条)", calledName, len(evs.Events))
	}
	if !strings.Contains(toolResult, "hello wisp") {
		t.Fatalf("工具结果未包含文件内容: %q", toolResult)
	}
	if !sawRequested || !sawResponded {
		t.Fatalf("缺少 llm.requested/llm.responded: %v %v", sawRequested, sawResponded)
	}
	t.Logf("真实端点回合完成: turn=%s 事件=%d 条, 工具结果=%s",
		started.TurnID, len(evs.Events), strings.TrimSpace(toolResult))
}

// envOrLive 读取环境变量，空则返回默认值。
func envOrLive(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
