// 真实端点冒烟测试：默认跳过，只有设置 WISP_LIVE_API_KEY 时才执行。
//
// 目的：确认工具调用的线格式与真实 OpenAI 兼容服务互认——既能解析服务端
// 返回的嵌套流式 tool_calls，也能把嵌套 tool_calls + tool 结果回传且被接受。
//
// 环境变量：
//
//	WISP_LIVE_API_KEY   API 密钥（必填，未设置则跳过）
//	WISP_LIVE_BASE_URL  基础地址，默认 https://api.deepseek.com
//	WISP_LIVE_MODEL     模型 id，默认 deepseek-flash
package provider

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveToolCallWireFormat 真实端点验证工具调用收发（需要 WISP_LIVE_API_KEY）。
func TestLiveToolCallWireFormat(t *testing.T) {
	key := os.Getenv("WISP_LIVE_API_KEY")
	if key == "" {
		t.Skip("未设置 WISP_LIVE_API_KEY，跳过真实端点测试")
	}
	baseURL := envOr("WISP_LIVE_BASE_URL", "https://api.deepseek.com")
	model := envOr("WISP_LIVE_MODEL", "deepseek-flash")

	c := &Client{BaseURL: baseURL, Secret: key, MaxRetries: 0, Timeout: 120 * time.Second}
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolDefinitionBody{
			Name:        "read_file",
			Description: "读取工作区中的文本文件",
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		},
	}}
	const prompt = "你必须调用 read_file 工具读取 a.txt，不要凭猜测回答，也不要直接回答。"

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// 1) 解析服务端返回的嵌套流式 tool_calls
	first, err := c.ChatStream(ctx, ChatRequest{
		Model:    model,
		Stream:   true,
		Messages: []Message{{Role: "user", Content: prompt}},
		Tools:    tools,
	}, nil)
	if err != nil {
		t.Fatalf("真实端点流式请求失败: %v", err)
	}
	if len(first.ToolCalls) == 0 {
		t.Fatalf("真实端点未返回工具调用（finish_reason=%q content=%q）", first.FinishReason, first.Content)
	}
	call := first.ToolCalls[0]
	if call.Function.Name != "read_file" {
		t.Fatalf("工具名解析错误（嵌套 function 未识别）: %+v", call)
	}
	if call.ID == "" || call.Type != "function" {
		t.Fatalf("工具调用 id/type 不完整: %+v", call)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("工具参数不是合法 JSON: %q (%v)", call.Function.Arguments, err)
	}
	if args.Path == "" {
		t.Fatalf("工具参数缺少 path: %q", call.Function.Arguments)
	}
	t.Logf("真实端点工具调用: id=%s name=%s args=%s", call.ID, call.Function.Name, call.Function.Arguments)

	// 2) 把嵌套 tool_calls 与工具结果回传，真实服务必须接受并给出正文
	second, err := c.ChatStream(ctx, ChatRequest{
		Model:  model,
		Stream: true,
		Messages: []Message{
			{Role: "user", Content: prompt},
			{Role: "assistant", Content: first.Content, ToolCalls: []ToolCall{call}},
			{Role: "tool", ToolCallID: call.ID, Content: `{"status":"success","output":"1\thello wisp\n"}`},
		},
		Tools: tools,
	}, nil)
	if err != nil {
		t.Fatalf("回传嵌套 tool_calls 被真实端点拒绝: %v", err)
	}
	if strings.TrimSpace(second.Content) == "" {
		text, _ := json.Marshal(second)
		t.Fatalf("回传工具结果后未得到正文: %s", text)
	}
	t.Logf("真实端点最终回复: %s", strings.TrimSpace(second.Content))
}

// envOr 读取环境变量，空则返回默认值。
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
