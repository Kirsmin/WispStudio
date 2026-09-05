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

type StorageConfig struct {
	DataDir string
}

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
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
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
			currentModel, currentProvider, currentOverride = nil, nil, nil
			continue
		case "[[models]]":
			cfg.Models = append(cfg.Models, ModelConfig{})
			currentModel = &cfg.Models[len(cfg.Models)-1]
			currentProvider, currentOverride = nil, nil
			section = "model"
			continue
		case "[[providers]]":
			cfg.Providers = append(cfg.Providers, ProviderConfig{})
			currentProvider = &cfg.Providers[len(cfg.Providers)-1]
			currentModel, currentOverride = nil, nil
			section = "provider"
			continue
		case "[[providers.model_overrides]]":
			if currentProvider == nil {
				return nil, fmt.Errorf("第 %d 行: model_overrides 必须位于对应 [[providers]] 之后", lineNo)
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
		if err := assign(cfg, section, currentModel, currentProvider, currentOverride, strings.TrimSpace(key), strings.TrimSpace(raw)); err != nil {
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
	if cfg.OpenAI.TimeoutSec <= 0 {
		cfg.OpenAI.TimeoutSec = 120
	}
	return cfg, nil
}

func assign(cfg *Config, section string, model *ModelConfig, provider *ProviderConfig, override *ModelOverrideConfig, key, raw string) error {
	str := func() (string, error) { return parseString(raw) }
	boolean := func() (bool, error) { return strconv.ParseBool(raw) }
	integer := func() (int, error) { return strconv.Atoi(raw) }
	array := func() ([]string, error) { return parseStringArray(raw) }

	switch section {
	case "server":
		switch key {
		case "host":
			v, e := str()
			cfg.Server.Host = v
			return e
		case "port":
			v, e := integer()
			cfg.Server.Port = v
			return e
		}
	case "storage":
		if key == "data_dir" {
			v, e := str()
			cfg.Storage.DataDir = v
			return e
		}
	case "openai":
		switch key {
		case "base_url":
			v, e := str()
			cfg.OpenAI.BaseURL = v
			return e
		case "api_key":
			v, e := str()
			cfg.OpenAI.APIKey = v
			return e
		case "timeout_sec":
			v, e := integer()
			cfg.OpenAI.TimeoutSec = v
			return e
		}
	case "model":
		if model == nil {
			return fmt.Errorf("models 配置上下文无效")
		}
		switch key {
		case "id":
			v, e := str()
			model.ID = v
			return e
		case "name":
			v, e := str()
			model.Name = v
			return e
		case "default":
			v, e := boolean()
			model.Default = v
			return e
		case "thinking_levels":
			v, e := array()
			model.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := str()
			model.ThinkingStyle = v
			return e
		case "base_url":
			v, e := str()
			model.BaseURL = v
			return e
		case "api_key":
			v, e := str()
			model.APIKey = v
			return e
		}
	case "provider":
		if provider == nil {
			return fmt.Errorf("providers 配置上下文无效")
		}
		switch key {
		case "name":
			v, e := str()
			provider.Name = v
			return e
		case "base_url":
			v, e := str()
			provider.BaseURL = v
			return e
		case "api_key":
			v, e := str()
			provider.APIKey = v
			return e
		case "default":
			v, e := boolean()
			provider.Default = v
			return e
		case "thinking_levels":
			v, e := array()
			provider.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := str()
			provider.ThinkingStyle = v
			return e
		}
	case "override":
		if override == nil {
			return fmt.Errorf("model_overrides 配置上下文无效")
		}
		switch key {
		case "id":
			v, e := str()
			override.ID = v
			return e
		case "name":
			v, e := str()
			override.Name = v
			return e
		case "default":
			v, e := boolean()
			override.Default = v
			return e
		case "thinking_levels":
			v, e := array()
			override.ThinkingLevels = v
			return e
		case "thinking_style":
			v, e := str()
			override.ThinkingStyle = v
			return e
		}
	}
	// 未知键保持向前兼容，不阻止程序启动。
	return nil
}

func stripComment(line string) string {
	quoted := false
	escaped := false
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
	if raw == "" {
		return "", nil
	}
	if raw[0] == '"' {
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

	var parts []string
	start := 0
	quoted := false
	escaped := false
	for i, r := range body {
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
		if r == ',' && !quoted {
			parts = append(parts, strings.TrimSpace(body[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(body[start:]))

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		v, err := parseString(part)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

const DefaultConfigTOML = `# WispStudio 配置
[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"

[openai]
# 旧版 [[models]] 没写 base_url/api_key 时的默认值
base_url = "https://api.openai.com/v1"
api_key = "sk-xxx"
timeout_sec = 120

# 推荐：Provider 自动拉取 /models
[[providers]]
name = "My Provider"
base_url = "https://api.example.com/v1"
api_key = "sk-xxx"
default = true

# 如果 Provider 的 /models 不提供思考能力元数据，可在这里统一指定：
# thinking_levels = ["off", "low", "medium", "high"]
# thinking_style = "reasoning_effort"
# thinking_style 可选: none / reasoning_effort / enable_thinking / thinking_object

# 也可只覆盖某个模型：
# [[providers.model_overrides]]
# id = "gpt-5"
# thinking_levels = ["off", "minimal", "low", "medium", "high"]
# thinking_style = "reasoning_effort"

# 旧版手工模型配置仍兼容：
# [[models]]
# id = "deepseek-chat"
# name = "DeepSeek Chat"
# default = true
# thinking_levels = ["off"]
# thinking_style = "none"
`

func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0o644)
}
