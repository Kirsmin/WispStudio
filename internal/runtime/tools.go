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
	"unicode/utf8"

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
	// OpenAI function schema 要求 `required` 始终是数组；空可变参数必须序列化成 []，不能变成 JSON null。
	if required == nil {
		required = []string{}
	}
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
	t.add(ToolSpec{Definition: toolDef("run_command", "在工作区执行 shell 命令。确定性识别出的只读检查可直接执行；其他命令需要用户批准。", objectSchema(`"command":{"type":"string"},"cwd":{"type":"string"}`, "command")), Risk: "执行本地 shell 命令，可能修改文件或调用外部程序", Execute: t.runCommand})
	planSchema := objectSchema(`"content":{"type":"string"},"summary":{"type":"string","description":"给用户看的1-3句计划摘要"},"complexity":{"type":"string","enum":["trivial","standard","complex"]},"phases":{"type":"array","items":{"type":"object","properties":{"title":{"type":"string"},"details":{"type":"string"}},"required":["title"],"additionalProperties":false}}`, "content", "summary", "complexity")
	t.add(ToolSpec{Definition: toolDef("new_plan", "保存 Active Plan。trivial/standard 不创建 phases；仅 complex 任务提供 phases。保存成功后 Runtime 会直接进入等待用户开始执行。", planSchema), Risk: "只写 Wisp 内部 Artifact", Execute: t.newPlan})
	t.add(ToolSpec{Definition: toolDef("edit_plan", "保存 Active Plan 的新版本。使用与 new_plan 相同的结构。", planSchema), Risk: "只写 Wisp 内部 Artifact", Execute: t.newPlan})
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

func skipWorkspaceNoiseDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".gocache", ".gopath", ".npm-cache", "dist", "build", ".next", "coverage", "Data", "__pycache__", "target", ".cache", ".venv", "venv":
		return true
	default:
		return false
	}
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
		if path != root && d.IsDir() && skipWorkspaceNoiseDir(d.Name()) {
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

func (t *ToolRuntime) readFile(ctx ToolContext, raw json.RawMessage) ToolResult {
	var args struct {
		Path  string `json:"path"`
		Start int    `json:"start_line"`
		End   int    `json:"end_line"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(args.Path)))
	if clean == "AGENTS.md" && ctx.SessionID != "" {
		if session, err := t.store.GetSession(ctx.SessionID); err == nil && session.InjectAgents {
			agentsPath := filepath.Join(t.workspaceRoot, "AGENTS.md")
			if info, statErr := os.Stat(agentsPath); statErr == nil && !info.IsDir() && info.Size() <= 128*1024 {
				return ToolResult{Status: "success", Output: "AGENTS.md 已由 Runtime 注入当前上下文；为避免重复 token，本次不再回传全文。", Data: map[string]any{"path": "AGENTS.md", "already_injected": true}}
			}
		}
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
	probe := make([]byte, 8192)
	n, _ := file.Read(probe)
	probe = probe[:n]
	if bytes.IndexByte(probe, 0) >= 0 || (len(probe) > 0 && !utf8.Valid(probe)) {
		return failure(fmt.Errorf("检测到二进制文件，read_file 只读取 UTF-8 文本: %s", filepath.ToSlash(args.Path)))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return failure(err)
	}
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
			if path != root && skipWorkspaceNoiseDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= args.Max {
			return io.EOF
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2*1024*1024 || likelyBinaryFile(path) {
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
		Content    string          `json:"content"`
		Summary    string          `json:"summary"`
		Complexity string          `json:"complexity"`
		Phases     []planPhaseSpec `json:"phases"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return failure(err)
	}
	if args.Complexity != "trivial" && args.Complexity != "standard" && args.Complexity != "complex" {
		args.Complexity = "standard"
	}
	if args.Complexity != "complex" {
		args.Phases = nil
	}
	data, _ := json.Marshal(planState{Complexity: args.Complexity, Phases: args.Phases})
	artifact, version, err := t.store.CreateArtifactVersion(ctx.SessionID, ctx.TurnID, "plan", "active", args.Content, data, ctx.AgentRunID)
	if err != nil {
		return failure(err)
	}
	turn, _ := t.store.GetTurn(ctx.TurnID)
	allowRecon := args.Complexity == "complex"
	if turn != nil && turn.AllowReconnaissance && args.Complexity == "standard" {
		allowRecon = true
	}
	_ = t.store.SetTurnTaskPolicy(ctx.TurnID, args.Complexity, allowRecon)
	return ToolResult{Status: "success", Output: fmt.Sprintf("Plan V%d 已保存", version.Version), Data: map[string]any{"artifact_id": artifact.ID, "version": version.Version, "complexity": args.Complexity, "phase_count": len(args.Phases), "user_summary": strings.TrimSpace(args.Summary)}}
}

type planPhaseSpec struct {
	Title   string `json:"title"`
	Details string `json:"details,omitempty"`
}
type planState struct {
	Complexity string          `json:"complexity"`
	Phases     []planPhaseSpec `json:"phases,omitempty"`
}

func decodePlanState(raw json.RawMessage) planState {
	var state planState
	_ = json.Unmarshal(raw, &state)
	if state.Complexity == "" {
		state.Complexity = "standard"
	}
	return state
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
	state, err := t.loadTodo(ctx.TurnID)
	if err != nil {
		return failure(err)
	}
	found := -1
	for i := range state.Phases {
		if state.Phases[i].ID == args.PhaseID {
			found = i
			break
		}
	}
	if found < 0 {
		return failure(fmt.Errorf("Phase 不存在: %s", args.PhaseID))
	}
	state.Phases[found].Status = "completed"
	nextID := ""
	for i := found + 1; i < len(state.Phases); i++ {
		if state.Phases[i].Status == "pending" {
			state.Phases[i].Status = "processing"
			nextID = state.Phases[i].ID
			break
		}
	}
	v, err := t.saveTodo(ctx, state)
	if err != nil {
		return failure(err)
	}
	return ToolResult{Status: "success", Output: "Phase 已完成", Data: map[string]any{"phase_id": args.PhaseID, "summary": args.Summary, "next_phase_id": nextID, "todo_version": v.Version}}
}

func (t *ToolRuntime) initializeBuildTodo(ctx ToolContext, plan planState) error {
	if plan.Complexity != "complex" {
		return nil
	}
	phases := plan.Phases
	if len(phases) == 0 {
		// complex Plan 即使漏给 phases，也由 Runtime 建立一个兜底 Phase，保证 Build 完成状态仍有明确不变量。
		phases = []planPhaseSpec{{Title: "执行 Active Plan", Details: "Runtime 自动生成的复杂任务兜底 Phase"}}
	}
	state := todoState{Phases: make([]phase, 0, len(phases))}
	for i, item := range phases {
		status := "pending"
		if i == 0 {
			status = "processing"
		}
		state.Phases = append(state.Phases, phase{ID: "phase_" + strconv.Itoa(i+1), Title: strings.TrimSpace(item.Title), Details: strings.TrimSpace(item.Details), Status: status})
	}
	_, err := t.saveTodo(ctx, state)
	return err
}

func (t *ToolRuntime) currentPhaseID(turnID string) string {
	state, err := t.loadTodo(turnID)
	if err != nil {
		return ""
	}
	for _, item := range state.Phases {
		if item.Status == "processing" {
			return item.ID
		}
	}
	return ""
}

func likelyBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	buf = buf[:n]
	return bytes.IndexByte(buf, 0) >= 0 || (len(buf) > 0 && !utf8.Valid(buf))
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
	Action    string
	Risk      string
	RiskClass string
}

func NewPermissionEngine(profiles *ProfileRegistry) PermissionEngine {
	return PermissionEngine{profiles: profiles}
}

