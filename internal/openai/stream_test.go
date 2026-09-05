package openai

import (
	"io"
	"strings"
	"testing"
)

func TestStreamReaderContentReasoningUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"think "},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\r\n")
	reader := NewStreamReader(io.NopCloser(strings.NewReader(input)))
	ch := make(chan StreamEvent, 16)
	go reader.ReadEvents(ch)
	var reasoning, content, finish string
	var usage *Usage
	for evt := range ch {
		switch evt.Type {
		case "reasoning":
			reasoning += evt.Text
		case "delta":
			content += evt.Text
		case "usage":
			usage = evt.Usage
		case "done":
			finish = evt.Finish
		}
	}
	if reasoning != "think " || content != "answer" || finish != "stop" {
		t.Fatalf("unexpected events reasoning=%q content=%q finish=%q", reasoning, content, finish)
	}
	if usage == nil || usage.ReasoningTokens != 2 || usage.CachedTokens != 1 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestExtractTextArray(t *testing.T) {
	raw := []byte(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	if got := extractText(raw); got != "ab" {
		t.Fatalf("got %q", got)
	}
}
