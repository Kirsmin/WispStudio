package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"wisp/internal/config"
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

// addModelRequest 是 POST /api/models 的请求体。
type addModelRequest struct {
	FullID string `json:"FullID"`
	Alias  string `json:"Alias"`
}

// handleAddModel 手动新增模型：校验后把模型与别名写回 Config.local.toml 并热更新目录。
// 解析、校验、写回、热更新全部成功才生效；任一步失败保持旧配置。
func (s *Server) handleAddModel(w http.ResponseWriter, r *http.Request) {
	var req addModelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	fullID, err := provider.Parse(strings.TrimSpace(req.FullID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	alias := strings.TrimSpace(req.Alias)
	if !utf8.ValidString(alias) {
		writeError(w, http.StatusBadRequest, "Alias 必须是合法 UTF-8")
		return
	}
	providerID, modelID := fullID.Split()
	if !s.providerConfigured(providerID) {
		writeError(w, http.StatusBadRequest, "provider 不存在: %q", providerID)
		return
	}

	text, err := s.cfg.Catalog.ConfigText()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取配置失败: %v", err)
		return
	}
	newText, err := addModelToConfigText(text, providerID, modelID, alias)
	if err != nil {
		s.writeConfigError(w, err)
		return
	}
	if err := s.cfg.Catalog.ReplaceConfig([]byte(newText)); err != nil {
		s.writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, modelDTO{
		ProviderID: providerID,
		ModelID:    modelID,
		FullID:     fullID.String(),
		Alias:      alias,
	})
}

// providerConfigured 判断 provider 是否已在目录配置中。
func (s *Server) providerConfigured(providerID string) bool {
	for _, id := range s.cfg.Catalog.ProviderIDs() {
		if id == providerID {
			return true
		}
	}
	return false
}

// writeConfigError 输出配置错误：带行号的解析/校验错误返回 400，其余返回 500。
func (s *Server) writeConfigError(w http.ResponseWriter, err error) {
	var configErr *config.Error
	if errors.As(err, &configErr) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": configErr.Message,
			"line":  configErr.Line,
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "保存配置失败: %v", err)
}

// 配置文本行匹配（键名与 config 包约定一致）。
var (
	providerIDLineRE = regexp.MustCompile(`^\s*ProviderID\s*=\s*"([^"]*)"`)
	modelsLineRE     = regexp.MustCompile(`^\s*Models\s*=`)
)

// addModelToConfigText 把模型追加到目标 provider 的 Models 列表：
// 先用 config.Parse 校验并取出现有模型，再整段替换 Models 数组（兼容多行数组），
// 其余内容与注释保持不动。重复模型报错。
func addModelToConfigText(text, providerID, modelID, alias string) (string, error) {
	cfg, err := config.Parse([]byte(text))
	if err != nil {
		return "", err
	}
	var models []config.ModelConfig
	found := false
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.ProviderID != providerID {
			continue
		}
		found = true
		for _, m := range p.Models {
			if m.ModelID == modelID {
				return "", fmt.Errorf("模型已存在于手动列表: %q", providerID+"/"+modelID)
			}
		}
		models = append(models, p.Models...)
	}
	if !found {
		return "", fmt.Errorf("provider 不存在: %q", providerID)
	}
	models = append(models, config.ModelConfig{ModelID: modelID, ModelAlias: alias})
	line := renderModelsLine(models)

	lines := strings.Split(text, "\n")
	inBlock := false
	isTarget := false
	providerIdx := -1
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "[[Providers]]") {
			inBlock, isTarget, providerIdx = true, false, -1
			continue
		}
		if !inBlock {
			continue
		}
		// 其他表头结束当前块；[Providers.*] 子表仍属块内。
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[Providers") {
			if isTarget {
				return strings.Join(insertAfter(lines, providerIdx, indentOf(lines[providerIdx])+line), "\n"), nil
			}
			inBlock = false
			continue
		}
		if m := providerIDLineRE.FindStringSubmatch(raw); m != nil {
			isTarget = m[1] == providerID
			if isTarget {
				providerIdx = i
			}
			continue
		}
		if isTarget && modelsLineRE.MatchString(raw) {
			end := i
			if !strings.Contains(trimmed, "]") {
				for j := i + 1; j < len(lines); j++ {
					end = j
					if strings.Contains(lines[j], "]") {
						break
					}
				}
			}
			out := append([]string{}, lines[:i]...)
			out = append(out, indentOf(raw)+line)
			out = append(out, lines[end+1:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	if isTarget && providerIdx >= 0 {
		return strings.Join(insertAfter(lines, providerIdx, indentOf(lines[providerIdx])+line), "\n"), nil
	}
	return "", fmt.Errorf("配置中找不到 provider %q 的 [[Providers]] 块", providerID)
}

// insertAfter 在 lines[idx] 之后插入一行。
func insertAfter(lines []string, idx int, line string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:idx+1]...)
	out = append(out, line)
	out = append(out, lines[idx+1:]...)
	return out
}

// indentOf 返回行的前导空白。
func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// renderModelsLine 把模型列表渲染为一行 TOML 数组；带别名的条目用内联表。
func renderModelsLine(models []config.ModelConfig) string {
	parts := make([]string, 0, len(models))
	for _, m := range models {
		if m.ModelAlias == "" {
			parts = append(parts, strconv.Quote(m.ModelID))
			continue
		}
		parts = append(parts, "{ ModelID = "+strconv.Quote(m.ModelID)+
			", ModelAlias = "+strconv.Quote(m.ModelAlias)+" }")
	}
	return "Models = [" + strings.Join(parts, ", ") + "]"
}
