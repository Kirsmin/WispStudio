package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
	"wisp/internal/runtime"
)

// 会话标题截断长度（按 rune）。
const sessionTitleRunes = 20

// sessionDTO 是 session.created 事件与后续内容事件的折叠结果。
type sessionDTO struct {
	ID        string `json:"ID"`
	SessionID string `json:"SessionID"`
	Title     string `json:"Title"`
	Model     string `json:"Model,omitempty"`

	CreatedAt int64 `json:"CreatedAt"` // Unix 毫秒
	UpdatedAt int64 `json:"UpdatedAt"` // Unix 毫秒

	MessageCount int    `json:"MessageCount"`
	TurnCount    int    `json:"TurnCount"`
	LastTurnID   string `json:"LastTurnID,omitempty"`
	Running      bool   `json:"Running"`

	// Deleted 只用于服务端折叠，不暴露给前端。
	Deleted bool `json:"-"`
}

// createSessionRequest 是 POST /api/sessions 的请求体。
type createSessionRequest struct {
	Title string `json:"Title"`
	Model string `json:"Model"`
}

// postMessageRequest 是 POST /api/sessions/{id}/messages 的请求体。
type postMessageRequest struct {
	Text  string `json:"Text"`
	Model string `json:"Model"`
}

// turnStartedDTO 是启动回合后的响应。
type turnStartedDTO struct {
	SessionID string `json:"SessionID"`
	ContextID string `json:"ContextID"`
	TurnID    string `json:"TurnID"`
	Model     string `json:"Model"`
}

// stopSessionDTO 是停止会话的响应。
type stopSessionDTO struct {
	SessionID string `json:"SessionID"`
	TurnID    string `json:"TurnID,omitempty"`
	Stopped   bool   `json:"Stopped"`
}

// handleListSessions 返回全部会话的投影，按最近活动倒序。
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionIDs, err := s.cfg.Store.ListSessionIDs(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话列表失败: %v", err)
		return
	}
	out := make([]sessionDTO, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		evs, err := s.cfg.Store.ListBySession(ctx, sessionID, 0, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取会话 %s 失败: %v", sessionID, err)
			return
		}
		item := s.projectSession(ctx, sessionID, evs)
		if item.Deleted {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].SessionID < out[j].SessionID
	})
	writeJSON(w, http.StatusOK, out)
}

