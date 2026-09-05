package provider

import (
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

type ModelInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Default        bool     `json:"default"`
	ThinkingLevels []string `json:"thinking_levels"`
	ThinkingStyle  string   `json:"thinking_style"`
	ProviderName   string   `json:"provider_name"`
	BaseURL        string   `json:"-"`
	APIKey         string   `json:"-"`
}

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) FetchModels(providers []config.ProviderConfig, global config.OpenAIConfig, legacy []config.ModelConfig) []ModelInfo {
	if len(providers) == 0 {
		return ConvertLegacyModels(legacy, global)
	}

	results := make([][]ModelInfo, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(index int, provider config.ProviderConfig) {
			defer wg.Done()
			results[index] = c.fetchFromProvider(provider, global)
		}(i, p)
	}
	wg.Wait()

	all := make([]ModelInfo, 0)
	for _, group := range results {
		all = append(all, group...)
	}
	all = mergeLegacyOverrides(all, legacy, global)
	ensureSingleDefault(all)
	return all
}

func (c *Client) fetchFromProvider(p config.ProviderConfig, global config.OpenAIConfig) []ModelInfo {
	baseURL := firstNonEmpty(p.BaseURL, global.BaseURL)
	apiKey := firstNonEmpty(p.APIKey, global.APIKey)
	if baseURL == "" {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}

	var payload struct {
		Data []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Name   string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	models := make([]ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		levels, style := inferThinkingCapability(item.ID)
		if len(p.ThinkingLevels) > 0 {
			levels = cleanLevels(p.ThinkingLevels)
		}
		if p.ThinkingStyle != "" {
			style = p.ThinkingStyle
		}
		info := ModelInfo{
			ID:             item.ID,
			Name:           firstNonEmpty(item.Name, inferModelName(item.ID)),
			ThinkingLevels: levels,
			ThinkingStyle:  style,
			ProviderName:   p.Name,
			BaseURL:        baseURL,
			APIKey:         apiKey,
		}
		applyProviderOverride(&info, p.ModelOverrides)
		models = append(models, info)
	}
	sort.SliceStable(models, func(i, j int) bool { return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name) })
	if p.Default && len(models) > 0 && !hasDefault(models) {
		models[0].Default = true
	}
	return models
}

func applyProviderOverride(info *ModelInfo, overrides []config.ModelOverrideConfig) {
	for _, override := range overrides {
		if override.ID != info.ID {
			continue
		}
		if override.Name != "" {
			info.Name = override.Name
		}
		if len(override.ThinkingLevels) > 0 {
			info.ThinkingLevels = cleanLevels(override.ThinkingLevels)
		}
		if override.ThinkingStyle != "" {
			info.ThinkingStyle = override.ThinkingStyle
		}
		if override.Default {
			info.Default = true
		}
		return
	}
}

func mergeLegacyOverrides(discovered []ModelInfo, legacy []config.ModelConfig, global config.OpenAIConfig) []ModelInfo {
	byID := make(map[string]int, len(discovered))
	for i := range discovered {
		byID[discovered[i].ID] = i
	}
	for _, configured := range legacy {
		if i, ok := byID[configured.ID]; ok {
			m := &discovered[i]
			if configured.Name != "" {
				m.Name = configured.Name
			}
			if configured.Default {
				m.Default = true
			}
			if len(configured.ThinkingLevels) > 0 {
				m.ThinkingLevels = cleanLevels(configured.ThinkingLevels)
			}
			if configured.ThinkingStyle != "" {
				m.ThinkingStyle = configured.ThinkingStyle
			}
			if configured.BaseURL != "" {
				m.BaseURL = configured.BaseURL
			}
			if configured.APIKey != "" {
				m.APIKey = configured.APIKey
			}
			continue
		}
		discovered = append(discovered, modelFromConfig(configured, global))
	}
	return discovered
}

func ConvertLegacyModels(models []config.ModelConfig, global config.OpenAIConfig) []ModelInfo {
	result := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		result = append(result, modelFromConfig(model, global))
	}
	ensureSingleDefault(result)
	return result
}

func modelFromConfig(m config.ModelConfig, global config.OpenAIConfig) ModelInfo {
	levels := cleanLevels(m.ThinkingLevels)
	style := m.ThinkingStyle
	if len(levels) == 0 {
		levels, style = inferThinkingCapability(m.ID)
	}
	return ModelInfo{
		ID: m.ID, Name: firstNonEmpty(m.Name, inferModelName(m.ID)), Default: m.Default,
		ThinkingLevels: levels, ThinkingStyle: style,
		BaseURL: firstNonEmpty(m.BaseURL, global.BaseURL), APIKey: firstNonEmpty(m.APIKey, global.APIKey),
	}
}

func inferThinkingCapability(id string) ([]string, string) {
	lower := strings.ToLower(id)
	// OpenAI-compatible reasoning effort families. "off" means omit the parameter.
	for _, keyword := range []string{"gpt-5", "o1", "o3", "o4", "reasoning", "reasoner"} {
		if strings.Contains(lower, keyword) {
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	// Qwen-family compatible gateways commonly use enable_thinking as a boolean.
	for _, keyword := range []string{"qwen3", "qwq", "thinking"} {
		if strings.Contains(lower, keyword) {
			return []string{"off", "on"}, "enable_thinking"
		}
	}
	return []string{"off"}, "none"
}

func inferModelName(id string) string {
	if id == "" {
		return "Unknown"
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '/' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		if strings.HasPrefix(upper, "GPT") || strings.HasPrefix(upper, "QWEN") || strings.HasPrefix(upper, "DEEPSEEK") {
			parts[i] = upper
		} else {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func cleanLevels(levels []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.TrimSpace(strings.ToLower(level))
		if level == "" || seen[level] {
			continue
		}
		seen[level] = true
		result = append(result, level)
	}
	return result
}

func ensureSingleDefault(models []ModelInfo) {
	found := false
	for i := range models {
		if models[i].Default && !found {
			found = true
			continue
		}
		if models[i].Default {
			models[i].Default = false
		}
	}
	if !found && len(models) > 0 {
		models[0].Default = true
	}
}

func hasDefault(models []ModelInfo) bool {
	for _, m := range models {
		if m.Default {
			return true
		}
	}
	return false
}

func FindModel(models []ModelInfo, id string) *ModelInfo {
	for i := range models {
		if models[i].ID == id {
			copy := models[i]
			return &copy
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ValidateThinking(model ModelInfo, level string) error {
	if level == "" {
		level = "off"
	}
	for _, allowed := range model.ThinkingLevels {
		if level == allowed {
			return nil
		}
	}
	return fmt.Errorf("模型 %s 不支持思考档位 %q", model.ID, level)
}