func (e PermissionEngine) Evaluate(profile AgentProfile, call ToolCall, tools *ToolRuntime) PermissionDecision {
	allowed := false
	for _, name := range profile.Tools {
		if name == call.Name {
			allowed = true
			break
		}
	}
	if !allowed {
		return PermissionDecision{Action: "deny", Risk: "当前 AgentProfile 不允许该工具", RiskClass: "policy.denied"}
	}
	policy, ok := e.profiles.PermissionPolicy(profile.PermissionPolicy)
	if !ok {
		return PermissionDecision{Action: "deny", Risk: "未知 PermissionPolicy: " + profile.PermissionPolicy, RiskClass: "policy.denied"}
	}
	if risk, denied := policy.DenyTools[call.Name]; denied {
		return PermissionDecision{Action: "deny", Risk: risk, RiskClass: "policy.denied"}
	}
	if call.Name == "run_command" {
		if command, ok := readCommandArg(call.Arguments); ok {
			risk := classifyCommandRisk(command)
			if risk.ReadOnly {
				return PermissionDecision{Action: "allow", Risk: risk.Description, RiskClass: risk.Class}
			}
			if _, ask := policy.AskUser[call.Name]; ask {
				return PermissionDecision{Action: "ask_user", Risk: risk.Description, RiskClass: risk.Class}
			}
		}
	}
	if risk, ask := policy.AskUser[call.Name]; ask {
		if risk == "" {
			risk = tools.Risk(call.Name)
		}
		return PermissionDecision{Action: "ask_user", Risk: risk, RiskClass: "workspace.write"}
	}
	class := "runtime.metadata"
	if call.Name == "list_files" || call.Name == "read_file" || call.Name == "search_text" || call.Name == "spawn_explorer" {
		class = "workspace.read"
	}
	return PermissionDecision{Action: "allow", Risk: tools.Risk(call.Name), RiskClass: class}
}

type commandRisk struct {
	Class       string
	Description string
	ReadOnly    bool
}

func classifyCommandRisk(command string) commandRisk {
	if isReadOnlyCommand(command) {
		return commandRisk{Class: "shell.readonly", Description: "只读检查命令，不修改工作区", ReadOnly: true}
	}

	type rankedRisk struct {
		rank int
		risk commandRisk
	}
	best := rankedRisk{rank: 1, risk: commandRisk{Class: "shell.execute.dynamic", Description: "执行本地 shell 命令；行为取决于命令与脚本内容"}}
	normalized := strings.NewReplacer("&&", ";", "||", ";", "|", ";", "\n", ";").Replace(command)
	for _, part := range strings.Split(normalized, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(filepath.Base(strings.Trim(fields[0], "'\"")))
		candidate := commandRisk{Class: "shell.execute." + safeRiskToken(name), Description: "执行本地程序 " + name + "；程序可能修改工作区或访问外部资源"}
		rank := 1

		if isDangerousCommand(name, fields[1:]) {
			rank = 4
			candidate = commandRisk{Class: "shell.dangerous." + safeRiskToken(name), Description: "危险命令 " + name + "；可能删除、覆盖或改变系统/工作区状态"}
		} else if isExternalCommand(name, fields[1:]) {
			rank = 3
			candidate = commandRisk{Class: "shell.external." + safeRiskToken(name), Description: "外部/网络命令 " + name + "；可能访问网络、远端服务或安装依赖"}
		} else if isWorkspaceMutationCommand(name, fields[1:]) {
			rank = 2
			candidate = commandRisk{Class: "shell.workspace." + safeRiskToken(name), Description: "工作区变更命令 " + name + "；会创建、移动或修改本地文件/版本状态"}
		}
		if rank > best.rank {
			best = rankedRisk{rank: rank, risk: candidate}
		} else if rank == best.rank && best.risk.Class == "shell.execute.dynamic" {
			best.risk = candidate
		}
	}
	if strings.ContainsAny(command, "<>`$(){}&") && best.rank <= 1 {
		return commandRisk{Class: "shell.execute.dynamic", Description: "动态 shell 命令；包含展开、重定向或后台语法，实际行为需用户确认"}
	}
	return best.risk
}

func safeRiskToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func isDangerousCommand(name string, args []string) bool {
	switch name {
	case "rm", "rmdir", "shred", "truncate", "dd", "mkfs", "chmod", "chown", "kill", "pkill", "killall", "shutdown", "reboot", "sudo", "su":
		return true
	case "git":
		joined := strings.ToLower(strings.Join(args, " "))
		return strings.Contains(joined, "reset --hard") || strings.HasPrefix(joined, "clean ") || joined == "clean" || strings.HasPrefix(joined, "restore ") || strings.HasPrefix(joined, "checkout --")
	case "find":
		joined := strings.ToLower(strings.Join(args, " "))
		return strings.Contains(joined, " -delete") || strings.Contains(joined, " -exec rm")
	}
	return false
}

func isExternalCommand(name string, args []string) bool {
	switch name {
	case "curl", "wget", "ssh", "scp", "sftp", "ftp", "telnet", "nc", "netcat", "rsync", "aws", "gcloud", "az":
		return true
	case "git":
		if len(args) == 0 {
			return false
		}
		switch strings.ToLower(strings.Trim(args[0], "'\"")) {
		case "clone", "fetch", "pull", "push", "remote", "submodule":
			return true
		}
	case "npm", "pnpm", "yarn", "pip", "pip3", "uv", "cargo", "go", "apt", "apt-get", "brew":
		joined := strings.ToLower(strings.Join(args, " "))
		for _, marker := range []string{" install", " add", " get", " update", " upgrade", " publish", " login", " download"} {
			if strings.Contains(" "+joined, marker) {
				return true
			}
		}
	}
	return false
}

func isWorkspaceMutationCommand(name string, args []string) bool {
	switch name {
	case "mkdir", "touch", "cp", "mv", "install", "ln", "patch":
		return true
	case "sed":
		for _, arg := range args {
			plain := strings.ToLower(strings.Trim(arg, "'\""))
			if plain == "-i" || strings.HasPrefix(plain, "-i") || plain == "--in-place" || strings.HasPrefix(plain, "--in-place=") {
				return true
			}
		}
	case "git":
		if len(args) == 0 {
			return false
		}
		switch strings.ToLower(strings.Trim(args[0], "'\"")) {
		case "add", "commit", "merge", "rebase", "cherry-pick", "tag", "branch", "switch":
			return true
		}
	}
	return false
}

func readCommandArg(raw json.RawMessage) (string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.Command) == "" {
		return "", false
	}
	return strings.TrimSpace(args.Command), true
}

// isReadOnlyCommand 故意采用保守策略：只允许已知检查命令，不允许重定向/替换，且管道或命令序列中的每一段都必须只读。
func isReadOnlyCommand(command string) bool {
	// 这里仅用于判断能否跳过 Approval，并不是 shell 沙箱；凡是包含展开、后台执行、越出工作区路径或已知写入/执行参数，都回到正常审批。
	if strings.ContainsAny(command, "<>`$(){}&") {
		return false
	}
	normalized := strings.NewReplacer("&&", ";", "||", ";", "|", ";", "\n", ";").Replace(command)
	for _, part := range strings.Split(normalized, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		for _, arg := range fields[1:] {
			plain := strings.Trim(arg, "'\"")
			if strings.HasPrefix(plain, "/") || strings.HasPrefix(plain, "~/") || plain == ".." || strings.HasPrefix(plain, "../") || strings.Contains(plain, "/../") {
				return false
			}
			lower := strings.ToLower(plain)
			if lower == "-o" || strings.HasPrefix(lower, "-o=") || (strings.HasPrefix(lower, "-o") && len(lower) > 2) || lower == "--output" || strings.HasPrefix(lower, "--output=") || lower == "--replace" || strings.HasPrefix(lower, "--compress-program=") {
				return false
			}
		}
		name := filepath.Base(strings.Trim(fields[0], "'\""))
		switch name {
		case "pwd", "ls", "cat", "head", "tail", "wc", "grep", "rg", "stat", "file", "od", "sort", "uniq", "cut", "diff":
			// 通用参数检查已覆盖这些命令。
		case "git":
			if len(fields) < 2 {
				return false
			}
			subcommand := strings.Trim(fields[1], "'\"")
			switch subcommand {
			case "status", "diff", "log", "show", "rev-parse", "ls-files":
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}
