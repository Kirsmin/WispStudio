package provider

import "testing"

func TestInferThinkingFromNestedMetadata(t *testing.T) {
	raw := map[string]any{
		"id": "custom-model",
		"metadata": map[string]any{
			"capabilities": map[string]any{
				"supported_reasoning_effort": []any{"low", "medium", "high"},
			},
		},
	}
	levels, style := inferThinking(raw, "custom-model")
	if style != "reasoning_effort" {
		t.Fatalf("style=%q", style)
	}
	want := []string{"off", "low", "medium", "high"}
	if len(levels) != len(want) {
		t.Fatalf("levels=%v", levels)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels=%v want=%v", levels, want)
		}
	}
}

func TestInferThinkingFromNestedSupportedParameters(t *testing.T) {
	raw := map[string]any{
		"id": "custom-model",
		"capabilities": map[string]any{
			"supported_parameters": []any{"temperature", "enable_thinking"},
		},
	}
	levels, style := inferThinking(raw, "custom-model")
	if style != "enable_thinking" || len(levels) != 2 || levels[0] != "off" || levels[1] != "on" {
		t.Fatalf("levels=%v style=%q", levels, style)
	}
}

func TestInferThinkingByReasonerName(t *testing.T) {
	levels, style := inferThinking(nil, "vendor/deepseek-reasoner")
	if style != "reasoning_effort" || len(levels) < 4 {
		t.Fatalf("levels=%v style=%q", levels, style)
	}
}
