package api

import (
	"encoding/json"
	"net/http"
	"time"

	"pylai/internal/config"
)

type Router struct {
	cfg *config.Config
	mux *http.ServeMux
}

func NewRouter(cfg *config.Config) *Router {
	r := &Router{cfg: cfg, mux: http.NewServeMux()}
	r.registerRoutes()
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	r.mux.HandleFunc("/api/health", cors(r.handleHealth))
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok": true,
		"ts": time.Now().UTC().Format(time.RFC3339),
	})
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}
