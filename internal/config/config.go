// 包 config 负责 Wisp 单一配置文件 Config.local.toml 的加载、生成与校验。
//
// 配置文件位于可执行文件同目录，不存在时用默认值生成一份。
// 配置键使用 PascalCase；思考深度直接透传 OpenAI 的 reasoning_effort 字段，
// 本包不做枚举与 provider 私有格式转换。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName 是配置文件名（固定，位于可执行文件同目录）。
const FileName = "Config.local.toml"

// 默认配置文件内容：简短、人类可读，首次运行自动生成。
const defaultTOML = `# Wisp 本地配置（与可执行文件同目录，首次运行自动生成，请勿提交到版本库）

[server]
Port = 8787

[[Providers]]
ProviderID   = "deepseek"                # 模型全称 id 的前缀 {ProviderID}/{ModelID}
ProviderName = "深度求索"                 # 别名，可中文
BaseURL      = "https://api.deepseek.com"
Secret       = "sk-..."                  # 明文，不做加密
Models       = ["deepseek-chat"]         # 手动维护的模型 id 列表

[Providers.Discovery]                    # GET {BaseURL}{Path} 自动发现模型
Enabled = true
Path    = "/models"
`

// Config 是根配置。
type Config struct {
	Server    ServerConfig
	Providers []ProviderConfig
}

// ServerConfig 服务配置。
type ServerConfig struct {
	Port int // 监听端口
}

// ProviderConfig 模型服务商配置。
type ProviderConfig struct {
	ProviderID   string          // 模型全称 id 的前缀 {ProviderID}/{ModelID}
	ProviderName string          // 显示别名，可中文
	BaseURL      string          // API 基础地址
	Secret       string          // API 密钥，明文
	Models       []ModelConfig   // 手动维护的模型列表
	Discovery    DiscoveryConfig // 自动发现配置
}

// ModelConfig 单个模型配置。
// TOML 中可简写为字符串（无别名），见 UnmarshalTOML。
type ModelConfig struct {
	ModelID    string // 模型 id（{ProviderID}/{ModelID} 为全称）
	ModelAlias string // 可选显示名
}

// UnmarshalTOML 支持 Models = ["id"] 简写形式与完整表形式混用。
func (m *ModelConfig) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		m.ModelID = t
		return nil
	case map[string]any:
		if id, ok := t["ModelID"].(string); ok {
			m.ModelID = id
		}
		if alias, ok := t["ModelAlias"].(string); ok {
			m.ModelAlias = alias
		}
		return nil
	default:
		return fmt.Errorf("config: 模型配置类型无法识别: %T", v)
	}
}

// DiscoveryConfig 模型自动发现配置。
type DiscoveryConfig struct {
	Enabled bool   // 是否启用
	Path    string // 发现接口路径，如 /models
}

// Load 从可执行文件同目录加载配置，不存在时先生成默认配置。
func Load() (*Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("config: 定位可执行文件失败: %w", err)
	}
	return LoadFile(filepath.Join(filepath.Dir(exe), FileName))
}

// LoadFile 从指定路径加载配置，不存在时先生成默认配置。
func LoadFile(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultTOML), 0o600); err != nil {
			return nil, fmt.Errorf("config: 生成默认配置失败: %w", err)
		}
	}
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("config: 解析配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate 校验配置并进行规范化（端口默认值、模型去重）。
func (c *Config) Validate() error {
	if c.Server.Port == 0 {
		c.Server.Port = 8787
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("config: server.Port 超出范围: %d", c.Server.Port)
	}
	seenProvider := make(map[string]bool, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if !providerIDPattern.MatchString(p.ProviderID) {
			return fmt.Errorf("config: Providers[%d].ProviderID 只允许小写字母数字连字符: %q", i, p.ProviderID)
		}
		if seenProvider[p.ProviderID] {
			return fmt.Errorf("config: ProviderID 重复: %q", p.ProviderID)
		}
		seenProvider[p.ProviderID] = true
		// 模型去重（保留首个），并校验 id 不含 "/"
		seenModel := make(map[string]bool, len(p.Models))
		deduped := p.Models[:0]
		for _, m := range p.Models {
			if strings.Contains(m.ModelID, "/") {
				return fmt.Errorf("config: 模型 id 不应包含 '/': %q", m.ModelID)
			}
			if seenModel[m.ModelID] {
				continue
			}
			seenModel[m.ModelID] = true
			deduped = append(deduped, m)
		}
		p.Models = deduped
	}
	return nil
}
