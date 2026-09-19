package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type Selection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

type callOutcome struct {
	Content    string
	Reasoning  string
	ToolCalls  []ToolCall
	Usage      *store.Usage
	Finish     string
	Error      string
	DurationMs int
	TTFTMs     int
}

type Coordinator struct {
	cfg         *config.Config
	catalog     *provider.Catalog
	store       *store.Store
	runs        *RunRegistry
	hub         *EventHub
	client      *openai.Client
	profiles    *ProfileRegistry
	tools       *ToolRuntime
	compiler    *ContextCompiler
	permissions PermissionEngine
}

func NewCoordinator(cfg *config.Config, catalog *provider.Catalog, st *store.Store, runs *RunRegistry, hub *EventHub, workspaceRoot string) *Coordinator {
	profiles := NewProfileRegistry()
	tools := NewToolRuntime(st, workspaceRoot)
	return &Coordinator{
		cfg: cfg, catalog: catalog, store: st, runs: runs, hub: hub,
		client: openai.NewClient(180), profiles: profiles, tools: tools,
		compiler: NewContextCompiler(cfg, st, profiles, tools, workspaceRoot), permissions: NewPermissionEngine(profiles),
	}
}

func (c *Coordinator) Profiles() []AgentProfile { return c.profiles.List() }
func (c *Coordinator) Hub() *EventHub           { return c.hub }
func (c *Coordinator) Runs() *RunRegistry       { return c.runs }

func (c *Coordinator) Capabilities(turn *store.Turn, executionActive, hasApproval, hasPlan bool) map[string]bool {
	caps := c.store.TurnCapabilities(turn, executionActive, hasApproval, hasPlan)
	if turn == nil {
		return caps
	}
	profile, ok := c.profiles.Get(turn.ActiveAgent)
	if !ok {
		caps["can_start_build"] = false
		return caps
	}
	transition, ok := c.profiles.Transition(profile.TransitionPolicy)
	caps["can_start_build"] = caps["can_start_build"] && ok && transition.Command == "start_build"
	return caps
}

func (c *Coordinator) ArtifactVersionMutable(turn *store.Turn, artifact *store.Artifact) bool {
	if turn == nil || artifact == nil {
		return false
	}
	// 终态 Turn 的 Artifact 属于已完成历史，不能再通过切 Active Version 改写最终状态。
	if turn.Status == store.TurnCompleted || turn.Status == store.TurnStopped || turn.Status == store.TurnCancelled || turn.Status == store.TurnFailed {
		return false
	}
	// Transition target profile（例如 Build）会冻结作为输入契约的 Artifact。
	return !c.profiles.ArtifactFrozenByProfile(turn.ActiveAgent, artifact.Type, artifact.Name)
}

// Start 在独立于浏览器连接的 Context 中继续一个 Turn。
func (c *Coordinator) Start(turnID string, selection Selection) bool {
	turn, err := c.store.GetTurn(turnID)
	if err != nil {
		return false
	}
	ctx, ok := c.runs.Begin(turn.SessionID, turn.ID)
	if !ok {
		return false
	}
	go func() {
		defer c.runs.End(turn.SessionID, turn.ID)
		c.runTurn(ctx, turnID, selection)
	}()
	return true
}

