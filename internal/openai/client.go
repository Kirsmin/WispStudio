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

type Client struct{ client *http.Client }

func NewClient(cfg *config.OpenAIConfig) *Client {
	t := time.Duration(cfg.TimeoutSec) * time.Second
	if t <= 0 {
		t = 120 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: t,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	// 不设置 Client.Timeout，避免把长时间 SSE 生成硬截断；TimeoutSec 只约束等待响应头。
	return &Client{client: &http.Client{Transport: transport}}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatRequest struct {
	Model           string            `json:"model"`
	Messages        []ChatMessage     `json:"messages"`
	Stream          bool              `json:"stream"`
	StreamOptions   *StreamOptions    `json:"stream_options,omitempty"`
	EnableThinking  *bool             `json:"enable_thinking,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Reasoning       map[string]string `json:"reasoning,omitempty"`
}
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (c *Client) BuildRequest(baseURL, apiKey, model, style, level string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{Model: model, Messages: messages, Stream: true, StreamOptions: &StreamOptions{IncludeUsage: true}}
	if level == "" {
		level = "off"
	}
	switch style {
	case "enable_thinking":
		v := level != "off"
		body.EnableThinking = &v
	case "reasoning_effort":
		if level != "off" {
			body.ReasoningEffort = level
		}
	case "openrouter_reasoning":
		if level != "off" {
			body.Reasoning = map[string]string{"effort": level}
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	return nil, fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}
