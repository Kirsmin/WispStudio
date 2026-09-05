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
	client *http.Client
}

func NewClient(cfg *config.OpenAIConfig) *Client {
	sec := cfg.TimeoutSec
	if sec <= 0 {
		sec = 120
	}
	dialTimeout := time.Duration(sec) * time.Second
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: dialTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
	}
	// 不设置 http.Client.Timeout：长时间流式响应由每次生成的 context 控制。
	return &Client{client: &http.Client{Transport: transport}}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatRequest struct {
	Model           string         `json:"model"`
	Messages        []ChatMessage  `json:"messages"`
	Stream          bool           `json:"stream"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	EnableThinking  *bool          `json:"enable_thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{
		Model: model, Messages: messages, Stream: true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}
	level := strings.TrimSpace(strings.ToLower(thinkingLevel))
	if level != "" && level != "off" && thinkingStyle != "none" {
		switch thinkingStyle {
		case "enable_thinking":
			enabled := level != "off" && level != "false" && level != "0"
			body.EnableThinking = &enabled
		case "reasoning_effort":
			body.ReasoningEffort = thinkingLevel
		}
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/chat/completions"
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("无效的 base_url: %s", baseURL)
	}
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

type UpstreamError struct {
	StatusCode int
	Message    string
}

func (e *UpstreamError) Error() string {
	if e.StatusCode == 0 {
		return e.Message
	}
	return fmt.Sprintf("上游 HTTP %d: %s", e.StatusCode, e.Message)
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	message := strings.TrimSpace(string(body))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.Error.Message != "" {
			message = parsed.Error.Message
		} else if parsed.Message != "" {
			message = parsed.Message
		}
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return nil, &UpstreamError{StatusCode: resp.StatusCode, Message: message}
}
