package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type Router struct {
	cfg       *config.Config
	mux       *http.ServeMux
	store     *store.Store
	providers *provider.Client
	modelsMu  sync.RWMutex
	models    []provider.ModelInfo
	client    *openai.Client
	runsMu    sync.Mutex
	runs      map[string]context.CancelFunc
}

func NewRouter(cfg *config.Config) *Router {
	pc := provider.NewClient()
	models := pc.FetchModels(cfg.Providers, cfg.OpenAI, cfg.Models)
	r := &Router{cfg: cfg, mux: http.NewServeMux(), store: store.New(cfg.Storage.DataDir), providers: pc, models: models, client: openai.NewClient(&cfg.OpenAI), runs: map[string]context.CancelFunc{}}
	r.routes()
	return r
}
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }
func (r *Router) routes() {
	r.mux.HandleFunc("/api/health", r.health)
	r.mux.HandleFunc("/api/models", r.handleModels)
	r.mux.HandleFunc("/api/sessions", r.sessions)
	r.mux.HandleFunc("/api/sessions/{id}", r.session)
	r.mux.HandleFunc("/api/sessions/{id}/messages", r.messages)
	r.mux.HandleFunc("/api/sessions/{id}/chat", r.chat)
	r.mux.HandleFunc("/api/sessions/{id}/chat/status", r.status)
	r.mux.HandleFunc("/api/sessions/{id}/chat/cancel", r.cancel)
	r.mux.HandleFunc("/", r.web)
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (r *Router) health(w http.ResponseWriter, q *http.Request) {
	if q.Method != "GET" {
		http.Error(w, "方法不允许", 405)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "time": time.Now().UnixMilli()})
}
func (r *Router) handleModels(w http.ResponseWriter, q *http.Request) {
	if q.Method != "GET" {
		http.Error(w, "方法不允许", 405)
		return
	}
	m := r.providers.FetchModels(r.cfg.Providers, r.cfg.OpenAI, r.cfg.Models)
	if len(m) > 0 {
		r.modelsMu.Lock()
		r.models = m
		r.modelsMu.Unlock()
	}
	r.modelsMu.RLock()
	defer r.modelsMu.RUnlock()
	jsonOut(w, 200, r.models)
}
func (r *Router) sessions(w http.ResponseWriter, q *http.Request) {
	switch q.Method {
	case "GET":
		v, e := r.store.ListSessions()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		jsonOut(w, 200, v)
	case "POST":
		var b struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(q.Body).Decode(&b)
		v, e := r.store.CreateSession(b.Title)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		jsonOut(w, 201, v)
	default:
		http.Error(w, "方法不允许", 405)
	}
}
func (r *Router) session(w http.ResponseWriter, q *http.Request) {
	id := q.PathValue("id")
	switch q.Method {
	case "PATCH":
		var b struct {
			Title string `json:"title"`
		}
		if json.NewDecoder(q.Body).Decode(&b) != nil {
			http.Error(w, "请求错误", 400)
			return
		}
		if e := r.store.Rename(id, b.Title); e != nil {
			http.Error(w, e.Error(), 404)
			return
		}
		w.WriteHeader(204)
	case "DELETE":
		if r.running(id) {
			http.Error(w, "生成中不能删除", 409)
			return
		}
		if e := r.store.Delete(id); e != nil {
			http.Error(w, e.Error(), 404)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "方法不允许", 405)
	}
}
func (r *Router) messages(w http.ResponseWriter, q *http.Request) {
	if q.Method != "GET" {
		http.Error(w, "方法不允许", 405)
		return
	}
	v, e := r.store.Messages(q.PathValue("id"))
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, 200, v)
}
func (r *Router) findModel(key string) *provider.ModelInfo {
	r.modelsMu.RLock()
	defer r.modelsMu.RUnlock()
	return provider.FindByKey(r.models, key)
}
func (r *Router) chat(w http.ResponseWriter, q *http.Request) {
	if q.Method != "POST" {
		http.Error(w, "方法不允许", 405)
		return
	}
	id := q.PathValue("id")
	var b struct {
		Message  string `json:"message"`
		ModelKey string `json:"model_key"`
		Thinking string `json:"thinking"`
	}
	if json.NewDecoder(io.LimitReader(q.Body, 2<<20)).Decode(&b) != nil || strings.TrimSpace(b.Message) == "" {
		http.Error(w, "消息不能为空", 400)
		return
	}
	m := r.findModel(b.ModelKey)
	if m == nil {
		http.Error(w, "模型不存在，请刷新模型列表", 400)
		return
	}
	if e := provider.ValidateThinking(*m, b.Thinking); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if _, e := r.store.GetSession(id); e != nil {
		http.Error(w, e.Error(), 404)
		return
	}
	ctx, ok := r.begin(id)
	if !ok {
		http.Error(w, "该会话正在生成", 409)
		return
	}
	defer r.end(id)
	hist, e := r.store.Messages(id)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	msgs := make([]openai.ChatMessage, 0, len(hist)+1)
	for _, x := range hist {
		role := "user"
		if x.Type == "assistant" {
			role = "assistant"
		}
		msgs = append(msgs, openai.ChatMessage{Role: role, Content: x.Content})
	}
	msgs = append(msgs, openai.ChatMessage{Role: "user", Content: b.Message})
	user, _ := r.store.AppendMessage(id, store.Message{Type: "user", Content: b.Message, Model: m.ID, Provider: m.ProviderName, Thinking: b.Thinking})
	_ = r.store.Touch(id, store.Title(b.Message), m.Key)
	sse, ok := NewSSE(w)
	if !ok {
		return
	}
	sse.Event("ack", must(map[string]any{"message": user}))
	req, e := r.client.BuildRequest(m.BaseURL, m.APIKey, m.ID, m.ThinkingStyle, b.Thinking, msgs)
	if e != nil {
		r.finishError(id, m, b.Thinking, sse, e.Error(), 0)
		return
	}
	if req.GetBody != nil {
		if rc, er := req.GetBody(); er == nil {
			raw, _ := io.ReadAll(rc)
			_ = rc.Close()
			r.store.WriteRequest(id, map[string]any{"type": "request", "ts": time.Now().UTC(), "provider": m.ProviderName, "url": req.URL.String(), "body": json.RawMessage(raw)})
		}
	}
	start := time.Now()
	resp, e := r.client.DoStream(ctx, req)
	if e != nil {
		r.finishError(id, m, b.Thinking, sse, e.Error(), int(time.Since(start).Milliseconds()))
		return
	}
	defer resp.Body.Close()
	events := make(chan openai.StreamEvent, 32)
	go openai.NewStreamReader(resp.Body).ReadEvents(events)
	var content, reasoning, finish, errText string
	var usage *openai.Usage
	ttft := 0
	for ev := range events {
		switch ev.Type {
		case "delta":
			if ttft == 0 {
				ttft = int(time.Since(start).Milliseconds())
				sse.Event("ttft", must(map[string]int{"ms": ttft}))
			}
			content += ev.Text
			sse.Event("delta", must(map[string]string{"text": ev.Text}))
		case "reasoning":
			if ttft == 0 {
				ttft = int(time.Since(start).Milliseconds())
				sse.Event("ttft", must(map[string]int{"ms": ttft}))
			}
			reasoning += ev.Text
			sse.Event("reasoning", must(map[string]string{"text": ev.Text}))
		case "usage":
			usage = ev.Usage
			sse.Event("usage", must(ev.Usage))
		case "error":
			errText = ev.Error
			finish = "error"
			sse.Event("error", must(map[string]string{"message": ev.Error}))
		case "done":
			if finish == "" {
				finish = ev.Finish
			}
		}
	}
	if ctx.Err() != nil {
		finish = "aborted"
		errText = "生成已停止"
	}
	if finish == "" {
		finish = "stop"
	}
	dur := int(time.Since(start).Milliseconds())
	su := toStoreUsage(usage)
	assistant, pe := r.store.AppendMessage(id, store.Message{Type: "assistant", Content: content, Reasoning: reasoning, Model: m.ID, Provider: m.ProviderName, Thinking: b.Thinking, Usage: su, DurationMs: dur, TTFTMs: ttft, Finish: finish, Error: errText})
	_ = r.store.Touch(id, "", m.Key)
	sse.Event("done", must(map[string]any{"finish": finish, "duration_ms": dur, "ttft_ms": ttft, "persisted": pe == nil, "message_id": func() string {
		if assistant != nil {
			return assistant.ID
		}
		return ""
	}()}))
}
func (r *Router) finishError(id string, m *provider.ModelInfo, thinking string, s *SSE, msg string, dur int) {
	a, e := r.store.AppendMessage(id, store.Message{Type: "assistant", Model: m.ID, Provider: m.ProviderName, Thinking: thinking, Finish: "error", Error: msg, DurationMs: dur})
	s.Event("error", must(map[string]string{"message": msg}))
	s.Event("done", must(map[string]any{"finish": "error", "persisted": e == nil, "message_id": func() string {
		if a != nil {
			return a.ID
		}
		return ""
	}()}))
}
func toStoreUsage(u *openai.Usage) *store.Usage {
	if u == nil {
		return nil
	}
	return &store.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, CachedTokens: u.CachedTokens, ReasoningTokens: u.ReasoningTokens}
}
func must(v any) string { b, _ := json.Marshal(v); return string(b) }
func (r *Router) begin(id string) (context.Context, bool) {
	r.runsMu.Lock()
	defer r.runsMu.Unlock()
	if _, ok := r.runs[id]; ok {
		return nil, false
	}
	ctx, c := context.WithCancel(context.Background())
	r.runs[id] = c
	return ctx, true
}
func (r *Router) end(id string) { r.runsMu.Lock(); delete(r.runs, id); r.runsMu.Unlock() }
func (r *Router) running(id string) bool {
	r.runsMu.Lock()
	defer r.runsMu.Unlock()
	_, ok := r.runs[id]
	return ok
}
func (r *Router) status(w http.ResponseWriter, q *http.Request) {
	jsonOut(w, 200, map[string]bool{"active": r.running(q.PathValue("id"))})
}
func (r *Router) cancel(w http.ResponseWriter, q *http.Request) {
	if q.Method != "POST" {
		http.Error(w, "方法不允许", 405)
		return
	}
	id := q.PathValue("id")
	r.runsMu.Lock()
	c, ok := r.runs[id]
	if ok {
		c()
	}
	r.runsMu.Unlock()
	jsonOut(w, 200, map[string]bool{"cancelled": ok})
}
func (r *Router) web(w http.ResponseWriter, q *http.Request) {
	if strings.HasPrefix(q.URL.Path, "/api/") {
		http.NotFound(w, q)
		return
	}
	dist := "web/dist"
	path := filepath.Join(dist, filepath.Clean(q.URL.Path))
	if q.URL.Path == "/" {
		path = filepath.Join(dist, "index.html")
	}
	if st, e := os.Stat(path); e == nil && !st.IsDir() {
		http.ServeFile(w, q, path)
		return
	}
	index := filepath.Join(dist, "index.html")
	if _, e := os.Stat(index); e != nil {
		http.Error(w, "前端尚未构建：请先执行 cd web && npm install && npm run build", 503)
		return
	}
	http.ServeFile(w, q, index)
}
