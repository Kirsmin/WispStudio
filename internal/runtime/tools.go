package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"wisp/internal/provider"
)

// ToolResult 是工具执行的统一结果结构（序列化为 tool.returned 正文）。
type ToolResult struct {
	Status   string         `json:"status"` // success | failure | cancelled
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Stdout   string         `json:"stdout,omitempty"`
	Stderr   string         `json:"stderr,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
}

// ToolSpec 是一个内置工具的定义与执行体。
type ToolSpec struct {
	Definition    provider.ToolDefinition
	NeedsApproval bool // 执行前是否需要用户审批（写 approval.requested 挂起）
	Execute       func(ctx context.Context, raw json.RawMessage) ToolResult
}

// Registry 是内置工具注册表；同时持有工作区根供 securePath 使用。
type Registry struct {
	workspaceRoot string
	workspaceReal string
	tools         map[string]ToolSpec
}

// NewRegistry 构造工具注册表并注册全部内置工具。
// workspaceRoot 为空时用当前目录；文件操作一律限制在其内。
func NewRegistry(workspaceRoot string) (*Registry, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("runtime: 定位工作区失败: %w", err)
		}
		workspaceRoot = wd
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("runtime: 解析工作区路径失败: %w", err)
	}
	realRoot := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = resolved
	}
	reg := &Registry{
		workspaceRoot: root,
		workspaceReal: realRoot,
		tools:         map[string]ToolSpec{},
	}
	reg.registerBuiltins()
	return reg, nil
}

// WorkspaceRoot 返回工作区根（绝对路径）。
func (r *Registry) WorkspaceRoot() string { return r.workspaceRoot }

func (r *Registry) add(spec ToolSpec) { r.tools[spec.Definition.Function.Name] = spec }

// Definitions 返回全部内置工具的定义（按名排序），发给 provider。
func (r *Registry) Definitions() []provider.ToolDefinition {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]provider.ToolDefinition, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name].Definition)
	}
	return out
}

// NeedsApproval 判断工具执行前是否需要用户审批。
func (r *Registry) NeedsApproval(name string) bool {
	spec, ok := r.tools[name]
	return ok && spec.NeedsApproval
}

// Execute 执行工具；未知工具返回 failure 结果（不中断回合）。
func (r *Registry) Execute(ctx context.Context, name string, raw []byte) (ToolResult, error) {
	spec, ok := r.tools[name]
	if !ok {
		return ToolResult{Status: "failure", Error: "未知工具: " + name}, nil
	}
	return spec.Execute(ctx, json.RawMessage(raw)), nil
}

// registerBuiltins 注册五个内置工具；schema 与旧实现保持一致。
func (r *Registry) registerBuiltins() {
	r.add(ToolSpec{
		Definition: toolDef("read_file", "读取 UTF-8 文本文件，可指定行范围。只读。",
			objectSchema(`"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}`, "path")),
		Execute: r.readFile,
	})
	r.add(ToolSpec{
		Definition: toolDef("write_file", "写入完整文件内容。会产生工作区副作用，执行前需要用户批准。",
			objectSchema(`"path":{"type":"string"},"content":{"type":"string"}`, "path", "content")),
		NeedsApproval: true,
		Execute:       r.writeFile,
	})
	r.add(ToolSpec{
		Definition: toolDef("list_files", "列出工作区中的文件。只读。",
			objectSchema(`"path":{"type":"string","description":"相对工作区路径，默认 ."},"max_entries":{"type":"integer","minimum":1,"maximum":1000}`)),
		Execute: r.listFiles,
	})
	r.add(ToolSpec{
		Definition: toolDef("search_text", "在工作区文本文件中搜索字符串。只读。",
			objectSchema(`"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer","minimum":1,"maximum":200}`, "query")),
		Execute: r.searchText,
	})
	r.add(ToolSpec{
		Definition: toolDef("run_command", "在工作区执行 shell 命令。可能产生副作用，执行前需要用户批准。",
			objectSchema(`"command":{"type":"string"},"cwd":{"type":"string"}`, "command")),
		NeedsApproval: true,
		Execute:       r.runCommand,
	})
}

