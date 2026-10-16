// 包 ids 定义 Wisp 全部实体的强类型 ID。
//
// ID 字符串格式为 {前缀}_{ULID}，例如 "msg_01J8Z..."。
// 下面的前缀常量表是全项目唯一允许出现这些简写的地方，
// 其他地方一律使用这里定义的强类型。
package ids

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oklog/ulid/v2"
)

// Kind 表示一个 ID 所属的实体类型。
type Kind int

const (
	KindUnknown Kind = iota
	KindEvent
	KindMessage
	KindToolCall
	KindPrompt
	KindRequest
	KindContext
	KindContextOp
	KindAgent
	KindCheckpoint
	KindTurn
	KindSession
)

// 前缀常量表（唯一允许出现简写的地方），按最长前缀优先匹配排序。
const (
	prefixEvent      = "evt"
	prefixMessage    = "msg"
	prefixToolCall   = "tc"
	prefixPrompt     = "pr"
	prefixRequest    = "req"
	prefixContext    = "ctx"
	prefixContextOp  = "op"
	prefixAgent      = "ag"
	prefixCheckpoint = "ck"
	prefixTurn       = "tn"
	prefixSession    = "sess"
)

// prefixKind 将前缀映射到实体类型，顺序即最长前缀优先的匹配顺序。
var prefixKind = []struct {
	prefix string
	kind   Kind
}{
	{prefixEvent, KindEvent},
	{prefixMessage, KindMessage},
	{prefixRequest, KindRequest},
	{prefixContext, KindContext},
	{prefixToolCall, KindToolCall},
	{prefixPrompt, KindPrompt},
	{prefixContextOp, KindContextOp},
	{prefixAgent, KindAgent},
	{prefixCheckpoint, KindCheckpoint},
	{prefixTurn, KindTurn},
	{prefixSession, KindSession},
}

func init() {
	// 保证匹配顺序为前缀长度降序（最长前缀优先）。
	sort.SliceStable(prefixKind, func(i, j int) bool {
		return len(prefixKind[i].prefix) > len(prefixKind[j].prefix)
	})
}

// Parse 解析任意 ID 字符串，按最长前缀优先匹配返回实体类型。
// 前缀无法识别时返回 KindUnknown 与错误。
func Parse(s string) (Kind, error) {
	for _, pk := range prefixKind {
		if strings.HasPrefix(s, pk.prefix+"_") {
			return pk.kind, nil
		}
	}
	return KindUnknown, fmt.Errorf("ids: 无法识别的 ID 前缀: %q", s)
}

// newID 拼接前缀与 ULID 生成 ID 字符串。
func newID(prefix string) string {
	return prefix + "_" + ulid.Make().String()
}

// 检查并剥掉指定前缀，返回错误给各类型的 Parse 使用。
func checkPrefix(s, prefix string) error {
	kind, err := Parse(s)
	if err != nil {
		return err
	}
	want, _ := kindOfPrefix(prefix)
	if kind != want {
		return fmt.Errorf("ids: ID %q 前缀与期望类型不符", s)
	}
	return nil
}

func kindOfPrefix(prefix string) (Kind, bool) {
	for _, pk := range prefixKind {
		if pk.prefix == prefix {
			return pk.kind, true
		}
	}
	return KindUnknown, false
}

// EventID 事件 ID。
type EventID string

// NewEventID 生成新事件 ID。
func NewEventID() EventID { return EventID(newID(prefixEvent)) }

// String 返回字符串形式。
func (id EventID) String() string { return string(id) }

// ParseEventID 校验并解析事件 ID。
func ParseEventID(s string) (EventID, error) {
	if err := checkPrefix(s, prefixEvent); err != nil {
		return "", err
	}
	return EventID(s), nil
}

// MessageID 消息 ID。
type MessageID string

// NewMessageID 生成新消息 ID。
func NewMessageID() MessageID { return MessageID(newID(prefixMessage)) }

// String 返回字符串形式。
func (id MessageID) String() string { return string(id) }

// ParseMessageID 校验并解析消息 ID。
func ParseMessageID(s string) (MessageID, error) {
	if err := checkPrefix(s, prefixMessage); err != nil {
		return "", err
	}
	return MessageID(s), nil
}

// ToolCallID 工具调用 ID。
type ToolCallID string

// NewToolCallID 生成新工具调用 ID。
func NewToolCallID() ToolCallID { return ToolCallID(newID(prefixToolCall)) }

