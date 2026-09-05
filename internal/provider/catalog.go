package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"wisp/internal/config"
)

const catalogRefreshTTL = 30 * time.Second

type ProviderInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Default     bool   `json:"default"`
	Available   bool   `json:"available"`
	ModelsCount int    `json:"models_count"`
	Error       string `json:"error,omitempty"`
}

type ModelInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ProviderID     string   `json:"provider_id"`
	ProviderName   string   `json:"provider_name"`
	Default        bool     `json:"default"`
	ThinkingLevels []string `json:"thinking_levels"`
	ThinkingStyle  string   `json:"thinking_style"`
}

type CatalogSnapshot struct {
	Providers   []ProviderInfo `json:"providers"`
	Models      []ModelInfo    `json:"models"`
	RefreshedAt time.Time      `json:"refreshed_at"`
}

type runtimeProvider struct {
	Config config.ProviderConfig
}

type Catalog struct {
	cfg       *config.Config
	providers []runtimeProvider
	mu        sync.RWMutex
	refreshMu sync.Mutex
	snapshot  CatalogSnapshot
}

func NewCatalog(cfg *config.Config) *Catalog {
	c := &Catalog{cfg: cfg}
	c.providers = buildRuntimeProviders(cfg)
	c.snapshot = c.configFallbackSnapshot()
	return c
}

func buildRuntimeProviders(cfg *config.Config) []runtimeProvider {
	if len(cfg.Providers) > 0 {
		result := make([]runtimeProvider, 0, len(cfg.Providers))
		for _, provider := range cfg.Providers {
			result = append(result, runtimeProvider{Config: provider})
		}
		return result
	}

	// 兼容旧配置：没有 [[providers]] 时，把 [openai] + [[models]] 映射为运行时 Provider。
	result := make([]runtimeProvider, 0)
	legacy := config.ProviderConfig{
		ID:         "default",
		Name:       "Default",
		BaseURL:    strings.TrimRight(strings.TrimSpace(cfg.OpenAI.BaseURL), "/"),
		APIKey:     cfg.OpenAI.APIKey,
		Default:    true,
		TimeoutSec: cfg.OpenAI.TimeoutSec,
	}
	for index, model := range cfg.Models {
		modelBaseURL := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
		if (modelBaseURL == "" || modelBaseURL == legacy.BaseURL) && (model.APIKey == "" || model.APIKey == legacy.APIKey) {
			legacy.ModelOverrides = append(legacy.ModelOverrides, config.ModelOverrideConfig{
				ID:             model.ID,
				Name:           model.Name,
				Default:        model.Default,
				ThinkingLevels: model.ThinkingLevels,
				ThinkingStyle:  model.ThinkingStyle,
			})
			continue
		}
		name := model.Name
		if name == "" {
			name = model.ID
		}
		provider := config.ProviderConfig{
			ID:         fmt.Sprintf("legacy-%d", index+1),
			Name:       name,
			BaseURL:    modelBaseURL,
			APIKey:     firstNonEmpty(model.APIKey, cfg.OpenAI.APIKey),
			TimeoutSec: cfg.OpenAI.TimeoutSec,
			ModelOverrides: []config.ModelOverrideConfig{{
				ID:             model.ID,
				Name:           name,
				Default:        true,
				ThinkingLevels: model.ThinkingLevels,
				ThinkingStyle:  model.ThinkingStyle,
			}},
		}
		result = append(result, runtimeProvider{Config: provider})
	}
	if legacy.BaseURL != "" || len(legacy.ModelOverrides) > 0 {
		result = append([]runtimeProvider{{Config: legacy}}, result...)
	}
	return result
}

