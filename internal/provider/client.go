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
	ProviderName   string   `json:"provider_name,omitempty"`
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
		models := convertLegacyModels(legacy, global)
		ensureSingleDefault(models)
		return models
	}

	groups := make([][]ModelInfo, len(providers))
	var wg sync.WaitGroup
	for i := range providers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			groups[index] = c.fetchProvider(providers[index], global)
		}(i)
	}
	wg.Wait()

	models := make([]ModelInfo, 0)
	for _, group := range groups {
		models = append(models, group...)
	}
	models = mergeLegacyOverrides(models, legacy, global)
	ensureSingleDefault(models)
	return models
}

func (c *Client) fetchProvider(p config.ProviderConfig, global config.OpenAIConfig) []ModelInfo {
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
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	models := make([]ModelInfo, 0, len(payload.Data))
	for _, raw := range payload.Data {
		id := stringField(raw, "id")
		if id == "" {
			continue
		}
		name := firstNonEmpty(stringField(raw, "name"), inferModelName(id))
		levels, style := inferThinking(raw, id)
		if len(p.ThinkingLevels) > 0 {
			levels = cleanLevels(p.ThinkingLevels)
		}
		if p.ThinkingStyle != "" {
			style = normalizeStyle(p.ThinkingStyle)
		}
		model := ModelInfo{
			ID: id, Name: name, ThinkingLevels: levels, ThinkingStyle: style,
			ProviderName: p.Name, BaseURL: baseURL, APIKey: apiKey,
		}
		applyOverride(&model, p.ModelOverrides)
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool { return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name) })
	if p.Default && len(models) > 0 && !hasDefault(models) {
		models[0].Default = true
	}
	return models
}

