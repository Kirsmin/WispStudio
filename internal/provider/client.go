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
	Key            string   `json:"key"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ProviderID     string   `json:"provider_id"`
	ProviderName   string   `json:"provider_name"`
	Default        bool     `json:"default"`
	ThinkingLevels []string `json:"thinking_levels"`
	ThinkingStyle  string   `json:"thinking_style"`
	BaseURL        string   `json:"-"`
	APIKey         string   `json:"-"`
}

type Client struct{ httpClient *http.Client }

func NewClient() *Client { return &Client{httpClient: &http.Client{Timeout: 12 * time.Second}} }

func (c *Client) FetchModels(providers []config.ProviderConfig, global config.OpenAIConfig, legacy []config.ModelConfig) []ModelInfo {
	if len(providers) == 0 {
		return legacyModels(legacy, global)
	}
	groups := make([][]ModelInfo, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p config.ProviderConfig) { defer wg.Done(); groups[i] = c.fetchProvider(i, p, global) }(i, p)
	}
	wg.Wait()
	var all []ModelInfo
	for _, g := range groups {
		all = append(all, g...)
	}
	ensureDefault(all)
	return all
}

func (c *Client) fetchProvider(index int, p config.ProviderConfig, global config.OpenAIConfig) []ModelInfo {
	pid := strings.TrimSpace(p.ID)
	if pid == "" {
		pid = slug(firstNonEmpty(p.Name, fmt.Sprintf("provider-%d", index+1)))
	}
	base := firstNonEmpty(p.BaseURL, global.BaseURL)
	key := firstNonEmpty(p.APIKey, global.APIKey)
	if base == "" {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		levels, style := inferThinking(m.ID)
		if len(p.ThinkingLevels) > 0 {
			levels = cleanLevels(p.ThinkingLevels)
		}
		if p.ThinkingStyle != "" {
			style = p.ThinkingStyle
		}
		info := ModelInfo{Key: pid + "::" + m.ID, ID: m.ID, Name: firstNonEmpty(m.Name, humanName(m.ID)), ProviderID: pid, ProviderName: firstNonEmpty(p.Name, pid), ThinkingLevels: levels, ThinkingStyle: style, BaseURL: base, APIKey: key}
		for _, ov := range p.ModelOverrides {
			if ov.ID == m.ID {
				if ov.Name != "" {
					info.Name = ov.Name
				}
				if len(ov.ThinkingLevels) > 0 {
					info.ThinkingLevels = cleanLevels(ov.ThinkingLevels)
				}
				if ov.ThinkingStyle != "" {
					info.ThinkingStyle = ov.ThinkingStyle
				}
				if ov.Default {
					info.Default = true
				}
				break
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if p.Default && len(out) > 0 && !hasDefault(out) {
		out[0].Default = true
	}
	return out
}

func legacyModels(models []config.ModelConfig, global config.OpenAIConfig) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		levels := cleanLevels(m.ThinkingLevels)
		style := m.ThinkingStyle
		if len(levels) == 0 {
			levels, style = inferThinking(m.ID)
		}
		out = append(out, ModelInfo{Key: "legacy::" + m.ID, ID: m.ID, Name: firstNonEmpty(m.Name, humanName(m.ID)), ProviderID: "legacy", ProviderName: "Legacy", Default: m.Default, ThinkingLevels: levels, ThinkingStyle: style, BaseURL: firstNonEmpty(m.BaseURL, global.BaseURL), APIKey: firstNonEmpty(m.APIKey, global.APIKey)})
	}
	ensureDefault(out)
	return out
}

func FindByKey(models []ModelInfo, key string) *ModelInfo {
	for i := range models {
		if models[i].Key == key {
			v := models[i]
			return &v
		}
	}
	return nil
}
func ValidateThinking(m ModelInfo, level string) error {
	if level == "" {
		level = "off"
	}
	for _, v := range m.ThinkingLevels {
		if v == level {
			return nil
		}
	}
	return fmt.Errorf("模型 %s 不支持思考深度 %q", m.Name, level)
}
func inferThinking(id string) ([]string, string) {
	s := strings.ToLower(id)
	if strings.Contains(s, "qwen3") || strings.Contains(s, "qwq") {
		return []string{"off", "on"}, "enable_thinking"
	}
	for _, k := range []string{"gpt-5", "o1", "o3", "o4", "reasoning", "reasoner", "deepseek-r1", "deepseek-v4", "r1-"} {
		if strings.Contains(s, k) {
			return []string{"off", "low", "medium", "high"}, "reasoning_effort"
		}
	}
	return []string{"off"}, "none"
}
func cleanLevels(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return []string{"off"}
	}
	return out
}
func ensureDefault(m []ModelInfo) {
	found := false
	for i := range m {
		if m[i].Default && !found {
			found = true
		} else if m[i].Default {
			m[i].Default = false
		}
	}
	if !found && len(m) > 0 {
		m[0].Default = true
	}
}
func hasDefault(m []ModelInfo) bool {
	for _, v := range m {
		if v.Default {
			return true
		}
	}
	return false
}
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func humanName(id string) string {
	return strings.ReplaceAll(strings.ReplaceAll(id, "-", " "), "_", " ")
}
