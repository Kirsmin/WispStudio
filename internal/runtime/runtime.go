package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"wisp/internal/compile"
	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
)

// refs 指针取值（turn 状态机的全部合法目标）。
const (
	// RefRunning 回合执行中。
	RefRunning = "running"

	// RefCompleted 回合正常结束。
	RefCompleted = "completed"

	// refFailedPrefix 失败结束，目标为 "failed:<原因>"。
	refFailedPrefix = "failed:"

	// refWaitingPrefix 挂起等待审批，目标为 "waiting_approval:<审批ID>"。
	refWaitingPrefix = "waiting_approval:"
)

// actorRuntime 是运行时写入事件的触发者标识。
const actorRuntime = "runtime"

// actorUser 是用户（或代表用户的 API 层）写入事件的触发者标识。
const actorUser = "user"

// TurnRefName 返回回合的 refs 指针名。
func TurnRefName(turnID string) string { return "turn/" + turnID }

// Options 是 Runtime 的构造参数。
type Options struct {
	WorkspaceRoot string           // 工作区根，文件操作一律限制在其内；空 = 当前目录
	ProviderID    string           // 写入 llm.requested attrs 的服务商 ID
	Model         string           // 模型全称 id（{ProviderID}/{ModelID}），写入事件
	ProviderModel string           // 发给服务商的裸模型 id；空则从 Model 去掉 provider 前缀
	Client        *provider.Client // OpenAI 兼容客户端；nil 时用默认客户端
	MaxIterations int              // 回合最大迭代次数，防死循环；<= 0 用默认 64
}

// Runtime 是事件驱动运行时：无自有可变状态，一切状态由事件日志折叠得出。
type Runtime struct {
	store         *eventlog.Store
	workspaceRoot string
	providerID    string
	model         string // 全称 id（事件用）
	wireModel     string // 服务商侧裸模型 id（请求用）
	client        *provider.Client
	tools         *Registry
	maxIterations int
}

// New 构造运行时。
func New(store *eventlog.Store, opts Options) (*Runtime, error) {
	if opts.Model == "" {
		return nil, fmt.Errorf("runtime: Model 不能为空")
	}
	reg, err := NewRegistry(opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	client := opts.Client
	if client == nil {
		client = &provider.Client{}
	}
	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = 64
	}
	return &Runtime{
		store:         store,
		workspaceRoot: reg.workspaceRoot,
		providerID:    opts.ProviderID,
		model:         opts.Model,
		wireModel:     wireModelID(opts.Model, opts.ProviderModel),
		client:        client,
		tools:         reg,
		maxIterations: maxIter,
	}, nil
}

// wireModelID 返回发给服务商的模型 id：优先用显式给定的裸 id，
// 否则从全称 id（{ProviderID}/{ModelID}）去掉 provider 前缀。
// 服务商只认裸 id，全称 id 仅用于事件记录。
func wireModelID(fullID, providerModel string) string {
	if v := strings.TrimSpace(providerModel); v != "" {
		return v
	}
	if _, modelID, ok := strings.Cut(strings.TrimSpace(fullID), "/"); ok && modelID != "" {
		return modelID
	}
	return fullID
}

// TurnParams 是一次回合执行/恢复的参数。
type TurnParams struct {
	SessionID string        // 会话 ID（写入事件的 session_id）
	TurnID    string        // 回合 ID；空则自动生成
	ContextID ids.ContextID // 编译用的上下文 ID
}

// RunTurn 启动或恢复一个回合的执行循环；指针处于终态或等待审批时
// 立即返回（幂等，可安全重入）。返回 nil 表示回合已结束或挂起。
func (r *Runtime) RunTurn(ctx context.Context, p TurnParams) error {
	if p.SessionID == "" {
		return fmt.Errorf("runtime: SessionID 不能为空")
	}
	if p.ContextID == "" {
		return fmt.Errorf("runtime: ContextID 不能为空")
	}
	if p.TurnID == "" {
		p.TurnID = ids.NewTurnID().String()
	}
	if err := r.ensureStarted(ctx, p); err != nil {
		return err
	}
	return r.loop(ctx, p)
}