// objectSchema 构造 OpenAI function 参数 schema；required 为 nil 时输出 []。
func objectSchema(properties string, required ...string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	req, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":` + string(req) + `,"additionalProperties":false}`)
}

func toolDef(name, description string, schema json.RawMessage) provider.ToolDefinition {
	return provider.ToolDefinition{
		Type:     "function",
		Function: provider.ToolDefinitionBody{Name: name, Description: description, Parameters: schema},
	}
}

// securePath 把用户给出的工作区相对路径解析为绝对路径：
// 词法限制在 workspaceRoot 内，并对已存在路径解析符号链接防 ../ 逃逸。
func (r *Registry) securePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("只允许工作区相对路径")
	}
	abs, err := filepath.Abs(filepath.Join(r.workspaceRoot, filepath.Clean(path)))
	if err != nil {
		return "", err
	}
	if !pathWithin(r.workspaceRoot, abs) {
		return "", fmt.Errorf("路径越出工作区")
	}
	// 词法 containment 不够：工作区内的 symlink 仍可能指向外部。
	// 对已存在目标解析真实路径；目标尚不存在时解析最近的已存在父目录。
	probe := abs
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			realPath, evalErr := filepath.EvalSymlinks(probe)
			if evalErr != nil {
				return "", evalErr
			}
			if !pathWithin(r.workspaceReal, realPath) {
				return "", fmt.Errorf("路径通过符号链接越出工作区")
			}
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("无法解析工作区路径")
		}
		probe = parent
	}
	return abs, nil
}

// pathWithin 判断 candidate 是否在 root 内（含 root 本身）。
func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func decodeArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("工具参数解析失败: %w", err)
	}
	return nil
}

func failure(err error) ToolResult { return ToolResult{Status: "failure", Error: err.Error()} }

// skipWorkspaceNoiseDir 跳过无意义的目录。
func skipWorkspaceNoiseDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".gocache", ".gopath", ".npm-cache", "dist", "build", "Data", ".venv", "__pycache__", ".next", ".cache", "coverage":
		return true
	default:
		return false
	}
}