// handleCreateSession 创建会话并写 session.created 事件。
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !decodeOptionalBody(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新会话"
	}
	if !utf8.ValidString(title) {
		writeError(w, http.StatusBadRequest, "Title 必须是合法 UTF-8")
		return
	}
	title = truncateRunes(title, 200)

	model := strings.TrimSpace(req.Model)
	if model != "" {
		fullID, err := provider.Parse(model)
		if err != nil {
			writeError(w, http.StatusBadRequest, "%v", err)
			return
		}
		if _, err := s.cfg.Catalog.Get(fullID); err != nil {
			writeError(w, http.StatusBadRequest, "%v", err)
			return
		}
		model = fullID.String()
	}

	sessionID := ids.NewSessionID().String()
	ev, err := s.writer.AppendEvent(r.Context(), eventlog.EventInput{
		Type:      eventlog.TypeSessionCreated,
		SessionID: sessionID,
		Actor:     "user",
		SubjectID: sessionID,
		Attrs: eventlog.SessionCreatedAttrs{
			Title: title,
			Model: model,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建会话失败: %v", err)
		return
	}
	writeJSON(w, http.StatusCreated, sessionDTO{
		ID:        sessionID,
		SessionID: sessionID,
		Title:     title,
		Model:     model,
		CreatedAt: ev.TS,
		UpdatedAt: ev.TS,
	})
}

// handleDeleteSession 软删除会话：追加 session.deleted 事件。
// 事件日志保持只追加，既有内容不物理删除；列表与会话读取会跳过它。
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}
	ctx := r.Context()
	facts, err := s.sessionFacts(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败: %v", err)
		return
	}
	if !facts.exists {
		writeError(w, http.StatusNotFound, "会话不存在: %s", sessionID)
		return
	}
	if facts.deleted {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.isRunning(sessionID) {
		writeError(w, http.StatusConflict, "回合正在执行，请先停止再删除会话")
		return
	}
	if turnID, waiting := s.waitingTurnID(ctx, sessionID); waiting {
		writeError(w, http.StatusConflict, "回合 %s 正在等待审批，请先批准、拒绝或停止", turnID)
		return
	}
	if _, err := s.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeSessionDeleted,
		SessionID: sessionID,
		Actor:     "user",
		SubjectID: sessionID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "删除会话失败: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostMessage 写 message.created + turn.started，然后异步启动 runTurn。
func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}
	facts, err := s.sessionFacts(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败: %v", err)
		return
	}
	if !facts.exists {
		writeError(w, http.StatusNotFound, "会话不存在: %s", sessionID)
		return
	}
	if facts.deleted {
		writeError(w, http.StatusGone, "会话已删除: %s", sessionID)
		return
	}
	var req postMessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if !validText(text) {
		writeError(w, http.StatusBadRequest, "Text 不能为空且必须是合法 UTF-8")
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = facts.model
	}
	fullID, err := s.resolveModel(model)
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

	turnID := ids.NewTurnID().String()
	lock := s.sessionState(sessionID)
	lock.lock.Lock()

	ctxRow, err := s.prepareTurn(r.Context(), sessionID, turnID, text, facts.tail)
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

	writeJSON(w, http.StatusCreated, turnStartedDTO{
		SessionID: sessionID,
		ContextID: ctxRow.ID.String(),
		TurnID:    turnID,
		Model:     fullID.String(),
	})
}

// handleStopSession 停止会话当前回合：取消执行并写 turn.completed(error=stopped)。
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}
	a := s.currentActive(sessionID)
	if a == nil {
		// 没有执行中的 goroutine，也可能正挂起等待审批；一并写成停止终态。
		// 持会话锁复核，避免与刚恢复的审批回合交错。
		state := s.sessionState(sessionID)
		if !state.lock.TryLock() {
			if active := s.currentActive(sessionID); active != nil {
				s.stopActive(w, sessionID, active)
				return
			}
			writeError(w, http.StatusConflict, "会话正在处理另一个请求，请稍后重试")
			return
		}
		if active := s.currentActive(sessionID); active != nil {
			state.lock.Unlock()
			s.stopActive(w, sessionID, active)
			return
		}
		stopped, turnID, err := s.stopWaitingTurn(r.Context(), sessionID)
		state.lock.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "停止回合失败: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, stopSessionDTO{SessionID: sessionID, TurnID: turnID, Stopped: stopped})
		return
	}

	s.stopActive(w, sessionID, a)
}

// stopActive 取消执行中的回合并等待终态事件落盘。
func (s *Server) stopActive(w http.ResponseWriter, sessionID string, a *activeTurn) {
	a.rt.Interrupt("stopped")
	a.cancel()
	select {
	case <-a.done:
		writeJSON(w, http.StatusOK, stopSessionDTO{SessionID: sessionID, TurnID: a.turnID, Stopped: true})
	case <-time.After(defaultStopTimeout):
		writeError(w, http.StatusGatewayTimeout, "停止回合超时")
	}
}

// waitingTurnID 返回最近一个挂起等待审批的回合 ID；没有则 waiting=false。
func (s *Server) waitingTurnID(ctx context.Context, sessionID string) (turnID string, waiting bool) {
	evs, err := s.cfg.Store.ListBySession(ctx, sessionID, 0, 0)
	if err != nil {
		return "", false
	}
	for _, ev := range evs {
		if ev.Type == eventlog.TypeTurnStarted {
			turnID = ev.TurnID
		}
	}
	if turnID == "" {
		return "", false
	}
	status, err := runtime.Status(ctx, s.cfg.Store, turnID)
	if err != nil {
		return turnID, false
	}
	return turnID, runtime.IsWaitingRef(status.Ref)
}

