// 本文件集中定义全部事件类型的 attrs 结构。
//
// 规则（与 eventlog 包注释一致）：
//   - 每个事件类型一个结构体，只放“读取时才解析的附加数据”；
//     被查询/索引的字段一律是表列（events 表列或 contexts/ctx_ops 表列）。
//   - 不做版本兼容：字段语义变化时直接改结构体与读取代码。
//   - 大块正文一律存 blobs（events.blob 存哈希引用），attrs 只留元数据。
package eventlog

// 事件类型常量：写入事件时 Type 字段的唯一合法取值集合。
const (
	// TypeSessionCreated 创建一个会话，SubjectID = 会话 ID。
	// session_id 列同时写会话 ID，便于事件流按会话订阅。
	TypeSessionCreated = "session.created"

	// TypeSessionDeleted 软删除一个会话，SubjectID = 会话 ID。
	// 事件日志保持只追加：列表与新建回合会折叠本事件并跳过已删除会话。
	TypeSessionDeleted = "session.deleted"

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

	// TypeLLMResponded 记录一次 LLM 响应完成，SubjectID = 请求 ID（对应
	// llm.requested 的 SubjectID）。usage / finish_reason / 耗时是读取时
	// 才解析的附加数据，故放 attrs。
	TypeLLMResponded = "llm.responded"

	// TypeTurnStarted 开始一个回合，SubjectID = 回合 ID。回合的当前状态
	// 不存任何表，由 refs（turn/<id>）+ 事件折叠得出。
	TypeTurnStarted = "turn.started"

	// TypeTurnCompleted 结束一个回合（成功或失败），SubjectID = 回合 ID。
	// 失败时 attrs.error 非空。
	TypeTurnCompleted = "turn.completed"

	// TypeMessageDelta 流式响应的一帧增量，SubjectID = 消息 ID（该轮
	// assistant 消息），正文增量存 blobs。仅供增量展示与回放，
	// 不属于内容事件，compile 忽略。
	TypeMessageDelta = "message.delta"

	// TypeApprovalRequested 请求用户批准一次工具调用，SubjectID = 审批 ID。
	// attrs.tool_call_id 指向对应的 tool.called 条目。
	TypeApprovalRequested = "approval.requested"

	// TypeApprovalDecided 用户/上层对一次审批做出决定，SubjectID = 审批 ID
	// （与 approval.requested 相同）。
	TypeApprovalDecided = "approval.decided"
)

// SessionCreatedAttrs 是 session.created 事件的附加属性。
// 标题与默认模型只作展示/恢复提示，会话的其他状态仍由事件折叠得出。
type SessionCreatedAttrs struct {
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
}

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

	// IdempotencyKey 是本次调用的幂等键（同一逻辑调用跨崩溃恢复不变），
	// 崩溃恢复据此判断调用是否已结清，绝不重复执行已落盘结果。
	IdempotencyKey string `json:"idempotency_key"`
}

// ToolReturnedAttrs 是 tool.returned 事件的附加属性。
// 结果正文存 blobs，此处只放幂等键（与 tool.called 相同）。
type ToolReturnedAttrs struct {
	// IdempotencyKey 与对应 tool.called 的幂等键一致。
	IdempotencyKey string `json:"idempotency_key"`
}

// LLMRequestedAttrs 是 llm.requested 事件的附加属性。
// RequestManifest 由 blob 字段引用，此处只放请求元数据。
type LLMRequestedAttrs struct {
	// ProviderID 是模型服务商 ID。
	ProviderID string `json:"provider_id"`

	// Model 是模型全称 id（{ProviderID}/{ModelID}）。
	Model string `json:"model"`
}

// LLMRespondedAttrs 是 llm.responded 事件的附加属性。
type LLMRespondedAttrs struct {
	// PromptTokens / CompletionTokens / TotalTokens 是 token 用量。
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// FinishReason 是 provider 侧的结束原因。
	FinishReason string `json:"finish_reason,omitempty"`

	// DurationMs 是请求耗时（含重试），毫秒。
	DurationMs int64 `json:"duration_ms"`
}

// TurnStartedAttrs 是 turn.started 事件的附加属性。
type TurnStartedAttrs struct {
	// ContextID 是本回合编译所用的上下文 ID；恢复回合（含审批后继续）
	// 需要用它重建编译入参，故随事件持久化，不依赖进程内存。
	ContextID string `json:"context_id,omitempty"`
}

// TurnCompletedAttrs 是 turn.completed 事件的附加属性。
type TurnCompletedAttrs struct {
	// FinishReason 是最后一轮 LLM 响应的结束原因，失败时为空。
	FinishReason string `json:"finish_reason,omitempty"`

	// Error 非空表示回合以失败结束。
	Error string `json:"error,omitempty"`
}

// MessageDeltaAttrs 是 message.delta 事件的附加属性。
// delta 正文存 blobs，此处标明增量类别。
type MessageDeltaAttrs struct {
	// Kind 是增量类别："content"（正文）或 "reasoning"（推理内容）。
	Kind string `json:"kind"`
}

// ApprovalRequestedAttrs 是 approval.requested 事件的附加属性。
type ApprovalRequestedAttrs struct {
	// ToolCallID 是等待审批的工具调用 ID（tool.called 的 SubjectID）。
	ToolCallID string `json:"tool_call_id"`

	// Name 是工具名（冗余存放，便于审批界面直接展示）。
	Name string `json:"name"`
}

// ApprovalDecidedAttrs 是 approval.decided 事件的附加属性。
type ApprovalDecidedAttrs struct {
	// Decision 是决定："approved"（批准）或 "rejected"（拒绝）。
	Decision string `json:"decision"`

	// Reason 是拒绝理由或备注，可空。
	Reason string `json:"reason,omitempty"`
}

// RelResultOf 是 links 表中工具调用与结果的分组关系：
// src = tool.returned 事件 ID，dst = tool.called 事件 ID。
const RelResultOf = "result_of"

// IsContentType 判断事件是否参与内容链与 compile 投影。
// message.delta 等仅用于展示的事件不属于内容事件。
func IsContentType(typ string) bool {
	switch typ {
	case TypeMessageCreated, TypeToolCalled, TypeToolReturned:
		return true
	default:
		return false
	}
}
