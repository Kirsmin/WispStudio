package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamEvent 是从 OpenAI Chat Completions 兼容 SSE 中抽象出的事件。
type StreamEvent struct {
	Type   string // delta | reasoning | usage | done | error
	Text   string
	Usage  *Usage
	Finish string
	Error  string
}

// Usage token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// StreamReader 解析上游 SSE。
type StreamReader struct {
	reader io.ReadCloser
	scan   *bufio.Scanner
}

func NewStreamReader(r io.ReadCloser) *StreamReader {
	scan := bufio.NewScanner(r)
	// 某些兼容网关会把工具调用/长 delta 放在单个 SSE data 行里，默认 64KB 不够。
	const maxCapacity = 16 * 1024 * 1024
	scan.Buffer(make([]byte, 64*1024), maxCapacity)
	return &StreamReader{reader: r, scan: scan}
}

func (sr *StreamReader) Close() error {
	return sr.reader.Close()
}

// ReadEvents 兼容：
//   - OpenAI Chat Completions 标准 delta.content / delta.refusal；
//   - 常见 reasoning 扩展：reasoning_content / reasoning / thinking / analysis；
//   - stream_options.include_usage 的末尾 usage chunk（choices 可为空）。
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
			// 不让单个非标准心跳/注释块击穿整个流。
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

// rawText 兼容标准 string content，也容忍部分网关返回 text parts 数组。
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