// ensureStarted 幂等入口：指针不存在时写 turn.started 并把指针移到 running。
func (r *Runtime) ensureStarted(ctx context.Context, p TurnParams) error {
	refName := TurnRefName(p.TurnID)
	target, err := r.store.GetRef(ctx, refName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("runtime: 读取指针 %s 失败: %w", refName, err)
	}
	if target != "" {
		return nil
	}
	if _, err := r.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeTurnStarted,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: p.TurnID,
		Attrs:     eventlog.TurnStartedAttrs{ContextID: p.ContextID.String()},
	}); err != nil {
		return fmt.Errorf("runtime: 写 turn.started 失败: %w", err)
	}
	if _, err := r.store.MoveRef(ctx, refName, RefRunning); err != nil {
		return fmt.Errorf("runtime: 移动指针 %s 失败: %w", refName, err)
	}
	return nil
}

// loop 是回合主循环：每轮起点 = 读 refs 指针 + fold 全部回合事件。
func (r *Runtime) loop(ctx context.Context, p TurnParams) error {
	refName := TurnRefName(p.TurnID)
	for iter := 0; ; iter++ {
		if iter >= r.maxIterations {
			return r.failTurn(ctx, p, fmt.Sprintf("超过最大迭代次数 %d", r.maxIterations))
		}
		target, err := r.store.GetRef(ctx, refName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r.failTurn(ctx, p, "读取指针失败: "+err.Error())
		}
		switch {
		case target == RefCompleted:
			return nil
		case target == RefRunning:
			// 继续本轮裁决
		case target == "":
			// 指针丢失的防御处理：ensureStarted 已保证不为空，视作失败避免死循环
			return r.failTurn(ctx, p, "回合指针丢失")
		case strings.HasPrefix(target, refFailedPrefix), strings.HasPrefix(target, refWaitingPrefix):
			// 失败终态 / 等待审批挂起：交还控制权
			return nil
		default:
			return r.failTurn(ctx, p, "未知指针状态: "+target)
		}

		evs, err := r.store.ListByTurn(ctx, p.TurnID, 0, 0)
		if err != nil {
			return r.failTurn(ctx, p, "读取回合事件失败: "+err.Error())
		}
		st, err := foldTurn(ctx, r.store, evs)
		if err != nil {
			return r.failTurn(ctx, p, err.Error())
		}

		// 内容链尾：新写的内容事件以它为 parent_id，compile 沿链回放才能看见。
		// 优先取回合事件 fold 出的链尾；回合尚无内容事件时回落到会话链尾。
		tail := st.chainTail
		if tail == "" {
			tail, err = r.sessionChainTail(ctx, p.SessionID)
			if err != nil {
				return r.failTurn(ctx, p, "读取会话内容链失败: "+err.Error())
			}
		}

		// 1. 结清 pending 工具调用（含审批裁决）
		if call, ok := st.nextPending(); ok {
			suspended, err := r.settleCall(ctx, p, st, call, tail)
			if err != nil {
				return r.failTurn(ctx, p, err.Error())
			}
			if suspended {
				return nil
			}
			continue
		}

		// 2. 无待执行工具 → LLM 步
		done, err := r.stepLLM(ctx, p, tail)
		if err != nil {
			return r.failTurn(ctx, p, err.Error())
		}
		if done {
			return nil
		}
		// 3. stepLLM 写了 tool.called，进入下一轮结清
	}
}

// SessionChainTail 返回会话内容链的尾事件 ID（无内容事件时为空串）。
// 上层为新回合 fork 上下文时以它为 ForkAt，保证历史连续。
func SessionChainTail(ctx context.Context, store *eventlog.Store, sessionID string) (ids.EventID, error) {
	evs, err := store.ListBySession(ctx, sessionID, 0, 0)
	if err != nil {
		return "", fmt.Errorf("runtime: 读取会话事件失败: %w", err)
	}
	tail := ids.EventID("")
	for _, ev := range evs {
		if isContentEvent(ev.Type) {
			tail = ev.ID
		}
	}
	return tail, nil
}

// sessionChainTail 是 SessionChainTail 的运行时方法形式。
func (r *Runtime) sessionChainTail(ctx context.Context, sessionID string) (ids.EventID, error) {
	return SessionChainTail(ctx, r.store, sessionID)
}

