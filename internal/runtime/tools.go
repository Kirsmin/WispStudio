package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"wisp/internal/openai"
	"wisp/internal/store"
)

type ToolResult struct {
	Status   string         `json:"status"`
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Stdout   string         `json:"stdout,omitempty"`
	Stderr   string         `json:"stderr,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolContext struct {
	Context    context.Context
	SessionID  string
	TurnID     string
	AgentRunID string
}

type ToolSpec struct {
	Definition openai.ToolDefinition
	Risk       string
	Execute    func(ToolContext, json.RawMessage) ToolResult
}

type ToolRuntime struct {
	store         *store.Store
	workspaceRoot string
	workspaceReal string
	tools         map[string]ToolSpec
}

func NewToolRuntime(st *store.Store, workspaceRoot string) *ToolRuntime {
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot, _ = os.Getwd()
	}
	root, _ := filepath.Abs(workspaceRoot)
	realRoot := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = resolved
	}
	t := &ToolRuntime{store: st, workspaceRoot: root, workspaceReal: realRoot, tools: map[string]ToolSpec{}}
	t.registerBuiltins()
	return t
}

func objectSchema(properties string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":` + string(req) + `,"additionalProperties":false}`)
}
func toolDef(name, description string, schema json.RawMessage) openai.ToolDefinition {
	return openai.ToolDefinition{Type: "function", Function: openai.ToolDefinitionBody{Name: name, Description: description, Parameters: schema}}
}
func (t *ToolRuntime) add(spec ToolSpec) { t.tools[spec.Definition.Function.Name] = spec }

func (t *ToolRuntime) Definitions(names []string) []openai.ToolDefinition {
	out := make([]openai.ToolDefinition, 0, len(names))
	for _, name := range names {
		if spec, ok := t.tools[name]; ok {
			out = append(out, spec.Definition)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Function.Name < out[j].Function.Name })
	return out
}
func (t *ToolRuntime) Execute(ctx ToolContext, call ToolCall) ToolResult {
	spec, ok := t.tools[call.Name]
	if !ok {
		return ToolResult{Status: "failure", Error: "未知 Tool: " + call.Name}
	}
	return spec.Execute(ctx, call.Arguments)
}
func (t *ToolRuntime) Risk(name string) string {
	if spec, ok := t.tools[name]; ok {
		return spec.Risk
	}
	return "未知工具"
}

func (t *ToolRuntime) registerBuiltins() {
	t.add(ToolSpec{Definition: toolDef("spawn_explorer", "启动一个同步只读 Explorer SubAgent，并只把结构化结果摘要返回父 Agent。", objectSchema(`"query":{"type":"string"}`, "query")), Risk: "只读子 Agent", Execute: func(_ ToolContext, _ json.RawMessage) ToolResult {
		return failure(fmt.Errorf("spawn_explorer 由 Runtime Coordinator 执行"))
	}})
	t.add(ToolSpec{Definition: toolDef("list_files", "列出工作区中的文件。只读。", objectSchema(`"path":{"type":"string","description":"相对工作区路径，默认 ."},"max_entries":{"type":"integer","minimum":1,"maximum":1000}`)), Risk: "只读列目录", Execute: t.listFiles})
	t.add(ToolSpec{Definition: toolDef("read_file", "读取 UTF-8 文本文件，可指定行范围。只读。", objectSchema(`"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}`, "path")), Risk: "只读文件", Execute: t.readFile})
	t.add(ToolSpec{Definition: toolDef("search_text", "在工作区文本文件中搜索字符串。只读。", objectSchema(`"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer","minimum":1,"maximum":200}`, "query")), Risk: "只读搜索", Execute: t.searchText})
	t.add(ToolSpec{Definition: toolDef("write_file", "写入完整文件内容。会产生工作区副作用，执行前需要用户批准。", objectSchema(`"path":{"type":"string"},"content":{"type":"string"}`, "path", "content")), Risk: "会创建或覆盖工作区文件", Execute: t.writeFile})
	t.add(ToolSpec{Definition: toolDef("run_command", "在工作区执行 shell 命令。可能产生副作用，执行前需要用户批准。", objectSchema(`"command":{"type":"string"},"cwd":{"type":"string"}`, "command")), Risk: "执行本地 shell 命令，可能修改文件或调用外部程序", Execute: t.runCommand})
	t.add(ToolSpec{Definition: toolDef("new_plan", "创建 Plan Artifact 的新版本；如果还没有 Plan 则创建 V1。", objectSchema(`"content":{"type":"string"}`, "content")), Risk: "只写 Wisp 内部 Artifact", Execute: t.newPlan})
	t.add(ToolSpec{Definition: toolDef("edit_plan", "基于当前 Plan 保存一个新版本，不覆盖历史版本。", objectSchema(`"content":{"type":"string"}`, "content")), Risk: "只写 Wisp 内部 Artifact", Execute: t.newPlan})
	t.add(ToolSpec{Definition: toolDef("create_phase", "创建 Build Todo Phase。", objectSchema(`"title":{"type":"string"},"details":{"type":"string"}`, "title")), Risk: "只写 Wisp 内部 Todo", Execute: t.createPhase})
	t.add(ToolSpec{Definition: toolDef("update_phase", "更新或重开一个 Phase。status 可为 pending/processing/completed。", objectSchema(`"phase_id":{"type":"string"},"status":{"type":"string","enum":["pending","processing","completed"]},"details":{"type":"string"}`, "phase_id", "status")), Risk: "只写 Wisp 内部 Todo", Execute: t.updatePhase})
	t.add(ToolSpec{Definition: toolDef("done_phase", "完成 Phase，并提供语义总结。Runtime 会同时记录确定性事实并创建 Checkpoint/新 Context Epoch。", objectSchema(`"phase_id":{"type":"string"},"summary":{"type":"string"}`, "phase_id", "summary")), Risk: "只写 Wisp 内部 Todo/Checkpoint", Execute: t.donePhase})
}

