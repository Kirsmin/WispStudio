package provider

import "time"

// Message 对话消息。
// ReasoningContent 是响应侧推理内容，原样透传不做转换。
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ChatRequest OpenAI 兼容 /chat/completions 请求体。
// ReasoningEffort 是请求侧思考深度，原样透传不做转换。
type ChatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Stream          bool      `json:"stream,omitempty"`
	Temperature     *float64  `json:"temperature,omitempty"`
	MaxTokens       *int      `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
}

// Usage token 用量统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice 响应选项；非流式响应填 Message，流式增量帧填 Delta。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message,omitempty"`
	Delta        Message `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

// ChatResponse 非流式 /chat/completions 响应体。
type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// streamChunk OpenAI 流式增量帧（SSE data 负载）。
type streamChunk struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Result 一次对话请求的完整结构化结果。
// provider 层只返回该结构，不感知事件；
// llm.requested / llm.responded（含 usage、finish_reason、耗时）由 runtime 负责写入。
type Result struct {
	Content      string        // 聚合后的正文
	Reasoning    string        // 聚合后的推理内容（原样透传，未做转换）
	Usage        Usage         // token 用量
	FinishReason string        // 结束原因
	Duration     time.Duration // 请求耗时（含重试）
}