// String 返回字符串形式。
func (id ToolCallID) String() string { return string(id) }

// ParseToolCallID 校验并解析工具调用 ID。
func ParseToolCallID(s string) (ToolCallID, error) {
	if err := checkPrefix(s, prefixToolCall); err != nil {
		return "", err
	}
	return ToolCallID(s), nil
}

// PromptID 提示词 ID。
type PromptID string

// NewPromptID 生成新提示词 ID。
func NewPromptID() PromptID { return PromptID(newID(prefixPrompt)) }

// String 返回字符串形式。
func (id PromptID) String() string { return string(id) }

// ParsePromptID 校验并解析提示词 ID。
func ParsePromptID(s string) (PromptID, error) {
	if err := checkPrefix(s, prefixPrompt); err != nil {
		return "", err
	}
	return PromptID(s), nil
}

// RequestID 请求 ID。
type RequestID string

// NewRequestID 生成新请求 ID。
func NewRequestID() RequestID { return RequestID(newID(prefixRequest)) }

// String 返回字符串形式。
func (id RequestID) String() string { return string(id) }

// ParseRequestID 校验并解析请求 ID。
func ParseRequestID(s string) (RequestID, error) {
	if err := checkPrefix(s, prefixRequest); err != nil {
		return "", err
	}
	return RequestID(s), nil
}

// ContextID 上下文 ID。
type ContextID string

// NewContextID 生成新上下文 ID。
func NewContextID() ContextID { return ContextID(newID(prefixContext)) }

// String 返回字符串形式。
func (id ContextID) String() string { return string(id) }

// ParseContextID 校验并解析上下文 ID。
func ParseContextID(s string) (ContextID, error) {
	if err := checkPrefix(s, prefixContext); err != nil {
		return "", err
	}
	return ContextID(s), nil
}

// ContextOpID 上下文操作 ID。
type ContextOpID string

// NewContextOpID 生成新上下文操作 ID。
func NewContextOpID() ContextOpID { return ContextOpID(newID(prefixContextOp)) }

// String 返回字符串形式。
func (id ContextOpID) String() string { return string(id) }

// ParseContextOpID 校验并解析上下文操作 ID。
func ParseContextOpID(s string) (ContextOpID, error) {
	if err := checkPrefix(s, prefixContextOp); err != nil {
		return "", err
	}
	return ContextOpID(s), nil
}

// AgentID 智能体 ID。
type AgentID string

// NewAgentID 生成新智能体 ID。
func NewAgentID() AgentID { return AgentID(newID(prefixAgent)) }

// String 返回字符串形式。
func (id AgentID) String() string { return string(id) }

// ParseAgentID 校验并解析智能体 ID。
func ParseAgentID(s string) (AgentID, error) {
	if err := checkPrefix(s, prefixAgent); err != nil {
		return "", err
	}
	return AgentID(s), nil
}

// CheckpointID 检查点 ID。
type CheckpointID string

// NewCheckpointID 生成新检查点 ID。
func NewCheckpointID() CheckpointID { return CheckpointID(newID(prefixCheckpoint)) }

// String 返回字符串形式。
func (id CheckpointID) String() string { return string(id) }

// ParseCheckpointID 校验并解析检查点 ID。
func ParseCheckpointID(s string) (CheckpointID, error) {
	if err := checkPrefix(s, prefixCheckpoint); err != nil {
		return "", err
	}
	return CheckpointID(s), nil
}

// TurnID 回合 ID。
type TurnID string

// NewTurnID 生成新回合 ID。
func NewTurnID() TurnID { return TurnID(newID(prefixTurn)) }

// String 返回字符串形式。
func (id TurnID) String() string { return string(id) }

// ParseTurnID 校验并解析回合 ID。
func ParseTurnID(s string) (TurnID, error) {
	if err := checkPrefix(s, prefixTurn); err != nil {
		return "", err
	}
	return TurnID(s), nil
}

// SessionID 会话 ID。
type SessionID string

// NewSessionID 生成新会话 ID。
func NewSessionID() SessionID { return SessionID(newID(prefixSession)) }

// String 返回字符串形式。
func (id SessionID) String() string { return string(id) }

// ParseSessionID 校验并解析会话 ID。
func ParseSessionID(s string) (SessionID, error) {
	if err := checkPrefix(s, prefixSession); err != nil {
		return "", err
	}
	return SessionID(s), nil
}
