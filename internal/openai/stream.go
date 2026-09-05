package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type StreamEvent struct {
	Type   string // delta | reasoning | usage | done | error
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

type StreamReader struct {
	scan *bufio.Scanner
}

func NewStreamReader(r io.Reader) *StreamReader {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &StreamReader{scan: scan}
}

func (sr *StreamReader) ReadEvents(ch chan<- StreamEvent) {
	defer close(ch)
	finish := ""

	for sr.scan.Scan() {
		line := strings.TrimSuffix(sr.scan.Text(), "\r")
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
			// 某些兼容网关在 SSE 中直接塞错误对象，尽量把可读错误透传出去。
			var fallback map[string]any
			if json.Unmarshal([]byte(data), &fallback) == nil {
				if message := extractError(fallback); message != "" {
					ch <- StreamEvent{Type: "error", Error: message}
					finish = "error"
				}
			}
			continue
		}
		if message := extractError(chunk.Error); message != "" {
			ch <- StreamEvent{Type: "error", Error: message}
			finish = "error"
		}

		for _, choice := range chunk.Choices {
			if text := rawText(choice.Delta.Content); text != "" {
				ch <- StreamEvent{Type: "delta", Text: text}
			}
			reasoning := firstNonEmpty(
				choice.Delta.ReasoningContent,
				choice.Delta.Reasoning,
				choice.Delta.Thinking,
				choice.Delta.Analysis,
			)
			if reasoning != "" {
				ch <- StreamEvent{Type: "reasoning", Text: reasoning}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" && finish == "" {
				finish = *choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			u := &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				ReasoningTokens:  chunk.Usage.ReasoningTokens,
			}
			if details := chunk.Usage.PromptTokensDetails; details != nil {
				u.CachedTokens = details.CachedTokens
			}
			if details := chunk.Usage.CompletionTokensDetails; details != nil && details.ReasoningTokens > 0 {
				u.ReasoningTokens = details.ReasoningTokens
			}
			ch <- StreamEvent{Type: "usage", Usage: u}
		}
	}

	if err := sr.scan.Err(); err != nil {
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
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		ReasoningTokens     int `json:"reasoning_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error any `json:"error"`
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(firstNonEmpty(part.Text, part.Content))
		}
		return b.String()
	}
	return ""
}

func extractError(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if nested, ok := typed["error"]; ok {
			if message := extractError(nested); message != "" {
				return message
			}
		}
		for _, key := range []string{"message", "detail", "error_description"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	case map[string]string:
		for _, key := range []string{"message", "detail", "error"} {
			if text := strings.TrimSpace(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
