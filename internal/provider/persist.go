package provider

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"wisp/internal/config"
)

var (
	// providerIDLine 匹配 ProviderID = "..."，捕获缩进与取值。
	providerIDLine = regexp.MustCompile(`^(\s*)ProviderID\s*=\s*"([^"]*)"`)
	// modelsLine 匹配 Models = ...（整行替换）。
	modelsLine = regexp.MustCompile(`^\s*Models\s*=`)
	// tableHeader 匹配 TOML 表头行（[x] 或 [[x]]）。
	tableHeader = regexp.MustCompile(`^\s*\[\[?[^\]]*\]?\]?\s*$`)
)

// AddModel 把模型写回 Config.local.toml 并刷新内存目录：
// 模型 id 追加到对应 provider 的 Models 列表；alias 非空时追加 [[ModelAliases]] 段。
// 写回采用行编辑，只改动 Models 一行与文件末尾的别名段，其余内容与注释保持不变。
func (c *Catalog) AddModel(providerID, modelID, alias string) error {
	if providerID == "" || modelID == "" {
		return fmt.Errorf("provider: providerID 与 modelID 不能为空")
	}
	if strings.ContainsAny(providerID+modelID, "/\"") {
		return fmt.Errorf("provider: id 不合法: %q/%q", providerID, modelID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.findProvider(providerID)
	if err != nil {
		return err
	}
	for _, m := range p.Models {
		if m.ModelID == modelID {
			return fmt.Errorf("provider: 模型已存在于手动列表: %q", Join(providerID, modelID))
		}
	}
	if c.path == "" {
		return fmt.Errorf("provider: 目录未绑定配置文件路径，无法写回")
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return fmt.Errorf("provider: 读取配置失败: %w", err)
	}

	// 1) 更新目标 provider 的 Models 行（保留原有缩进，带别名的条目维持表形式）
	newModels := append(append([]config.ModelConfig{}, p.Models...), config.ModelConfig{ModelID: modelID})
	lines, err := upsertModelsLine(strings.Split(string(data), "\n"), providerID, newModels)
	if err != nil {
		return err
	}

	// 2) 追加 [[ModelAliases]] 段
	if alias != "" {
		q := strconv.Quote
		lines = append(lines, "",
			"[[ModelAliases]]",
			"ProviderID = "+q(providerID),
			"ModelID    = "+q(modelID),
			"Alias      = "+q(alias),
		)
	}

	if err := os.WriteFile(c.path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return fmt.Errorf("provider: 写回配置失败: %w", err)
	}

	// 3) 重新加载内存配置
	var cfg config.Config
	if _, err := toml.DecodeFile(c.path, &cfg); err != nil {
		return fmt.Errorf("provider: 写回后重新解析配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("provider: 写回后配置校验失败: %w", err)
	}
	c.cfg = &cfg
	return nil
}

// upsertModelsLine 在文本行中找到 providerID 对应 [[Providers]] 块的 Models 行并整体替换；
// 块内无 Models 行时插到 ProviderID 行之后。其余行原样保留。
func upsertModelsLine(lines []string, providerID string, models []config.ModelConfig) ([]string, error) {
	inBlock := false      // 当前处于某个 [[Providers]] 块内
	isTarget := false     // 当前块就是目标 provider
	providerIdx := -1     // 目标块的 ProviderID 行号
	replace := func(idx int) []string {
		indent := lines[idx][:len(lines[idx])-len(strings.TrimLeft(lines[idx], " \t"))]
		out := append([]string{}, lines...)
		out[idx] = indent + renderModels(models)
		return out
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[Providers]]") {
			inBlock, isTarget, providerIdx = true, false, -1
			continue
		}
		if !inBlock {
			continue
		}
		// 其他表头结束当前块：[Providers.xxx] 子表仍属块内，其余（含 [[ModelAliases]]）出块
		if tableHeader.MatchString(line) && !strings.HasPrefix(trimmed, "[Providers") {
			if isTarget {
				return insertModelsLine(lines, providerID, models, providerIdx), nil
			}
			inBlock = false
			continue
		}
		if m := providerIDLine.FindStringSubmatch(line); m != nil {
			isTarget = m[2] == providerID
			if isTarget {
				providerIdx = i
			}
			continue
		}
		if isTarget && modelsLine.MatchString(line) {
			return replace(i), nil // 命中目标 Models 行，立即替换返回
		}
	}
	if inBlock && isTarget { // EOF 收尾
		return insertModelsLine(lines, providerID, models, providerIdx), nil
	}
	return nil, fmt.Errorf("provider: 配置中找不到 provider %q 的 [[Providers]] 块", providerID)
}

// insertModelsLine 在 ProviderID 行之后插入 Models 行。
func insertModelsLine(lines []string, providerID string, models []config.ModelConfig, providerIdx int) []string {
	indent := lines[providerIdx][:len(lines[providerIdx])-len(strings.TrimLeft(lines[providerIdx], " \t"))]
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:providerIdx+1]...)
	out = append(out, indent+renderModels(models))
	out = append(out, lines[providerIdx+1:]...)
	return out
}

// renderModels 把模型列表渲染为 TOML 数组字面量；带别名的条目用表形式以保留别名。
func renderModels(models []config.ModelConfig) string {
	parts := make([]string, 0, len(models))
	for _, m := range models {
		if m.ModelAlias == "" {
			parts = append(parts, strconv.Quote(m.ModelID))
		} else {
			parts = append(parts, "{ ModelID = "+strconv.Quote(m.ModelID)+
				", ModelAlias = "+strconv.Quote(m.ModelAlias)+" }")
		}
	}
	return "Models = [" + strings.Join(parts, ", ") + "]"
}
