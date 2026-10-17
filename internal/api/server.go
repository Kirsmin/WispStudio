package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	defaultStopTimeout = 5 * time.Second  // 停止回合时等待终态事件的时间
	maxBodyBytes       = 1 << 20          // 请求体上限 1 MiB
)

// Config 是 Server 的构造参数。
type Config struct {
	Store         *eventlog.Store      // 事件日志，必填
	Catalog       *provider.Catalog    // 模型目录，必填
	WorkspaceRoot string               // 工具工作区根，空 = 进程当前目录
	DefaultModel  provider.ModelFullID // 请求未指定模型时的默认模型，空 = 目录内第一个
	TurnTimeout   time.Duration        // 单回合超时，<=0 用默认 10 分钟
	OnCompile     func(time.Duration)  // 编译耗时观察者，nil = 不观察
	Logf          func(string, ...any) // 日志，nil = 丢弃
	WebDist       string               // 前端构建产物目录，空 = 不托管静态页面
}

// Server 是 HTTP API 服务。
type Server struct {
	cfg    Config
	writer eventlog.Writer
	hub    *eventlog.Hub

	mu       sync.Mutex
	sessions map[string]*sessionState // 会话串行锁（懒创建）
	active   map[string]*activeTurn   // 会话 → 当前回合

	closing   atomic.Bool
	closeOnce sync.Once
	turnWG    sync.WaitGroup
}

// sessionState 是一个会话的执行状态。
type sessionState struct {
	lock sync.Mutex
}

// activeTurn 是当前进程中正在执行的回合，用于 stop 与优雅关闭。
type activeTurn struct {
	turnID string
	rt     *runtime.Runtime
	cancel context.CancelFunc
	done   chan struct{}
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
	return &Server{
		cfg:      cfg,
		writer:   cfg.Store,
		hub:      cfg.Store.Hub(),
		sessions: make(map[string]*sessionState),
		active:   make(map[string]*activeTurn),
	}, nil
}

// Handler 返回路由。路径参数使用 Go 1.22 的 {name} 通配。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)

	// 会话、消息、停止、审批、模型与配置。
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("DELETE /api/sessions/{sessionID}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{sessionID}/events", s.handleEventsSSE)
	mux.HandleFunc("POST /api/sessions/{sessionID}/messages", s.handlePostMessage)
	mux.HandleFunc("POST /api/sessions/{sessionID}/stop", s.handleStopSession)
	mux.HandleFunc("POST /api/sessions/{sessionID}/approvals/{toolCallID}", s.handleApproval)
	mux.HandleFunc("GET /api/models", s.handleListModels)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config", s.handleUpdateConfig)
	mux.HandleFunc("GET /api/events/{eventID}/trace", s.handleTrace)

	// 前端构建产物存在时由同一端口托管，刷新页面仍回到 index.html。
	if s.cfg.WebDist != "" {
		mux.Handle("GET /", spaHandler(s.cfg.WebDist))
	}

	return corsMiddleware(rejectWhenClosing(s.closing.Load, mux))
}

// Shutdown 停止接收新的回合、取消在途回合并等待它们写完终态事件，
// 最后关闭通知中心唤醒全部 SSE 订阅。调用方随后再关闭 HTTP 服务与 Store。
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)

		s.mu.Lock()
		active := make([]*activeTurn, 0, len(s.active))
		for _, a := range s.active {
			active = append(active, a)
		}
		s.mu.Unlock()

		for _, a := range active {
			a.rt.Interrupt("stopped")
			a.cancel()
		}
	})

	done := make(chan struct{})
	go func() {
		s.turnWG.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	// 先让回合把终态事件落盘，再唤醒/关闭 SSE 订阅。
	s.hub.Close()
	return err
}

// sessionState 返回会话状态（懒创建）。
func (s *Server) sessionState(sessionID string) *sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		st = &sessionState{}
		s.sessions[sessionID] = st
	}
	return st
}

// addActive 登记当前回合。关闭中拒绝登记，避免 Shutdown 漏掉新回合。
func (s *Server) addActive(sessionID string, a *activeTurn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing.Load() {
		return false
	}
	s.active[sessionID] = a
	s.turnWG.Add(1)
	return true
}

// clearActive 仅当登记的是同一个回合时才删除。
func (s *Server) clearActive(sessionID string, a *activeTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[sessionID] == a {
		delete(s.active, sessionID)
	}
}

