package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type Router struct {
	cfg            *config.Config
	mux            *http.ServeMux
	sessionStore   *store.SessionStore
	messageStore   *store.MessageStore
	requestStore   *store.RequestStore
	chatHandler    *ChatHandler
	providerClient *provider.Client
	modelsMu       sync.RWMutex
	cachedModels   []provider.ModelInfo
}

func NewRouter(cfg *config.Config) *Router {
	ss := store.NewSessionStore(cfg.Storage.DataDir)
	ms := store.NewMessageStore(cfg.Storage.DataDir)
	rs := store.NewRequestStore(cfg.Storage.DataDir)
	pc := provider.NewClient()
	models := pc.FetchModels(cfg.Providers, cfg.OpenAI, cfg.Models)
	r := &Router{cfg: cfg, mux: http.NewServeMux(), sessionStore: ss, messageStore: ms, requestStore: rs, providerClient: pc, cachedModels: models}
	r.chatHandler = NewChatHandler(cfg, ss, ms, rs, models)
	r.registerRoutes()
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }
func (r *Router) registerRoutes() {
	r.mux.HandleFunc("/api/health", cors(r.handleHealth))
	r.mux.HandleFunc("/api/models", cors(r.handleModels))
	r.mux.HandleFunc("/api/sessions", cors(r.handleSessions))
	r.mux.HandleFunc("/api/sessions/{id}", cors(r.handleSession))
	r.mux.HandleFunc("/api/sessions/{id}/messages", cors(r.handleMessages))
	r.mux.HandleFunc("/api/sessions/{id}/chat", cors(r.chatHandler.HandleChat))
	r.mux.HandleFunc("/api/sessions/{id}/chat/status", cors(r.handleChatStatus))
	r.mux.HandleFunc("/api/sessions/{id}/chat/cancel", cors(r.handleChatCancel))
}
func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ts": time.Now().UTC().Format(time.RFC3339)})
}
func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	models := r.providerClient.FetchModels(r.cfg.Providers, r.cfg.OpenAI, r.cfg.Models)
	if len(models) > 0 {
		r.modelsMu.Lock()
		r.cachedModels = models
		r.modelsMu.Unlock()
		r.chatHandler.SetModels(models)
	} else {
		r.modelsMu.RLock()
		models = append([]provider.ModelInfo(nil), r.cachedModels...)
		r.modelsMu.RUnlock()
	}
	writeJSON(w, http.StatusOK, models)
}
func (r *Router) handleSessions(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		list, err := r.sessionStore.List()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		session, err := r.sessionStore.Create(body.Title)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, http.StatusCreated, session)
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
	}
}
func (r *Router) handleSession(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if id == "" {
		http.Error(w, "缺少会话ID", 400)
		return
	}
	switch req.Method {
	case http.MethodPatch:
		var body struct {
			Title string `json:"title"`
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil {
			http.Error(w, "请求体解析失败", 400)
			return
		}
		if err := r.sessionStore.UpdateTitle(id, body.Title); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.WriteHeader(204)
	case http.MethodDelete:
		if r.chatHandler.IsRunning(id) {
			writeJSONError(w, 409, "会话正在生成，停止后再删除")
			return
		}
		if err := r.sessionStore.Delete(id); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
	}
}
func (r *Router) handleMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	messages, err := r.messageStore.List(req.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, messages)
}
func (r *Router) handleChatStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, map[string]bool{"active": r.chatHandler.IsRunning(req.PathValue("id"))})
}
func (r *Router) handleChatCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, map[string]bool{"cancelled": r.chatHandler.Cancel(req.PathValue("id"))})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		if req.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next(w, req)
	}
}