func (c *Coordinator) runTurn(ctx context.Context, turnID string, selection Selection) {
	turn, err := c.store.GetTurn(turnID)
	if err != nil {
		return
	}
	selection, err = c.resolveSelection(ctx, turn.SessionID, selection)
	if err != nil {
		c.failTurn(turn, "解析 Provider/Model 失败: "+err.Error())
		return
	}
	if turn.Status == store.TurnPaused {
		return
	}
	_ = c.store.SetTurnStatus(turn.ID, store.TurnRunning)
	c.publish("runtime.status", turn, map[string]any{"status": store.TurnRunning})

	if turn.ActiveAgentRunID == "" {
		run, err := c.store.BeginAgentRun(turn.SessionID, turn.ID, "", turn.ActiveAgent)
		if err != nil {
			c.failTurn(turn, err.Error())
			return
		}
		_ = c.store.SetTurnRootRun(turn.ID, run.ID)
		turn.ActiveAgentRunID = run.ID
		turn.RootAgentRunID = run.ID
	}

	for action := 0; action < 96; action++ {
		if c.handleCancellation(ctx, turn.ID) {
			return
		}
		turn, err = c.store.GetTurn(turn.ID)
		if err != nil {
			return
		}
		if c.safePoint(turn) {
			return
		}

		resolved, err := c.store.NextResolvedApproval(turn.ID)
		if err != nil {
			c.failTurn(turn, "读取 Approval 失败: "+err.Error())
			return
		}
		if resolved != nil {
			if !c.applyResolvedApproval(ctx, turn, resolved) {
				return
			}
			continue
		}
		pending, err := c.store.PendingApproval(turn.ID)
		if err != nil {
			c.failTurn(turn, err.Error())
			return
		}
		if pending != nil {
			_ = c.store.SetTurnStatus(turn.ID, store.TurnWaitingUser)
			c.publish("runtime.status", turn, map[string]any{"status": store.TurnWaitingUser, "reason": "approval", "approval_id": pending.ID})
			return
		}

		// 一批 ToolCalls 的全部请求已落盘；在编译下轮上下文前逐个结清结果。
		// 如有 Approval，先暂停，重新启动后继续批次中尚未执行的调用。
		pendingCall, pendingErr := c.store.NextPendingToolCall(turn.ID)
		if pendingErr != nil {
			c.failTurn(turn, "读取待执行工具失败: "+pendingErr.Error())
			return
		}
		if pendingCall != nil {
			if !c.runPendingTool(ctx, turn, selection, pendingCall) {
				return
			}
			continue
		}
		profile, ok := c.profiles.Get(turn.ActiveAgent)
		if !ok {
			c.failTurn(turn, "未知 AgentProfile: "+turn.ActiveAgent)
			return
		}
		if turn.TaskMode == "complex" {
			if err := c.compactProgress(turn); err != nil {
				c.failTurn(turn, "进度 Checkpoint 失败: "+err.Error())
				return
			}
		}
		compiled, err := c.compiler.Compile(CompileInput{Turn: turn, Profile: profile, AgentRunID: turn.ActiveAgentRunID})
		if err != nil {
			c.failTurn(turn, "Context Compile 失败: "+err.Error())
			return
		}
		if compiled.MaxSteeringSeq > turn.SteeringCursor {
			_ = c.store.ConsumeSteering(turn.ID, compiled.MaxSteeringSeq)
		}
		_ = c.store.UpdateEpochPrefixHash(turn.ID, turn.ContextEpoch, compiled.PrefixHash)

		out, callID := c.callModel(ctx, turn, profile, turn.ActiveAgentRunID, selection, compiled, false)
		if ctx.Err() != nil {
			c.cancelTurn(turn, callID, out)
			return
		}
		if out.Error != "" {
			c.failTurn(turn, out.Error)
			return
		}
		if len(out.ToolCalls) == 0 {
			switch profile.IdlePolicy {
			case "wait_user":
				_ = c.store.SetTurnStatus(turn.ID, store.TurnWaitingUser)
				c.publish("runtime.status", turn, map[string]any{"status": store.TurnWaitingUser, "reason": "agent_waiting", "profile_id": profile.ID})
			case "complete_turn":
				c.completeTurn(turn, out.Content)
			default:
				c.failTurn(turn, "Root Agent 不支持 IdlePolicy: "+profile.IdlePolicy)
			}
			return
		}

		// 所有 ToolCall 一次性落盘再结清；跨依赖写入/执行不允许成批静默推进。
		// 只接受 ≤4 个相互独立的只读操作；其他场景仅执行第一个，并为剩余调用记录拒绝结果。
		readonly := map[string]bool{"list_files": true, "read_file": true, "search_text": true, "verify_file": true}
		allowBatch := len(out.ToolCalls) <= 4
		for _, tool := range out.ToolCalls {
			if !readonly[tool.Name] {
				allowBatch = false
			}
		}
		for _, tool := range out.ToolCalls {
			c.recordToolRequested(turn, turn.ActiveAgentRunID, callID, out.Reasoning, tool)
		}
		for index, tool := range out.ToolCalls {
			if (len(out.ToolCalls) > 1 && !allowBatch && index > 0) || index >= 4 {
				c.recordToolResult(turn, tool, ToolResult{Status: "rejected", Error: "批量执行仅支持至多四个互不依赖的只读操作；请等待首个工具结果再继续"})
			}
		}
		continue
	}
	c.failTurn(turn, "Action Loop 超过安全上限（96）")
}

// runPendingTool 不让工具的权限与阶段迁移依赖 LLM 自觉；每个操作后都有安全点。
func (c *Coordinator) runPendingTool(ctx context.Context, turn *store.Turn, selection Selection, pending *store.PendingToolCall) bool {
	call := ToolCall{ID: pending.ToolCallID, Name: pending.Name, Arguments: pending.Arguments}
	profile, ok := c.profiles.Get(turn.ActiveAgent)
	if !ok {
		c.failTurn(turn, "未知 AgentProfile: "+turn.ActiveAgent)
		return false
	}
	decision := c.permissions.Evaluate(profile, call.Name, c.tools)
	if decision.Action == "allow" {
		if risk := c.reconnaissanceGuard(turn, call); risk != "" {
			decision = PermissionDecision{Action: "deny", Risk: risk}
		}
	}
	if decision.Action == "deny" {
		c.recordToolResult(turn, call, ToolResult{Status: "rejected", Error: decision.Risk})
		return !c.safePointAfterReload(turn.ID)
	}
	if decision.Action == "ask_user" {
		grant, err := c.store.HasApprovalGrant(turn.ID, call.Name, call.Arguments)
		if err != nil {
			c.failTurn(turn, err.Error())
			return false
		}
		if !grant {
			approval, err := c.store.CreateApproval(turn.SessionID, turn.ID, turn.ActiveAgentRunID, call.ID, call.Name, call.Arguments, decision.Risk, "")
			if err != nil {
				c.failTurn(turn, err.Error())
				return false
			}
			_ = c.store.SetTurnStatus(turn.ID, store.TurnWaitingUser)
			c.publish("approval.requested", turn, approval)
			return false
		}
	}
	result := c.executeTool(ctx, turn, selection, call)
	c.recordToolResult(turn, call, result)
	if result.Status == "success" && call.Name == "verify_file" && turn.Stage == "build" {
		_ = c.store.TransitionStage(turn.ID, "verify")
	} else if result.Status == "success" && (call.Name == "write_file" || call.Name == "run_command") && turn.Stage == "verify" {
		_ = c.store.TransitionStage(turn.ID, "build")
	}
	return !c.safePointAfterReload(turn.ID)
}

