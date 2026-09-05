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

	// 不给 http.Client 设置整体 Timeout：长时间流式输出不能被固定总时长切断。
	// 这里只限制建连、TLS 握手与等待首个响应头的时间。
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: dialTimeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		cfg:    cfg,
		client: &http.Client{Transport: transport},
	}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model           string          `json:"model"`
	Messages        []ChatMessage   `json:"messages"`
	Stream          bool            `json:"stream"`
	StreamOptions   *StreamOptions  `json:"stream_options,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Thinking        *ThinkingConfig `json:"thinking,omitempty"`
	EnableThinking  *bool           `json:"enable_thinking,omitempty"`
}

type ThinkingConfig struct {
	Type string `json:"type"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// BuildRequest 构造 OpenAI Chat Completions 兼容请求。
//
// thinkingLevel 的语义：
//   - "default" / "off" / 空字符串：不发送 OpenAI reasoning_effort，保持服务端默认值；
//   - none/minimal/low/medium/high/xhigh/max：按 OpenAI v1 Chat Completions 的
//     reasoning_effort 参数发送；
//   - thinkingStyle == "enable_thinking"：兼容部分 OpenAI-compatible 网关的
//     enable_thinking 布尔扩展。
//
// 旧配置里 thinking_style = "none" 不再强制把 UI 锁死为“关闭”：当用户显式
// 选择了 OpenAI reasoning effort 时，仍按标准参数发送。若某模型不支持该档位，
// 上游应返回 4xx，DoStream 会把原始错误内容返回给前端，而不是表现为“永远等待”。
func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}

	style := strings.ToLower(strings.TrimSpace(thinkingStyle))
	level := strings.ToLower(strings.TrimSpace(thinkingLevel))
	if level == "" {
		level = "default"
	}

	// DeepSeek V4 的 OpenAI-compatible Chat Completions 同时支持：
	//   thinking: {type: enabled/disabled} + reasoning_effort
	// 其中 none/关闭应通过 thinking.type=disabled 表达，而不是把 none 直接塞给
	// reasoning_effort。这样 deepseek-v4-flash / deepseek-v4-pro 能正确切换思考模式。
	if isDeepSeekV4(model) {
		switch level {
		case "default", "off", "":
			// 保持服务端默认（DeepSeek 当前默认开启思考，effort=high）。
		case "none", "false":
			body.Thinking = &ThinkingConfig{Type: "disabled"}
		default:
			body.Thinking = &ThinkingConfig{Type: "enabled"}
			body.ReasoningEffort = level
		}
	} else {
		switch style {
		case "disabled":
			// 显式禁用思考参数。用于确知某个兼容网关完全不接受相关字段的模型。
		case "enable_thinking":
			if level != "default" {
				enabled := level != "off" && level != "none" && level != "false"
				body.EnableThinking = &enabled
			}
		default:
			// OpenAI Chat Completions 标准字段。"default"/"off" 都表示省略该参数，
			// 而不是强行发送 none；因为支持的 effort 是 model-dependent。
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

	// 非 2xx 不能交给 SSE parser。很多 OpenAI-compatible 服务会返回普通 JSON 错误；
	// 若继续当 SSE 读，会得到“没有任何 delta，然后正常 done”的假象。
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return nil, fmt.Errorf("上游返回 HTTP %d: %s", resp.StatusCode, message)
}
