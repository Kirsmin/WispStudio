package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// StreamEvent 是上游流到 Runtime 的协议事件，不依赖 SSE/HTTP。
type StreamEvent struct {
	Type     string // delta | reasoning | tool_call | usage | done | error
	Text     string
	ToolCall *ToolCallDelta
	Usage    *Usage
	Finish   string
	Error    string
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type StreamReader struct {
	reader io.ReadCloser
	scan   *bufio.Scanner
}

func NewStreamReader(r io.ReadCloser) *StreamReader {
	scan := bufio.NewScanner(r)
	const maxCapacity = 16 * 1024 * 1024
	scan.Buffer(make([]byte, 64*1024), maxCapacity)
	return &StreamReader{reader: r, scan: scan}
}
func (sr *StreamReader) Close() error { return sr.reader.Close() }

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
			if choice.Delta.Refusal != "" {
				ch <- StreamEvent{Type: "delta", Text: choice.Delta.Refusal}
			}
			reasoning := firstNonEmpty(choice.Delta.ReasoningContent, choice.Delta.Reasoning, choice.Delta.Thinking, choice.Delta.Analysis)
			if reasoning != "" {
				ch <- StreamEvent{Type: "reasoning", Text: reasoning}
			}
			for _, call := range choice.Delta.ToolCalls {
				index := call.Index
				ch <- StreamEvent{Type: "tool_call", ToolCall: &ToolCallDelta{
					Index: index, ID: call.ID, Type: call.Type, Name: call.Function.Name, Arguments: call.Function.Arguments,
				}}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" && finish == "" {
				finish = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			u := &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, ReasoningTokens: chunk.Usage.ReasoningTokens}
			if chunk.Usage.PromptTokensDetails != nil {
				u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if d := chunk.Usage.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
				u.ReasoningTokens = d.ReasoningTokens
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
			Refusal          string          `json:"refusal"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			Thinking         string          `json:"thinking"`
			Analysis         string          `json:"analysis"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
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

func errorMessage(value any) string {
	if value == nil {
		return "上游流返回错误"
	}
	if object, ok := value.(map[string]any); ok {
		if message, ok := object["message"].(string); ok && message != "" {
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
