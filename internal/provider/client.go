package provider

import (
	"encoding/json"
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

func (c *Client) FetchModels(providers []config.ProviderConfig, global config.OpenAIConfig) []ModelInfo {
	if len(providers) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	resultCh := make(chan []ModelInfo, len(providers))
	for _, p := range providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultCh <- c.fetchFromProvider(p, global)
		}()
	}
	wg.Wait()
	close(resultCh)

	var all []ModelInfo
	for models := range resultCh {
		all = append(all, models...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Default != all[j].Default {
			return all[i].Default
		}
		return all[i].Name < all[j].Name
	})
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	var result struct {
		Data []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return nil
	}
	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID == "" || (m.Object != "" && m.Object != "model") {
			continue
		}
		levels, style := inferThinkingCapability(m.ID)
		models = append(models, ModelInfo{
			ID:             m.ID,
			Name:           inferModelName(m.ID),
			ThinkingLevels: levels,
			ThinkingStyle:  style,
			ProviderName:   p.Name,
			BaseURL:        baseURL,
			APIKey:         apiKey,
		})
	}
	if p.Default && len(models) > 0 {
		models[0].Default = true
	}
	return models
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ensureSingleDefault(models []ModelInfo) {
	found := false
	for i := range models {
		if models[i].Default && !found {
			found = true
			continue
		}
		models[i].Default = false
	}
	if !found && len(models) > 0 {
		models[0].Default = true
	}
}

func inferModelName(id string) string {
	known := map[string]string{
		"deepseek-chat": "DeepSeek-V3", "deepseek-reasoner": "DeepSeek-R1",
		"gpt-4o": "GPT-4o", "gpt-4o-mini": "GPT-4o Mini",
	}
	if name, ok := known[id]; ok {
		return name
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i := range parts {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, " ")
}

func inferThinkingCapability(id string) ([]string, string) {
	low := strings.ToLower(id)
	// DeepSeek reasoner 本身就是推理模型，不额外发送 OpenAI reasoning_effort，避免兼容网关拒绝未知字段。
	if strings.Contains(low, "deepseek-reasoner") || strings.Contains(low, "deepseek-r1") {
		return []string{"auto"}, "none"
	}
	if strings.Contains(low, "o1") || strings.Contains(low, "o3") || strings.Contains(low, "o4") {
		return []string{"low", "medium", "high"}, "reasoning_effort"
	}
	if strings.Contains(low, "qwen3") && strings.Contains(low, "thinking") {
		return []string{"off", "on"}, "enable_thinking"
	}
	return []string{"off"}, "none"
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
