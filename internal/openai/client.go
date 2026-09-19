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

	"wisp/internal/config"
)

type Client struct {
	cfg    *config.OpenAIConfig
	client *http.Client
}

func NewClient(cfg *config.OpenAIConfig) *Client {
	sec := cfg.TimeoutSec
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
	return &Client{cfg: cfg, client: &http.Client{Transport: transport}}
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
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

func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	return c.BuildRequestWithTools(baseURL, apiKey, model, thinkingStyle, thinkingLevel, messages, nil)
}

// BuildRequestWithTools 构造 OpenAI Chat Completions 兼容请求，并在需要时附带工具定义。
func (c *Client) BuildRequestWithTools(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage, tools []ToolDefinition) (*http.Request, error) {
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
