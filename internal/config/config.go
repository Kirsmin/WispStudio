package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ServerConfig struct {
	Host string
	Port int
}
type StorageConfig struct{ DataDir string }
type OpenAIConfig struct {
	BaseURL    string
	APIKey     string
	TimeoutSec int
}
type ModelConfig struct {
	ID             string
	Name           string
	Default        bool
	ThinkingLevels []string
	ThinkingStyle  string
	BaseURL        string
	APIKey         string
}
type ModelOverrideConfig struct {
	ID             string
	Name           string
	Default        bool
	ThinkingLevels []string
	ThinkingStyle  string
}
type ProviderConfig struct {
	Name           string
	BaseURL        string
	APIKey         string
	Default        bool
	ThinkingLevels []string
	ThinkingStyle  string
	ModelOverrides []ModelOverrideConfig
}
type Config struct {
	Server    ServerConfig
	Storage   StorageConfig
	OpenAI    OpenAIConfig
	Models    []ModelConfig
	Providers []ProviderConfig
}

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	defer file.Close()
	cfg := &Config{}
	section := ""
	var currentModel *ModelConfig
	var currentProvider *ProviderConfig
	var currentOverride *ModelOverrideConfig
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}
		switch line {
		case "[server]", "[storage]", "[openai]":
			section = strings.Trim(line, "[]")
			currentModel = nil
			currentProvider = nil
			currentOverride = nil
			continue
		case "[[models]]":
			cfg.Models = append(cfg.Models, ModelConfig{})
			currentModel = &cfg.Models[len(cfg.Models)-1]
			currentProvider = nil
			currentOverride = nil
			section = "model"
			continue
		case "[[providers]]":
			cfg.Providers = append(cfg.Providers, ProviderConfig{})
			currentProvider = &cfg.Providers[len(cfg.Providers)-1]
			currentModel = nil
			currentOverride = nil
			section = "provider"
			continue
		case "[[providers.model_overrides]]":
			if currentProvider == nil {
				return nil, fmt.Errorf("第 %d 行: model_overrides 必须位于某个 [[providers]] 之后", lineNo)
			}
			currentProvider.ModelOverrides = append(currentProvider.ModelOverrides, ModelOverrideConfig{})
			currentOverride = &currentProvider.ModelOverrides[len(currentProvider.ModelOverrides)-1]
			section = "override"
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("第 %d 行: 无法解析配置项", lineNo)
		}
		key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
		if err := assign(cfg, section, currentModel, currentProvider, currentOverride, key, raw); err != nil {
			return nil, fmt.Errorf("第 %d 行: %w", lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
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
	if cfg.OpenAI.TimeoutSec == 0 {
		cfg.OpenAI.TimeoutSec = 120
	}
	return cfg, nil
}

func assign(cfg *Config, section string, model *ModelConfig, provider *ProviderConfig, override *ModelOverrideConfig, key, raw string) error {
	stringValue := func() (string, error) { return parseString(raw) }
	boolValue := func() (bool, error) { return strconv.ParseBool(raw) }
	intValue := func() (int, error) { return strconv.Atoi(raw) }
	arrayValue := func() ([]string, error) { return parseStringArray(raw) }
	switch section {
	case "server":
		switch key {
		case "host":
			v, e := stringValue()
			cfg.Server.Host = v
			return e
		case "port":
			v, e := intValue()
			cfg.Server.Port = v
			return e
		}
	case "storage":
		if key == "data_dir" {
			v, e := stringValue()
			cfg.Storage.DataDir = v
			return e
		}
	case "openai":
		switch key {
		case "base_url":
			v, e := stringValue()
			cfg.OpenAI.BaseURL = v
			return e
		case "api_key":
			v, e := stringValue()
			cfg.OpenAI.APIKey = v
			return e
		case "timeout_sec":
			v, e := intValue()
			cfg.OpenAI.TimeoutSec = v
			return e
		}
	case "model":
		if model == nil {
			return fmt.Errorf("models 配置上下文无效")
		}
		switch key {
		case "id":
			v, e := stringValue()
			model.ID = v
			return e
		case "name":
			v, e := stringValue()
			model.Name = v
			return e
		case "default":
			v, e := boolValue()
			model.Default = v
			return e
		case "thinking_levels":
			v, e := arrayValue()
			model.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := stringValue()
			model.ThinkingStyle = v
			return e
		case "base_url":
			v, e := stringValue()
			model.BaseURL = v
			return e
		case "api_key":
			v, e := stringValue()
			model.APIKey = v
			return e
		}
	case "provider":
		if provider == nil {
			return fmt.Errorf("providers 配置上下文无效")
		}
		switch key {
		case "name":
			v, e := stringValue()
			provider.Name = v
			return e
		case "base_url":
			v, e := stringValue()
			provider.BaseURL = v
			return e
		case "api_key":
			v, e := stringValue()
			provider.APIKey = v
			return e
		case "default":
			v, e := boolValue()
			provider.Default = v
			return e
		case "thinking_levels":
			v, e := arrayValue()
			provider.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := stringValue()
			provider.ThinkingStyle = v
			return e
		}
	case "override":
		if override == nil {
			return fmt.Errorf("model_overrides 配置上下文无效")
		}
		switch key {
		case "id":
			v, e := stringValue()
			override.ID = v
			return e
		case "name":
			v, e := stringValue()
			override.Name = v
			return e
		case "default":
			v, e := boolValue()
			override.Default = v
			return e
		case "thinking_levels":
			v, e := arrayValue()
			override.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := stringValue()
			override.ThinkingStyle = v
			return e
		}
	}
	// 向前兼容：忽略未知键，而不是因为新版本配置项导致旧程序无法启动。
	return nil
}

func stripComment(line string) string {
	quoted, escaped := false, false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quoted {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if r == '#' && !quoted {
			return line[:i]
		}
	}
	return line
}
func parseString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' {
		return strconv.Unquote(raw)
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	return raw, nil
}
func parseStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, fmt.Errorf("字符串数组格式错误")
	}
	body := strings.TrimSpace(raw[1 : len(raw)-1])
	if body == "" {
		return []string{}, nil
	}
	parts := strings.Split(body, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		value, err := parseString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

const DefaultConfigTOML = `# Wisp 默认配置（首次运行自动生成，请勿提交到版本库）
[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

[openai]
base_url = "https://api.deepseek.com/v1"
api_key = "sk-xxx"
timeout_sec = 120

[[providers]]
name = "DeepSeek"
base_url = "https://api.deepseek.com/v1"
api_key = "sk-xxx"
default = true

# 如自动识别的思考档位不符合服务商，可覆盖：
# [[providers.model_overrides]]
# id = "your-reasoning-model"
# thinking_levels = ["off", "low", "medium", "high"]
# thinking_style = "reasoning_effort"
`

func WriteDefault(path string) error { return os.WriteFile(path, []byte(DefaultConfigTOML), 0o644) }