func (c *Coordinator) reconnaissanceGuard(turn *store.Turn, call ToolCall) string {
	if call.Name == "read_file" {
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		if strings.TrimPrefix(strings.TrimSpace(args.Path), "./") == "AGENTS.md" {
			session, err := c.store.GetSession(turn.SessionID)
			if err == nil && session.AgentsEnabled {
				return "AGENTS.md 已在本轮 Context 注入，无需再次读取"
			}
		}
	}
	if looksStandalone(turn.Objective) && (call.Name == "list_files" || call.Name == "search_text" || call.Name == "spawn_explorer") {
		return "独立新文件任务不需要仓库探查；直接保存计划或实现文件"
	}
	return ""
}

func looksStandalone(objective string) bool {
	text := strings.ToLower(objective)
	for _, word := range []string{"wisp", "仓库", "项目", "现有", "已有", "修改", "重构", "前端", "后端", "网页", "多个文件", "多文件", "已有代码", "源代码", "源码"} {
		if strings.Contains(text, word) {
			return false
		}
	}
	for _, word := range []string{"python", "脚本", "单文件", "密码本", "生成器", "独立文件"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// 复杂任务按计划文本确定性选择阶段模式；无需 LLM 为阶段状态消耗额外请求。
func classifyMode(objective, plan string) string {
	if looksStandalone(objective) && len([]rune(plan)) < 1200 {
		return "trivial"
	}
	complexity := 0
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "1.") || strings.HasPrefix(line, "2.") || strings.HasPrefix(line, "3.") {
			complexity++
		}
	}
	if len([]rune(plan)) > 1000 || complexity >= 7 || strings.Contains(plan, "跨模块") {
		return "complex"
	}
	return "standard"
}

// 复杂任务每完成一批真实 Tool Result，自动创建事实 Checkpoint；绝不在批次尚未结清时压缩。
func (c *Coordinator) compactProgress(turn *store.Turn) error {
	latest, err := c.store.LatestCheckpoint(turn.ID)
	if err != nil {
		return err
	}
	after := int64(0)
	if latest != nil {
		after = latest.TimelineSeq
	}
	records, err := c.store.TimelineByTurn(turn.ID, after)
	if err != nil {
		return err
	}
	count := 0
	for _, r := range records {
		if r.Kind == store.EventToolCompleted || r.Kind == store.EventToolFailed {
			count++
		}
	}
	if count < 8 {
		return nil
	}
	facts, err := c.collectDeterministicFacts(turn, after)
	if err != nil {
		return err
	}
	cp, err := c.store.CreateCheckpoint(turn.SessionID, turn.ID, "runtime.progress", fmt.Sprintf("Runtime 已处理 %d 个执行动作", count), facts)
	if err != nil {
		return err
	}
	_, err = c.store.AdvanceContextEpoch(turn.ID, "runtime.progress", cp.ID, "")
	return err
}

