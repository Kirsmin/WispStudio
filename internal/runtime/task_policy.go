package runtime

import "strings"

// TaskPolicy 使用确定性规则，在模型消耗 Token 调查工作区之前先决定 Runtime 暴露多少编排能力。
type TaskPolicy struct {
	Complexity          string `json:"complexity"`
	AllowReconnaissance bool   `json:"allow_reconnaissance"`
}

// ClassifyTask 为小型独立任务提供保守的快速路径；模型仍可保存更完整的计划，但不能为明显无需理解仓库的任务启动 Explorer。
func ClassifyTask(text string) TaskPolicy {
	value := strings.ToLower(strings.TrimSpace(text))
	if value == "" {
		return TaskPolicy{Complexity: "standard", AllowReconnaissance: true}
	}

	complexMarkers := []string{
		"全面", "重构", "架构", "状态机", "底层", "跨模块", "多模块", "迁移", "migration",
		"refactor", "architecture", "state machine", "debug api", "性能优化", "兼容", "审查源码",
		"review the codebase", "whole project", "entire project",
	}
	for _, marker := range complexMarkers {
		if strings.Contains(value, marker) {
			return TaskPolicy{Complexity: "complex", AllowReconnaissance: true}
		}
	}

	projectMarkers := []string{
		"现有", "已有", "这个项目", "项目里", "源码", "仓库", "代码库", "修复", "bug", "接口", "api",
		"前端", "后端", "组件", "函数", "类", "模块", "existing", "repository", "codebase", "fix ",
	}
	for _, marker := range projectMarkers {
		if strings.Contains(value, marker) {
			return TaskPolicy{Complexity: "standard", AllowReconnaissance: true}
		}
	}

	greenfieldMarkers := []string{
		"写一个", "创建一个", "新建一个", "生成一个", "做一个", "用python", "用 python", "脚本", "单文件",
		"write a", "create a", "generate a", "python script", "single file",
	}
	for _, marker := range greenfieldMarkers {
		if strings.Contains(value, marker) {
			return TaskPolicy{Complexity: "trivial", AllowReconnaissance: false}
		}
	}

	if len([]rune(value)) <= 80 {
		return TaskPolicy{Complexity: "standard", AllowReconnaissance: false}
	}
	return TaskPolicy{Complexity: "standard", AllowReconnaissance: true}
}

func effectiveProfile(profile AgentProfile, complexity string, allowReconnaissance bool) AgentProfile {
	out := profile
	out.Tools = append([]string(nil), profile.Tools...)
	if profile.ID == "plan" {
		allowed := map[string]bool{"new_plan": true, "edit_plan": true}
		if allowReconnaissance {
			allowed["list_files"] = true
			allowed["read_file"] = true
			allowed["search_text"] = true
		}
		if complexity == "complex" && allowReconnaissance {
			allowed["spawn_explorer"] = true
		}
		out.Tools = filterTools(out.Tools, allowed)
	}
	if profile.ID == "build" {
		allowed := map[string]bool{"write_file": true, "run_command": true}
		// 轻量新建任务走真实快速路径：写入/运行前不能先消耗一轮模型做泛化仓库调查；标准和复杂任务保留定向只读工具。
		if complexity != "trivial" {
			allowed["list_files"] = true
			allowed["read_file"] = true
			allowed["search_text"] = true
		}
		if complexity == "complex" {
			allowed["done_phase"] = true
		}
		out.Tools = filterTools(out.Tools, allowed)
	}
	return out
}

func filterTools(tools []string, allowed map[string]bool) []string {
	out := make([]string, 0, len(tools))
	for _, name := range tools {
		if allowed[name] {
			out = append(out, name)
		}
	}
	return out
}
