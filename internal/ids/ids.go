// 包 ids 定义 Wisp 全部实体的强类型 ID。
//
// ID 字符串格式为 {前缀}_{ULID}，例如 "msg_01J8Z..."。
// 前缀常量只在本文件出现，其他包一律通过强类型构造与解析。
package ids

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

// 前缀常量表。
const (
	prefixEvent     = "evt"
	prefixMessage   = "msg"
	prefixToolCall  = "tc"
	prefixRequest   = "req"
	prefixContext   = "ctx"
	prefixContextOp = "op"
	prefixTurn      = "tn"
	prefixSession   = "sess"
)

// newID 拼接前缀与 ULID 生成 ID 字符串。
func newID(prefix string) string {
	return prefix + "_" + ulid.Make().String()
}

// checkPrefix 校验 ID 是否带指定前缀。
func checkPrefix(s, prefix string) error {
	if !strings.HasPrefix(s, prefix+"_") {
		return fmt.Errorf("ids: ID %q 前缀与期望类型不符", s)
	}
	return nil
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

// RequestID 请求 ID。
type RequestID string

// NewRequestID 生成新请求 ID。
func NewRequestID() RequestID { return RequestID(newID(prefixRequest)) }

// String 返回字符串形式。
func (id RequestID) String() string { return string(id) }

// ContextID 上下文 ID。
type ContextID string

// NewContextID 生成新上下文 ID。
func NewContextID() ContextID { return ContextID(newID(prefixContext)) }

// String 返回字符串形式。
func (id ContextID) String() string { return string(id) }

// ContextOpID 上下文操作 ID。
type ContextOpID string

// NewContextOpID 生成新上下文操作 ID。
func NewContextOpID() ContextOpID { return ContextOpID(newID(prefixContextOp)) }

// String 返回字符串形式。
func (id ContextOpID) String() string { return string(id) }

// TurnID 回合 ID。
type TurnID string

// NewTurnID 生成新回合 ID。
func NewTurnID() TurnID { return TurnID(newID(prefixTurn)) }

// String 返回字符串形式。
func (id TurnID) String() string { return string(id) }

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
