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
	seconds := cfg.TimeoutSec
	if seconds <= 0 {
		seconds = 120
	}
	timeout := time.Duration(seconds) * time.Second
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
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

func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{Model: model, Messages: messages, Stream: true, StreamOptions: &StreamOptions{IncludeUsage: true}}
	level := strings.ToLower(strings.TrimSpace(thinkingLevel))
	if level == "" {
		level = "off"
	}
	switch thinkingStyle {
	case "enable_thinking":
		enabled := level != "off" && level != "none" && level != "false"
		body.EnableThinking = &enabled
	case "reasoning_effort":
		if level != "off" && level != "none" {
			body.ReasoningEffort = level
		}
	case "reasoning_effort_none":
		if level == "off" {
			body.ReasoningEffort = "none"
		} else {
			body.ReasoningEffort = level
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
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "text/event-stream")
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return nil, fmt.Errorf("上游返回 HTTP %d: %s", resp.StatusCode, message)
}
