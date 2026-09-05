package openai

import (
	"bytes"
	"context"
	"encoding/json"
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

	// 注意：不能给 http.Client 设整体 Timeout —— 它会覆盖整个响应体读取过程，
	// 长流式回复（reasoner 动辄几分钟）会被硬切断。
	// 这里只在传输层限制建连与等待首个响应头的时间，流式时长交由调用方 context 控制。
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: dialTimeout,
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
	Model           string         `json:"model"`
	Messages        []ChatMessage  `json:"messages"`
	Stream          bool           `json:"stream"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	EnableThinking  interface{}    `json:"enable_thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// BuildRequest 构造上游请求；baseURL / apiKey 传模型生效后的值
func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}

	// 只在 thinkingLevel 不为 off 时发送推理参数
	if thinkingLevel != "off" && thinkingLevel != "" {
		switch thinkingStyle {
		case "enable_thinking":
			body.EnableThinking = true
		case "reasoning_effort":
			body.ReasoningEffort = thinkingLevel
		default:
			// 默认使用 reasoning_effort（OpenAI 标准）
			body.ReasoningEffort = thinkingLevel
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

func (c *Client) DoStream(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	return c.client.Do(req)
}