func (c *Coordinator) callModel(ctx context.Context, turn *store.Turn, profile AgentProfile, agentRunID string, selection Selection, compiled CompiledContext, child bool) (callOutcome, string) {
	provCfg, modelInfo, err := c.catalog.Resolve(ctx, selection.Provider, selection.Model)
	if err != nil {
		return callOutcome{Error: err.Error()}, ""
	}
	baseURL, apiKey := provCfg.BaseURL, provCfg.APIKey
	index, err := c.store.NextModelCallIndex(turn.ID)
	if err != nil {
		return callOutcome{Error: err.Error()}, ""
	}
	callID, err := c.store.BeginModelCallWithContext(turn.SessionID, turn.ID, index, provCfg.ID, modelInfo.ID, selection.Thinking, compiled.SystemPromptSnapshot, store.ModelCallContext{
		AgentRunID: agentRunID, Epoch: turn.ContextEpoch, ContextHash: compiled.ContextHash, PrefixHash: compiled.PrefixHash, DebugJSON: compiled.DebugJSON,
	})
	if err != nil {
		return callOutcome{Error: err.Error()}, ""
	}
	c.publish("model.start", turn, map[string]any{"call_id": callID, "index": index, "provider": provCfg.ID, "model": modelInfo.ID, "thinking": selection.Thinking, "agent_run_id": agentRunID, "profile_id": profile.ID, "context_epoch": turn.ContextEpoch})

	req, err := c.client.BuildRequestWithTools(baseURL, apiKey, modelInfo.ID, modelInfo.ThinkingStyle, selection.Thinking, compiled.Messages, compiled.Tools)
	if err != nil {
		message := "构造请求失败: " + err.Error()
		_ = c.store.FinishModelCall(callID, store.ModelCallResult{Status: "failed", Finish: "error", Error: message})
		return callOutcome{Error: message}, callID
	}
	if req.GetBody != nil {
		if body, bodyErr := req.GetBody(); bodyErr == nil {
			if data, readErr := io.ReadAll(body); readErr == nil {
				_ = c.store.SetModelCallRequest(callID, req.URL.String(), string(data))
			}
			_ = body.Close()
		}
	}
	out := c.runModelStream(ctx, turn, callID, req)
	if out.Reasoning != "" {
		data := eventJSON(map[string]any{"agent_run_id": agentRunID, "child": child})
		_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, ModelCallID: callID, Kind: store.EventModelReasoning, Content: out.Reasoning, Data: data})
	}
	if out.Content != "" {
		data := eventJSON(map[string]any{"agent_run_id": agentRunID, "child": child, "reasoning_content": out.Reasoning})
		_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, ModelCallID: callID, Kind: store.EventAssistantMessage, Content: out.Content, Data: data})
	}
	status := "completed"
	finish := out.Finish
	errText := out.Error
	if ctx.Err() != nil {
		status = "cancelled"
		finish = "aborted"
		errText = "生成已停止"
	} else if out.Error != "" {
		status = "failed"
		finish = "error"
	}
	_ = c.store.FinishModelCall(callID, store.ModelCallResult{Status: status, Finish: finish, Usage: out.Usage, DurationMs: out.DurationMs, TTFTMs: out.TTFTMs, Error: errText})
	c.publish("model.done", turn, map[string]any{"call_id": callID, "finish": finish, "duration_ms": out.DurationMs, "ttft_ms": out.TTFTMs, "error": errText, "has_tool_calls": len(out.ToolCalls) > 0})
	return out, callID
}

func (c *Coordinator) runModelStream(ctx context.Context, turn *store.Turn, callID string, req *http.Request) callOutcome {
	start := time.Now()
	resp, err := c.client.DoStream(ctx, req)
	if err != nil {
		return callOutcome{Finish: "error", Error: err.Error(), DurationMs: int(time.Since(start).Milliseconds())}
	}
	defer resp.Body.Close()
	reader := openai.NewStreamReader(resp.Body)
	ch := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(ch)
	var out callOutcome
	var usage *openai.Usage
	var first time.Time
	type partial struct{ id, typ, name, args string }
	calls := map[int]*partial{}
	for evt := range ch {
		switch evt.Type {
		case "reasoning":
			if first.IsZero() {
				first = time.Now()
				c.publish("ttft", turn, map[string]any{"call_id": callID, "ms": first.Sub(start).Milliseconds()})
			}
			out.Reasoning += evt.Text
			c.publish("reasoning", turn, map[string]any{"call_id": callID, "text": evt.Text})
		case "delta":
			if first.IsZero() {
				first = time.Now()
				c.publish("ttft", turn, map[string]any{"call_id": callID, "ms": first.Sub(start).Milliseconds()})
			}
			out.Content += evt.Text
			c.publish("delta", turn, map[string]any{"call_id": callID, "text": evt.Text})
		case "tool_call":
			if evt.ToolCall != nil {
				p := calls[evt.ToolCall.Index]
				if p == nil {
					p = &partial{}
					calls[evt.ToolCall.Index] = p
				}
				if evt.ToolCall.ID != "" {
					p.id = evt.ToolCall.ID
				}
				if evt.ToolCall.Type != "" {
					p.typ = evt.ToolCall.Type
				}
				p.name += evt.ToolCall.Name
				p.args += evt.ToolCall.Arguments
				c.publish("tool.delta", turn, map[string]any{"call_id": callID, "index": evt.ToolCall.Index, "name": evt.ToolCall.Name, "arguments": evt.ToolCall.Arguments})
			}
		case "usage":
			usage = evt.Usage
			c.publish("usage", turn, evt.Usage)
		case "done":
			if out.Finish == "" {
				out.Finish = evt.Finish
			}
		case "error":
			out.Error = evt.Error
			out.Finish = "error"
		}
	}
	keys := make([]int, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		p := calls[k]
		id := p.id
		if id == "" {
			id = fmt.Sprintf("tool_%s_%d", strings.TrimPrefix(callID, "mc_"), k)
		}
		args := normalizeArguments(p.args)
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: p.name, Arguments: args})
	}
	out.DurationMs = int(time.Since(start).Milliseconds())
	if !first.IsZero() {
		out.TTFTMs = int(first.Sub(start).Milliseconds())
	}
	if usage != nil {
		out.Usage = &store.Usage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens}
	}
	if out.Finish == "" && out.Error == "" {
		out.Finish = "stop"
	}
	return out
}

func normalizeArguments(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	data, _ := json.Marshal(map[string]string{"_invalid_json": raw})
	return data
}

