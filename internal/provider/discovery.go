package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"wisp/internal/config"
)

// modelEntry / modelsResponse 是 OpenAI 兼容 GET /models 的响应结构。
type modelEntry struct {
	ID string `json:"id"`
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// Discover 请求 provider 的发现接口（GET {BaseURL}{Path}），
// 返回发现的模型全称 id 列表；单个 provider 失败返回错误，由调用方记日志，不阻塞启动。
func Discover(ctx context.Context, p config.ProviderConfig, hc *http.Client) ([]ModelFullID, error) {
	if p.Discovery.Path == "" {
		return nil, fmt.Errorf("provider: %q 开启了 Discovery 但 Path 为空", p.ProviderID)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	url := strings.TrimRight(p.BaseURL, "/") + p.Discovery.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider: 构造发现请求失败: %w", err)
	}
	if p.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+p.Secret)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: 发现请求失败 %q: %w", p.ProviderID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyStatus(resp.StatusCode,
			fmt.Sprintf("provider: 发现接口返回异常 %q", p.ProviderID))
	}
	var out modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("provider: 发现响应解析失败 %q: %w", p.ProviderID, err)
	}
	seen := make(map[string]bool, len(out.Data))
	ids := make([]ModelFullID, 0, len(out.Data))
	for _, e := range out.Data {
		if e.ID == "" || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		ids = append(ids, Join(p.ProviderID, e.ID))
	}
	return ids, nil
}
