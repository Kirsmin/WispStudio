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

type StreamReader struct {
	scanner *bufio.Scanner
}

func NewStreamReader(r io.Reader) *StreamReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	return &StreamReader{scanner: scanner}
}

func (sr *StreamReader) ReadEvents(out chan<- StreamEvent) {
	defer close(out)
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
			out <- StreamEvent{Type: "error", Error: "无法解析上游流数据: " + err.Error()}
			finish = "error"
			continue
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			out <- StreamEvent{Type: "error", Error: parseError(chunk.Error)}
			finish = "error"
			continue
		}

		for _, choice := range chunk.Choices {
			if text := rawText(choice.Delta.Content); text != "" {
				out <- StreamEvent{Type: "delta", Text: text}
			}
			reasoning := firstNonEmpty(
				rawText(choice.Delta.ReasoningContent),
				rawText(choice.Delta.Reasoning),
				rawText(choice.Delta.Thinking),
				rawText(choice.Delta.Analysis),
			)
			if reasoning != "" {
				out <- StreamEvent{Type: "reasoning", Text: reasoning}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			u := &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens}
			u.ReasoningTokens = chunk.Usage.ReasoningTokens
			if chunk.Usage.PromptTokensDetails != nil {
				u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if chunk.Usage.CompletionTokensDetails != nil && chunk.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
				u.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
			out <- StreamEvent{Type: "usage", Usage: u}
		}
	}
	if err := sr.scanner.Err(); err != nil {
		out <- StreamEvent{Type: "error", Error: fmt.Sprintf("读取上游流失败: %v", err)}
		return
	}
	if finish == "" {
		finish = "stop"
	}
	out <- StreamEvent{Type: "done", Finish: finish}
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			Thinking         json.RawMessage `json:"thinking"`
			Analysis         json.RawMessage `json:"analysis"`
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
	Error json.RawMessage `json:"error"`
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

func parseError(raw json.RawMessage) string {
	var object map[string]any
	if json.Unmarshal(raw, &object) == nil {
		if msg, ok := object["message"].(string); ok {
			return msg
		}
		if inner, ok := object["error"].(map[string]any); ok {
			if msg, ok := inner["message"].(string); ok {
				return msg
			}
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