// currentActive 返回会话当前回合。
func (s *Server) currentActive(sessionID string) *activeTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[sessionID]
}

// isRunning 判断会话当前是否有回合在执行。
func (s *Server) isRunning(sessionID string) bool {
	return s.currentActive(sessionID) != nil
}

// newRuntime 按 provider 连接参数构造运行时；写入统一走 eventlog.Writer。
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
		OnCompile:     s.cfg.OnCompile,
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

// runTurn 在后台执行回合；结束时释放会话锁并清理 active 登记。
func (s *Server) runTurn(ctx context.Context, lock *sync.Mutex, a *activeTurn, p runtime.TurnParams) {
	defer s.turnWG.Done()
	defer close(a.done)
	defer a.cancel()
	defer s.clearActive(p.SessionID, a)
	defer lock.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.cfg.Logf("api: 回合 %s 执行 panic: %v", p.TurnID, r)
		}
	}()
	if err := a.rt.RunTurn(ctx, p); err != nil {
		s.cfg.Logf("api: 回合 %s 结束于错误: %v", p.TurnID, err)
	}
	// 若 RunTurn 因 context 已取消而提前返回，兜底把停止终态写完整。
	if err := a.rt.Stop(context.Background(), p); err != nil {
		s.cfg.Logf("api: 回合 %s 写停止终态失败: %v", p.TurnID, err)
	}
}

// prepareTurn 为新回合建上下文并落盘用户消息。
// tail 是调用方已经折叠出的会话内容链尾，避免重复读整条会话。
// 所有写入都经 s.writer。
func (s *Server) prepareTurn(ctx context.Context, sessionID, turnID, text string, tail ids.EventID) (eventlog.ContextRow, error) {
	ctxRow, err := s.writer.CreateContext(ctx, eventlog.ContextInput{ForkAt: tail, SessionID: sessionID})
	if err != nil {
		return eventlog.ContextRow{}, err
	}
	if _, err := runtime.AppendUserMessage(ctx, s.cfg.Store, runtime.UserMessage{
		SessionID: sessionID,
		TurnID:    turnID,
		ContextID: ctxRow.ID,
		ParentID:  tail,
		Text:      text,
	}); err != nil {
		return eventlog.ContextRow{}, err
	}
	return ctxRow, nil
}

// ---- GET /api/health ----

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "wisp"})
}

// ---- CORS / 关闭保护 ----

// corsMiddleware 只允许本机来源跨端口访问，并处理预检。
// 配置接口会返回明文密钥，因此不能再用通配 "*" 放开任意网页读取。
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		local := origin == "" || isLocalOrigin(origin)
		if origin != "" && local {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID, Cache-Control")
			h.Set("Access-Control-Expose-Headers", "Last-Event-ID")
			h.Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			if !local {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalOrigin 判断 Origin 是否属于本机（localhost / 127.x / ::1）。
func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// rejectForeignOrigin 拒绝非本机 Origin 的配置读写，防止任意网页读取密钥。
func rejectForeignOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && !isLocalOrigin(origin) {
		writeError(w, http.StatusForbidden, "仅允许本机前端访问配置")
		return true
	}
	return false
}

// rejectWhenClosing 在优雅关闭期间拒绝新请求（health 除外）。
func rejectWhenClosing(closing func() bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if closing() && r.URL.Path != "/api/health" {
			writeError(w, http.StatusServiceUnavailable, "服务正在关闭")
			return
		}
		next.ServeHTTP(w, r)
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

// decodeOptionalBody 解析可空 JSON 请求体；空 body 视为零值。
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, "请求体解析失败: %v", err)
		return false
	}
	return true
}

// int64Param 读取非负 int64 查询参数；失败时已写响应并返回 false。
func int64Param(w http.ResponseWriter, r *http.Request, name string, def int64) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		writeError(w, http.StatusBadRequest, "%s 必须是非负整数，收到 %q", name, raw)
		return 0, false
	}
	return v, true
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

// validText 校验正文非空且是合法 UTF-8。
func validText(text string) bool {
	return strings.TrimSpace(text) != "" && utf8.ValidString(text)
}

// newTurnContext 为后台回合创建带超时的 context。
func (s *Server) newTurnContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.cfg.TurnTimeout)
}