func (c *Coordinator) executeTool(ctx context.Context, turn *store.Turn, selection Selection, call ToolCall) ToolResult {
	_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: store.EventToolStarted, Data: eventJSON(map[string]any{"tool_call_id": call.ID, "name": call.Name, "arguments": call.Arguments})})
	c.publish("tool.started", turn, map[string]any{"tool_call_id": call.ID, "name": call.Name})
	if call.Name == "spawn_explorer" {
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return ToolResult{Status: "failure", Error: err.Error()}
		}
		return c.runExplorer(ctx, turn, selection, args.Query)
	}
	return c.tools.Execute(ToolContext{Context: ctx, SessionID: turn.SessionID, TurnID: turn.ID, AgentRunID: turn.ActiveAgentRunID}, call)
}

func (c *Coordinator) recordToolRequested(turn *store.Turn, agentRunID, modelCallID, reasoning string, call ToolCall) {
	_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, ModelCallID: modelCallID, Kind: store.EventToolRequested, Data: eventJSON(map[string]any{
		"tool_call_id": call.ID, "name": call.Name, "arguments": call.Arguments, "agent_run_id": agentRunID, "reasoning_content": reasoning,
	})})
}
func (c *Coordinator) recordToolResult(turn *store.Turn, call ToolCall, result ToolResult) {
	kind := store.EventToolCompleted
	switch result.Status {
	case "failure":
		kind = store.EventToolFailed
	case "rejected":
		kind = store.EventToolRejected
	case "cancelled":
		kind = store.EventToolCancelled
	}
	_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: kind, Data: eventJSON(map[string]any{"tool_call_id": call.ID, "name": call.Name, "result": result})})
	c.publish(kind, turn, map[string]any{"tool_call_id": call.ID, "name": call.Name, "result": result})
	if result.Status == "failure" && result.Output == "" && result.Stdout == "" {
		hint := "Tool 执行失败"
		if result.ExitCode != nil {
			hint += fmt.Sprintf("；Exit Code: %d", *result.ExitCode)
		}
		if result.Stderr == "" {
			hint += "；No Output"
		}
		_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: store.EventRuntimeHint, Content: hint, Data: eventJSON(map[string]any{"tool_call_id": call.ID})})
	}
}

func (c *Coordinator) applyResolvedApproval(ctx context.Context, turn *store.Turn, approval *store.Approval) bool {
	call := ToolCall{ID: approval.ToolCallID, Name: approval.ToolName, Arguments: approval.Args}
	if approval.Status == "rejected" {
		c.recordToolResult(turn, call, ToolResult{Status: "rejected", Error: "用户拒绝了该操作"})
		return true
	}
	profile, ok := c.profiles.Get(turn.ActiveAgent)
	if !ok {
		return false
	}
	decision := c.permissions.Evaluate(profile, call.Name, c.tools)
	if decision.Action == "deny" {
		c.recordToolResult(turn, call, ToolResult{Status: "rejected", Error: "批准后权限策略已变化: " + decision.Risk})
		return true
	}
	result := c.executeTool(ctx, turn, Selection{}, call)
	c.recordToolResult(turn, call, result)
	if result.Status == "success" && call.Name == "verify_file" && turn.Stage == "build" {
		_ = c.store.TransitionStage(turn.ID, "verify")
	}
	return ctx.Err() == nil
}

func (c *Coordinator) safePoint(turn *store.Turn) bool {
	if turn.StopRequested {
		_ = c.store.SetTurnStatus(turn.ID, store.TurnStopped)
		_ = c.store.FinishAgentRun(turn.ActiveAgentRunID, "stopped", map[string]any{"reason": "user_stop"})
		c.publish("runtime.status", turn, map[string]any{"status": store.TurnStopped})
		return true
	}
	if turn.PauseRequested {
		_ = c.store.SetPauseRequested(turn.ID, false)
		_ = c.store.SetTurnStatus(turn.ID, store.TurnPaused)
		c.publish("runtime.status", turn, map[string]any{"status": store.TurnPaused})
		return true
	}
	return false
}
func (c *Coordinator) safePointAfterReload(turnID string) bool {
	turn, err := c.store.GetTurn(turnID)
	return err == nil && c.safePoint(turn)
}
func (c *Coordinator) handleCancellation(ctx context.Context, turnID string) bool {
	if ctx.Err() == nil {
		return false
	}
	turn, err := c.store.GetTurn(turnID)
	if err == nil {
		c.cancelTurn(turn, "", callOutcome{})
	}
	return true
}
func (c *Coordinator) cancelTurn(turn *store.Turn, _ string, _ callOutcome) {
	_ = c.store.SetTurnStatus(turn.ID, store.TurnCancelled)
	if turn.ActiveAgentRunID != "" {
		_ = c.store.FinishAgentRun(turn.ActiveAgentRunID, "cancelled", map[string]any{"reason": "hard_cancel"})
	}
	c.publish("runtime.status", turn, map[string]any{"status": store.TurnCancelled})
}
func (c *Coordinator) failTurn(turn *store.Turn, message string) {
	_ = c.store.AppendError(turn.SessionID, turn.ID, "", message)
	_ = c.store.SetTurnStatus(turn.ID, store.TurnFailed)
	if turn.ActiveAgentRunID != "" {
		_ = c.store.FinishAgentRun(turn.ActiveAgentRunID, "failed", map[string]any{"error": message})
	}
	c.publish("runtime.error", turn, map[string]any{"message": message})
	c.publish("runtime.status", turn, map[string]any{"status": store.TurnFailed})
}

