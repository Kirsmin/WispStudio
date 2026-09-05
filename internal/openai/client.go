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

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewClient(cfg *config.OpenAIConfig) *Client {
	seconds := cfg.TimeoutSec
	if seconds <= 0 {
		seconds = 120
	}
	headerTimeout := time.Duration(seconds) * time.Second
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	// 不设置 Client.Timeout：它会把长时间 SSE 生成误杀。只限制建连/首包。
	return &Client{client: &http.Client{Transport: transport}}
}

func (c *Client) BuildRequest(baseURL, apiKey, model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, []byte, error) {
	body := map[string]any{
		"model":          model,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	level := strings.ToLower(strings.TrimSpace(thinkingLevel))
	if level == "" {
		level = "off"
	}
	switch strings.ToLower(strings.TrimSpace(thinkingStyle)) {
	case "reasoning_effort":
		if level != "off" && level != "none" {
			body["reasoning_effort"] = level
		}
	case "enable_thinking":
		body["enable_thinking"] = level != "off" && level != "none" && level != "false"
	case "thinking_object":
		if level == "off" || level == "none" {
			body["thinking"] = map[string]any{"type": "disabled"}
		} else {
			body["thinking"] = map[string]any{"type": "enabled"}
			if level != "on" && level != "auto" {
				body["thinking"].(map[string]any)["effort"] = level
			}
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, jsonBody, nil
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
