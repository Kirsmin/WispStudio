package api

import (
	"net/http"
	"strings"

	"wisp/internal/provider"
)

// modelDTO 是 /api/models 中的单个模型条目。
type modelDTO struct {
	ProviderID string `json:"ProviderID"`
	ModelID    string `json:"ModelID"`
	FullID     string `json:"FullID"`
	Alias      string `json:"Alias,omitempty"`
	Discovered bool   `json:"Discovered"`
}

// providerDTO 是 /api/models 中的一个 provider。
type providerDTO struct {
	ProviderID   string     `json:"ProviderID"`
	ProviderName string     `json:"ProviderName,omitempty"`
	Models       []modelDTO `json:"Models"`
}

// modelsResponse 是 GET /api/models 的响应：手动配置 + 自动发现 + 别名。
type modelsResponse struct {
	DefaultModel string        `json:"DefaultModel,omitempty"`
	Providers    []providerDTO `json:"Providers"`
}

// addModelRequest 是 POST /api/models 的请求体。
// 兼容直接传 FullID/Model（"{ProviderID}/{ModelID}"）。
type addModelRequest struct {
	ProviderID string `json:"ProviderID"`
	ModelID    string `json:"ModelID"`
	Alias      string `json:"Alias"`
	ModelAlias string `json:"ModelAlias"`
	FullID     string `json:"FullID"`
	Model      string `json:"Model"`
}

// handleListModels 返回 catalog 全量：每个 provider 的手动模型、发现结果与别名。
func (s *Server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	resp := modelsResponse{Providers: []providerDTO{}}
	if def, err := s.cfg.Catalog.DefaultModel(); err == nil {
		resp.DefaultModel = def.String()
	}
	for _, providerID := range s.cfg.Catalog.ProviderIDs() {
		models, err := s.cfg.Catalog.ListByProvider(providerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "%v", err)
			return
		}
		item := providerDTO{
			ProviderID:   providerID,
			ProviderName: s.cfg.Catalog.ProviderName(providerID),
			Models:       make([]modelDTO, 0, len(models)),
		}
		for _, m := range models {
			item.Models = append(item.Models, modelDTO{
				ProviderID: m.FullID.ProviderID(),
				ModelID:    m.FullID.ModelID(),
				FullID:     m.FullID.String(),
				Alias:      m.Alias,
				Discovered: m.Discovered,
			})
		}
		resp.Providers = append(resp.Providers, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAddModel 手动添加模型并写回 Config.local.toml。
func (s *Server) handleAddModel(w http.ResponseWriter, r *http.Request) {
	var req addModelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	providerID := strings.TrimSpace(req.ProviderID)
	modelID := strings.TrimSpace(req.ModelID)
	if providerID == "" || modelID == "" {
		full := strings.TrimSpace(firstNonEmpty(req.FullID, req.Model))
		if full == "" {
			writeError(w, http.StatusBadRequest, "缺少 ProviderID/ModelID 或 FullID")
			return
		}
		parsed, err := provider.Parse(full)
		if err != nil {
			writeError(w, http.StatusBadRequest, "%v", err)
			return
		}
		providerID, modelID = parsed.Split()
	}
	alias := strings.TrimSpace(firstNonEmpty(req.Alias, req.ModelAlias))

	if err := s.cfg.Catalog.AddModel(providerID, modelID, alias); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "已存在") {
			status = http.StatusConflict
		}
		writeError(w, status, "%v", err)
		return
	}
	fullID := provider.Join(providerID, modelID)
	writeJSON(w, http.StatusCreated, modelDTO{
		ProviderID: providerID,
		ModelID:    modelID,
		FullID:     fullID.String(),
		Alias:      alias,
	})
}
