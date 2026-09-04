package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamEvent 流式事件
type StreamEvent struct {
	Type      string // delta | reasoning | usage | done | error
	Text      string
	Usage     *Usage
	Finish    string
	Error     string
}

// Usage token 用量
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// StreamReader 解析上游 SSE
type StreamReader struct {
	reader io.ReadCloser
	scan   *bufio.Scanner
}

func NewStreamReader(r io.ReadCloser) *StreamReader {
	scan := bufio.NewScanner(r)
	// buffer 提到 1MB
	const maxCapacity = 1024 * 1024
	buf := make([]byte, maxCapacity)
	scan.Buffer(buf, maxCapacity)
	return &StreamReader{reader: r, scan: scan}
}

func (sr *StreamReader) Close() error {
	return sr.reader.Close()
}

// ReadEvents 读取所有事件，通过 channel 发送
func (sr *StreamReader) ReadEvents(ch chan<- StreamEvent) {
	defer close(ch)
	for sr.scan.Scan() {
		line := sr.scan.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			ch <- StreamEvent{Type: "done", Finish: "stop"}
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				PromptTokensDetails *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// 处理 delta content
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				ch <- StreamEvent{Type: "delta", Text: delta.Content}
			}
			if delta.ReasoningContent != "" {
				ch <- StreamEvent{Type: "reasoning", Text: delta.ReasoningContent}
			}
			if chunk.Choices[0].FinishReason != nil {
				ch <- StreamEvent{Type: "done", Finish: *chunk.Choices[0].FinishReason}
				return
			}
		}

		// 处理 usage（最后一个带 usage 的 chunk）
		if chunk.Usage != nil {
			u := &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}
			if chunk.Usage.PromptTokensDetails != nil {
				u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			ch <- StreamEvent{Type: "usage", Usage: u}
		}
	}

	if err := sr.scan.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("读取流失败: %v", err)}
		return
	}
	ch <- StreamEvent{Type: "done", Finish: "stop"}
}
