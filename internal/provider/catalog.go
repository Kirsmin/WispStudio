package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"wisp/internal/config"
)

// Model 是目录中的一个模型条目（手动配置与自动发现合并去重后的展示视图）。
type Model struct {
	FullID     ModelFullID // 全称 id，如 deepseek/deepseek-chat
	Alias      string      // 显示别名（手动 Models 条目或 [[ModelAliases]] 段），可为空
	Discovered bool        // 是否来自自动发现（手动 Models 中的同名模型优先，不算发现）
}

// Catalog 模型目录：由 Config.local.toml 构建，按 ProviderID 索引。
// 并发安全；自动发现结果与手动 Models 合并去重后展示。
type Catalog struct {
	mu         sync.Mutex
	cfg        *config.Config
	path       string                   // 配置文件路径，AddModel 写回用
	discovered map[string][]ModelFullID // providerID -> 发现的模型
	httpClient *http.Client             // 发现请求用的客户端
}

// NewCatalog 由配置构建目录；path 为配置文件路径（写回新增模型用），可为空（不写回）。
func NewCatalog(cfg *config.Config, path string) *Catalog {
	return &Catalog{
		cfg:        cfg,
		path:       path,
		discovered: make(map[string][]ModelFullID),
	}
}

// ProviderIDs 返回全部已配置 provider id（排序后），供错误提示与展示。
func (c *Catalog) ProviderIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.providerIDsLocked()
}

// providerIDsLocked 是 ProviderIDs 的持锁版本；调用方需已持锁。
func (c *Catalog) providerIDsLocked() []string {
	ids := make([]string, 0, len(c.cfg.Providers))
	for i := range c.cfg.Providers {
		ids = append(ids, c.cfg.Providers[i].ProviderID)
	}
	sort.Strings(ids)
	return ids
}

