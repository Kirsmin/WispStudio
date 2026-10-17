package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
)

// pendingCall 是一次已落盘、尚未结清的工具调用。
type pendingCall struct {
	id             ids.ToolCallID // 内部 ToolCallID（tool.called 的 SubjectID）
	name           string         // 工具名
	providerID     string         // provider 侧 tool_use_id
	arguments      string         // 调用参数 JSON 原文（blob 正文）
	idempotencyKey string         // 幂等键
	calledEventID  ids.EventID    // tool.called 事件 ID（写 result_of 链接用）
}

// approvalInfo 是一次审批请求及其决定（若有）。
type approvalInfo struct {
	toolCallID ids.ToolCallID
	decided    *approvalDecision
}

// approvalDecision 是 approval.decided 事件的解析结果。
type approvalDecision struct {
	approved bool
	reason   string
}

// turnState 是从回合全部事件折叠出的当前状态（无任何可变存储）。
type turnState struct {
	started   bool
	completed bool
	failure   string

	// sessionID / contextID 来自 turn.started：恢复回合时重建编译入参。
	sessionID string
	contextID ids.ContextID

	// model / providerID 来自最近一次 llm.requested：审批后继续时据此
	// 重建 provider 客户端，不依赖进程内存。
	model      string
	providerID string

	// chainTail 是回合内最后一个内容事件的 ID：新内容事件以它为
	// parent_id，保证 compile 沿 parent_id 链能看见本条内容。
	chainTail ids.EventID

	calls           []pendingCall            // 按 seq 升序的全部 tool.called
	settledSubjects map[string]bool          // 已有 tool.returned 的调用 SubjectID
	settledKeys     map[string]bool          // 已有 tool.returned 的幂等键
	approvals       map[string]*approvalInfo // 审批 ID（= 工具调用 ID）→ 审批信息
}

// foldTurn 把回合事件按 seq 升序折叠为当前状态。
// tool.called 的参数正文需读 blobs，故需要 store。
func foldTurn(ctx context.Context, store *eventlog.Store, evs []eventlog.Event) (*turnState, error) {
	st := &turnState{
		settledSubjects: map[string]bool{},
		settledKeys:     map[string]bool{},
		approvals:       map[string]*approvalInfo{},
	}
	for _, ev := range evs {
		if st.sessionID == "" {
			st.sessionID = ev.SessionID
		}
		// 内容事件推进 parent_id 链尾（ListByTurn 已按 seq 升序）
		if eventlog.IsContentType(ev.Type) {
			st.chainTail = ev.ID
		}
		switch ev.Type {
		case eventlog.TypeTurnStarted:
			st.started = true
			var attrs eventlog.TurnStartedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil {
				st.contextID = ids.ContextID(attrs.ContextID)
			}
		case eventlog.TypeLLMRequested:
			var attrs eventlog.LLMRequestedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil {
				st.model = attrs.Model
				st.providerID = attrs.ProviderID
			}
		case eventlog.TypeTurnCompleted:
			var attrs eventlog.TurnCompletedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil {
				st.completed = true
				st.failure = attrs.Error
			}
		case eventlog.TypeToolCalled:
			var attrs eventlog.ToolCalledAttrs
			if json.Unmarshal(ev.Attrs, &attrs) != nil {
				continue
			}
			args := ""
			if ev.Blob != "" {
				data, err := store.GetBlob(ctx, ev.Blob)
				if err != nil {
					return nil, fmt.Errorf("runtime: 读取 tool.called 参数失败: %w", err)
				}
				args = string(data)
			}
			st.calls = append(st.calls, pendingCall{
				id:             ids.ToolCallID(ev.SubjectID),
				name:           attrs.Name,
				providerID:     attrs.ProviderID,
				arguments:      args,
				idempotencyKey: attrs.IdempotencyKey,
				calledEventID:  ev.ID,
			})
		case eventlog.TypeToolReturned:
			st.settledSubjects[ev.SubjectID] = true
			var attrs eventlog.ToolReturnedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.IdempotencyKey != "" {
				st.settledKeys[attrs.IdempotencyKey] = true
			}
		case eventlog.TypeApprovalRequested:
			var attrs eventlog.ApprovalRequestedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) != nil {
				continue
			}
			st.approvals[ev.SubjectID] = &approvalInfo{toolCallID: ids.ToolCallID(attrs.ToolCallID)}
		case eventlog.TypeApprovalDecided:
			info, ok := st.approvals[ev.SubjectID]
			if !ok {
				info = &approvalInfo{}
				st.approvals[ev.SubjectID] = info
			}
			var attrs eventlog.ApprovalDecidedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil {
				info.decided = &approvalDecision{approved: attrs.Decision == "approved", reason: attrs.Reason}
			}
		}
	}
	return st, nil
}

// isSettled 判断一次调用是否已结清：调用 SubjectID 命中 tool.returned，
// 或其幂等键已命中（崩溃恢复时 subject 可能不同）。
func (st *turnState) isSettled(c pendingCall) bool {
	if st.settledSubjects[c.id.String()] {
		return true
	}
	return c.idempotencyKey != "" && st.settledKeys[c.idempotencyKey]
}

// nextPending 返回第一个未结清的工具调用。
// 崩溃恢复按幂等键判断：调用 SubjectID 或幂等键已有 tool.returned 即视为结清。
func (st *turnState) nextPending() (pendingCall, bool) {
	for _, c := range st.calls {
		if st.isSettled(c) {
			continue
		}
		return c, true
	}
	return pendingCall{}, false
}

// approvalFor 返回某工具调用对应的审批信息（审批 ID 复用工具调用 ID）。
func (st *turnState) approvalFor(toolCallID ids.ToolCallID) *approvalInfo {
	return st.approvals[toolCallID.String()]
}
