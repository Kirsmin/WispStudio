package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type StreamEvent struct {
	Type   string
	Text   string
	Usage  *Usage
	Finish string
	Error  string
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type StreamReader struct{ scanner *bufio.Scanner }

func NewStreamReader(r io.Reader) *StreamReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &StreamReader{scanner: scanner}
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

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// 某些兼容网关会在 SSE 中返回 JSON 错误对象。
			var upstreamErr struct {
				Error   any    `json:"error"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(data), &upstreamErr) == nil && (upstreamErr.Error != nil || upstreamErr.Message != "") {
				ch <- StreamEvent{Type: "error", Error: firstNonEmpty(upstreamErr.Message, fmt.Sprint(upstreamErr.Error))}
				finish = "error"
			}
			continue
		}
		if chunk.Error != nil {
			ch <- StreamEvent{Type: "error", Error: errorMessage(chunk.Error)}
			finish = "error"
			continue
		}
		for _, choice := range chunk.Choices {
			if text := rawText(choice.Delta.Content); text != "" {
				ch <- StreamEvent{Type: "delta", Text: text}
			}
			reasoning := firstNonEmpty(choice.Delta.ReasoningContent, choice.Delta.Reasoning, choice.Delta.Thinking, choice.Delta.Analysis)
			if reasoning != "" {
				ch <- StreamEvent{Type: "reasoning", Text: reasoning}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			u := &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, ReasoningTokens: chunk.Usage.ReasoningTokens}
			if chunk.Usage.PromptTokensDetails != nil {
				u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if chunk.Usage.CompletionTokensDetails != nil && chunk.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
				u.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
			ch <- StreamEvent{Type: "usage", Usage: u}
		}
	}
	if err := sr.scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("读取流失败: %v", err)}
		return
	}
	if finish == "" {
		finish = "stop"
	}
	ch <- StreamEvent{Type: "done", Finish: finish}
}

type streamChunk struct {
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
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		ReasoningTokens         int `json:"reasoning_tokens"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error any `json:"error"`
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(firstNonEmpty(part.Text, part.Content))
		}
		return b.String()
	}
	return ""
}

func errorMessage(value any) string {
	if value == nil {
		return "上游流返回错误"
	}
	if object, ok := value.(map[string]any); ok {
		if message, ok := object["message"].(string); ok {
			return message
		}
	}
	return fmt.Sprint(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