func (c *Catalog) Snapshot(ctx context.Context, force bool) CatalogSnapshot {
	c.mu.RLock()
	last := c.snapshot.RefreshedAt
	c.mu.RUnlock()
	if force || last.IsZero() || time.Since(last) >= catalogRefreshTTL {
		_ = c.Refresh(ctx)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneSnapshot(c.snapshot)
}

func (c *Catalog) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	if len(c.providers) == 0 {
		c.mu.Lock()
		c.snapshot = c.configFallbackSnapshot()
		c.snapshot.RefreshedAt = time.Now().UTC()
		c.mu.Unlock()
		return nil
	}

	type result struct {
		index  int
		info   ProviderInfo
		models []ModelInfo
	}
	results := make(chan result, len(c.providers))
	var wg sync.WaitGroup
	for index, runtime := range c.providers {
		wg.Add(1)
		go func(index int, runtime runtimeProvider) {
			defer wg.Done()
			info, models := c.fetchProvider(ctx, runtime.Config)
			results <- result{index: index, info: info, models: models}
		}(index, runtime)
	}
	wg.Wait()
	close(results)

	ordered := make([]result, len(c.providers))
	for item := range results {
		ordered[item.index] = item
	}

	c.mu.RLock()
	previous := cloneSnapshot(c.snapshot)
	c.mu.RUnlock()

	providers := make([]ProviderInfo, 0, len(ordered))
	models := make([]ModelInfo, 0)
	for _, item := range ordered {
		// 单个 Provider 暂时失败时保留它上一轮的模型缓存，避免多 Provider 场景下选项突然消失。
		if len(item.models) == 0 && item.info.Error != "" {
			for _, cached := range previous.Models {
				if cached.ProviderID == item.info.ID {
					item.models = append(item.models, cached)
				}
			}
			if len(item.models) > 0 {
				item.info.ModelsCount = len(item.models)
			}
		}
		providers = append(providers, item.info)
		models = append(models, item.models...)
	}
	applyDefaults(providers, models)

	snapshot := CatalogSnapshot{Providers: providers, Models: models, RefreshedAt: time.Now().UTC()}
	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()
	return nil
}

func (c *Catalog) Resolve(ctx context.Context, providerID, modelID string) (*config.ProviderConfig, *ModelInfo, error) {
	lookup := func() (*config.ProviderConfig, *ModelInfo) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var model *ModelInfo
		for i := range c.snapshot.Models {
			candidate := c.snapshot.Models[i]
			if candidate.ProviderID == providerID && candidate.ID == modelID {
				copy := candidate
				model = &copy
				break
			}
		}
		if model == nil {
			return nil, nil
		}
		for i := range c.providers {
			if c.providers[i].Config.ID == providerID {
				copy := c.providers[i].Config
				return &copy, model
			}
		}
		return nil, nil
	}

	if provider, model := lookup(); provider != nil && model != nil {
		return provider, model, nil
	}
	_ = c.Refresh(ctx)
	if provider, model := lookup(); provider != nil && model != nil {
		return provider, model, nil
	}
	return nil, nil, fmt.Errorf("Provider %q 中不存在模型 %q", providerID, modelID)
}

func (c *Catalog) fetchProvider(parent context.Context, provider config.ProviderConfig) (ProviderInfo, []ModelInfo) {
	info := ProviderInfo{ID: provider.ID, Name: provider.Name, Default: provider.Default}
	fallback := modelsFromOverrides(provider)
	if provider.BaseURL == "" {
		info.Error = "缺少 base_url"
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}

	timeout := time.Duration(provider.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 8*time.Second {
		// 模型发现只需要短请求，不沿用聊天的长等待上限。
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(provider.BaseURL, "/")+"/models", nil)
	if err != nil {
		info.Error = err.Error()
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		info.Error = err.Error()
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		info.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		info.Error = err.Error()
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}
	rawModels, err := decodeModelList(body)
	if err != nil {
		info.Error = "模型列表解析失败: " + err.Error()
		info.ModelsCount = len(fallback)
		info.Available = len(fallback) > 0
		return info, fallback
	}

	models := make([]ModelInfo, 0, len(rawModels)+len(fallback))
	seen := map[string]struct{}{}
	for _, raw := range rawModels {
		id := stringValue(raw["id"])
		if id == "" {
			continue
		}
		model := modelFromRaw(provider, raw, id)
		models = append(models, model)
		seen[id] = struct{}{}
	}
	// 显式 override 同时也是 fallback：即使 /models 没返回该模型，也仍允许使用。
	for _, model := range fallback {
		if _, exists := seen[model.ID]; exists {
			continue
		}
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})
	info.Available = true
	info.ModelsCount = len(models)
	return info, models
}

func decodeModelList(body []byte) ([]map[string]any, error) {
	// OpenAI 标准是 {"data": [...]}; 少数兼容网关返回 {"models": [...]} 或直接数组。
	var object struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &object); err == nil {
		if object.Data != nil {
			return object.Data, nil
		}
		if object.Models != nil {
			return object.Models, nil
		}
	}

	var array []map[string]any
	if err := json.Unmarshal(body, &array); err == nil && array != nil {
		return array, nil
	}
	return nil, fmt.Errorf("响应中不存在 data/models 模型数组")
}

