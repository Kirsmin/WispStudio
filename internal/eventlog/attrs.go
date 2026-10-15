// 本文件集中定义全部事件类型的 attrs 结构。
//
// 规则（与 eventlog 包注释一致）：
//   - 每个事件类型一个结构体，只放“读取时才解析的附加数据”；
//     被查询/索引的字段一律是表列（events 表列或 contexts/ctx_ops 表列）。
//   - 结构体字段一旦发布不得改变语义，只增不改；
//     语义演进靠事件 v 递增 + Upcaster 完成。
//   - 大块正文一律存 blobs（events.blob 存哈希引用），attrs 只留元数据。
package eventlog

// 事件类型常量：写入事件时 Type 字段的唯一合法取值集合。
const (
	// TypeMessageCreated 创建/更新一条消息，SubjectID = 消息 ID。
	// 同一 SubjectID 多次出现表示多版本，seq 最大者为最新版本。
	// role 属于读取时才解析的附加数据，故放 attrs。
	TypeMessageCreated = "message.created"

	// TypeToolCalled 发起一次工具调用，SubjectID = 工具调用 ID。
	// 参数正文存 blobs；provider_id 只是内部 ToolCallID 与 provider
	// tool_use_id 的映射值，不当主键。
	TypeToolCalled = "tool.called"

	// TypeToolReturned 工具调用返回，SubjectID = 工具调用 ID。
	// 结果正文存 blobs。与 tool.called 的分组关系由 links 表
	// rel='result_of'（src=本事件 ID，dst=对应 tool.called 事件 ID）表达。
	TypeToolReturned = "tool.returned"

	// TypePromptInjected 注入一段提示词，SubjectID = 提示词 ID。
	// 正文存 blobs；渲染为 user 角色消息。
	TypePromptInjected = "prompt.injected"

	// TypeCtxCreated 创建一个上下文，SubjectID = 上下文 ID。
	// 被查询/索引的字段（base_ctx/fork_at）是 contexts 表列，attrs 通常为空。
	TypeCtxCreated = "ctx.created"

	// TypeCtxOp 对上下文内容的一次编辑操作，SubjectID = 操作 ID。
	// 被查询/索引的字段（op/target/anchor/okey/new_id）是 ctx_ops 表列，
	// attrs 通常为空；新内容一律是独立内容事件，由 ctx_ops.new_id 引用。
	TypeCtxOp = "ctx.op"

	// TypeLLMRequested 记录一次 LLM 请求，blob 字段引用 RequestManifest
	// （编译产出的 [(id, blob_hash, revision)] 清单），供回放用。
	TypeLLMRequested = "llm.requested"
)

// MessageCreatedAttrs 是 message.created 事件的附加属性。
// 消息正文存 blobs，此处只放元数据。
type MessageCreatedAttrs struct {
	// Role 是消息角色，取值 "user" / "assistant"。
	Role string `json:"role"`
}

// ToolCalledAttrs 是 tool.called 事件的附加属性。
// 调用参数 JSON 正文存 blobs，此处只放元数据。
type ToolCalledAttrs struct {
	// Name 是工具名。
	Name string `json:"name"`

	// ProviderID 是 provider 侧的 tool_use_id，仅用于内部 ToolCallID ↔
	// provider tool_use_id 映射，绝不充当主键；为空表示尚未分配。
	ProviderID string `json:"provider_id,omitempty"`
}

// ToolReturnedAttrs 是 tool.returned 事件的附加属性。
// 结果正文存 blobs，此处通常为空，保留扩展位。
type ToolReturnedAttrs struct{}

// PromptInjectedAttrs 是 prompt.injected 事件的附加属性。
// 提示词正文存 blobs，此处只放元数据。
type PromptInjectedAttrs struct {
	// Source 是注入来源，可空。
	Source string `json:"source,omitempty"`
}

// CtxCreatedAttrs 是 ctx.created 事件的附加属性。
// base_ctx/fork_at 是 contexts 表列，此处通常为空，保留扩展位。
type CtxCreatedAttrs struct{}

// CtxOpAttrs 是 ctx.op 事件的附加属性。
// op/target/anchor/okey/new_id 是 ctx_ops 表列，此处通常为空，保留扩展位。
type CtxOpAttrs struct{}
