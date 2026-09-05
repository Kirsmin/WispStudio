package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type StorageConfig struct {
	DataDir string `toml:"data_dir"`
}

type OpenAIConfig struct {
	BaseURL    string `toml:"base_url"`
	APIKey     string `toml:"api_key"`
	TimeoutSec int    `toml:"timeout_sec"`
}

// ModelConfig 保留旧版 [[models]] 配置兼容。
// 新配置优先使用 [[providers]] + [[providers.model_overrides]]。
type ModelConfig struct {
	ID             string   `toml:"id"`
	Name           string   `toml:"name"`
	Default        bool     `toml:"default"`
	ThinkingLevels []string `toml:"thinking_levels"`
	ThinkingStyle  string   `toml:"thinking_style"`
	BaseURL        string   `toml:"base_url" json:"base_url,omitempty"`
	APIKey         string   `toml:"api_key" json:"-"`
}

type ModelOverrideConfig struct {
	ID             string   `toml:"id"`
	Name           string   `toml:"name"`
	Default        bool     `toml:"default"`
	ThinkingLevels []string `toml:"thinking_levels"`
	ThinkingStyle  string   `toml:"thinking_style"`
}

type ProviderConfig struct {
	ID             string                `toml:"id"`
	Name           string                `toml:"name"`
	BaseURL        string                `toml:"base_url"`
	APIKey         string                `toml:"api_key"`
	Default        bool                  `toml:"default"`
	TimeoutSec     int                   `toml:"timeout_sec"`
	ThinkingLevels []string              `toml:"thinking_levels"`
	ThinkingStyle  string                `toml:"thinking_style"`
	ModelOverrides []ModelOverrideConfig `toml:"model_overrides"`
}

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Storage   StorageConfig    `toml:"storage"`
	OpenAI    OpenAIConfig     `toml:"openai"`
	Models    []ModelConfig    `toml:"models"`
	Providers []ProviderConfig `toml:"providers"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Server.Host) == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 7860
	}
	if strings.TrimSpace(c.Storage.DataDir) == "" {
		c.Storage.DataDir = "Data"
	}
	if c.OpenAI.TimeoutSec <= 0 {
		c.OpenAI.TimeoutSec = 120
	}

	usedIDs := make(map[string]struct{}, len(c.Providers))
	for i := range c.Providers {
		provider := &c.Providers[i]
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Name = strings.TrimSpace(provider.Name)
		provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		if provider.ID == "" {
			provider.ID = makeProviderID(provider.Name, i+1)
		}
		baseID := provider.ID
		for suffix := 2; ; suffix++ {
			if _, exists := usedIDs[provider.ID]; !exists {
				break
			}
			provider.ID = fmt.Sprintf("%s-%d", baseID, suffix)
		}
		usedIDs[provider.ID] = struct{}{}
		if provider.Name == "" {
			provider.Name = provider.ID
		}
		if provider.TimeoutSec <= 0 {
			provider.TimeoutSec = c.OpenAI.TimeoutSec
		}
		provider.ThinkingLevels = cleanLevels(provider.ThinkingLevels)
		provider.ThinkingStyle = normalizeThinkingStyle(provider.ThinkingStyle)
		for j := range provider.ModelOverrides {
			override := &provider.ModelOverrides[j]
			override.ID = strings.TrimSpace(override.ID)
			override.Name = strings.TrimSpace(override.Name)
			override.ThinkingLevels = cleanLevels(override.ThinkingLevels)
			override.ThinkingStyle = normalizeThinkingStyle(override.ThinkingStyle)
		}
	}
	for i := range c.Models {
		model := &c.Models[i]
		model.ID = strings.TrimSpace(model.ID)
		model.Name = strings.TrimSpace(model.Name)
		model.BaseURL = strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
		model.ThinkingLevels = cleanLevels(model.ThinkingLevels)
		model.ThinkingStyle = normalizeThinkingStyle(model.ThinkingStyle)
	}
}

func (c *Config) validate() error {
	for i, provider := range c.Providers {
		if provider.BaseURL == "" {
			return fmt.Errorf("providers[%d] (%s) 缺少 base_url", i, provider.Name)
		}
		seen := map[string]struct{}{}
		for _, override := range provider.ModelOverrides {
			if override.ID == "" {
				return fmt.Errorf("provider %s 存在缺少 id 的 model_overrides", provider.ID)
			}
			if _, exists := seen[override.ID]; exists {
				return fmt.Errorf("provider %s 重复配置模型 %s", provider.ID, override.ID)
			}
			seen[override.ID] = struct{}{}
		}
	}
	return nil
}

func makeProviderID(name string, index int) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return fmt.Sprintf("provider-%d", index)
	}
	return id
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

func normalizeThinkingStyle(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	switch style {
	case "", "auto":
		return style
	case "none", "reasoning_effort", "enable_thinking":
		return style
	default:
		return style
	}
}

// DefaultConfigTOML 首次运行自动生成的默认配置模板。
const DefaultConfigTOML = `# Wisp 默认配置（首次运行自动生成，请勿提交到版本库）
# 修改 api_key 等配置后保存，再重新启动服务器。

[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

# 连接多家 OpenAI 兼容 Provider。模型列表会从每家的 /models 自动获取。
[[providers]]
id = "deepseek"
name = "DeepSeek"
base_url = "https://api.deepseek.com/v1"
api_key = "sk-xxx"
default = true
timeout_sec = 120

# 如果 Provider 的 /models 没有暴露思考能力，可按模型覆盖。
# thinking_style 可用：none / reasoning_effort / enable_thinking
# [[providers.model_overrides]]
# id = "your-reasoning-model"
# name = "Reasoning Model"
# thinking_levels = ["off", "low", "medium", "high"]
# thinking_style = "reasoning_effort"

# 第二家 Provider 示例：取消注释即可同时使用。
# [[providers]]
# id = "dashscope"
# name = "DashScope"
# base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
# api_key = "sk-xxx"
# timeout_sec = 120
#
# [[providers.model_overrides]]
# id = "qwen3-max"
# thinking_levels = ["off", "on"]
# thinking_style = "enable_thinking"

# 旧版兼容：如果没有配置 [[providers]]，仍可继续使用 [openai] + [[models]]。
# [openai]
# base_url = "https://api.deepseek.com/v1"
# api_key = "sk-xxx"
# timeout_sec = 120
#
# [[models]]
# id = "deepseek-chat"
# name = "DeepSeek-V3"
# default = true
# thinking_levels = ["off"]
# thinking_style = "none"
`

func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0644)
}