func (t *ToolRuntime) securePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("只允许工作区相对路径")
	}
	abs, err := filepath.Abs(filepath.Join(t.workspaceRoot, filepath.Clean(path)))
	if err != nil {
		return "", err
	}
	if !pathWithin(t.workspaceRoot, abs) {
		return "", fmt.Errorf("路径越出工作区")
	}

	// 仅做词法 containment 不够：工作区内的 symlink 仍可能指向外部。
	// 对已存在目标解析真实路径；目标尚不存在时解析最近的已存在父目录。
	probe := abs
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			real, evalErr := filepath.EvalSymlinks(probe)
			if evalErr != nil {
				return "", evalErr
			}
			if !pathWithin(t.workspaceReal, real) {
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

func (t *ToolRuntime) listFiles(_ ToolContext, raw json.RawMessage) ToolResult {
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
	root, err := t.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	var items []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path != root && d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(t.workspaceRoot, path)
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

func (t *ToolRuntime) readFile(_ ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Path  string `json:"path"`
		Start int    `json:"start_line"`
		End   int    `json:"end_line"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	path, err := t.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	file, err := os.Open(path)
	if err != nil {
		return failure(err)
	}
	defer file.Close()
	if args.Start <= 0 {
		args.Start = 1
	}
	if args.End <= 0 || args.End < args.Start {
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
	return ToolResult{Status: "success", Output: b.String(), Data: map[string]any{"path": filepath.ToSlash(args.Path), "start_line": args.Start, "end_line": minInt(line, args.End)}}
}

func (t *ToolRuntime) searchText(_ ToolContext, raw json.RawMessage) ToolResult {
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
	root, err := t.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	var results []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "node_modules") {
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
		scan := bufio.NewScanner(file)
		scan.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for scan.Scan() {
			line++
			if strings.Contains(scan.Text(), args.Query) {
				rel, _ := filepath.Rel(t.workspaceRoot, path)
				results = append(results, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), line, scan.Text()))
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

func (t *ToolRuntime) writeFile(_ ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	path, err := t.securePath(args.Path)
	if err != nil {
		return failure(err)
	}
	before, beforeErr := os.ReadFile(path)
	created := os.IsNotExist(beforeErr)
	if beforeErr != nil && !created {
		return failure(beforeErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return failure(err)
	}
	if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: "文件已写入", Data: map[string]any{
		"path": filepath.ToSlash(args.Path), "bytes": len(args.Content), "created": created, "before_sha256": shaHex(before), "after_sha256": shaHex([]byte(args.Content)),
	}}
}

func (t *ToolRuntime) runCommand(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Command string `json:"command"`
		CWD     string `json:"cwd"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	cwd, err := t.securePath(args.CWD)
	if err != nil {
		return failure(err)
	}
	cmd := exec.CommandContext(ctx.Context, "sh", "-lc", args.Command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{W: &stdout, N: 128 * 1024}
	cmd.Stderr = &limitedWriter{W: &stderr, N: 128 * 1024}
	err = cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if ctx.Context.Err() != nil {
			return ToolResult{Status: "cancelled", Error: ctx.Context.Err().Error()}
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
	return ToolResult{Status: status, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: &code, Error: errText, Data: map[string]any{"command": args.Command, "cwd": filepath.ToSlash(args.CWD)}}
}

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

func (t *ToolRuntime) newPlan(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Content string `json:"content"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	artifact, version, err := t.store.CreateArtifactVersion(ctx.SessionID, ctx.TurnID, "plan", "active", args.Content, nil, ctx.AgentRunID)
	if err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: fmt.Sprintf("Plan V%d 已保存", version.Version), Data: map[string]any{"artifact_id": artifact.ID, "version": version.Version}}
}

type phase struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Details string `json:"details,omitempty"`
	Status  string `json:"status"`
}
type todoState struct {
	Phases []phase `json:"phases"`
}

func (t *ToolRuntime) loadTodo(turnID string) (todoState, error) {
	_, v, err := t.store.ActiveArtifact(turnID, "todo", "build")
	if err != nil {
		return todoState{}, err
	}
	if v == nil {
		return todoState{}, nil
	}
	var state todoState
	if len(v.Data) > 0 {
		_ = json.Unmarshal(v.Data, &state)
	}
	return state, nil
}
func renderTodo(state todoState) string {
	var b strings.Builder
	b.WriteString("# Build Phases\n\n")
	for _, p := range state.Phases {
		fmt.Fprintf(&b, "- [%s] %s — %s\n", p.Status, p.ID, p.Title)
	}
	return b.String()
}
func (t *ToolRuntime) saveTodo(ctx ToolContext, state todoState) (*store.ArtifactVersion, error) {
	data, _ := json.Marshal(state)
	_, v, err := t.store.CreateArtifactVersion(ctx.SessionID, ctx.TurnID, "todo", "build", renderTodo(state), data, ctx.AgentRunID)
	return v, err
}
func (t *ToolRuntime) createPhase(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Title   string `json:"title"`
		Details string `json:"details"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	state, err := t.loadTodo(ctx.TurnID)
	if err != nil {
		return failure(err)
	}
	id := "phase_" + strconv.Itoa(len(state.Phases)+1)
	state.Phases = append(state.Phases, phase{ID: id, Title: args.Title, Details: args.Details, Status: "pending"})
	v, err := t.saveTodo(ctx, state)
	if err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: "Phase 已创建", Data: map[string]any{"phase_id": id, "todo_version": v.Version}}
}
func (t *ToolRuntime) updatePhase(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		PhaseID string `json:"phase_id"`
		Status  string `json:"status"`
		Details string `json:"details"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	state, err := t.loadTodo(ctx.TurnID)
	if err != nil {
		return failure(err)
	}
	found := false
	for i := range state.Phases {
		if state.Phases[i].ID == args.PhaseID {
			state.Phases[i].Status = args.Status
			if args.Details != "" {
				state.Phases[i].Details = args.Details
			}
			found = true
		}
	}
	if !found {
		return failure(fmt.Errorf("Phase 不存在: %s", args.PhaseID))
	}
	v, err := t.saveTodo(ctx, state)
	if err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: "Phase 已更新", Data: map[string]any{"phase_id": args.PhaseID, "status": args.Status, "todo_version": v.Version}}
}
func (t *ToolRuntime) donePhase(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		PhaseID string `json:"phase_id"`
		Summary string `json:"summary"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	update, _ := json.Marshal(map[string]any{"phase_id": args.PhaseID, "status": "completed"})
	result := t.updatePhase(ctx, update)
	if result.Status != "success" {
		return result
	}
	if result.Data == nil {
		result.Data = map[string]any{}
	}
	result.Data["summary"] = args.Summary
	result.Output = "Phase 已完成"
	return result
}

func failure(err error) ToolResult { return ToolResult{Status: "failure", Error: err.Error()} }
func shaHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PermissionEngine 在执行前做确定性裁决，Prompt 无权绕过。
type PermissionEngine struct{ profiles *ProfileRegistry }
type PermissionDecision struct {
	Action string
	Risk   string
}

func NewPermissionEngine(profiles *ProfileRegistry) PermissionEngine {
	return PermissionEngine{profiles: profiles}
}

func (e PermissionEngine) Evaluate(profile AgentProfile, toolName string, tools *ToolRuntime) PermissionDecision {
	allowed := false
	for _, name := range profile.Tools {
		if name == toolName {
			allowed = true
			break
		}
	}
	if !allowed {
		return PermissionDecision{Action: "deny", Risk: "当前 AgentProfile 不允许该工具"}
	}
	policy, ok := e.profiles.PermissionPolicy(profile.PermissionPolicy)
	if !ok {
		return PermissionDecision{Action: "deny", Risk: "未知 PermissionPolicy: " + profile.PermissionPolicy}
	}
	if risk, denied := policy.DenyTools[toolName]; denied {
		return PermissionDecision{Action: "deny", Risk: risk}
	}
	if risk, ask := policy.AskUser[toolName]; ask {
		if risk == "" {
			risk = tools.Risk(toolName)
		}
		return PermissionDecision{Action: "ask_user", Risk: risk}
	}
	return PermissionDecision{Action: "allow", Risk: tools.Risk(toolName)}
}
