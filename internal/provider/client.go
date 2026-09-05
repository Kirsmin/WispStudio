package provider

import (
	"encoding/json"
	"io"
	"net/http"
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
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) FetchModels(providers []config.ProviderConfig, globalOpenAI config.OpenAIConfig) []ModelInfo {
	if len(providers) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	resultCh := make(chan []ModelInfo, len(providers))

	for _, p := range providers {
		wg.Add(1)
		go func(provider config.ProviderConfig) {
			defer wg.Done()
			models := c.fetchFromProvider(provider, globalOpenAI)
			resultCh <- models
		}(p)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var allModels []ModelInfo
	for models := range resultCh {
		allModels = append(allModels, models...)
	}

	if len(allModels) == 0 {
		return nil
	}

	hasDefault := false
	for i := range allModels {
		if allModels[i].Default {
			if hasDefault {
				allModels[i].Default = false
			} else {
				hasDefault = true
			}
		}
	}
	if !hasDefault && len(allModels) > 0 {
		allModels[0].Default = true
	}

	return allModels
}

func (c *Client) fetchFromProvider(provider config.ProviderConfig, globalOpenAI config.OpenAIConfig) []ModelInfo {
	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = globalOpenAI.BaseURL
	}
	apiKey := provider.APIKey
	if apiKey == "" {
		apiKey = globalOpenAI.APIKey
	}

	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var result struct {
		Data []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	var models []ModelInfo
	for _, m := range result.Data {
		if m.Object != "model" {
			continue
		}
		info := ModelInfo{
			ID:           m.ID,
			Name:         inferModelName(m.ID),
			ProviderName: provider.Name,
			BaseURL:      baseURL,
			APIKey:       apiKey,
		}
		info.ThinkingLevels, info.ThinkingStyle = inferThinkingCapability(m.ID)
		models = append(models, info)
	}

	if provider.Default && len(models) > 0 {
		models[0].Default = true
	}

	return models
}

func inferModelName(id string) string {
	nameMap := map[string]string{
		"deepseek-chat":     "DeepSeek-V3",
		"deepseek-reasoner": "DeepSeek-R1",
		"deepseek-coder":    "DeepSeek-Coder",
		"gpt-4o":            "GPT-4o",
		"gpt-4o-mini":       "GPT-4o Mini",
		"gpt-4-turbo":       "GPT-4 Turbo",
		"gpt-4":             "GPT-4",
		"claude-3-5-sonnet": "Claude 3.5 Sonnet",
		"claude-3-opus":     "Claude 3 Opus",
		"qwen3-max":         "Qwen3-Max",
	}
	if name, ok := nameMap[id]; ok {
		return name
	}
	parts := strings.Split(id, "-")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, " ")
}

func inferThinkingCapability(id string) ([]string, string) {
	idLower := strings.ToLower(id)
	reasoningKeywords := []string{"reasoner", "r1", "thinking", "o1", "o3"}
	for _, kw := range reasoningKeywords {
		if strings.Contains(idLower, kw) {
			return []string{"off", "low", "high"}, "reasoning_effort"
		}
	}
	return []string{"off"}, "none"
}

func FindModel(models []ModelInfo, id string) *ModelInfo {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	return nil
}
