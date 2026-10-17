package api

import "net/http"

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
