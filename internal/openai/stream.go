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

	finish := "" // 累计的 finish_reason，流结束时统一发一次 done

	for sr.scan.Scan() {
		line := sr.scan.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			break
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
				ReasoningTokens  int `json:"reasoning_tokens"`
				CompletionTokensDetails *struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
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
			// 只记录 finish_reason，不能在这里 return：
			// 有些兼容服务商把 finish_reason 和 usage 放在同一个 chunk，
			// 直接跳出会导致 usage 永远收不到
			if fr := chunk.Choices[0].FinishReason; fr != nil && finish == "" {
				finish = *fr
			}
		}

		// 处理 usage（可能出现在流末尾的独立 chunk，也可能与 finish_reason 同 chunk）
		if chunk.Usage != nil {
			u := &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}
			if chunk.Usage.PromptTokensDetails != nil {
				u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			// reasoning_tokens 各家放置位置不同：优先 completion_tokens_details，兼容顶层
			if d := chunk.Usage.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
				u.ReasoningTokens = d.ReasoningTokens
			} else if chunk.Usage.ReasoningTokens > 0 {
				u.ReasoningTokens = chunk.Usage.ReasoningTokens
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
