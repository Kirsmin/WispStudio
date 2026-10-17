package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
)

// relResultOf 是 links 表中工具调用与结果的分组关系：
// src = tool.returned 事件 ID，dst = tool.called 事件 ID（与 compile 约定一致）。
const relResultOf = "result_of"

// settleCall 结清一次工具调用：需要审批的工具先走审批事件流，
// 批准后执行并写 tool.returned（含幂等键与 result_of 链接）。
// tail 是当前内容链尾事件 ID，结果事件以它为 parent_id。
// 返回 suspended=true 表示已挂起等待审批，调用方应立即退出循环。
func (r *Runtime) settleCall(ctx context.Context, p TurnParams, st *turnState, call pendingCall, tail ids.EventID) (suspended bool, err error) {
	if r.tools.NeedsApproval(call.name) {
		ap := st.approvalFor(call.id)
		if ap == nil {
			// write-ahead：审批请求先落盘，再挂起指针
			if _, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
				Type:      eventlog.TypeApprovalRequested,
				SessionID: p.SessionID,
				TurnID:    p.TurnID,
				Actor:     actorRuntime,
				SubjectID: call.id.String(), // 审批 ID 复用工具调用 ID，天然一对一且幂等
				Attrs: eventlog.ApprovalRequestedAttrs{
					ToolCallID: call.id.String(),
					Name:       call.name,
				},
			}); err != nil {
				return false, fmt.Errorf("runtime: 写 approval.requested 失败: %w", err)
			}
			if _, err := r.writer.MoveRef(ctx, TurnRefName(p.TurnID), refWaitingPrefix+call.id.String()); err != nil {
				return false, fmt.Errorf("runtime: 挂起回合指针失败: %w", err)
			}
			return true, nil
		}
		if ap.decided == nil {
			// 审批未决定：兜底把指针挂起（正常路径 API 层已移过指针）
			if _, err := r.writer.MoveRef(ctx, TurnRefName(p.TurnID), refWaitingPrefix+call.id.String()); err != nil {
				return false, fmt.Errorf("runtime: 挂起回合指针失败: %w", err)
			}
			return true, nil
		}
		if !ap.decided.approved {
			// 拒绝：写占位结果结清调用，回合继续
			return false, r.returnResult(ctx, p, call, ToolResult{
				Status: "failure",
				Error:  "用户拒绝了该操作: " + ap.decided.reason,
			}, tail)
		}
	}

	result, err := r.tools.Execute(ctx, call.name, []byte(call.arguments))
	if err != nil {
		result = ToolResult{Status: "failure", Error: err.Error()}
	}
	return false, r.returnResult(ctx, p, call, result, tail)
}

// returnResult 写 tool.returned、补 result_of 链接，并把结果追加到上下文。
func (r *Runtime) returnResult(ctx context.Context, p TurnParams, call pendingCall, result ToolResult, tail ids.EventID) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("runtime: 序列化工具结果失败: %w", err)
	}
	ev, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeToolReturned,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: call.id.String(),
		ParentID:  tail.String(),
		CauseID:   call.calledEventID.String(),
		Blob:      raw,
		Attrs:     eventlog.ToolReturnedAttrs{IdempotencyKey: call.idempotencyKey},
	})
	if err != nil {
		return fmt.Errorf("runtime: 写 tool.returned 失败: %w", err)
	}
	if err := r.writer.AddLink(ctx, ev.ID.String(), call.calledEventID.String(), relResultOf); err != nil {
		return fmt.Errorf("runtime: 写 result_of 链接失败: %w", err)
	}
	return r.appendContentToCtx(ctx, p, ev.ID)
}
