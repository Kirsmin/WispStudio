package provider

import (
	"encoding/json"
	"time"
)

// ToolCallDelta 是流式响应中一帧工具调用增量，按 Index 聚合。
type ToolCallDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCall 是一次聚合完成的工具调用（Arguments 为 JSON 原文）。
type ToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDefinition 是发给 provider 的工具定义（OpenAI function 格式）。
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function ToolDefinitionBody `json:"function"`
}

// ToolDefinitionBody 工具定义的 function 部分。
type ToolDefinitionBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Message 对话消息。
// ReasoningContent 是响应侧推理内容，原样透传不做转换；
// ToolCalls 是 assistant 消息携带的工具调用；ToolCallID 是
// tool 角色消息对应的 provider tool_use_id。
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// ChatRequest OpenAI 兼容 /chat/completions 请求体。
// ReasoningEffort 是请求侧思考深度，原样透传不做转换。
type ChatRequest struct {
	Model           string           `json:"model"`
	Messages        []Message        `json:"messages"`
	Stream          bool             `json:"stream,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxTokens       *int             `json:"max_tokens,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
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
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

// Delta 是流式增量帧的负载。
type Delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
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

// StreamDelta 是流式过程中向 runtime 推送的一帧增量。
// 正文/推理增量直接给出；工具调用增量按帧透传，由 runtime 自行聚合。
type StreamDelta struct {
	Content   string
	Reasoning string
	ToolCalls []ToolCallDelta
}

// Result 一次对话请求的完整结构化结果。
// provider 层只返回该结构，不感知事件；
// llm.requested / llm.responded（含 usage、finish_reason、耗时）由 runtime 负责写入。
type Result struct {
	Content      string        // 聚合后的正文
	Reasoning    string        // 聚合后的推理内容（原样透传，未做转换）
	ToolCalls    []ToolCall    // 聚合后的工具调用（按出现顺序）
	Usage        Usage         // token 用量
	FinishReason string        // 结束原因
	Duration     time.Duration // 请求耗时（含重试）
}
