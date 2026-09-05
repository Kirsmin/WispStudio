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

type StreamReader struct {
	reader io.ReadCloser
	scan   *bufio.Scanner
}

func NewStreamReader(r io.ReadCloser) *StreamReader {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64<<10), 8<<20)
	return &StreamReader{reader: r, scan: scan}
}

func (sr *StreamReader) Close() error { return sr.reader.Close() }

func (sr *StreamReader) ReadEvents(ch chan<- StreamEvent) {
	defer close(ch)
	var dataLines []string
	finish := ""
	failed := false
	doneMarker := false

	flush := func() bool {
		if len(dataLines) == 0 {
			return false
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "" {
			return false
		}
		if data == "[DONE]" {
			doneMarker = true
			return true
		}
		if data == "ping" || data == ": ping" {
			return false
		}
		fr, err := parseChunk(data, ch)
		if err != nil {
			failed = true
			ch <- StreamEvent{Type: "error", Error: err.Error()}
			return true
		}
		if finish == "" && fr != "" {
			finish = fr
		}
		return false
	}

	for sr.scan.Scan() {
		line := strings.TrimSuffix(sr.scan.Text(), "\r")
		if line == "" {
			if flush() {
				break
			}
			continue
		}
		// SSE 注释/keep-alive。
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if !doneMarker && !failed {
		_ = flush()
	}
	if err := sr.scan.Err(); err != nil && !failed {
		failed = true
		ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("读取上游流失败: %v", err)}
	}
	if failed {
		finish = "error"
	} else if finish == "" {
		finish = "stop"
	}
	ch <- StreamEvent{Type: "done", Finish: finish}
}

type upstreamChunk struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			ReasoningText    json.RawMessage `json:"reasoning_text"`
			ReasoningDetails json.RawMessage `json:"reasoning_details"`
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
}

func parseChunk(data string, ch chan<- StreamEvent) (string, error) {
	var chunk upstreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", fmt.Errorf("解析上游 SSE JSON 失败: %w", err)
	}
	if chunk.Error != nil {
		msg := strings.TrimSpace(chunk.Error.Message)
		if msg == "" {
			msg = "上游返回未知错误"
		}
		return "error", fmt.Errorf("%s", msg)
	}
	finish := ""
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		// 兼容网关可能同时镜像多个 reasoning 字段：按优先级只取第一个非空值，避免重复展示。
		for _, raw := range []json.RawMessage{delta.ReasoningContent, delta.Reasoning, delta.ReasoningText, delta.ReasoningDetails} {
			if text := extractText(raw); text != "" {
				ch <- StreamEvent{Type: "reasoning", Text: text}
				break
			}
		}
		if text := extractText(delta.Content); text != "" {
			ch <- StreamEvent{Type: "delta", Text: text}
		}
		if choice.FinishReason != nil && finish == "" {
			finish = *choice.FinishReason
		}
	}
	if chunk.Usage != nil {
		u := &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens}
		if chunk.Usage.PromptTokensDetails != nil {
			u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		if chunk.Usage.CompletionTokensDetails != nil && chunk.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
			u.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
		} else {
			u.ReasoningTokens = chunk.Usage.ReasoningTokens
		}
		ch <- StreamEvent{Type: "usage", Usage: u}
	}
	return finish, nil
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var b strings.Builder
	collectText(value, &b)
	return b.String()
}

func collectText(v any, b *strings.Builder) {
	switch x := v.(type) {
	case string:
		b.WriteString(x)
	case []any:
		for _, item := range x {
			collectText(item, b)
		}
	case map[string]any:
		for _, key := range []string{"text", "content", "value"} {
			if val, ok := x[key]; ok {
				collectText(val, b)
				return
			}
		}
	}
}
