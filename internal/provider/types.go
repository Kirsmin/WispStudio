package provider

import (
	"encoding/json"
	"strings"
	"time"
)

// ToolCallFunction 是工具调用的 function 负载。
// 线格式与 OpenAI 兼容：name / arguments 必须嵌在 "function" 对象内，
// 顶层不允许出现 name / arguments。
type ToolCallFunction struct {
	// Name 是工具名。
	Name string `json:"name"`

	// Arguments 是调用参数的 JSON 原文（字符串形式）。
	Arguments string `json:"arguments"`
}

// ToolCall 是一次（流式聚合完成的）工具调用。
//
// MarshalJSON 始终输出 OpenAI 兼容的嵌套结构：
//
//	{"id":"call_x","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}
//
// 反序列化则宽容接受嵌套写法（规范）与扁平写法（部分非标准实现），
// 见 UnmarshalJSON。
type ToolCall struct {
	// ID 是 provider 侧的 tool_use_id。
	ID string

	// Type 恒为 "function"；为空时序列化按 "function" 输出。
	Type string

	// Function 是工具名与参数。
	Function ToolCallFunction
}

// toolCallWire 是宽容解析的中间结构：同时容纳嵌套 function（规范写法）
// 与扁平的 name / arguments（非标准写法）。arguments 用 RawMessage
// 以便同时接受字符串与直接的 JSON 对象。
type toolCallWire struct {
	Index     int             `json:"index"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Function  *struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// normalize 把宽容结构归一化为 (index, id, type, function)：
// 嵌套 function 优先，缺失字段回落到扁平写法。
func (w *toolCallWire) normalize() (index int, id, typ string, fn ToolCallFunction) {
	name, args := w.Name, w.Arguments
	if w.Function != nil {
		if w.Function.Name != "" {
			name = w.Function.Name
		}
		if len(w.Function.Arguments) > 0 {
			args = w.Function.Arguments
		}
	}
	return w.Index, w.ID, w.Type, ToolCallFunction{Name: name, Arguments: argumentsString(args)}
}

// argumentsString 把 arguments 归一化为 JSON 文本：字符串原样取出
// （去掉引号与转义），对象 / 数组 / 数字保留其 JSON 文本，缺失或
// null 返回空串。OpenAI 规范要求字符串，但直接传对象的实现也一并接受。
func argumentsString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
	}
	return s
}

// UnmarshalJSON 宽容解析工具调用：优先取嵌套 function，缺失时回落到扁平写法。
func (t *ToolCall) UnmarshalJSON(data []byte) error {
	var w toolCallWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	_, t.ID, t.Type, t.Function = w.normalize()
	return nil
}

// MarshalJSON 始终输出嵌套 function 结构，并保证 type 有值（默认 "function"）。
func (t ToolCall) MarshalJSON() ([]byte, error) {
	type wire struct {
		ID       string           `json:"id"`
		Type     string           `json:"type"`
		Function ToolCallFunction `json:"function"`
	}
	typ := t.Type
	if typ == "" {
		typ = "function"
	}
	return json.Marshal(wire{ID: t.ID, Type: typ, Function: t.Function})
}

// ToolCallDelta 是流式响应中一帧工具调用增量，按 Index 聚合。
// 线格式与 ToolCall 相同：name 只在首帧出现，arguments 分帧累加。
type ToolCallDelta struct {
	// Index 是同一响应内的调用序号，聚合键。
	Index int `json:"index"`

	// ID / Type 只在首帧出现。
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`

	// Function 是本帧携带的 function 增量（name 或 arguments 分片）。
	Function ToolCallFunction `json:"function"`
}

// UnmarshalJSON 宽容解析增量帧（嵌套 function 或扁平写法）。
func (d *ToolCallDelta) UnmarshalJSON(data []byte) error {
	var w toolCallWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	index, id, typ, fn := w.normalize()
	d.Index, d.ID, d.Type, d.Function = index, id, typ, fn
	return nil
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