// findProvider 按 ProviderID 找配置；找不到返回列出已有 id 的明确错误。
// 调用方需已持锁（Get/ListByProvider/AddModel 均持锁调用）。
func (c *Catalog) findProvider(providerID string) (*config.ProviderConfig, error) {
	for i := range c.cfg.Providers {
		if c.cfg.Providers[i].ProviderID == providerID {
			return &c.cfg.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("provider: provider 不存在: %q（已配置: %s）",
		providerID, strings.Join(c.providerIDsLocked(), ", "))
}

// Get 按全称 id 取模型；id 格式错误、provider 不存在、模型不存在均返回明确错误。
func (c *Catalog) Get(fullID ModelFullID) (Model, error) {
	providerID, modelID := fullID.Split()
	if providerID == "" || modelID == "" {
		return Model{}, fmt.Errorf("provider: 模型全称 id 格式应为 {ProviderID}/{ModelID}: %q", string(fullID))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.findProvider(providerID)
	if err != nil {
		return Model{}, err
	}
	// 手动 Models 优先
	for _, m := range p.Models {
		if m.ModelID == modelID {
			alias := m.ModelAlias
			if alias == "" {
				alias = c.modelAliasLocked(providerID, modelID)
			}
			return Model{FullID: fullID, Alias: alias}, nil
		}
	}
	// 再看自动发现结果
	for _, d := range c.discovered[providerID] {
		if d.ModelID() == modelID {
			return Model{FullID: fullID, Alias: c.modelAliasLocked(providerID, modelID), Discovered: true}, nil
		}
	}
	available := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		available = append(available, m.ModelID)
	}
	for _, d := range c.discovered[providerID] {
		available = append(available, d.ModelID())
	}
	return Model{}, fmt.Errorf("provider: 模型不存在: %q（%s 可用: %s）",
		string(fullID), providerID, strings.Join(available, ", "))
}

// Endpoint 是模型对应的连接参数（发请求用）。
type Endpoint struct {
	FullID     ModelFullID // 模型全称 id
	ProviderID string      // 服务商 ID
	ModelID    string      // provider 侧模型 id（原样透传，不做映射）
	BaseURL    string      // API 基础地址
	Secret     string      // API 密钥
}

// Endpoint 按模型全称 id 返回连接参数；id 格式错误、provider 或模型不存在
// 时返回明确错误。模型 id 一律原样透传，不做任何 provider 私有转换。
func (c *Catalog) Endpoint(fullID ModelFullID) (Endpoint, error) {
	if _, err := c.Get(fullID); err != nil {
		return Endpoint{}, err
	}
	providerID, modelID := fullID.Split()
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.findProvider(providerID)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		FullID:     fullID,
		ProviderID: providerID,
		ModelID:    modelID,
		BaseURL:    p.BaseURL,
		Secret:     p.Secret,
	}, nil
}

// DefaultModel 返回目录中的默认模型：第一个配置了模型（含自动发现）的
// provider 的第一个模型。一个都没有时返回明确错误。
func (c *Catalog) DefaultModel() (ModelFullID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.cfg.Providers {
		p := &c.cfg.Providers[i]
		if len(p.Models) > 0 {
			return Join(p.ProviderID, p.Models[0].ModelID), nil
		}
		if found := c.discovered[p.ProviderID]; len(found) > 0 {
			return found[0], nil
		}
	}
	return "", fmt.Errorf("provider: 没有可用模型（请在 %s 配置 [[Providers]].Models）", c.path)
}

// ListByProvider 列出某 provider 的全部模型（手动 Models 在前，发现的补齐去重）。
func (c *Catalog) ListByProvider(providerID string) ([]Model, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.findProvider(providerID)
	if err != nil {
		return nil, err
	}
	var out []Model
	seen := make(map[string]bool)
	for _, m := range p.Models {
		alias := m.ModelAlias
		if alias == "" {
			alias = c.modelAliasLocked(providerID, m.ModelID)
		}
		seen[m.ModelID] = true
		out = append(out, Model{FullID: Join(providerID, m.ModelID), Alias: alias})
	}
	for _, d := range c.discovered[providerID] {
		if seen[d.ModelID()] {
			continue
		}
		seen[d.ModelID()] = true
		out = append(out, Model{FullID: d, Alias: c.modelAliasLocked(providerID, d.ModelID()), Discovered: true})
	}
	return out, nil
}

// ResolveAlias 把别名（如 ProviderName "深度求索"）解析为 ProviderID；未命中返回明确错误。
func (c *Catalog) ResolveAlias(alias string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var known []string
	for i := range c.cfg.Providers {
		p := &c.cfg.Providers[i]
		if p.ProviderName == "" {
			continue
		}
		if p.ProviderName == alias {
			return p.ProviderID, nil
		}
		known = append(known, p.ProviderName)
	}
	return "", fmt.Errorf("provider: 别名不存在: %q（已知别名: %s）", alias, strings.Join(known, ", "))
}

// modelAliasLocked 查 [[ModelAliases]] 段；调用方需已持锁。
func (c *Catalog) modelAliasLocked(providerID, modelID string) string {
	for _, a := range c.cfg.ModelAliases {
		if a.ProviderID == providerID && a.ModelID == modelID {
			return a.Alias
		}
	}
	return ""
}

// DiscoverAll 对所有开启 Discovery 的 provider 做自动发现并合并进目录。
// 单个 provider 失败仅记日志，不影响其他 provider 与启动流程。
// logf 为 nil 时使用静默丢弃。
func (c *Catalog) DiscoverAll(ctx context.Context, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c.mu.Lock()
	providers := make([]config.ProviderConfig, len(c.cfg.Providers))
	copy(providers, c.cfg.Providers)
	hc := c.httpClient
	c.mu.Unlock()

	for i := range providers {
		p := &providers[i]
		if !p.Discovery.Enabled {
			continue
		}
		found, err := Discover(ctx, *p, hc)
		if err != nil {
			logf("provider: 自动发现失败 %q: %v", p.ProviderID, err)
			continue
		}
		c.mu.Lock()
		known := make(map[string]bool, len(c.cfg.Providers[i].Models))
		for _, m := range c.cfg.Providers[i].Models {
			known[m.ModelID] = true
		}
		added := 0
		for _, id := range found {
			if known[id.ModelID()] {
				continue
			}
			known[id.ModelID()] = true
			c.discovered[p.ProviderID] = append(c.discovered[p.ProviderID], id)
			added++
		}
		c.mu.Unlock()
		logf("provider: 自动发现 %q 新增模型 %d 个", p.ProviderID, added)
	}
}
