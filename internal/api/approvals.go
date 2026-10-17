package api

import (
	"net/http"
	"strings"

	"wisp/internal/ids"
	"wisp/internal/runtime"
)

// approvalRequest 是 POST /api/sessions/{sessionID}/approvals/{toolCallID} 的请求体。
type approvalRequest struct {
	Decision string `json:"Decision"` // approved | rejected
	Reason   string `json:"Reason"`   // 拒绝理由，可空
}

// approvalResponse 是审批被接受后的响应。
type approvalResponse struct {
	TurnID     string `json:"TurnID"`
	ToolCallID string `json:"ToolCallID"`
	Decision   string `json:"Decision"`
}

// handleApproval 提交审批决定并把挂起等待审批的回合恢复执行。
// 会话同一时刻最多只有一个等待审批的回合，因此不需要客户端传 TurnID。
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}
	toolCallID, err := ids.ParseToolCallID(r.PathValue("toolCallID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "工具调用 ID 格式不合法: %v", err)
		return
	}
	var req approvalRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var approved bool
	switch strings.ToLower(strings.TrimSpace(req.Decision)) {
	case "approved":
		approved = true
	case "rejected":
	default:
		writeError(w, http.StatusBadRequest, "Decision 只能是 approved 或 rejected，收到 %q", req.Decision)
		return
	}

	ctx := r.Context()
	// 先拿会话锁再读状态：避免与 stop 并发时把已结束的回合重新拉起。
	lock := s.sessionState(sessionID)
	lock.lock.Lock()
	// 成功启动回合后，锁的所有权移交给 runTurn；失败路径由这里释放。
	started := false
	defer func() {
		if !started {
			lock.lock.Unlock()
		}
	}()

	turnID, waiting := s.waitingTurnID(ctx, sessionID)
	if !waiting {
		writeError(w, http.StatusConflict, "会话当前没有等待审批的回合")
		return
	}
	status, err := runtime.Status(ctx, s.cfg.Store, turnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if status.SessionID != sessionID {
		writeError(w, http.StatusConflict, "回合 %s 不属于会话 %s", turnID, sessionID)
		return
	}
	if status.Completed || runtime.IsTerminalRef(status.Ref) {
		writeError(w, http.StatusConflict, "回合已结束，无法再审批")
		return
	}
	pending := false
	for _, pa := range status.PendingApprovals {
		if pa.ToolCallID == toolCallID {
			pending = true
			break
		}
	}
	if !pending {
		writeError(w, http.StatusConflict, "工具调用 %s 不在待审批状态", toolCallID)
		return
	}
	if status.ContextID == "" {
		writeError(w, http.StatusConflict, "回合 %s 缺少上下文信息，无法恢复", turnID)
		return
	}

	fullID, err := s.resolveModel(status.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	ep, err := s.cfg.Catalog.Endpoint(fullID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	rt, err := s.newRuntime(ep)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	p := runtime.TurnParams{SessionID: sessionID, TurnID: turnID, ContextID: status.ContextID}
	if err := rt.DecideApproval(ctx, p, toolCallID, approved, strings.TrimSpace(req.Reason)); err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	runCtx, cancel := s.newTurnContext()
	a := &activeTurn{turnID: turnID, rt: rt, cancel: cancel, done: make(chan struct{})}
	if !s.addActive(sessionID, a) {
		cancel()
		writeError(w, http.StatusServiceUnavailable, "服务正在关闭")
		return
	}
	started = true
	go s.runTurn(runCtx, &lock.lock, a, p)

	decision := "rejected"
	if approved {
		decision = "approved"
	}
	writeJSON(w, http.StatusAccepted, approvalResponse{
		TurnID: turnID, ToolCallID: toolCallID.String(), Decision: decision,
	})
}
