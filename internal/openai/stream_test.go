package openai

import (
	"strings"
	"testing"
)

func TestStreamReaderKeepsUsageAfterFinish(t *testing.T) {
	input := strings.Join([]string{
		`data:{"choices":[{"delta":{"reasoning":"think"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`, "",
	}, "\r\n\r\n")
	ch := make(chan StreamEvent, 8)
	go NewStreamReader(strings.NewReader(input)).ReadEvents(ch)
	var content, reasoning string
	var usage *Usage
	finish := ""
	for event := range ch {
		switch event.Type {
		case "delta":
			content += event.Text
		case "reasoning":
			reasoning += event.Text
		case "usage":
			usage = event.Usage
		case "done":
			finish = event.Finish
		}
	}
	if content != "answer" || reasoning != "think" || finish != "stop" || usage == nil || usage.ReasoningTokens != 3 {
		t.Fatalf("unexpected stream result: content=%q reasoning=%q finish=%q usage=%#v", content, reasoning, finish, usage)
	}
}
