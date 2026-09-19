package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const DefaultSystemPrompt = `你是 Wisp，目标是以最少的无效步骤可靠完成用户的实际任务。
语言：面向用户的回答使用用户当前的语言；没有新信息时不要为工具动作逐条旁白。

指令层次：Runtime 安全边界不可覆盖；用户最新的明确要求优先于项目指令和当前 Plan；当前有效 Plan 是工作基线；原始要求仅供归档。若新要求与 Plan 冲突，按新要求工作，并在需要时更新 Plan，不要反复质疑已确认的决定。
低风险且可逆的格式、路径、命名等细节自行采用合理默认值；仅在重大开销、不可逆操作、权限或核心产物有真实歧义时，集中询问一个关键问题。不要让清单式确认阻塞任务。

先判断是否需要现有仓库知识：独立脚本、独立新文件等任务不做 list_files/read_file/search_text/Explorer 调研；项目指令如已在上下文注入，不再重复读取 AGENTS.md。调查前必须能说明哪项实现依赖被调查的事实；优先有针对性地读取相关文件。
只在必要时调用工具，不创建仪式化 Phase；真实副作用以 Tool Result 为准，完成前明确区分“已执行”和“未验证”。
工具可以一次提出最多四个相互独立的只读调用。写入与执行命令需要按依赖顺序单独确认/执行，永远不要臆造工具结果。`

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type StorageConfig struct {
	DataDir string `toml:"data_dir"`
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
	SystemPrompt string           `toml:"system_prompt"`
	Server       ServerConfig     `toml:"server"`
	Storage      StorageConfig    `toml:"storage"`
	Providers    []ProviderConfig `toml:"providers"`
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
	if strings.TrimSpace(c.SystemPrompt) == "" {
		c.SystemPrompt = DefaultSystemPrompt
	}
	if strings.TrimSpace(c.Server.Host) == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 7860
	}
	if strings.TrimSpace(c.Storage.DataDir) == "" {
		c.Storage.DataDir = "Data"
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
			provider.TimeoutSec = 120
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

}

func (c *Config) validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("至少配置一个 [[providers]]；旧版 [openai]/[[models]] 已移除")
	}
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

# 不设置 system_prompt 即使用 Wisp 任务型默认提示词；配置后将作为核心指令。

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


`

func WriteDefault(path string) error {
	return os.WriteFile(path, []byte(DefaultConfigTOML), 0644)
}
