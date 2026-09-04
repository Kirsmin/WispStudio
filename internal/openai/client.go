package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"pylai/internal/config"
)

type Client struct {
	cfg    *config.OpenAIConfig
	client *http.Client
}

func NewClient(cfg *config.OpenAIConfig) *Client {
	return &Client{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSec) * time.Second,
		},
	}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model          string        `json:"model"`
	Messages       []ChatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	StreamOptions  *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	EnableThinking interface{}   `json:"enable_thinking,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (c *Client) BuildRequest(model, thinkingStyle, thinkingLevel string, messages []ChatMessage) (*http.Request, error) {
	body := ChatRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}

	// thinking_style 映射
	switch thinkingStyle {
	case "reasoning_effort":
		if thinkingLevel != "off" {
			body.ReasoningEffort = thinkingLevel
		}
	case "enable_thinking":
		body.EnableThinking = thinkingLevel != "off"
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := c.cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

func (c *Client) DoStream(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	return c.client.Do(req)
}
