package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"wisp/internal/config"
	"wisp/internal/provider"
	"wisp/internal/store"
	"wisp/internal/webui"
)

type Router struct {
	cfg          *config.Config
	mux          *http.ServeMux
	catalog      *provider.Catalog
	runs         *RunRegistry
	sessionStore *store.SessionStore
	messageStore *store.MessageStore
	requestStore *store.RequestStore
	chatHandler  *ChatHandler
}

func NewRouter(cfg *config.Config) *Router {
	ss := store.NewSessionStore(cfg.Storage.DataDir)
	ms := store.NewMessageStore(cfg.Storage.DataDir)
	rs := store.NewRequestStore(cfg.Storage.DataDir)
	catalog := provider.NewCatalog(cfg)
	runs := NewRunRegistry()
	router := &Router{
		cfg:          cfg,
		mux:          http.NewServeMux(),
		catalog:      catalog,
		runs:         runs,
		sessionStore: ss,
		messageStore: ms,
		requestStore: rs,
	}
	router.chatHandler = NewChatHandler(cfg, catalog, runs, ss, ms, rs)
	router.registerRoutes()
	return router
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	r.mux.HandleFunc("/api/health", cors(r.handleHealth))
	r.mux.HandleFunc("/api/catalog", cors(r.handleCatalog))
	r.mux.HandleFunc("/api/providers", cors(r.handleProviders))
	r.mux.HandleFunc("/api/models", cors(r.handleModels))
	r.mux.HandleFunc("/api/sessions", cors(r.handleSessions))
	r.mux.HandleFunc("/api/sessions/{id}", cors(r.handleSession))
	r.mux.HandleFunc("/api/sessions/{id}/messages", cors(r.handleMessages))
	r.mux.HandleFunc("/api/sessions/{id}/chat", cors(r.chatHandler.HandleChat))
	r.mux.HandleFunc("/api/sessions/{id}/chat/status", cors(r.handleChatStatus))
	r.mux.HandleFunc("/api/sessions/{id}/chat/cancel", cors(r.handleChatCancel))
	// 未定义 API 不应该回退到 Vue index。
	r.mux.HandleFunc("/api/", cors(func(w http.ResponseWriter, req *http.Request) { http.NotFound(w, req) }))
	// Vue build 由 Go 直接托管。
	r.mux.Handle("/", webui.Handler())
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"ts": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (r *Router) handleCatalog(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	force, _ := strconv.ParseBool(req.URL.Query().Get("refresh"))
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, r.catalog.Snapshot(ctx, force))
}

func (r *Router) handleProviders(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, r.catalog.Snapshot(ctx, false).Providers)
}

func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, r.catalog.Snapshot(ctx, false).Models)
}

func (r *Router) handleSessions(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		sessions, err := r.sessionStore.List()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sessions)
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		session, err := r.sessionStore.Create(body.Title)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
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
		writeJSONError(w, http.StatusBadRequest, "缺少会话ID")
		return
	}
	switch req.Method {
	case http.MethodPatch:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		if err := r.sessionStore.UpdateTitle(id, body.Title); err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if active, _ := r.runs.Status(id); active {
			writeJSONError(w, http.StatusConflict, "会话正在生成，请先停止生成")
			return
		}
		if err := r.sessionStore.Delete(id); err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
	}
}

func (r *Router) handleMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	id := req.PathValue("id")
	messages, err := r.messageStore.List(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, messages)
}

func (r *Router) handleChatStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	active, started := r.runs.Status(req.PathValue("id"))
	payload := map[string]any{"active": active}
	if active {
		payload["started_at"] = started.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r *Router) handleChatCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": r.runs.Cancel(req.PathValue("id"))})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		if req.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, req)
	}
}

func contextWithCatalogTimeout(req *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(req.Context(), timeout)
}
