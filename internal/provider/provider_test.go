// 验收测试：流式多 chunk、reasoning 原样透传、429 重试、错误分类、Catalog 缺字段明确错误。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"wisp/internal/config"
)

// 校验流式多 chunk 聚合：正文、推理内容、usage、finish_reason 都应完整累加。
func TestChatStreamMultiChunk(t *testing.T) {
	var gotReq ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"想"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"好了"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Secret: "sk-test", MaxRetries: 0}
	res, err := c.Chat(context.Background(), ChatRequest{
		Model:           "deepseek-chat",
		Stream:          true,
		ReasoningEffort: "high", // 应原样出现在请求体中
		Messages:        []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if res.Content != "你好" {
		t.Errorf("正文聚合错误: %q", res.Content)
	}
	if res.Reasoning != "想好了" {
		t.Errorf("推理内容聚合错误: %q", res.Reasoning)
	}
	if res.Usage.TotalTokens != 7 || res.Usage.PromptTokens != 3 || res.Usage.CompletionTokens != 4 {
		t.Errorf("usage 聚合错误: %+v", res.Usage)
	}
	if res.FinishReason != "stop" {
		t.Errorf("finish_reason 错误: %q", res.FinishReason)
	}
	// reasoning_effort 原样透传，不做枚举转换
	if gotReq.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort 未透传: %q", gotReq.ReasoningEffort)
	}
	if !gotReq.Stream {
		t.Error("stream 标志未透传")
	}
}

// 校验非流式响应的 reasoning_content 原样聚合返回。
func TestChatNonStreamReasoningPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"答","reasoning_content":"思考过程"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 0}
	res, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "q"}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if res.Reasoning != "思考过程" || res.Content != "答" {
		t.Errorf("透传错误: content=%q reasoning=%q", res.Content, res.Reasoning)
	}
}

// 校验 429 触发重试并最终成功；MaxRetries=1 表示最多重试一次。
func TestChatRetryOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 1}
	res, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "q"}},
	})
	if err != nil {
		t.Fatalf("重试后仍失败: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("内容错误: %q", res.Content)
	}
	if calls.Load() != 2 {
		t.Errorf("应请求 2 次（429 + 200），实际 %d 次", calls.Load())
	}
}

// 校验 5xx 也按可重试处理：重试耗尽后返回 KindServer 错误。
func TestChatRetryExhaustedOnServerError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 2} // 共 3 次尝试
	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "q"}},
	})
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("应返回 *Error，实际 %T: %v", err, err)
	}
	if pe.Kind != KindServer || !pe.Retryable || pe.StatusCode != 500 {
		t.Errorf("错误分类错误: %+v", pe)
	}
	if calls.Load() != 3 {
		t.Errorf("应尝试 3 次，实际 %d 次", calls.Load())
	}
}

