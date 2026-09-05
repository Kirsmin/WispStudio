package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}
type StreamEvent struct {
	Type   string
	Text   string
	Usage  *Usage
	Finish string
	Error  string
}
type StreamReader struct{ scanner *bufio.Scanner }

func NewStreamReader(r io.Reader) *StreamReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &StreamReader{s}
}
func (sr *StreamReader) ReadEvents(ch chan<- StreamEvent) {
	defer close(ch)
	finish := ""
	for sr.scanner.Scan() {
		line := strings.TrimSuffix(sr.scanner.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content          json.RawMessage `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
					Thinking         string          `json:"thinking"`
					Analysis         string          `json:"analysis"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				PromptDetails    *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionDetails *struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue
		}
		if c.Error != nil {
			ch <- StreamEvent{Type: "error", Error: fmt.Sprint(c.Error)}
			finish = "error"
			continue
		}
		for _, choice := range c.Choices {
			if t := rawText(choice.Delta.Content); t != "" {
				ch <- StreamEvent{Type: "delta", Text: t}
			}
			r := first(choice.Delta.ReasoningContent, choice.Delta.Reasoning, choice.Delta.Thinking, choice.Delta.Analysis)
			if r != "" {
				ch <- StreamEvent{Type: "reasoning", Text: r}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
		if c.Usage != nil {
			u := &Usage{PromptTokens: c.Usage.PromptTokens, CompletionTokens: c.Usage.CompletionTokens}
			if c.Usage.PromptDetails != nil {
				u.CachedTokens = c.Usage.PromptDetails.CachedTokens
			}
			if c.Usage.CompletionDetails != nil {
				u.ReasoningTokens = c.Usage.CompletionDetails.ReasoningTokens
			}
			ch <- StreamEvent{Type: "usage", Usage: u}
		}
	}
	if err := sr.scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: "读取流失败: " + err.Error()}
		return
	}
	if finish == "" {
		finish = "stop"
	}
	ch <- StreamEvent{Type: "done", Finish: finish}
}
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(first(p.Text, p.Content))
		}
		return b.String()
	}
	return ""
}
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
