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
}

// createSessionRequest 是 POST /api/sessions 的请求体。
type createSessionRequest struct {
	Title string `json:"Title"`
	Model string `json:"Model"`
}

// postMessageRequest 是 POST /api/sessions/{id}/messages 的请求体。
// JSON 解码大小写不敏感，Message 兼容旧前端的 message 字段。
type postMessageRequest struct {
	Text    string `json:"Text"`
	Message string `json:"Message"`
	Model   string `json:"Model"`
}

// turnStartedDTO 是启动回合后的响应。
type turnStartedDTO struct {
	SessionID string `json:"SessionID"`
	ContextID string `json:"ContextID"`
	TurnID    string `json:"TurnID"`
	Model     string `json:"Model"`
	MessageID string `json:"MessageID,omitempty"`
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
		out = append(out, s.projectSession(ctx, sessionID, evs))
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

// handlePostMessage 写 message.created + turn.started，然后异步启动 runTurn。
func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}
	var req postMessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		text = strings.TrimSpace(req.Message)
	}
	if !validText(text) {
		writeError(w, http.StatusBadRequest, "Text 不能为空且必须是合法 UTF-8")
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.sessionModel(r.Context(), sessionID)
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

	ctxRow, msgEv, err := s.prepareTurn(r.Context(), sessionID, turnID, text)
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
		MessageID: msgEv.SubjectID,
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
		writeJSON(w, http.StatusOK, stopSessionDTO{SessionID: sessionID, Stopped: false})
		return
	}

	a.rt.Interrupt("stopped")
	a.cancel()
	select {
	case <-a.done:
		writeJSON(w, http.StatusOK, stopSessionDTO{SessionID: sessionID, TurnID: a.turnID, Stopped: true})
	case <-time.After(defaultStopTimeout):
		writeError(w, http.StatusGatewayTimeout, "停止回合超时")
	}
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
		}
	}
	if firstUserText != "" && (dto.Title == "" || dto.Title == "新会话") {
		dto.Title = truncateRunes(firstUserText, sessionTitleRunes)
	}
	dto.Running = s.isRunning(sessionID)
	return dto
}

// sessionModel 返回会话最近一次使用的模型；用于未显式传 Model 的消息。
func (s *Server) sessionModel(ctx context.Context, sessionID string) string {
	evs, err := s.cfg.Store.ListBySession(ctx, sessionID, 0, 0)
	if err != nil {
		return ""
	}
	model := ""
	for _, ev := range evs {
		switch ev.Type {
		case eventlog.TypeSessionCreated:
			var attrs eventlog.SessionCreatedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Model != "" {
				model = attrs.Model
			}
		case eventlog.TypeLLMRequested:
			var attrs eventlog.LLMRequestedAttrs
			if json.Unmarshal(ev.Attrs, &attrs) == nil && attrs.Model != "" {
				model = attrs.Model
			}
		}
	}
	return model
}
