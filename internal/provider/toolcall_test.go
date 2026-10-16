// 工具调用线格式验收测试：确认 tool_calls 是 OpenAI 兼容的嵌套 function
// 结构（请求方向），并能解析真实服务返回的嵌套流式增量（响应方向），
// 同时宽容接受扁平写法与 arguments 直接为对象的非标准实现。
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestToolCallNestedRequestWireFormat 校验发给 provider 的 tool_calls 是
// 标准嵌套 function 结构：顶层只有 id/type/function，不得出现 name/arguments。
func TestToolCallNestedRequestWireFormat(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 0}
	if _, err := c.Chat(context.Background(), ChatRequest{
		Model: "m",
		Messages: []Message{{
			Role:    "assistant",
			Content: "调用工具",
			ToolCalls: []ToolCall{{
				ID:       "call_1",
				Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":"a.txt"}`},
			}},
		}},
	}); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	// 1) 顶层不得出现 name / arguments
	var loose struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v\n%s", err, raw)
	}
	if len(loose.Messages) != 1 {
		t.Fatalf("消息数 = %d, 期望 1", len(loose.Messages))
	}
	if _, ok := loose.Messages[0]["name"]; ok {
		t.Fatalf("tool_calls 顶层不得出现 name: %s", raw)
	}
	if _, ok := loose.Messages[0]["arguments"]; ok {
		t.Fatalf("tool_calls 顶层不得出现 arguments: %s", raw)
	}

	// 2) 嵌套 function 结构完整，type 缺省补 "function"
	var typed struct {
		Messages []struct {
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
	tcs := typed.Messages[0].ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("tool_calls 数 = %d, 期望 1: %s", len(tcs), raw)
	}
	if tcs[0].ID != "call_1" || tcs[0].Type != "function" {
		t.Fatalf("tool_calls[0] id/type 不正确: %+v", tcs[0])
	}
	if tcs[0].Function.Name != "read_file" || tcs[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("tool_calls[0].function 不正确: %+v", tcs[0].Function)
	}
}

// TestChatStreamNestedToolCalls 校验真实服务形态的嵌套流式增量能被完整聚合：
// 首帧给出 id/type/function.name，后续帧只累加 function.arguments。
func TestChatStreamNestedToolCalls(t *testing.T) {
	frames := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":null,"reasoning_content":"The user"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":null,"tool_calls":[{"index":0,"id":"call_00_x","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":299,"completion_tokens":58,"total_tokens":357}}`,
	}
	srv := sseServer(t, frames)
	defer srv.Close()

	var deltas []StreamDelta
	c := &Client{BaseURL: srv.URL, MaxRetries: 0}
	res, err := c.ChatStream(context.Background(), ChatRequest{
		Model:    "deepseek-flash",
		Stream:   true,
		Messages: []Message{{Role: "user", Content: "读 a.txt"}},
	}, func(d StreamDelta) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	if res.Content != "" || res.Reasoning != "The user" {
		t.Fatalf("正文/推理聚合错误: content=%q reasoning=%q", res.Content, res.Reasoning)
	}
	if res.FinishReason != "tool_calls" || res.Usage.TotalTokens != 357 {
		t.Fatalf("finish_reason/usage 错误: %q %+v", res.FinishReason, res.Usage)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("工具调用数 = %d, 期望 1: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	got := res.ToolCalls[0]
	want := ToolCall{ID: "call_00_x", Type: "function",
		Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":"a.txt"}`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("工具调用聚合错误:\n实际 %+v\n期望 %+v", got, want)
	}

	// onDelta 透传的增量同样已完成嵌套解析（name 在首帧，arguments 分片）
	var sawName, sawArgChunk bool
	for _, d := range deltas {
		for _, tc := range d.ToolCalls {
			if tc.Function.Name == "read_file" {
				sawName = true
			}
			if tc.Function.Arguments != "" && tc.Function.Name == "" {
				sawArgChunk = true
			}
		}
	}
	if !sawName || !sawArgChunk {
		t.Fatalf("增量透传丢失嵌套字段: sawName=%v sawArgChunk=%v", sawName, sawArgChunk)
	}
}

// TestChatStreamFlatToolCallsTolerated 校验非标准实现的扁平写法仍能解析。
func TestChatStreamFlatToolCallsTolerated(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_f","type":"function","name":"list_files","arguments":"{\"path\":\".\"}"}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 0}
	res, err := c.Chat(context.Background(), ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "list_files" ||
		res.ToolCalls[0].Function.Arguments != `{"path":"."}` {
		t.Fatalf("扁平写法应被宽容解析: %+v", res.ToolCalls)
	}
}

// TestChatNonStreamNestedToolCalls 校验非流式响应中的嵌套 tool_calls，
// 以及 arguments 直接给对象（非规范但常见）的宽容处理。
func TestChatNonStreamNestedToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"",
			"tool_calls":[{"id":"call_n","type":"function","function":{"name":"read_file","arguments":{"path":"a.txt"}}}]}}]}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, MaxRetries: 0}
	res, err := c.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("工具调用数 = %d, 期望 1", len(res.ToolCalls))
	}
	if got := res.ToolCalls[0]; got.Function.Name != "read_file" || got.Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("嵌套非流式解析错误: %+v", got)
	}
}

// TestToolCallMarshalDefaultsType 校验序列化永远补全 type="function"。
func TestToolCallMarshalDefaultsType(t *testing.T) {
	raw, err := json.Marshal(ToolCall{ID: "x", Function: ToolCallFunction{Name: "n"}})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"function"`) {
		t.Fatalf("缺少 type: %s", raw)
	}
	if strings.Contains(string(raw), `"name":"n"`) == false {
		t.Fatalf("name 未嵌套在 function 内: %s", raw)
	}
}

// TestCatalogEndpointAndDefaultModel 校验模型到连接参数的解析与默认模型选择。
func TestCatalogEndpointAndDefaultModel(t *testing.T) {
	c := newTestCatalog(t)

	ep, err := c.Endpoint("deepseek/deepseek-chat")
	if err != nil {
		t.Fatalf("Endpoint 失败: %v", err)
	}
	if ep.ProviderID != "deepseek" || ep.ModelID != "deepseek-chat" ||
		ep.BaseURL != "https://api.deepseek.com" || ep.FullID != "deepseek/deepseek-chat" {
		t.Fatalf("Endpoint 不正确: %+v", ep)
	}

	def, err := c.DefaultModel()
	if err != nil {
		t.Fatalf("DefaultModel 失败: %v", err)
	}
	if def != "deepseek/deepseek-chat" {
		t.Fatalf("默认模型 = %q, 期望 deepseek/deepseek-chat", def)
	}

	for _, bad := range []ModelFullID{"deepseek/nope", "badid", "nope/m"} {
		if _, err := c.Endpoint(bad); err == nil {
			t.Fatalf("Endpoint(%q) 应报错", bad)
		}
	}
}

// sseServer 用给定帧启动一个 SSE 假服务。
func sseServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}