func modelsFromOverrides(provider config.ProviderConfig) []ModelInfo {
	models := make([]ModelInfo, 0, len(provider.ModelOverrides))
	for _, override := range provider.ModelOverrides {
		if override.ID == "" {
			continue
		}
		levels, style := effectiveThinking(provider, override, nil, override.ID)
		name := override.Name
		if name == "" {
			name = override.ID
		}
		models = append(models, ModelInfo{
			ID:             override.ID,
			Name:           name,
			ProviderID:     provider.ID,
			ProviderName:   provider.Name,
			Default:        override.Default,
			ThinkingLevels: levels,
			ThinkingStyle:  style,
		})
	}
	return models
}

func modelFromRaw(provider config.ProviderConfig, raw map[string]any, id string) ModelInfo {
	override := findOverride(provider.ModelOverrides, id)
	levels, style := effectiveThinking(provider, override, raw, id)
	name := firstNonEmpty(
		override.Name,
		stringValue(raw["name"]),
		stringValue(raw["display_name"]),
		id,
	)
	return ModelInfo{
		ID:             id,
		Name:           name,
		ProviderID:     provider.ID,
		ProviderName:   provider.Name,
		Default:        override.Default,
		ThinkingLevels: levels,
		ThinkingStyle:  style,
	}
}

func effectiveThinking(provider config.ProviderConfig, override config.ModelOverrideConfig, raw map[string]any, modelID string) ([]string, string) {
	if len(override.ThinkingLevels) > 0 || override.ThinkingStyle != "" {
		levels := override.ThinkingLevels
		style := override.ThinkingStyle
		if style == "" || style == "auto" {
			style = inferStyleFromLevels(levels)
		}
		if style == "" || style == "auto" {
			inferredLevels, inferredStyle := inferThinking(modelID)
			style = inferredStyle
			if len(levels) == 0 {
				levels = inferredLevels
			}
		}
		if len(levels) == 0 {
			levels = defaultLevelsForStyle(style)
		}
		return cleanLevels(levels), normalizeStyle(style)
	}

	if raw != nil {
		if levels := stringSlice(raw["thinking_levels"]); len(levels) > 0 {
			style := firstNonEmpty(stringValue(raw["thinking_style"]), inferStyleFromParameters(raw), inferStyleFromLevels(levels), "auto")
			if style == "auto" {
				_, style = inferThinking(modelID)
			}
			return cleanLevels(levels), normalizeStyle(style)
		}
		if levels := stringSlice(raw["reasoning_effort"]); len(levels) > 0 {
			return withOff(levels), "reasoning_effort"
		}
		if levels := stringSlice(raw["supported_reasoning_effort"]); len(levels) > 0 {
			return withOff(levels), "reasoning_effort"
		}
		if style := inferStyleFromParameters(raw); style != "" {
			switch style {
			case "enable_thinking":
				return []string{"off", "on"}, style
			case "reasoning_effort":
				return []string{"off", "low", "medium", "high"}, style
			}
		}
	}

	if len(provider.ThinkingLevels) > 0 || provider.ThinkingStyle != "" {
		levels := provider.ThinkingLevels
		style := provider.ThinkingStyle
		if style == "" || style == "auto" {
			style = inferStyleFromLevels(levels)
		}
		if style == "" || style == "auto" {
			inferredLevels, inferredStyle := inferThinking(modelID)
			style = inferredStyle
			if len(levels) == 0 {
				levels = inferredLevels
			}
		}
		if len(levels) == 0 {
			levels = defaultLevelsForStyle(style)
		}
		return cleanLevels(levels), normalizeStyle(style)
	}
	return inferThinking(modelID)
}

func inferStyleFromParameters(raw map[string]any) string {
	params := stringSlice(raw["supported_parameters"])
	for _, param := range params {
		switch strings.ToLower(param) {
		case "reasoning_effort":
			return "reasoning_effort"
		case "enable_thinking":
			return "enable_thinking"
		}
	}
	if capabilities, ok := raw["capabilities"].(map[string]any); ok {
		if boolValue(capabilities["reasoning"]) || boolValue(capabilities["thinking"]) {
			return "reasoning_effort"
		}
	}
	return ""
}

