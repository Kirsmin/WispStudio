package openai

import (
	"encoding/json"
	"io"
	"testing"

	"wisp/internal/config"
)

func requestBody(t *testing.T, style, level string) map[string]any {
	t.Helper()
	client := NewClient(&config.OpenAIConfig{TimeoutSec: 1})
	req, err := client.BuildRequest("https://example.test/v1", "k", "m", style, level, []ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestThinkingParameters(t *testing.T) {
	if body := requestBody(t, "reasoning_effort", "off"); body["reasoning_effort"] != nil {
		t.Fatalf("off should omit reasoning_effort: %#v", body)
	}
	if body := requestBody(t, "reasoning_effort", "high"); body["reasoning_effort"] != "high" {
		t.Fatalf("expected high: %#v", body)
	}
	if body := requestBody(t, "enable_thinking", "off"); body["enable_thinking"] != false {
		t.Fatalf("off should explicitly disable thinking: %#v", body)
	}
}