// stopWaitingTurn 把挂起等待审批的回合写成停止终态。
// 返回 stopped=false 表示当前没有可停止的挂起回合。
func (s *Server) stopWaitingTurn(ctx context.Context, sessionID string) (stopped bool, turnID string, err error) {
	turnID, waiting := s.waitingTurnID(ctx, sessionID)
	if !waiting {
		return false, turnID, nil
	}
	if _, err := s.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeTurnCompleted,
		SessionID: sessionID,
		TurnID:    turnID,
		Actor:     "runtime",
		SubjectID: turnID,
		Attrs:     eventlog.TurnCompletedAttrs{Error: "stopped"},
	}); err != nil {
		return false, turnID, err
	}
	if _, err := s.writer.MoveRefInSession(ctx, runtime.TurnRefName(turnID), "failed:stopped", sessionID); err != nil {
		return false, turnID, err
	}
	return true, turnID, nil
}

// projectSession 把某会话的全部事件折叠为列表项。
func (s *Server) projectSession(ctx context.Context, sessionID string, evs []eventlog.Event) sessionDTO {
	dto := sessionDTO{ID: sessionID, SessionID: sessionID, Title: "新会话"}
	if len(evs) > 0 {
		dto.CreatedAt = evs[0].TS
		dto.UpdatedAt = evs[len(evs)-1].TS
	}
	var firstUserText string
	for _, ev := range evs {
		switch ev.Type {
		case eventlog.TypeSessionCreated:
			var attrs eventlog.SessionCreatedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil {
				if strings.TrimSpace(attrs.Title) != "" {
					dto.Title = attrs.Title
				}
				if attrs.Model != "" {
					dto.Model = attrs.Model
				}
			}
		case eventlog.TypeMessageCreated:
			dto.MessageCount++
			if firstUserText == "" {
				var attrs eventlog.MessageCreatedAttrs
				if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Role == "user" && ev.Blob != "" {
					if data, err := s.cfg.Store.GetBlob(ctx, ev.Blob); err == nil && utf8.Valid(data) {
						firstUserText = string(data)
					}
				}
			}
		case eventlog.TypeTurnStarted:
			dto.TurnCount++
			dto.LastTurnID = ev.TurnID
		case eventlog.TypeLLMRequested:
			var attrs eventlog.LLMRequestedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Model != "" {
				dto.Model = attrs.Model
			}
		case eventlog.TypeSessionDeleted:
			dto.Deleted = true
		}
	}
	if firstUserText != "" && (dto.Title == "" || dto.Title == "新会话") {
		dto.Title = truncateRunes(firstUserText, sessionTitleRunes)
	}
	dto.Running = s.isRunning(sessionID)
	return dto
}

// sessionFacts 是一次会话读取折叠出的事实，供消息写入路径复用，
// 避免为“存在性 / 删除状态 / 模型 / 内容链尾”重复扫描整条会话。
type sessionFacts struct {
	exists  bool
	deleted bool
	model   string
	tail    ids.EventID
}

// sessionFacts 读取整个会话并折叠出写入新回合需要的事实。
func (s *Server) sessionFacts(ctx context.Context, sessionID string) (sessionFacts, error) {
	evs, err := s.cfg.Store.ListBySession(ctx, sessionID, 0, 0)
	if err != nil {
		return sessionFacts{}, err
	}
	facts := sessionFacts{exists: len(evs) > 0}
	for _, ev := range evs {
		switch ev.Type {
		case eventlog.TypeSessionCreated:
			var attrs eventlog.SessionCreatedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Model != "" {
				facts.model = attrs.Model
			}
		case eventlog.TypeLLMRequested:
			var attrs eventlog.LLMRequestedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Model != "" {
				facts.model = attrs.Model
			}
		case eventlog.TypeSessionDeleted:
			facts.deleted = true
		}
		if eventlog.IsContentType(ev.Type) {
			facts.tail = ev.ID
		}
	}
	return facts, nil
}