func inferStyleFromLevels(levels []string) string {
	for _, level := range levels {
		switch strings.ToLower(strings.TrimSpace(level)) {
		case "low", "medium", "high", "minimal":
			return "reasoning_effort"
		case "on", "true":
			return "enable_thinking"
		}
	}
	return ""
}

func inferThinking(modelID string) ([]string, string) {
	id := strings.ToLower(modelID)
	for _, keyword := range []string{"deepseek-reasoner", "deepseek-r1"} {
		if strings.Contains(id, keyword) {
			return []string{"on"}, "none"
		}
	}
	for _, keyword := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.Contains(id, keyword) {
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	for _, keyword := range []string{"qwen3", "qwq", "thinking"} {
		if strings.Contains(id, keyword) {
			return []string{"off", "on"}, "enable_thinking"
		}
	}
	for _, keyword := range []string{"reasoning", "reasoner"} {
		if strings.Contains(id, keyword) {
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	return []string{"off"}, "none"
}

func defaultLevelsForStyle(style string) []string {
	switch normalizeStyle(style) {
	case "reasoning_effort":
		return []string{"off", "low", "medium", "high"}
	case "enable_thinking":
		return []string{"off", "on"}
	default:
		return []string{"off"}
	}
}

func findOverride(overrides []config.ModelOverrideConfig, id string) config.ModelOverrideConfig {
	for _, override := range overrides {
		if override.ID == id {
			return override
		}
	}
	return config.ModelOverrideConfig{}
}

func applyDefaults(providers []ProviderInfo, models []ModelInfo) {
	providerDefault := -1
	for i := range providers {
		if providers[i].Default && providerDefault < 0 {
			providerDefault = i
		} else if providers[i].Default {
			providers[i].Default = false
		}
	}
	if providerDefault < 0 && len(providers) > 0 {
		providerDefault = 0
		providers[0].Default = true
	}

	for i := range providers {
		foundModelDefault := false
		firstModel := -1
		for j := range models {
			if models[j].ProviderID != providers[i].ID {
				continue
			}
			if firstModel < 0 {
				firstModel = j
			}
			if models[j].Default && !foundModelDefault {
				foundModelDefault = true
				continue
			}
			if models[j].Default {
				models[j].Default = false
			}
		}
		if !foundModelDefault && firstModel >= 0 {
			models[firstModel].Default = true
		}
	}
}

func (c *Catalog) configFallbackSnapshot() CatalogSnapshot {
	providers := make([]ProviderInfo, 0, len(c.providers))
	models := make([]ModelInfo, 0)
	for _, runtime := range c.providers {
		provider := runtime.Config
		fallback := modelsFromOverrides(provider)
		providers = append(providers, ProviderInfo{
			ID:          provider.ID,
			Name:        provider.Name,
			Default:     provider.Default,
			Available:   len(fallback) > 0,
			ModelsCount: len(fallback),
		})
		models = append(models, fallback...)
	}

	applyDefaults(providers, models)
	return CatalogSnapshot{Providers: providers, Models: models}
}

func cloneSnapshot(source CatalogSnapshot) CatalogSnapshot {
	clone := CatalogSnapshot{RefreshedAt: source.RefreshedAt}
	clone.Providers = append([]ProviderInfo(nil), source.Providers...)
	clone.Models = make([]ModelInfo, len(source.Models))
	for i := range source.Models {
		clone.Models[i] = source.Models[i]
		clone.Models[i].ThinkingLevels = append([]string(nil), source.Models[i].ThinkingLevels...)
	}
	return clone
}

func ValidateThinking(model ModelInfo, level string) error {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		level = "off"
	}
	for _, allowed := range model.ThinkingLevels {
		if level == allowed {
			return nil
		}
	}
	return fmt.Errorf("模型 %s 不支持思考深度 %q", model.ID, level)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func stringSlice(value any) []string {
	if direct, ok := value.([]string); ok {
		return cleanLevels(direct)
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func cleanLevels(levels []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level == "" {
			continue
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		result = append(result, level)
	}
	return result
}

func withOff(levels []string) []string {
	levels = cleanLevels(levels)
	for _, level := range levels {
		if level == "off" || level == "none" {
			return levels
		}
	}
	return append([]string{"off"}, levels...)
}

func normalizeStyle(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	if style == "" || style == "auto" {
		return "none"
	}
	return style
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