// readFile 读取 UTF-8 文本文件的指定行范围（默认最多 400 行）。
func (r *Registry) readFile(_ context.Context, raw json.RawMessage) ToolResult {
	var args struct {
		Path  string `json:"path"`
		Start int    `json:"start_line"`
		End   int    `json:"end_line"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	path, err := r.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	file, err := os.Open(path)
	if err != nil {
		return failure(err)
	}
	defer file.Close()
	prefix := make([]byte, 4096)
	n, _ := file.Read(prefix)
	if !utf8.Valid(prefix[:n]) || strings.ContainsRune(string(prefix[:n]), '\x00') {
		return failure(fmt.Errorf("文件不是 UTF-8 文本；拒绝作为文本读取"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return failure(err)
	}
	if args.Start <= 0 {
		args.Start = 1
	}
	if args.End <= 0 || args.End < args.Start || args.End-args.Start >= 400 {
		args.End = args.Start + 399
	}
	var b strings.Builder
	scan := bufio.NewScanner(io.LimitReader(file, 4*1024*1024))
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scan.Scan() {
		line++
		if line < args.Start {
			continue
		}
		if line > args.End {
			break
		}
		fmt.Fprintf(&b, "%d\t%s\n", line, scan.Text())
	}
	if err := scan.Err(); err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: b.String(), Data: map[string]any{
		"path": filepath.ToSlash(args.Path), "start_line": args.Start, "end_line": minInt(line, args.End),
	}}
}

// writeFile 写入完整文件内容（需审批）；返回前后 SHA-256 便于核对。
func (r *Registry) writeFile(_ context.Context, raw json.RawMessage) ToolResult {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	path, err := r.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	before, beforeErr := os.ReadFile(path)
	created := os.IsNotExist(beforeErr)
	if beforeErr != nil && !created {
		return failure(beforeErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return failure(err)
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: "文件已写入", Data: map[string]any{
		"path": filepath.ToSlash(args.Path), "bytes": len(args.Content), "created": created,
		"before_sha256": shaHex(before), "after_sha256": shaHex([]byte(args.Content)),
	}}
}

// listFiles 递归列出工作区文件（跳过噪声目录，最多 max_entries 条）。
func (r *Registry) listFiles(_ context.Context, raw json.RawMessage) ToolResult {
	var args struct {
		Path string `json:"path"`
		Max  int    `json:"max_entries"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	if args.Max <= 0 {
		args.Max = 200
	}
	root, err := r.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	var items []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path != root && d.IsDir() && skipWorkspaceNoiseDir(d.Name()) {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(r.workspaceRoot, path)
		if d.IsDir() {
			rel += "/"
		}
		items = append(items, filepath.ToSlash(rel))
		if len(items) >= args.Max {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: strings.Join(items, "\n"), Data: map[string]any{"count": len(items)}}
}

// searchText 在工作区文本文件中搜索字符串（跳过噪声目录与二进制文件）。
func (r *Registry) searchText(_ context.Context, raw json.RawMessage) ToolResult {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
		Max   int    `json:"max_results"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	if args.Query == "" {
		return failure(fmt.Errorf("query 不能为空"))
	}
	if args.Max <= 0 {
		args.Max = 50
	}
	root, err := r.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	var results []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipWorkspaceNoiseDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= args.Max {
			return io.EOF
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2*1024*1024 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		probe := make([]byte, 4096)
		n, _ := file.Read(probe)
		if !utf8.Valid(probe[:n]) || strings.ContainsRune(string(probe[:n]), '\x00') {
			_ = file.Close()
			return nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil
		}
		scan := bufio.NewScanner(file)
		scan.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for scan.Scan() {
			line++
			if strings.Contains(scan.Text(), args.Query) {
				rel, _ := filepath.Rel(r.workspaceRoot, path)
				results = append(results, fmt.Sprintf("%s:%d: %.300s", filepath.ToSlash(rel), line, scan.Text()))
				if len(results) >= args.Max {
					break
				}
			}
		}
		_ = file.Close()
		return nil
	})
	return ToolResult{Status: "success", Output: strings.Join(results, "\n"), Data: map[string]any{"count": len(results)}}
}

// runCommand 在工作区执行 shell 命令（需审批）；输出截断到 128 KiB。
func (r *Registry) runCommand(ctx context.Context, raw json.RawMessage) ToolResult {
	var args struct {
		Command string `json:"command"`
		CWD     string `json:"cwd"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	cwd, err := r.securePath(args.CWD)
	if err != nil {
		return failure(err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-lc", args.Command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{W: &stdout, N: 128 * 1024}
	cmd.Stderr = &limitedWriter{W: &stderr, N: 128 * 1024}
	err = cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if ctx.Err() != nil {
			return ToolResult{Status: "cancelled", Error: ctx.Err().Error()}
		} else {
			code = -1
		}
	}
	status := "success"
	errText := ""
	if err != nil {
		status = "failure"
		errText = err.Error()
	}
	return ToolResult{
		Status: status, Stdout: stdout.String(), Stderr: stderr.String(),
		ExitCode: &code, Error: errText,
		Data: map[string]any{"command": args.Command, "cwd": filepath.ToSlash(args.CWD)},
	}
}

// limitedWriter 把输出截断到 N 字节（写入计数仍按原始长度返回）。
type limitedWriter struct {
	W io.Writer
	N int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.N <= 0 {
		return original, nil
	}
	if len(p) > w.N {
		p = p[:w.N]
	}
	n, err := w.W.Write(p)
	w.N -= n
	if err != nil {
		return n, err
	}
	return original, nil
}

// shaHex 计算内容的 SHA-256 十六进制摘要；空内容返回空串。
func shaHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
