package config

import (
	"fmt"
	"os"

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

type ModelConfig struct {
	ID             string   `toml:"id"`
	Name           string   `toml:"name"`
	Default        bool     `toml:"default"`
	ThinkingLevels []string `toml:"thinking_levels"`
	ThinkingStyle  string   `toml:"thinking_style"`
	BaseURL        string   `toml:"base_url"`
	APIKey         string   `toml:"api_key"`
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
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "127.0.0.1"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 7860
	}
	if cfg.Storage.DataDir == "" {
		cfg.Storage.DataDir = "Data"
	}
	if cfg.OpenAI.TimeoutSec <= 0 {
		cfg.OpenAI.TimeoutSec = 120
	}
	return &cfg, nil
}

const DefaultConfigTOML = `# Wisp 本地配置
[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

[openai]
timeout_sec = 120

# 可配置多家 Provider；模型会从每家的 /models 自动拉取。
[[providers]]
id = "provider-a"
name = "Provider A"
base_url = "https://api.example.com/v1"
api_key = "sk-xxx"
default = true
# 如果该 Provider 的模型支持统一思考深度，可直接设置：
# thinking_levels = ["off", "low", "medium", "high"]
# thinking_style = "reasoning_effort"

# 第二家 Provider 示例：
# [[providers]]
# id = "provider-b"
# name = "Provider B"
# base_url = "https://another.example.com/v1"
# api_key = "sk-xxx"

# 可按模型覆盖思考能力：
# [[providers.model_overrides]]
# id = "gpt-5"
# thinking_levels = ["off", "low", "medium", "high"]
# thinking_style = "reasoning_effort"
`

func WriteDefault(path string) error { return os.WriteFile(path, []byte(DefaultConfigTOML), 0o644) }
