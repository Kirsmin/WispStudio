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
	// BaseURL / APIKey 可省略，省略时沿用 [openai] 的全局配置；
	// 这样不同服务商的模型（如 qwen3-max 走 dashscope）可以各自指定网关。
	BaseURL string `toml:"base_url" json:"base_url,omitempty"`
	APIKey  string `toml:"api_key" json:"-"`
}

type Config struct {
	Server  ServerConfig  `toml:"server"`
	Storage StorageConfig `toml:"storage"`
	OpenAI  OpenAIConfig  `toml:"openai"`
	Models  []ModelConfig `toml:"models"`
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

// DefaultConfigTOML 首次运行自动生成的默认配置模板（占位 key，须由用户自行填写）
const DefaultConfigTOML = `# Wisp 默认配置（首次运行自动生成，请勿提交到版本库）
# 修改 api_key 等配置后保存，再重新启动服务器

[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

# 默认上游服务（OpenAI 兼容接口）
# 模型可在下方单独覆盖 base_url / api_key，未覆盖的沿用这里的默认值
[openai]
base_url = "https://api.deepseek.com/v1"
api_key  = "sk-xxx"      # 替换为你的 API Key
timeout_sec = 120        # 建连与等待首个响应包的超时（不限制整个流式时长）

# 模型列表
[[models]]
id = "deepseek-chat"
name = "DeepSeek-V3"
default = true
thinking_levels = ["off"]
thinking_style = "none"

[[models]]
id = "deepseek-reasoner"
name = "DeepSeek-R1"
thinking_levels = ["off"]
thinking_style = "none"

# 示例：走其他服务商的模型，可单独指定 base_url / api_key
[[models]]
id = "qwen3-max"
name = "Qwen3-Max"
base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "sk-xxx"
thinking_levels = ["off"]
thinking_style = "none"
`

// WriteDefault 生成一份默认配置文件（带占位 API Key）
func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0644)
}
