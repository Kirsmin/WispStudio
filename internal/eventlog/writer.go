package eventlog

import "context"

// Writer 是事件日志的唯一写入入口。
//
// API/runtime 等上层只依赖该接口，不直接操作 SQL。Store 实现本接口；
// 所有事件在事务提交成功后才通过 Hub 发出通知，通知只携带会话 ID，
// 订阅方必须回读日志，保证“通知可丢、数据不丢”。
type Writer interface {
	// AppendEvent 追加单个事件。
	AppendEvent(ctx context.Context, in EventInput) (Event, error)

	// AppendEvents 在同一事务中原子追加一批事件。
	AppendEvents(ctx context.Context, ins []EventInput) ([]Event, error)

	// MoveRef 更新命名引用并追加 ref.moved 事件。
	MoveRef(ctx context.Context, name, target string) (Event, error)

	// MoveRefInSession 同 MoveRef，并把 session_id 写进 ref.moved 事件。
	MoveRefInSession(ctx context.Context, name, target, sessionID string) (Event, error)

	// CreateContext 创建上下文并追加 ctx.created 事件。
	CreateContext(ctx context.Context, in ContextInput) (ContextRow, error)

	// AppendCtxOp 追加一次上下文编辑操作并写 ctx.op 事件。
	AppendCtxOp(ctx context.Context, in CtxOpInput) (CtxOpRow, error)

	// AddLink 写入一条关系，幂等。
	AddLink(ctx context.Context, src, dst, rel string) error
}

// 编译期确认 Store 实现 Writer。
var _ Writer = (*Store)(nil)