// 校验错误分类：状态码与 ctx 取消都映射到正确的 ErrorKind，且不再重试。
func TestChatErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		wantKind ErrorKind
	}{
		{"认证失败 401", 401, KindAuth},
		{"认证失败 403", 403, KindAuth},
		{"限流 429", 429, KindRateLimit},
		{"请求错误 400", 400, KindRequest},
		{"请求错误 404", 404, KindRequest},
		{"服务端 500", 500, KindServer},
		{"服务端 502", 502, KindServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"x"}`)
			}))
			defer srv.Close()

			c := &Client{BaseURL: srv.URL, MaxRetries: 0} // 关闭重试，验证单次分类
			_, err := c.Chat(context.Background(), ChatRequest{
				Model:    "m",
				Messages: []Message{{Role: "user", Content: "q"}},
			})
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("应返回 *Error，实际 %T: %v", err, err)
			}
			if pe.Kind != tc.wantKind {
				t.Errorf("Kind=%v，期望 %v", pe.Kind, tc.wantKind)
			}
			if pe.StatusCode != tc.status {
				t.Errorf("StatusCode=%d，期望 %d", pe.StatusCode, tc.status)
			}
			// 非可重试错误（401/403/4xx 非 429）不得重试；429/5xx 在 MaxRetries=0 时也不重试
			if calls.Load() != 1 {
				t.Errorf("应只请求 1 次，实际 %d 次", calls.Load())
			}
		})
	}

	t.Run("ctx 取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := &Client{BaseURL: "http://127.0.0.1:1", MaxRetries: 3}
		_, err := c.Chat(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}})
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != KindCanceled {
			t.Errorf("应分类为 KindCanceled，实际 %v (%v)", err, pe)
		}
	})
}

// newTestCatalog 构造一份双 provider 的测试目录。
func newTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{
				ProviderID:   "deepseek",
				ProviderName: "深度求索",
				BaseURL:      "https://api.deepseek.com",
				Models: []config.ModelConfig{
					{ModelID: "deepseek-chat", ModelAlias: "DeepSeek 聊天"},
				},
				Discovery: config.DiscoveryConfig{Enabled: true, Path: "/models"},
			},
			{
				ProviderID: "openai",
				BaseURL:    "https://api.openai.com",
				Models:     []config.ModelConfig{{ModelID: "gpt-4o"}},
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{ProviderID: "openai", ModelID: "gpt-4o", Alias: "GPT-4o"},
		},
	}
	return NewCatalog(cfg, "")
}

// 校验 Catalog 在配置缺字段/缺条目时给出明确错误，而不是空引用或含糊失败。
func TestCatalogClearErrors(t *testing.T) {
	c := newTestCatalog(t)

	// 全称 id 格式错误
	if _, err := c.Get("badid"); err == nil || !strings.Contains(err.Error(), "{ProviderID}/{ModelID}") {
		t.Errorf("格式错误应给出明确提示，实际: %v", err)
	}
	// provider 不存在：错误应列出已配置 id
	if _, err := c.Get("anthropic/claude"); err == nil ||
		!strings.Contains(err.Error(), `provider 不存在`) ||
		!strings.Contains(err.Error(), "deepseek") || !strings.Contains(err.Error(), "openai") {
		t.Errorf("provider 不存在应列出已配置 id，实际: %v", err)
	}
	// 模型不存在：错误应列出可用模型
	if _, err := c.Get("deepseek/nope"); err == nil ||
		!strings.Contains(err.Error(), "模型不存在") ||
		!strings.Contains(err.Error(), "deepseek-chat") {
		t.Errorf("模型不存在应列出可用模型，实际: %v", err)
	}
	if _, err := c.ListByProvider("nope"); err == nil ||
		!strings.Contains(err.Error(), "provider 不存在") {
		t.Errorf("ListByProvider 应给出明确错误，实际: %v", err)
	}
	// 别名解析：命中与未命中
	if id, err := c.ResolveAlias("深度求索"); err != nil || id != "deepseek" {
		t.Errorf("别名应解析为 deepseek，实际 %q, %v", id, err)
	}
	if _, err := c.ResolveAlias("不存在"); err == nil || !strings.Contains(err.Error(), "别名不存在") {
		t.Errorf("未命中别名应给出明确错误，实际: %v", err)
	}
	// AddModel 到未知 provider 应报错而非 panic
	if err := c.AddModel("nope", "m", ""); err == nil ||
		!strings.Contains(err.Error(), "provider 不存在") {
		t.Errorf("AddModel 未知 provider 应给出明确错误，实际: %v", err)
	}
	// 未绑定配置文件路径时 AddModel 不能写回
	if err := c.AddModel("openai", "gpt-5", ""); err == nil ||
		!strings.Contains(err.Error(), "无法写回") {
		t.Errorf("无路径时应明确拒绝写回，实际: %v", err)
	}
}

// 校验手动 Models 优先、发现结果补齐去重、别名正确解析。
func TestCatalogManualPreferredAndAlias(t *testing.T) {
	c := newTestCatalog(t)

	m, err := c.Get("deepseek/deepseek-chat")
	if err != nil {
		t.Fatalf("Get 手动模型失败: %v", err)
	}
	if m.Discovered {
		t.Error("手动 Models 中的同名模型不应标记为发现")
	}
	if m.Alias != "DeepSeek 聊天" {
		t.Errorf("手动别名优先，实际 %q", m.Alias)
	}

	m, err = c.Get("openai/gpt-4o")
	if err != nil {
		t.Fatalf("Get 别名模型失败: %v", err)
	}
	if m.Alias != "GPT-4o" {
		t.Errorf("ModelAliases 别名解析错误: %q", m.Alias)
	}
}

// 校验 DiscoverAll 合并 /models 发现结果：手动已有项不重复，新项标记 Discovered，失败只记日志。
func TestDiscoverAllMerges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("发现路径错误: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("发现请求缺少鉴权头: %q", auth)
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"},{"id":""},{"id":"deepseek-reasoner"}]}`)
	}))
	defer srv.Close()

	cfg := &config.Config{Providers: []config.ProviderConfig{
		{
			ProviderID: "deepseek", BaseURL: srv.URL, Secret: "sk-test",
			Models:    []config.ModelConfig{{ModelID: "deepseek-chat"}},
			Discovery: config.DiscoveryConfig{Enabled: true, Path: "/models"},
		},
		{
			ProviderID: "openai", BaseURL: srv.URL, Secret: "sk-test",
			Models:    []config.ModelConfig{{ModelID: "gpt-4o"}},
			Discovery: config.DiscoveryConfig{Enabled: true, Path: "/models"},
		},
		{
			// 发现接口不可达的 provider：失败只记日志，不影响其他 provider
			ProviderID: "broken", BaseURL: "http://127.0.0.1:1",
			Discovery: config.DiscoveryConfig{Enabled: true, Path: "/models"},
		},
	}}
	c := NewCatalog(cfg, "")
	var logs []string
	c.DiscoverAll(context.Background(), func(msg string, args ...any) { logs = append(logs, fmt.Sprintf(msg, args...)) })

	models, err := c.ListByProvider("deepseek")
	if err != nil {
		t.Fatalf("ListByProvider 失败: %v", err)
	}
	if len(models) != 2 { // deepseek-chat（手动）+ deepseek-reasoner（发现），去重后 2 个
		t.Fatalf("应合并出 2 个模型，实际 %d: %+v", len(models), models)
	}
	if models[0].FullID != "deepseek/deepseek-chat" || models[0].Discovered {
		t.Errorf("手动模型应在前且非发现: %+v", models[0])
	}
	if models[1].FullID != "deepseek/deepseek-reasoner" || !models[1].Discovered {
		t.Errorf("发现模型应补齐且标记 Discovered: %+v", models[1])
	}
	// 发现失败的 provider 有日志记录
	foundLog := false
	for _, l := range logs {
		if strings.Contains(l, "broken") {
			foundLog = true
		}
	}
	if !foundLog {
		t.Errorf("失败 provider 应产生日志，实际: %v", logs)
	}
}