func (c *Coordinator) resolveSelection(ctx context.Context, sessionID string, in Selection) (Selection, error) {
	sess, err := c.store.GetSession(sessionID)
	if err != nil {
		return in, err
	}
	if strings.TrimSpace(in.Provider) == "" {
		in.Provider = sess.Provider
	}
	if strings.TrimSpace(in.Model) == "" {
		in.Model = sess.Model
	}
	if in.Thinking == "" {
		in.Thinking = "default"
	}
	prov, model, err := c.catalog.Resolve(ctx, in.Provider, in.Model)
	if err != nil && in.Provider != "" {
		prov, model, err = c.catalog.Resolve(ctx, "", in.Model)
	}
	if err != nil {
		return in, err
	}
	in.Provider = prov.ID
	in.Model = model.ID
	return in, nil
}

func (c *Coordinator) publish(eventType string, turn *store.Turn, value any) {
	if eventType == store.EventRuntimeStatus {
		_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: store.EventRuntimeStatus, Data: eventJSON(value)})
	}
	c.hub.Publish(NewEvent(eventType, turn.SessionID, turn.ID, value))
}
func eventJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func (c *Coordinator) runExplorer(ctx context.Context, parent *store.Turn, selection Selection, query string) ToolResult {
	child, err := c.store.BeginAgentRun(parent.SessionID, parent.ID, parent.ActiveAgentRunID, "explore")
	if err != nil {
		return ToolResult{Status: "failure", Error: err.Error()}
	}
	result, err := c.runChild(ctx, parent, child, selection, query, "explore")
	if err != nil {
		_ = c.store.FinishAgentRun(child.ID, "failed", map[string]any{"error": err.Error()})
		return ToolResult{Status: "failure", Error: err.Error(), Data: map[string]any{"agent_run_id": child.ID}}
	}
	_ = c.store.FinishAgentRun(child.ID, "completed", map[string]any{"summary": result})
	return ToolResult{Status: "success", Output: result, Data: map[string]any{"agent_run_id": child.ID, "summary": result}}
}

func (c *Coordinator) runChild(ctx context.Context, turn *store.Turn, child *store.AgentRun, selection Selection, objective, profileID string) (string, error) {
	profile, ok := c.profiles.Get(profileID)
	if !ok {
		return "", fmt.Errorf("未知 child profile: %s", profileID)
	}
	selection, err := c.resolveSelection(ctx, turn.SessionID, selection)
	if err != nil {
		return "", err
	}
	for i := 0; i < 5; i++ {
		fresh, err := c.store.GetTurn(turn.ID)
		if err != nil {
			return "", err
		}
		compiled, err := c.compiler.Compile(CompileInput{Turn: fresh, Profile: profile, AgentRunID: child.ID, ExtraUser: objective})
		if err != nil {
			return "", err
		}
		out, callID := c.callModel(ctx, fresh, profile, child.ID, selection, compiled, true)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if out.Error != "" {
			return "", fmt.Errorf("%s", out.Error)
		}
		if len(out.ToolCalls) == 0 {
			return strings.TrimSpace(out.Content), nil
		}
		for index, call := range out.ToolCalls {
			c.recordToolRequested(fresh, child.ID, callID, out.Reasoning, call)
			if index >= 4 {
				c.recordToolResult(fresh, call, ToolResult{Status: "rejected", Error: "每轮最多四个工具"})
				continue
			}
			decision := c.permissions.Evaluate(profile, call.Name, c.tools)
			if decision.Action != "allow" {
				c.recordToolResult(fresh, call, ToolResult{Status: "rejected", Error: decision.Risk})
				continue
			}
			if risk := c.reconnaissanceGuard(fresh, call); risk != "" {
				c.recordToolResult(fresh, call, ToolResult{Status: "rejected", Error: risk})
				continue
			}
			result := c.tools.Execute(ToolContext{Context: ctx, SessionID: fresh.SessionID, TurnID: fresh.ID, AgentRunID: child.ID}, call)
			c.recordToolResult(fresh, call, result)
		}
	}
	return "", fmt.Errorf("Child Agent 超过 Action 上限")
}

