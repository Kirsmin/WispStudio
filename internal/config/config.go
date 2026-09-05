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
	BaseURL          string `toml:"base_url"`
	APIKey           string `toml:"api_key"`
	TimeoutSec       int    `toml:"timeout_sec"`
	MaxGenerationSec int    `toml:"max_generation_sec"`
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
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
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
	if cfg.OpenAI.MaxGenerationSec <= 0 {
		cfg.OpenAI.MaxGenerationSec = 900
	}
}

func validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port 必须在 1-65535 之间")
	}
	if len(cfg.Models) == 0 && len(cfg.Providers) == 0 {
		return fmt.Errorf("至少配置一个 [[models]] 或 [[providers]]")
	}
	return nil
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
# 浏览器刷新/切换会话时，后端仍可继续生成；这里限制单次后台生成的最长时间。
max_generation_sec = 900

# 推荐：服务商列表。Wisp 会从每个 Provider 的 /models 获取模型。
[[providers]]
name = "DeepSeek"
base_url = "https://api.deepseek.com/v1"
api_key = "sk-xxx"
default = true

# 也可使用静态模型配置覆盖推理参数，例如：
# [[models]]
# id = "o3-mini"
# name = "o3-mini"
# default = true
# thinking_levels = ["low", "medium", "high"]
# thinking_style = "reasoning_effort"
`

func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0644)
}
