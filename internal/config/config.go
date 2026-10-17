// 包 config 负责 Wisp 单一配置文件 Config.local.toml 的加载、生成与校验。
//
// 配置文件位于可执行文件同目录，不存在时用默认值生成一份。
// 配置键使用 PascalCase；思考深度直接透传 OpenAI 的 reasoning_effort 字段，
// 本包不做枚举与 provider 私有格式转换。
package config

import (
	"errors"
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
	Server       ServerConfig
	Providers    []ProviderConfig
	ModelAliases []ModelAliasConfig // [[ModelAliases]] 段，新增模型的别名写在这里
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

// ModelAliasConfig 单个模型别名配置（[[ModelAliases]] 段）。
type ModelAliasConfig struct {
	ProviderID string // 模型所属 provider
	ModelID    string // 模型 id
	Alias      string // 显示别名，可中文
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
		if err := WriteAtomic(path, []byte(defaultTOML)); err != nil {
			return nil, fmt.Errorf("config: 生成默认配置失败: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: 读取配置失败: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config: 解析配置失败: %w", err)
	}
	return cfg, nil
}

// WriteAtomic 原子替换配置文件：在目标目录创建临时文件，写入并 fsync 后
// rename。任何一步失败都保留原文件，调用方无需回滚。
func WriteAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("config: 创建临时配置文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("config: 设置临时配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("config: 写入临时配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("config: 同步临时配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: 关闭临时配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: 替换配置失败: %w", err)
	}
	return nil
}

// Error 是带源行号的配置错误；Line 从 1 开始，0 表示无法定位。
type Error struct {
	Line    int
	Message string
}

// Error 实现 error 接口。
func (e *Error) Error() string { return e.Message }

// Parse 解析并校验一段配置文本，不写文件。
// TOML 语法错误与 ProviderID 等校验错误会尽量带上源行号。
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		var parseErr toml.ParseError
		if errors.As(err, &parseErr) {
			message := strings.TrimSpace(parseErr.Message)
			if message == "" {
				message = parseErr.Error()
			}
			return nil, &Error{Line: parseErr.Position.Line, Message: message}
		}
		return nil, &Error{Message: err.Error()}
	}
	if err := cfg.Validate(); err != nil {
		line := 0
		var fieldErr *fieldError
		if errors.As(err, &fieldErr) {
			line = locateLine(string(data), fieldErr)
		}
		return nil, &Error{Line: line, Message: err.Error()}
	}
	return &cfg, nil
}

// fieldError 是 Validate 返回的字段级错误，供 Parse 定位源行号。
type fieldError struct {
	message string
	table   string
	index   int
	field   string
	value   string
}

// Error 实现 error 接口。
func (e *fieldError) Error() string { return e.message }

// locateLine 在配置文本中定位字段错误所在行；找不到时返回 0。
func locateLine(text string, fe *fieldError) int {
	lines := strings.Split(text, "\n")
	switch fe.table {
	case "Providers":
		index := -1
		inside := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[[Providers]]") {
				index++
				inside = index == fe.index
				continue
			}
			if !inside {
				continue
			}
			// [Providers.Discovery] 属于当前块，其他表头结束当前块。
			if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[Providers") {
				inside = false
				continue
			}
			if keyLine(line, fe.field) {
				return i + 1
			}
		}
	case "ModelAliases":
		index := -1
		inside := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[[ModelAliases]]") {
				index++
				inside = index == fe.index
				continue
			}
			if !inside {
				continue
			}
			if strings.HasPrefix(trimmed, "[") {
				inside = false
				continue
			}
			if keyLine(line, fe.field) {
				return i + 1
			}
		}
	case "server":
		inside := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "[server]" {
				inside = true
				continue
			}
			if inside && strings.HasPrefix(trimmed, "[") {
				inside = false
				continue
			}
			if inside && keyLine(line, fe.field) {
				return i + 1
			}
		}
	}
	// 兜底：按错误中出现的取值找第一条匹配行。
	if fe.value != "" {
		needle := `"` + fe.value + `"`
		for i, line := range lines {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
	}
	return 0
}

// keyLine 判断某行是否是 field = ... 形式的键值行。
func keyLine(line, field string) bool {
	if field == "" {
		return false
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, field) {
		return false
	}
	rest := strings.TrimSpace(trimmed[len(field):])
	return strings.HasPrefix(rest, "=")
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate 校验配置并进行规范化（端口默认值、模型去重）。
func (c *Config) Validate() error {
	if c.Server.Port == 0 {
		c.Server.Port = 8787
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return &fieldError{
			message: fmt.Sprintf("config: server.Port 超出范围: %d", c.Server.Port),
			table:   "server",
			field:   "Port",
			value:   fmt.Sprintf("%d", c.Server.Port),
		}
	}
	seenProvider := make(map[string]bool, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if !providerIDPattern.MatchString(p.ProviderID) {
			return &fieldError{
				message: fmt.Sprintf("config: Providers[%d].ProviderID 只允许小写字母数字连字符: %q", i, p.ProviderID),
				table:   "Providers",
				index:   i,
				field:   "ProviderID",
				value:   p.ProviderID,
			}
		}
		if seenProvider[p.ProviderID] {
			return &fieldError{
				message: fmt.Sprintf("config: ProviderID 重复: %q", p.ProviderID),
				table:   "Providers",
				index:   i,
				field:   "ProviderID",
				value:   p.ProviderID,
			}
		}
		seenProvider[p.ProviderID] = true
		// 发现接口必须使用绝对路径；未启用时允许留空。
		if p.Discovery.Path != "" && !strings.HasPrefix(p.Discovery.Path, "/") {
			return &fieldError{
				message: fmt.Sprintf("config: Providers[%d].Discovery.Path 必须以 / 开头: %q", i, p.Discovery.Path),
				table:   "Providers",
				index:   i,
				field:   "Path",
				value:   p.Discovery.Path,
			}
		}
		if p.Discovery.Enabled && p.Discovery.Path == "" {
			return &fieldError{
				message: fmt.Sprintf("config: Providers[%d] 启用 Discovery 时必须设置 Path", i),
				table:   "Providers",
				index:   i,
				field:   "Path",
				value:   "",
			}
		}
		// 模型去重（保留首个），并校验 id 非空且不含 "/"
		seenModel := make(map[string]bool, len(p.Models))
		deduped := p.Models[:0]
		for _, m := range p.Models {
			if m.ModelID == "" || strings.Contains(m.ModelID, "/") {
				return &fieldError{
					message: fmt.Sprintf("config: 模型 id 必须非空且不包含 '/': %q", m.ModelID),
					table:   "Providers",
					index:   i,
					field:   "Models",
					value:   m.ModelID,
				}
			}
			if seenModel[m.ModelID] {
				continue
			}
			seenModel[m.ModelID] = true
			deduped = append(deduped, m)
		}
		p.Models = deduped
	}
	for i, a := range c.ModelAliases {
		if strings.Contains(a.ModelID, "/") || strings.Contains(a.ProviderID, "/") {
			field, value := "ProviderID", a.ProviderID
			if strings.Contains(a.ModelID, "/") {
				field, value = "ModelID", a.ModelID
			}
			return &fieldError{
				message: fmt.Sprintf("config: ModelAliases[%d] 的 id 不应包含 '/': %q/%q", i, a.ProviderID, a.ModelID),
				table:   "ModelAliases",
				index:   i,
				field:   field,
				value:   value,
			}
		}
	}
	return nil
}