func inferThinking(raw map[string]any, id string) ([]string, string) {
	// 先看 /models 返回的显式元数据。不同兼容网关常把 capability
	// 放在 capabilities / metadata / features 等嵌套对象里，所以递归检测，
	// 不只依赖固定的顶层字段。
	if levels, style := detectThinkingMetadata(raw); len(levels) > 0 {
		return levels, style
	}

	lower := strings.ToLower(id)
	for _, marker := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.Contains(lower, marker) {
			if strings.Contains(lower, "gpt-5") {
				return []string{"off", "minimal", "low", "medium", "high"}, "reasoning_effort"
			}
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	for _, marker := range []string{"qwen3", "qwq"} {
		if strings.Contains(lower, marker) {
			return []string{"off", "on"}, "enable_thinking"
		}
	}
	// 常见第三方 OpenAI-compatible 网关会给 reasoning/reasoner/r1/thinking
	// 模型暴露 reasoning_effort，却不在 /models 中声明 supported_parameters。
	// 对这些明确命名的推理模型提供深度选择；普通 DeepSeek chat 模型仍不伪造能力。
	for _, marker := range []string{"reasoner", "reasoning", "-r1", "_r1", "/r1", "thinking"} {
		if strings.Contains(lower, marker) {
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	return []string{"off"}, "none"
}

func detectThinkingMetadata(raw map[string]any) ([]string, string) {
	if raw == nil {
		return nil, ""
	}
	// 精确字段优先，这样能保留服务端声明的档位。
	for _, key := range []string{"supported_reasoning_effort", "supported_reasoning_efforts", "reasoning_efforts", "reasoning_effort_levels", "thinking_levels"} {
		if value, ok := findKeyRecursive(raw, key); ok {
			if levels := anyStringSlice(value); len(levels) > 0 {
				levels = cleanLevels(levels)
				if !contains(levels, "off") {
					levels = append([]string{"off"}, levels...)
				}
				return levels, "reasoning_effort"
			}
		}
	}

	// supported_parameters / parameters / capabilities 既可能是字符串数组，
	// 也可能是对象或嵌套对象。把键和值都扁平化后识别。
	words := collectCapabilityWords(raw)
	if words["reasoning_effort"] || words["reasoning-effort"] {
		return []string{"off", "low", "medium", "high"}, "reasoning_effort"
	}
	if words["enable_thinking"] || words["enable-thinking"] {
		return []string{"off", "on"}, "enable_thinking"
	}
	if words["thinking"] {
		return []string{"off", "on"}, "thinking_object"
	}
	return nil, ""
}

func findKeyRecursive(value any, target string) (any, bool) {
	target = strings.ToLower(target)
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if strings.ToLower(key) == target {
				return child, true
			}
		}
		for _, child := range node {
			if found, ok := findKeyRecursive(child, target); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range node {
			if found, ok := findKeyRecursive(child, target); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func collectCapabilityWords(value any) map[string]bool {
	words := map[string]bool{}
	var walk func(any)
	walk = func(node any) {
		switch current := node.(type) {
		case map[string]any:
			for key, child := range current {
				lowerKey := strings.ToLower(strings.TrimSpace(key))
				if lowerKey != "" {
					words[lowerKey] = true
				}
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		case string:
			for _, token := range strings.FieldsFunc(strings.ToLower(current), func(r rune) bool {
				return r == ',' || r == ';' || r == ' ' || r == '|' || r == ':'
			}) {
				if token != "" {
					words[token] = true
				}
			}
		}
	}
	walk(value)
	return words
}

func anyStringSlice(value any) []string {
	switch raw := value.(type) {
	case []any:
		result := make([]string, 0, len(raw))
		for _, item := range raw {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return append([]string(nil), raw...)
	case string:
		parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == ' ' })
		return parts
	default:
		return nil
	}
}

func applyOverride(model *ModelInfo, overrides []config.ModelOverrideConfig) {
	for _, o := range overrides {
		if o.ID != model.ID {
			continue
		}
		if o.Name != "" {
			model.Name = o.Name
		}
		if o.Default {
			model.Default = true
		}
		if len(o.ThinkingLevels) > 0 {
			model.ThinkingLevels = cleanLevels(o.ThinkingLevels)
		}
		if o.ThinkingStyle != "" {
			model.ThinkingStyle = normalizeStyle(o.ThinkingStyle)
		}
		return
	}
}

func mergeLegacyOverrides(models []ModelInfo, legacy []config.ModelConfig, global config.OpenAIConfig) []ModelInfo {
	index := map[string]int{}
	for i := range models {
		index[models[i].ID] = i
	}
	for _, m := range legacy {
		if i, ok := index[m.ID]; ok {
			if m.Name != "" {
				models[i].Name = m.Name
			}
			if m.Default {
				models[i].Default = true
			}
			if len(m.ThinkingLevels) > 0 {
				models[i].ThinkingLevels = cleanLevels(m.ThinkingLevels)
			}
			if m.ThinkingStyle != "" {
				models[i].ThinkingStyle = normalizeStyle(m.ThinkingStyle)
			}
			if m.BaseURL != "" {
				models[i].BaseURL = m.BaseURL
			}
			if m.APIKey != "" {
				models[i].APIKey = m.APIKey
			}
			continue
		}
		models = append(models, modelFromConfig(m, global))
	}
	return models
}

func convertLegacyModels(items []config.ModelConfig, global config.OpenAIConfig) []ModelInfo {
	result := make([]ModelInfo, 0, len(items))
	for _, item := range items {
		result = append(result, modelFromConfig(item, global))
	}
	return result
}

func modelFromConfig(m config.ModelConfig, global config.OpenAIConfig) ModelInfo {
	levels := cleanLevels(m.ThinkingLevels)
	style := normalizeStyle(m.ThinkingStyle)
	if len(levels) == 0 {
		levels, style = inferThinking(nil, m.ID)
	}
	if style == "" {
		style = "none"
	}
	return ModelInfo{
		ID: m.ID, Name: firstNonEmpty(m.Name, inferModelName(m.ID)), Default: m.Default,
		ThinkingLevels: levels, ThinkingStyle: style,
		BaseURL: firstNonEmpty(m.BaseURL, global.BaseURL), APIKey: firstNonEmpty(m.APIKey, global.APIKey),
	}
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

func ValidateThinking(model ModelInfo, level string) error {
	if level == "" {
		level = firstLevel(model.ThinkingLevels)
	}
	for _, allowed := range model.ThinkingLevels {
		if strings.EqualFold(allowed, level) {
			return nil
		}
	}
	return fmt.Errorf("模型 %s 不支持思考档位 %q，可选: %s", model.ID, level, strings.Join(model.ThinkingLevels, ", "))
}

func firstLevel(levels []string) string {
	if len(levels) == 0 {
		return "off"
	}
	if contains(levels, "off") {
		return "off"
	}
	return levels[0]
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

func normalizeStyle(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	switch style {
	case "", "none", "reasoning_effort", "enable_thinking", "thinking_object":
		return style
	default:
		return style
	}
}
func cleanLevels(levels []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level == "" || seen[level] {
			continue
		}
		seen[level] = true
		result = append(result, level)
	}
	return result
}
func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func extractStringSlice(m map[string]any, keys ...string) []string {
	if m == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		if raw, ok := value.([]any); ok {
			result := make([]string, 0, len(raw))
			for _, item := range raw {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			if len(result) > 0 {
				return result
			}
		}
	}
	return nil
}
func inferModelName(id string) string {
	if id == "" {
		return "Unknown"
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '/' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		up := strings.ToUpper(p)
		if strings.HasPrefix(up, "GPT") || strings.HasPrefix(up, "QWEN") || strings.HasPrefix(up, "DEEPSEEK") {
			parts[i] = up
		} else {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
