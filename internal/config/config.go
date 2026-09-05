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
	BaseURL        string   `toml:"base_url" json:"base_url,omitempty"`
	APIKey         string   `toml:"api_key" json:"-"`
}

// ProviderConfig 服务商配置：后端会从每个 Provider 的 /models 端点拉取模型列表
type ProviderConfig struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
	Default bool   `toml:"default"`
}

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Storage   StorageConfig    `toml:"storage"`
	OpenAI    OpenAIConfig     `toml:"openai"`
	Models    []ModelConfig    `toml:"models"`    // 保留向后兼容
	Providers []ProviderConfig `toml:"providers"` // 新增：从 Provider 自动拉取模型
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
	return &cfg, nil
}

const DefaultConfigTOML = `# Wisp 默认配置（首次运行自动生成，请勿提交到版本库）

[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

[openai]
base_url = "https://api.deepseek.com/v1"
api_key  = "sk-xxx"
timeout_sec = 120

# 服务商列表：Wisp 会从每个 Provider 的 /models 端点自动获取可用模型
[[providers]]
name = "DeepSeek"
base_url = "https://api.deepseek.com/v1"
api_key = "sk-xxx"
default = true
`

func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0644)
}
