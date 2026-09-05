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
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	headerTimeout := time.Duration(timeoutSec) * time.Second
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   headerTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	// 不设置整体 Timeout，避免长时间流式响应被 http.Client 主动切断。
	return &Client{client: &http.Client{Transport: transport}}
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
	EnableThinking  *bool          `json:"enable_thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// BuildRequest 构造 OpenAI 兼容的聊天请求。
// "off" 对 reasoning_effort 表示不发送该参数；enable_thinking 则显式发送 false。
func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("Provider 缺少 base_url")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("缺少模型 ID")
	}

	body := ChatRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}
	level := strings.ToLower(strings.TrimSpace(thinkingLevel))
	if level == "" {
		level = "off"
	}
	switch strings.ToLower(strings.TrimSpace(thinkingStyle)) {
	case "enable_thinking":
		enabled := level != "off" && level != "none" && level != "false"
		body.EnableThinking = &enabled
	case "reasoning_effort":
		if level != "off" && level != "none" {
			body.ReasoningEffort = level
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := baseURL + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
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
