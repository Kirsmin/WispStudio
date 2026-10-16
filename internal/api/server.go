package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
	"wisp/internal/runtime"
)

// 默认值。
const (
	defaultTurnTimeout = 10 * time.Minute // 单回合执行超时
	defaultEventLimit  = 500              // 事件流单次返回上限
	maxEventLimit      = 2000
	maxBodyBytes       = 1 << 20 // 请求体上限 1 MiB
)

// Config 是 Server 的构造参数。
type Config struct {
	Store         *eventlog.Store      // 事件日志，必填
	Catalog       *provider.Catalog    // 模型目录，必填
	WorkspaceRoot string               // 工具工作区根，空 = 进程当前目录
	DefaultModel  provider.ModelFullID // 请求未指定模型时的默认模型，空 = 目录内第一个
	TurnTimeout   time.Duration        // 单回合超时，<=0 用默认 10 分钟
	Logf          func(string, ...any) // 日志，nil = 丢弃
}

// Server 是最小 HTTP API 服务。
type Server struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*sync.Mutex // 会话 ID → 串行执行锁
}

// NewServer 构造 API 服务。
func NewServer(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("api: Store 不能为空")
	}
	if cfg.Catalog == nil {
		return nil, fmt.Errorf("api: Catalog 不能为空")
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = defaultTurnTimeout
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Server{cfg: cfg, sessions: map[string]*sync.Mutex{}}, nil
}

// Handler 返回路由。路径参数使用 Go 1.22 的 {name} 通配。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	mux.HandleFunc("POST /api/turns", s.handleStartTurn)
	mux.HandleFunc("GET /api/turns/{turnID}", s.handleTurnStatus)
	mux.HandleFunc("GET /api/sessions/{sessionID}/events", s.handleEvents)
	mux.HandleFunc("POST /api/turns/{turnID}/approvals/{toolCallID}", s.handleApproval)
	return mux
}

// sessionLock 返回会话的执行锁（懒创建）。
func (s *Server) sessionLock(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.sessions[sessionID]
	if !ok {
		lock = &sync.Mutex{}
		s.sessions[sessionID] = lock
	}
	return lock
}

// newRuntime 按 provider 连接参数构造运行时。
func (s *Server) newRuntime(ep provider.Endpoint) (*runtime.Runtime, error) {
	client := &provider.Client{
		BaseURL:    ep.BaseURL,
		Secret:     ep.Secret,
		MaxRetries: -1, // 用默认重试（幂等的 HTTP 重试，非工具重放）
		Timeout:    -1, // 用默认单次超时
	}
	return runtime.New(s.cfg.Store, runtime.Options{
		WorkspaceRoot: s.cfg.WorkspaceRoot,
		ProviderID:    ep.ProviderID,
		Model:         ep.FullID.String(),
		ProviderModel: ep.ModelID, // 服务商只认裸 id，全称 id 仅用于事件
		Client:        client,
	})
}

// resolveModel 确定本次请求使用的模型全称 id。
func (s *Server) resolveModel(requested string) (provider.ModelFullID, error) {
	if requested != "" {
		return provider.Parse(requested)
	}
	if s.cfg.DefaultModel != "" {
		return s.cfg.DefaultModel, nil
	}
	return s.cfg.Catalog.DefaultModel()
}

// runTurn 在后台执行回合；结束时释放会话锁。回合内的错误已由 runtime
// 写成 turn.completed(error)，这里只记日志，不改变 HTTP 响应（已发出）。
func (s *Server) runTurn(lock *sync.Mutex, rt *runtime.Runtime, p runtime.TurnParams) {
	defer lock.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.cfg.Logf("api: 回合 %s 执行 panic: %v", p.TurnID, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TurnTimeout)
	defer cancel()
	if err := rt.RunTurn(ctx, p); err != nil {
		s.cfg.Logf("api: 回合 %s 结束于错误: %v", p.TurnID, err)
	}
}

// ---- GET /api/health ----

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "wisp"})
}

// ---- GET /api/catalog ----

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

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
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

