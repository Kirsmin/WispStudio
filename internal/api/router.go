package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/provider"
	"wisp/internal/store"
	webassets "wisp/web"
)

type Router struct {
	cfg       *config.Config
	mux       *http.ServeMux
	sessions  *store.SessionStore
	messages  *store.MessageStore
	requests  *store.RequestStore
	providers *provider.Client
	chat      *ChatHandler
	modelsMu  sync.RWMutex
	models    []provider.ModelInfo
}

func NewRouter(cfg *config.Config) *Router {
	sessions := store.NewSessionStore(cfg.Storage.DataDir)
	messages := store.NewMessageStore(cfg.Storage.DataDir)
	requests := store.NewRequestStore(cfg.Storage.DataDir)
	providers := provider.NewClient()
	models := providers.FetchModels(cfg.Providers, cfg.OpenAI, cfg.Models)
	r := &Router{
		cfg: cfg, mux: http.NewServeMux(), sessions: sessions, messages: messages,
		requests: requests, providers: providers, models: models,
	}
	r.chat = NewChatHandler(cfg, sessions, messages, requests, models)
	r.register()
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

func (r *Router) register() {
	r.mux.HandleFunc("/api/health", cors(r.handleHealth))
	r.mux.HandleFunc("/api/models", cors(r.handleModels))
	r.mux.HandleFunc("/api/sessions", cors(r.handleSessions))
	r.mux.HandleFunc("/api/sessions/{id}", cors(r.handleSession))
	r.mux.HandleFunc("/api/sessions/{id}/messages", cors(r.handleMessages))
	r.mux.HandleFunc("/api/sessions/{id}/chat", cors(r.chat.HandleChat))
	r.mux.HandleFunc("/api/sessions/{id}/chat/status", cors(r.handleChatStatus))
	r.mux.HandleFunc("/api/sessions/{id}/chat/cancel", cors(r.handleChatCancel))

	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(staticFS))
	r.mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			http.NotFound(w, req)
			return
		}
		files.ServeHTTP(w, req)
	})
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "ts": time.Now().UTC().Format(time.RFC3339)})
}

func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	fresh := r.providers.FetchModels(r.cfg.Providers, r.cfg.OpenAI, r.cfg.Models)
	if len(fresh) > 0 {
		r.modelsMu.Lock()
		r.models = fresh
		r.modelsMu.Unlock()
		r.chat.SetModels(fresh)
	}
	r.modelsMu.RLock()
	models := append([]provider.ModelInfo(nil), r.models...)
	r.modelsMu.RUnlock()
	writeJSON(w, 200, models)
}

func (r *Router) handleSessions(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		list, err := r.sessions.List()
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, list)
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10)).Decode(&body)
		session, err := r.sessions.Create(body.Title)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, session)
	default:
		http.Error(w, "方法不允许", 405)
	}
}

func (r *Router) handleSession(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	switch req.Method {
	case http.MethodPatch:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10)).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求体解析失败")
			return
		}
		if strings.TrimSpace(body.Title) == "" {
			writeJSONError(w, 400, "标题不能为空")
			return
		}
		if err := r.sessions.UpdateTitle(id, body.Title); err != nil {
			writeJSONError(w, 404, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if r.chat.IsRunning(id) {
			writeJSONError(w, 409, "会话正在生成，请先停止")
			return
		}
		if err := r.sessions.Delete(id); err != nil {
			writeJSONError(w, 404, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "方法不允许", 405)
	}
}

func (r *Router) handleMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	if _, err := r.sessions.Get(req.PathValue("id")); err != nil {
		writeJSONError(w, 404, "会话不存在")
		return
	}
	messages, err := r.messages.List(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 500, err.Error())
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
	writeJSON(w, 200, map[string]bool{"active": r.chat.IsRunning(req.PathValue("id"))})
}
func (r *Router) handleChatCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, map[string]bool{"cancelled": r.chat.Cancel(req.PathValue("id"))})
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
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, req)
	}
}
