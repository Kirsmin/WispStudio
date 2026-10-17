package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"wisp/internal/config"
)

// configDTO 是 GET /api/config 的响应：配置文件路径与原文。
type configDTO struct {
	Path string `json:"Path"`
	Text string `json:"Text"`
}

// updateConfigRequest 是 POST /api/config 的请求体。
type updateConfigRequest struct {
	Text string `json:"Text"`
}

// handleGetConfig 返回 Config.local.toml 原文（含明文密钥）。
// 仅允许本机来源读取，避免任意网页通过 CORS 拿到密钥。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if rejectForeignOrigin(w, r) {
		return
	}
	text, err := s.cfg.Catalog.ConfigText()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取配置失败: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, configDTO{
		Path: s.cfg.Catalog.ConfigPath(),
		Text: text,
	})
}

// handleUpdateConfig 校验整段 TOML 后写回并热更新模型目录。
// 校验失败返回 400，响应中的 line 为出错行号（0 表示无法定位）。
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if rejectForeignOrigin(w, r) {
		return
	}
	var req updateConfigRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.cfg.Catalog.ReplaceConfig([]byte(req.Text)); err != nil {
		var configErr *config.Error
		if errors.As(err, &configErr) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": configErr.Message,
				"line":  configErr.Line,
			})
			return
		}
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}

	// 配置已热更新；自动发现在后台重跑，避免保存请求被网络探测拖慢。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.cfg.Catalog.DiscoverAll(ctx, s.cfg.Logf)
	}()

	writeJSON(w, http.StatusOK, map[string]any{"OK": true})
}
