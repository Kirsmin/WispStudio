package api

import (
	"net/http"
	"strings"

	"wisp/internal/ids"
	"wisp/internal/runtime"
)

// ---- 兼容旧接口：模型目录 / 启动回合 / 回合状态 / 审批 ----

type catalogModel struct {
	FullID     string `json:"full_id"`
	Alias      string `json:"alias,omitempty"`
	Discovered bool   `json:"discovered,omitempty"`
}

type catalogProvider struct {
	ProviderID string         `json:"provider_id"`
	Models     []catalogModel `json:"models"`
}

type catalogResponse struct {
	DefaultModel string            `json:"default_model,omitempty"`
	Providers    []catalogProvider `json:"providers"`
}

// handleCatalog 是旧版模型目录接口，保留给尚未迁移的前端。
func (s *Server) handleCatalog(w http.ResponseWriter, _ *http.Request) {
	resp := catalogResponse{Providers: []catalogProvider{}}
	if def, err := s.cfg.Catalog.DefaultModel(); err == nil {
		resp.DefaultModel = def.String()
	}
	for _, pid := range s.cfg.Catalog.ProviderIDs() {
		models, err := s.cfg.Catalog.ListByProvider(pid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "%v", err)
			return
		}
		item := catalogProvider{ProviderID: pid, Models: []catalogModel{}}
		for _, m := range models {
			item.Models = append(item.Models, catalogModel{
				FullID: m.FullID.String(), Alias: m.Alias, Discovered: m.Discovered,
			})
		}
		resp.Providers = append(resp.Providers, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

type startTurnRequest struct {
	SessionID string `json:"session_id"` // 空 = 新建会话
	Text      string `json:"text"`       // 用户输入
	Model     string `json:"model"`      // {ProviderID}/{ModelID}，空 = 默认模型
}

type startTurnResponse struct {
	SessionID string `json:"session_id"`
	ContextID string `json:"context_id"`
	TurnID    string `json:"turn_id"`
	Model     string `json:"model"`
}

// handleStartTurn 是旧版启动回合接口；内部仍写 message.created + turn.started。
func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	var req startTurnRequest
	if !decodeBody(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if !validText(text) {
		writeError(w, http.StatusBadRequest, "text 不能为空且必须是合法 UTF-8")
		return
	}

	fullID, err := s.resolveModel(strings.TrimSpace(req.Model))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	ep, err := s.cfg.Catalog.Endpoint(fullID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = ids.NewSessionID().String()
	} else if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "session_id 格式不合法（应为 sess_<ULID>）: %v", err)
		return
	}

	rt, err := s.newRuntime(ep)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	turnID := ids.NewTurnID().String()
	lock := s.sessionState(sessionID)
	lock.lock.Lock()
	ctxRow, _, err := s.prepareTurn(r.Context(), sessionID, turnID, text)
	if err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	p := runtime.TurnParams{SessionID: sessionID, TurnID: turnID, ContextID: ctxRow.ID}
	if err := rt.EnsureStarted(r.Context(), p); err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	runCtx, cancel := s.newTurnContext()
	a := &activeTurn{turnID: turnID, rt: rt, cancel: cancel, done: make(chan struct{})}
	if !s.addActive(sessionID, a) {
		cancel()
		lock.lock.Unlock()
		writeError(w, http.StatusServiceUnavailable, "服务正在关闭")
		return
	}
	go s.runTurn(runCtx, &lock.lock, a, p)

	writeJSON(w, http.StatusCreated, startTurnResponse{
		SessionID: sessionID,
		ContextID: ctxRow.ID.String(),
		TurnID:    turnID,
		Model:     fullID.String(),
	})
}

type approvalRequest struct {
	Decision string `json:"decision"` // approved | rejected
	Reason   string `json:"reason"`   // 拒绝理由，可空
	Model    string `json:"model"`    // 可选覆盖，空 = 沿用回合内记录的模型
}

type approvalResponse struct {
	TurnID     string `json:"turn_id"`
	ToolCallID string `json:"tool_call_id"`
	Decision   string `json:"decision"`
}

// handleTurnStatus 读取回合状态（refs 指针 + 事件折叠）。
func (s *Server) handleTurnStatus(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turnID")
	status, err := runtime.Status(r.Context(), s.cfg.Store, turnID)
	if err != nil {
		if strings.Contains(err.Error(), "回合不存在") {
			writeError(w, http.StatusNotFound, "%v", err)
			return
		}
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleApproval 提交审批决定并恢复回合执行。
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turnID")
	toolCallID := ids.ToolCallID(r.PathValue("toolCallID"))
	var req approvalRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var approved bool
	switch strings.ToLower(strings.TrimSpace(req.Decision)) {
	case "approved":
		approved = true
	case "rejected":
		approved = false
	default:
		writeError(w, http.StatusBadRequest, "decision 只能是 approved 或 rejected，收到 %q", req.Decision)
		return
	}

	ctx := r.Context()
	status, err := runtime.Status(ctx, s.cfg.Store, turnID)
	if err != nil {
		if strings.Contains(err.Error(), "回合不存在") {
			writeError(w, http.StatusNotFound, "%v", err)
			return
		}
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// 先拿会话锁再复核状态：避免与 stop 并发时把已结束的回合重新拉起。
	lock := s.sessionState(status.SessionID)
	lock.lock.Lock()
	status, err = runtime.Status(ctx, s.cfg.Store, turnID)
	if err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if status.Completed || status.Ref == "completed" || strings.HasPrefix(status.Ref, "failed:") {
		lock.lock.Unlock()
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
		lock.lock.Unlock()
		writeError(w, http.StatusConflict, "工具调用 %s 不在待审批状态", toolCallID)
		return
	}
	if status.ContextID == "" {
		lock.lock.Unlock()
		writeError(w, http.StatusConflict, "回合 %s 缺少上下文信息，无法恢复", turnID)
		return
	}

	fullID, err := s.resolveModel(firstNonEmpty(strings.TrimSpace(req.Model), status.Model))
	if err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	ep, err := s.cfg.Catalog.Endpoint(fullID)
	if err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	rt, err := s.newRuntime(ep)
	if err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	p := runtime.TurnParams{SessionID: status.SessionID, TurnID: turnID, ContextID: status.ContextID}
	if err := rt.DecideApproval(ctx, p, toolCallID, approved, strings.TrimSpace(req.Reason)); err != nil {
		lock.lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	runCtx, cancel := s.newTurnContext()
	a := &activeTurn{turnID: turnID, rt: rt, cancel: cancel, done: make(chan struct{})}
	if !s.addActive(status.SessionID, a) {
		cancel()
		lock.lock.Unlock()
		writeError(w, http.StatusServiceUnavailable, "服务正在关闭")
		return
	}
	go s.runTurn(runCtx, &lock.lock, a, p)

	decision := "rejected"
	if approved {
		decision = "approved"
	}
	writeJSON(w, http.StatusAccepted, approvalResponse{
		TurnID: turnID, ToolCallID: toolCallID.String(), Decision: decision,
	})
}