// isContentEvent 判断事件是否为内容事件，规则与 compile 的可见性判断一致：
// 只有内容事件参与 parent_id 链与 ctx_op；message.delta 等仅作展示。
func isContentEvent(typ string) bool {
	switch typ {
	case eventlog.TypeMessageCreated, eventlog.TypeToolCalled,
		eventlog.TypeToolReturned, eventlog.TypePromptInjected:
		return true
	}
	return false
}

// publishToCtx 把内容事件追加到上下文，使其在后续 compile 中可见；
// 该事件已追加过（崩溃恢复后重放）则跳过，保证幂等。
func publishToCtx(ctx context.Context, store *eventlog.Store, ctxID ids.ContextID, evID ids.EventID) error {
	if evID == "" {
		return nil
	}
	if ctxID == "" {
		return fmt.Errorf("runtime: ContextID 不能为空")
	}
	ops, err := store.ListCtxOps(ctx, ctxID)
	if err != nil {
		return fmt.Errorf("runtime: 读取上下文操作失败: %w", err)
	}
	for _, op := range ops {
		if op.NewID == evID {
			return nil
		}
	}
	if _, err := store.AppendCtxOp(ctx, eventlog.CtxOpInput{
		Ctx: ctxID, Op: eventlog.CtxOpAppend, NewID: evID,
	}); err != nil {
		return fmt.Errorf("runtime: 把内容事件追加到上下文失败: %w", err)
	}
	return nil
}

// appendContentToCtx 是 publishToCtx 的运行时方法形式。
func (r *Runtime) appendContentToCtx(ctx context.Context, p TurnParams, evID ids.EventID) error {
	return publishToCtx(ctx, r.store, p.ContextID, evID)
}

// UserMessage 是一次用户输入的落盘参数。
type UserMessage struct {
	SessionID string        // 会话 ID
	TurnID    string        // 归属回合 ID，可空
	ContextID ids.ContextID // 追加到的上下文
	Text      string        // 正文
}

// AppendUserMessage 写入 user 角色的 message.created（以会话内容链尾为
// parent_id）并以 ctx_op.append 追加到指定上下文，返回落盘事件。
// 上层启动新回合时：先取 SessionChainTail 建上下文，再调用本函数。
func AppendUserMessage(ctx context.Context, store *eventlog.Store, m UserMessage) (eventlog.Event, error) {
	if m.SessionID == "" {
		return eventlog.Event{}, fmt.Errorf("runtime: SessionID 不能为空")
	}
	if m.ContextID == "" {
		return eventlog.Event{}, fmt.Errorf("runtime: ContextID 不能为空")
	}
	tail, err := SessionChainTail(ctx, store, m.SessionID)
	if err != nil {
		return eventlog.Event{}, err
	}
	ev, err := store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SessionID: m.SessionID,
		TurnID:    m.TurnID,
		Actor:     actorUser,
		SubjectID: ids.NewMessageID().String(),
		ParentID:  tail.String(),
		Blob:      []byte(m.Text),
		Attrs:     eventlog.MessageCreatedAttrs{Role: string(compile.RoleUser)},
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("runtime: 写用户消息失败: %w", err)
	}
	if err := publishToCtx(ctx, store, m.ContextID, ev.ID); err != nil {
		return eventlog.Event{}, err
	}
	return ev, nil
}

// PendingApproval 是一次已请求但尚未裁决的审批。
type PendingApproval struct {
	ToolCallID ids.ToolCallID `json:"tool_call_id"`
	Name       string         `json:"name"`
}

// TurnStatus 是回合的只读状态视图：refs 指针 + 事件 fold 得出，
// 不读任何可变字段表，进程重启后依然可得。
type TurnStatus struct {
	TurnID    string        `json:"turn_id"`
	SessionID string        `json:"session_id"`
	ContextID ids.ContextID `json:"context_id,omitempty"`
	Model     string        `json:"model,omitempty"`

	// Ref 取值见 RefRunning / RefCompleted / "waiting_approval:<审批ID>" /
	// "failed:<原因>"；空串表示回合尚未落盘任何事件。
	Ref string `json:"ref"`

	Started   bool   `json:"started"`
	Completed bool   `json:"completed"`
	Error     string `json:"error,omitempty"`

	// PendingApprovals 是等待用户裁决的工具调用（含工具名）。
	PendingApprovals []PendingApproval `json:"pending_approvals,omitempty"`
}