func (c *Coordinator) collectDeterministicFacts(turn *store.Turn, after int64) (map[string]any, error) {
	records, err := c.store.TimelineByTurn(turn.ID, after)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	var failures []string
	var commands []map[string]any
	maxSeq := after
	for _, r := range records {
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
		if r.Kind != store.EventToolCompleted && r.Kind != store.EventToolFailed && r.Kind != store.EventToolRejected && r.Kind != store.EventToolCancelled {
			continue
		}
		var d struct {
			Name   string     `json:"name"`
			Result ToolResult `json:"result"`
		}
		if json.Unmarshal(r.Data, &d) != nil {
			continue
		}
		if d.Name == "write_file" && d.Result.Status == "success" {
			if path, ok := d.Result.Data["path"].(string); ok && path != "" {
				files[path] = true
			}
		}
		if d.Name == "run_command" {
			commands = append(commands, map[string]any{
				"status":    d.Result.Status,
				"command":   d.Result.Data["command"],
				"cwd":       d.Result.Data["cwd"],
				"exit_code": d.Result.ExitCode,
			})
		}
		if d.Result.Status == "failure" {
			failures = append(failures, d.Name+": "+d.Result.Error)
		}
	}
	fileList := make([]string, 0, len(files))
	for path := range files {
		fileList = append(fileList, path)
	}
	sort.Strings(fileList)

	artifacts, err := c.store.ListArtifacts(turn.ID)
	if err != nil {
		return nil, err
	}
	var completedPhases, openPhases []map[string]string
	if _, todo, todoErr := c.store.ActiveArtifact(turn.ID, "todo", "build"); todoErr == nil && todo != nil {
		var state todoState
		if json.Unmarshal(todo.Data, &state) == nil {
			for _, phase := range state.Phases {
				item := map[string]string{"id": phase.ID, "title": phase.Title, "status": phase.Status}
				if phase.Status == "completed" {
					completedPhases = append(completedPhases, item)
				} else {
					openPhases = append(openPhases, item)
				}
			}
		}
	}
	pending, err := c.store.PendingSteering(turn.ID, turn.SteeringCursor)
	if err != nil {
		return nil, err
	}
	steering := make([]map[string]any, 0, len(pending))
	for _, record := range pending {
		steering = append(steering, map[string]any{"seq": record.Seq, "content": record.Content})
	}
	session, err := c.store.GetSession(turn.SessionID)
	if err != nil {
		return nil, err
	}
	project, err := c.compiler.resolver.Resolve(session.AgentsEnabled)
	if err != nil {
		return nil, err
	}
	constraintSnapshot := map[string]any{"agents_enabled": session.AgentsEnabled, "project_instructions": project, "hash": hashValue(project)}
	return map[string]any{
		"modified_files":           fileList,
		"command_actions":          commands,
		"tool_failures":            failures,
		"active_artifacts":         artifactRefs(artifacts),
		"completed_phases":         completedPhases,
		"open_phases":              openPhases,
		"constraint_snapshot":      constraintSnapshot,
		"unconsumed_user_steering": steering,
		"timeline_cursor":          maxSeq,
		"active_agent":             turn.ActiveAgent,
		"active_context_epoch":     turn.ContextEpoch,
		"active_checkpoint_id":     turn.ActiveCheckpoint,
	}, nil
}

func (c *Coordinator) completeTurn(turn *store.Turn, content string) {
	if turn.TaskMode == "complex" {
		if err := c.tools.finishAutomaticPhase(turn.SessionID, turn.ID, turn.ActiveAgentRunID); err != nil {
			c.failTurn(turn, "完成自动 Phase 失败: "+err.Error())
			return
		}
	}
	if err := c.store.TransitionStage(turn.ID, "complete"); err != nil {
		c.failTurn(turn, err.Error())
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		content = "任务已完成。"
	}
	facts, factsErr := c.collectDeterministicFacts(turn, 0)
	if factsErr != nil {
		facts = map[string]any{"status": store.TurnCompleted, "facts_error": factsErr.Error()}
	}
	facts["status"] = store.TurnCompleted
	_, _, _ = c.store.CreateArtifactVersion(turn.SessionID, turn.ID, "summary", "final", content, eventJSON(facts), turn.ActiveAgentRunID)
	// Summary Artifact 已经成为事实的一部分，Checkpoint 再收集一次以包含它的 active version。
	if finalFacts, err := c.collectDeterministicFacts(turn, 0); err == nil {
		finalFacts["status"] = store.TurnCompleted
		facts = finalFacts
	}
	_, _ = c.store.CreateCheckpoint(turn.SessionID, turn.ID, "turn.completed", content, facts)
	_ = c.store.FinishAgentRun(turn.ActiveAgentRunID, "completed", map[string]any{"summary": content, "facts": facts})
	_ = c.store.SetTurnStatus(turn.ID, store.TurnCompleted)
	_ = c.store.Touch(turn.SessionID)
	c.publish("runtime.status", turn, map[string]any{"status": store.TurnCompleted})
}

