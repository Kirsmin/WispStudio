package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	client *http.Client
}

func NewClient(timeoutSec int) *Client {
	sec := timeoutSec
	if sec <= 0 {
		sec = 120
	}
	dialTimeout := time.Duration(sec) * time.Second
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: dialTimeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{client: &http.Client{Transport: transport}}
}

type ChatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string             `json:"type"`
	Function ToolDefinitionBody `json:"function"`
}

type ToolDefinitionBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ChatRequest struct {
	Model           string           `json:"model"`
	Messages        []ChatMessage    `json:"messages"`
	Stream          bool             `json:"stream"`
	StreamOptions   *StreamOptions   `json:"stream_options,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Thinking        *ThinkingConfig  `json:"thinking,omitempty"`
	EnableThinking  *bool            `json:"enable_thinking,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	ToolChoice      any              `json:"tool_choice,omitempty"`
}

type ThinkingConfig struct {
	Type string `json:"type"`
}
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// BuildRequestWithTools 构造 OpenAI Chat Completions 兼容请求，并在需要时附带工具定义。
func (c *Client) BuildRequestWithTools(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage, tools []ToolDefinition) (*http.Request, error) {
	// 在请求边界再次规范化 Tool Schema 与消息序列，避免历史 Timeline 或新增 Tool
	// 把兼容性问题直接传给上游，最终变成难以定位的 HTTP 400。
	messages = NormalizeToolMessages(messages)
	tools = NormalizeToolDefinitions(tools)
	body := ChatRequest{
		Model: model, Messages: messages, Stream: true,
		StreamOptions: &StreamOptions{IncludeUsage: true}, Tools: tools,
	}
	if len(tools) > 0 {
		body.ToolChoice = "auto"
	}

	style := strings.ToLower(strings.TrimSpace(thinkingStyle))
	level := strings.ToLower(strings.TrimSpace(thinkingLevel))
	if level == "" {
		level = "default"
	}

	if isDeepSeekV4(model) {
		switch level {
		case "default", "off", "":
		case "none", "false":
			body.Thinking = &ThinkingConfig{Type: "disabled"}
		default:
			body.Thinking = &ThinkingConfig{Type: "enabled"}
			body.ReasoningEffort = level
		}
	} else {
		switch style {
		case "disabled":
		case "enable_thinking":
			if level != "default" {
				enabled := level != "off" && level != "none" && level != "false"
				body.EnableThinking = &enabled
			}
		default:
			if level != "default" && level != "off" {
				body.ReasoningEffort = level
			}
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

// NormalizeToolDefinitions 保证 function parameters 中的 required 永远是数组。
// 部分 OpenAI 兼容服务会严格拒绝 required:null，即使该 Tool 没有必填参数。
func NormalizeToolDefinitions(tools []ToolDefinition) []ToolDefinition {
	if len(tools) == 0 {
		return tools
	}
	out := make([]ToolDefinition, len(tools))
	copy(out, tools)
	for i := range out {
		var schema map[string]any
		if len(out[i].Function.Parameters) == 0 || json.Unmarshal(out[i].Function.Parameters, &schema) != nil {
			continue
		}
		if required, ok := schema["required"]; ok {
			if _, isArray := required.([]any); isArray {
				continue
			}
		}
		schema["required"] = []any{}
		if normalized, err := json.Marshal(schema); err == nil {
			out[i].Function.Parameters = normalized
		}
	}
	return out
}

// NormalizeToolMessages 修复 OpenAI Chat Completions 的 Tool 消息约束：
// assistant.tool_calls 后必须立刻跟齐对应的 tool 消息。历史版本曾把多个
// ToolCall 拆成多条 assistant 消息，导致上游报 “insufficient tool messages”。
// 这里按 tool_call_id 配对并把结果贴回调用之后；没有结果的孤立调用不会发送给模型。
func NormalizeToolMessages(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}
	toolMessages := map[string][]int{}
	for i, message := range messages {
		if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			toolMessages[message.ToolCallID] = append(toolMessages[message.ToolCallID], i)
		}
	}

	used := map[int]bool{}
	out := make([]ChatMessage, 0, len(messages))
	for i, message := range messages {
		if used[i] {
			continue
		}
		if message.Role == "tool" {
			// Tool 结果统一由对应 assistant.tool_calls 分支输出；孤立结果直接忽略。
			continue
		}
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			out = append(out, message)
			continue
		}

		calls := make([]ToolCall, 0, len(message.ToolCalls))
		results := make([]ChatMessage, 0, len(message.ToolCalls))
		resultIndexes := make([]int, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			match := -1
			for _, candidate := range toolMessages[call.ID] {
				if candidate > i && !used[candidate] {
					match = candidate
					break
				}
			}
			if match < 0 {
				continue
			}
			calls = append(calls, call)
			results = append(results, messages[match])
			resultIndexes = append(resultIndexes, match)
		}

		if len(calls) == 0 {
			if strings.TrimSpace(message.Content) != "" {
				message.ToolCalls = nil
				out = append(out, message)
			}
			continue
		}
		message.ToolCalls = calls
		out = append(out, message)
		for j, result := range results {
			used[resultIndexes[j]] = true
			out = append(out, result)
		}
	}
	return out
}

func isDeepSeekV4(model string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(id, "deepseek-v4-")
}

func (c *Client) DoStream(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return nil, fmt.Errorf("上游返回 HTTP %d: %s", resp.StatusCode, message)
}