// Status 读取回合状态。回合不存在（无任何事件）时返回错误。
//
// 读取顺序为先指针后事件：每次 MoveRef 都发生在其对应事件之后，
// 因此这样读到的事件一定不旧于指针，避免出现"指针已挂起等待审批、
// 却看不到 approval.requested"（或"已完成却看不到 turn.completed"）的读偏斜。
func Status(ctx context.Context, store *eventlog.Store, turnID string) (TurnStatus, error) {
	if strings.TrimSpace(turnID) == "" {
		return TurnStatus{}, fmt.Errorf("runtime: TurnID 不能为空")
	}
	target, err := store.GetRef(ctx, TurnRefName(turnID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TurnStatus{}, fmt.Errorf("runtime: 读取回合指针失败: %w", err)
	}
	evs, err := store.ListByTurn(ctx, turnID, 0, 0)
	if err != nil {
		return TurnStatus{}, fmt.Errorf("runtime: 读取回合事件失败: %w", err)
	}
	if len(evs) == 0 {
		return TurnStatus{}, fmt.Errorf("runtime: 回合不存在: %s", turnID)
	}
	st, err := foldTurn(ctx, store, evs)
	if err != nil {
		return TurnStatus{}, err
	}
	status := TurnStatus{
		TurnID:    turnID,
		SessionID: st.sessionID,
		ContextID: st.contextID,
		Model:     st.model,
		Ref:       target,
		Started:   st.started,
		Completed: st.completed,
		Error:     st.failure,
	}
	for _, c := range st.calls {
		if st.isSettled(c) {
			continue
		}
		ap := st.approvalFor(c.id)
		if ap == nil || ap.decided != nil {
			continue
		}
		status.PendingApprovals = append(status.PendingApprovals,
			PendingApproval{ToolCallID: c.id, Name: c.name})
	}
	return status, nil
}

// DecideApproval 记录一次审批决定并把挂起等待审批的回合指针移回 running；
// 随后再次 RunTurn 即从审批点继续执行（不轮询任何 pending 表）。
func (r *Runtime) DecideApproval(ctx context.Context, p TurnParams, toolCallID ids.ToolCallID, approved bool, reason string) error {
	if p.TurnID == "" {
		return fmt.Errorf("runtime: TurnID 不能为空")
	}
	if toolCallID == "" {
		return fmt.Errorf("runtime: 工具调用 ID 不能为空")
	}
	decision := "rejected"
	if approved {
		decision = "approved"
	}
	if _, err := r.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeApprovalDecided,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorUser,
		SubjectID: toolCallID.String(), // 审批 ID 复用工具调用 ID
		Attrs:     eventlog.ApprovalDecidedAttrs{Decision: decision, Reason: reason},
	}); err != nil {
		return fmt.Errorf("runtime: 写 approval.decided 失败: %w", err)
	}
	if _, err := r.store.MoveRef(ctx, TurnRefName(p.TurnID), RefRunning); err != nil {
		return fmt.Errorf("runtime: 移动指针回 running 失败: %w", err)
	}
	return nil
}

// failTurn 写 turn.completed（error 非空）并把指针移到 failed:<原因>。
func (r *Runtime) failTurn(ctx context.Context, p TurnParams, reason string) error {
	if _, err := r.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeTurnCompleted,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: p.TurnID,
		Attrs:     eventlog.TurnCompletedAttrs{Error: reason},
	}); err != nil {
		return fmt.Errorf("runtime: 写 turn.completed 失败: %w", err)
	}
	if _, err := r.store.MoveRef(ctx, TurnRefName(p.TurnID), refFailedPrefix+reason); err != nil {
		return fmt.Errorf("runtime: 移动指针到 failed 失败: %w", err)
	}
	return fmt.Errorf("runtime: 回合 %s 失败: %s", p.TurnID, reason)
}