// StartBuild 是 Runtime Command，不伪装成普通 user message。
func (c *Coordinator) StartBuild(turnID string, selection Selection) error {
	turn, err := c.store.GetTurn(turnID)
	if err != nil {
		return err
	}
	if turn.Status != store.TurnWaitingUser {
		return fmt.Errorf("当前 Turn 不能开始 Build")
	}
	if c.runs.Status(turn.SessionID).Active {
		return fmt.Errorf("当前 Turn 仍有 Action 在执行")
	}
	profile, ok := c.profiles.Get(turn.ActiveAgent)
	if !ok {
		return fmt.Errorf("未知 AgentProfile: %s", turn.ActiveAgent)
	}
	transition, ok := c.profiles.Transition(profile.TransitionPolicy)
	if !ok || transition.Command != "start_build" {
		return fmt.Errorf("当前 Agent 不支持 start_build")
	}
	artifact, version, err := c.store.ActiveArtifact(turn.ID, transition.RequiredArtifactType, transition.RequiredArtifactName)
	if err != nil {
		return err
	}
	if version == nil {
		return fmt.Errorf("没有 Active Plan")
	}
	_, _ = c.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: store.EventRuntimeCommand, Content: "start_build", Data: eventJSON(map[string]any{"artifact_id": artifact.ID, "plan_version": version.Version})})
	cp, err := c.store.CreateCheckpoint(turn.SessionID, turn.ID, transition.CheckpointKind, "Planning 已完成，Build 从固定的 Active Plan 开始。", map[string]any{"plan_artifact_id": artifact.ID, "plan_version": version.Version})
	if err != nil {
		return err
	}
	if _, err = c.store.AdvanceContextEpoch(turn.ID, transition.Reason, cp.ID, ""); err != nil {
		return err
	}
	mode := classifyMode(turn.Objective, version.Content)
	if err := c.store.SetTaskMode(turn.ID, mode); err != nil {
		return err
	}
	if err := c.store.TransitionStage(turn.ID, "build"); err != nil {
		return err
	}
	if turn.ActiveAgentRunID != "" {
		_ = c.store.FinishAgentRun(turn.ActiveAgentRunID, "completed", map[string]any{"transition": "build", "plan_version": version.Version})
	}
	buildRun, err := c.store.BeginAgentRun(turn.SessionID, turn.ID, turn.RootAgentRunID, transition.TargetProfile)
	if err != nil {
		return err
	}
	if err := c.store.SetTurnAgent(turn.ID, transition.TargetProfile, buildRun.ID); err != nil {
		return err
	}
	if mode == "complex" {
		if err := c.tools.beginAutomaticPhase(turn.SessionID, turn.ID, buildRun.ID); err != nil {
			return err
		}
	}
	if err := c.store.SetTurnStatus(turn.ID, store.TurnRunning); err != nil {
		return err
	}
	if !c.Start(turn.ID, selection) {
		return fmt.Errorf("未能启动 Build：当前会话已有执行任务")
	}
	return nil
}

func (c *Coordinator) approvalRelatedTimeline(turnID string, limit int) string {
	records, err := c.store.TimelineByTurn(turnID, 0)
	if err != nil || len(records) == 0 || limit <= 0 {
		return "(无)"
	}
	var lines []string
	for i := len(records) - 1; i >= 0 && len(lines) < limit; i-- {
		r := records[i]
		if r.Kind == store.EventModelReasoning || r.Kind == store.EventRuntimeStatus || r.Kind == store.EventApprovalRequested {
			continue
		}
		text := strings.TrimSpace(r.Content)
		if text == "" && len(r.Data) > 0 && string(r.Data) != "{}" {
			text = string(r.Data)
		}
		if len(text) > 1200 {
			text = text[:1200] + "…"
		}
		if text != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", r.Kind, text))
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return "(无)"
	}
	return strings.Join(lines, "\n")
}

func (c *Coordinator) ChallengeApproval(approvalID, question string, selection Selection) error {
	approval, err := c.store.GetApproval(approvalID)
	if err != nil {
		return err
	}
	if approval.Status != "pending" {
		return fmt.Errorf("Approval 已处理")
	}
	turn, err := c.store.GetTurn(approval.TurnID)
	if err != nil {
		return err
	}
	ctx, ok := c.runs.Begin(turn.SessionID, turn.ID)
	if !ok {
		return fmt.Errorf("Turn 正在执行")
	}
	go func() {
		defer c.runs.End(turn.SessionID, turn.ID)
		// Challenge 也是一个 Action；完成后必须经过同一个 Safe Point，
		// 这样执行期间到达的 Pause/Stop 不会被遗留到下一次主循环。
		defer c.safePointAfterReload(turn.ID)
		child, err := c.store.BeginAgentRun(turn.SessionID, turn.ID, turn.ActiveAgentRunID, "explain")
		if err != nil {
			return
		}
		recent := c.approvalRelatedTimeline(turn.ID, 5)
		prompt := fmt.Sprintf("冻结的 ToolCall: %s\nArguments: %s\nRisk: %s\n最近相关 Timeline（最多 5 条）:\n%s\n用户问题: %s", approval.ToolName, string(approval.Args), approval.Risk, recent, question)
		result, runErr := c.runChild(ctx, turn, child, selection, prompt, "explain")
		if runErr != nil {
			_ = c.store.FinishAgentRun(child.ID, "failed", map[string]any{"error": runErr.Error()})
			return
		}
		_ = c.store.FinishAgentRun(child.ID, "completed", map[string]any{"summary": result, "approval_id": approval.ID})
	}()
	return nil
}