// ---- POST /api/turns ----

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

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	var req startTurnRequest
	if !decodeBody(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text 不能为空")
		return
	}
	if !utf8.ValidString(text) {
		writeError(w, http.StatusBadRequest, "text 必须是合法 UTF-8")
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

	ctx := r.Context()
	turnID := ids.NewTurnID().String()

	// 同步准备阶段与会话锁：新建上下文（fork 在会话内容链尾）→ 落盘用户消息。
	lock := s.sessionLock(sessionID)
	lock.Lock()
	ctxRow, err := s.prepareTurn(ctx, sessionID, turnID, text)
	if err != nil {
		lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	p := runtime.TurnParams{SessionID: sessionID, TurnID: turnID, ContextID: ctxRow.ID}
	go s.runTurn(lock, rt, p)

	writeJSON(w, http.StatusCreated, startTurnResponse{
		SessionID: sessionID,
		ContextID: ctxRow.ID.String(),
		TurnID:    turnID,
		Model:     fullID.String(),
	})
}

// prepareTurn 为新回合建上下文并落盘用户消息，返回新建上下文。
func (s *Server) prepareTurn(ctx context.Context, sessionID, turnID, text string) (eventlog.ContextRow, error) {
	tail, err := runtime.SessionChainTail(ctx, s.cfg.Store, sessionID)
	if err != nil {
		return eventlog.ContextRow{}, err
	}
	ctxRow, err := s.cfg.Store.CreateContext(ctx, eventlog.ContextInput{ForkAt: tail})
	if err != nil {
		return eventlog.ContextRow{}, err
	}
	if _, err := runtime.AppendUserMessage(ctx, s.cfg.Store, runtime.UserMessage{
		SessionID: sessionID,
		TurnID:    turnID,
		ContextID: ctxRow.ID,
		Text:      text,
	}); err != nil {
		return eventlog.ContextRow{}, err
	}
	return ctxRow, nil
}

// ---- GET /api/turns/{turnID} ----

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

// ---- GET /api/sessions/{sessionID}/events ----

type eventDTO struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Actor     string          `json:"actor,omitempty"`
	SubjectID string          `json:"subject_id,omitempty"`
	CauseID   string          `json:"cause_id,omitempty"`
	ParentID  string          `json:"parent_id,omitempty"`
	TS        int64           `json:"ts"`
	Attrs     json.RawMessage `json:"attrs,omitempty"`
	Blob      string          `json:"blob,omitempty"`
	BlobText  string          `json:"blob_text,omitempty"`
}

type eventsResponse struct {
	Events  []eventDTO `json:"events"`
	NextSeq int64      `json:"next_seq"` // 下次轮询传 after_seq=next_seq
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	afterSeq, ok := intParam(w, r, "after_seq", 0)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultEventLimit)
	if !ok {
		return
	}
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}

	var (
		evs []eventlog.Event
		err error
	)
	if turnID := r.URL.Query().Get("turn_id"); turnID != "" {
		evs, err = s.cfg.Store.ListByTurn(r.Context(), turnID, int64(afterSeq), limit)
	} else {
		evs, err = s.cfg.Store.ListBySession(r.Context(), sessionID, int64(afterSeq), limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取事件失败: %v", err)
		return
	}

	resp := eventsResponse{Events: make([]eventDTO, 0, len(evs)), NextSeq: int64(afterSeq)}
	for _, ev := range evs {
		dto := eventDTO{
			ID: ev.ID.String(), Seq: ev.Seq, Type: ev.Type,
			SessionID: ev.SessionID, TurnID: ev.TurnID, Actor: ev.Actor,
			SubjectID: ev.SubjectID, CauseID: ev.CauseID, ParentID: ev.ParentID,
			TS: ev.TS, Attrs: ev.Attrs, Blob: ev.Blob,
		}
		if ev.Blob != "" {
			if data, err := s.cfg.Store.GetBlob(r.Context(), ev.Blob); err == nil && utf8.Valid(data) {
				dto.BlobText = string(data)
			}
		}
		resp.Events = append(resp.Events, dto)
		resp.NextSeq = ev.Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- POST /api/turns/{turnID}/approvals/{toolCallID} ----

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

	fullID, err := s.resolveModel(firstNonEmpty(strings.TrimSpace(req.Model), status.Model))
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

	p := runtime.TurnParams{SessionID: status.SessionID, TurnID: turnID, ContextID: status.ContextID}
	lock := s.sessionLock(status.SessionID)
	lock.Lock()
	if err := rt.DecideApproval(ctx, p, toolCallID, approved, strings.TrimSpace(req.Reason)); err != nil {
		lock.Unlock()
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	go s.runTurn(lock, rt, p)

	decision := "rejected"
	if approved {
		decision = "approved"
	}
	writeJSON(w, http.StatusAccepted, approvalResponse{
		TurnID: turnID, ToolCallID: toolCallID.String(), Decision: decision,
	})
}

// ---- 辅助 ----

// decodeBody 解析 JSON 请求体；失败时已写响应并返回 false。
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: %v", err)
		return false
	}
	return true
}

// intParam 读取非负整数查询参数；失败时已写响应并返回 false。
func intParam(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		writeError(w, http.StatusBadRequest, "%s 必须是非负整数，收到 %q", name, raw)
		return 0, false
	}
	return v, true
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		// 响应头已发出，无法再改状态码，只能忽略写入错误
		_ = err
	}
}

// writeError 输出 {"error": "..."} 响应。
func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}
