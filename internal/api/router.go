package api

import (
	"encoding/json"
	"net/http"
	"strings"
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
	models := initialModels(cfg, pc)
	r := &Router{cfg: cfg, mux: http.NewServeMux(), sessionStore: ss, messageStore: ms, requestStore: rs, providerClient: pc, cachedModels: models}
	r.chatHandler = NewChatHandler(cfg, ss, ms, rs, models)
	r.registerRoutes()
	return r
}

func initialModels(cfg *config.Config, pc *provider.Client) []provider.ModelInfo {
	if len(cfg.Providers) > 0 {
		if models := pc.FetchModels(cfg.Providers, cfg.OpenAI); len(models) > 0 {
			return models
		}
	}
	return convertLegacyModels(cfg.Models)
}

func convertLegacyModels(models []config.ModelConfig) []provider.ModelInfo {
	result := make([]provider.ModelInfo, 0, len(models))
	for _, m := range models {
		levels := m.ThinkingLevels
		if len(levels) == 0 {
			levels = []string{"off"}
		}
		result = append(result, provider.ModelInfo{
			ID: m.ID, Name: m.Name, Default: m.Default, ThinkingLevels: levels, ThinkingStyle: m.ThinkingStyle,
			BaseURL: m.BaseURL, APIKey: m.APIKey,
		})
	}
	return result
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "0.1.0-fixed", "ts": time.Now().UTC().Format(time.RFC3339)})
}

func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	// Provider 暂时不可用时保留上一次成功缓存，不让前端模型列表突然清空。
	if len(r.cfg.Providers) > 0 {
		if fresh := r.providerClient.FetchModels(r.cfg.Providers, r.cfg.OpenAI); len(fresh) > 0 {
			r.modelsMu.Lock()
			r.cachedModels = append([]provider.ModelInfo(nil), fresh...)
			r.modelsMu.Unlock()
			r.chatHandler.SetModels(fresh)
		}
	}
	r.modelsMu.RLock()
	models := append([]provider.ModelInfo(nil), r.cachedModels...)
	r.modelsMu.RUnlock()
	writeJSON(w, http.StatusOK, models)
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
		req.Body = http.MaxBytesReader(w, req.Body, 64<<10)
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		sess, err := r.sessionStore.Create(body.Title)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sess)
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
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		body.Title = strings.TrimSpace(body.Title)
		if body.Title == "" {
			writeJSONError(w, http.StatusBadRequest, "标题不能为空")
			return
		}
		if err := r.sessionStore.UpdateTitle(id, body.Title); err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if active, _ := r.chatHandler.RunStatus(id); active {
			writeJSONError(w, http.StatusConflict, "该会话仍在生成，请先停止生成")
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
	if _, err := r.sessionStore.Get(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "会话不存在")
		return
	}
	msgs, err := r.messageStore.List(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (r *Router) handleChatStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	id := req.PathValue("id")
	active, started := r.chatHandler.RunStatus(id)
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
	id := req.PathValue("id")
	cancelled := r.chatHandler.CancelRun(id)
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}
