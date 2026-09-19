package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/store"
)

type CompiledContext struct {
	Messages             []openai.ChatMessage
	Tools                []openai.ToolDefinition
	SystemPromptSnapshot string
	ContextHash          string
	PrefixHash           string
	DebugJSON            string
	MaxSteeringSeq       int64
}

type CompileInput struct {
	Turn       *store.Turn
	Profile    AgentProfile
	AgentRunID string
	ExtraUser  string
	Trigger    string
}

type ConstraintResolver struct{ workspaceRoot string }

func NewConstraintResolver(root string) *ConstraintResolver {
	if root == "" {
		root, _ = os.Getwd()
	}
	abs, _ := filepath.Abs(root)
	return &ConstraintResolver{workspaceRoot: abs}
}
func (r *ConstraintResolver) Resolve(injectAgents bool) (project []string, scoped []string) {
	if !injectAgents {
		return nil, nil
	}
	path := filepath.Join(r.workspaceRoot, "AGENTS.md")
	if data, err := os.ReadFile(path); err == nil && len(data) <= 128*1024 {
		project = append(project, string(data))
	}
	return project, nil
}

type ContextCompiler struct {
	cfg      *config.Config
	store    *store.Store
	profiles *ProfileRegistry
	tools    *ToolRuntime
	resolver *ConstraintResolver
}

func NewContextCompiler(cfg *config.Config, st *store.Store, profiles *ProfileRegistry, tools *ToolRuntime, root string) *ContextCompiler {
	return &ContextCompiler{cfg: cfg, store: st, profiles: profiles, tools: tools, resolver: NewConstraintResolver(root)}
}

func (c *ContextCompiler) Compile(input CompileInput) (CompiledContext, error) {
	turn := input.Turn
	if turn == nil {
		return CompiledContext{}, fmt.Errorf("缺少 Turn")
	}
	session, err := c.store.GetSession(turn.SessionID)
	if err != nil {
		return CompiledContext{}, err
	}
	effective := effectiveProfile(input.Profile, turn.TaskComplexity, turn.AllowReconnaissance)
	definitions := openai.NormalizeToolDefinitions(c.tools.Definitions(effective.Tools))
	kernel := strings.TrimSpace(c.cfg.SystemPrompt)
	if kernel == "" {
		kernel = config.DefaultSystemPrompt
	}
	project, scoped := c.resolver.Resolve(session.InjectAgents)
	instructions := buildInstructionPrompt(kernel, effective, project, scoped, session.InjectAgents)
	prefix := []openai.ChatMessage{{Role: "system", Content: instructions}}

	policy, ok := c.profiles.ContextPolicy(input.Profile.ContextPolicy)
	if !ok {
		return CompiledContext{}, fmt.Errorf("未知 ContextPolicy: %s", input.Profile.ContextPolicy)
	}

	var checkpoint *store.Checkpoint
	var artifacts []store.Artifact
	var childRuns []store.AgentRun
	fullState := policy.FullState
	if fullState {
		checkpoint, err = c.store.LatestCheckpoint(turn.ID)
		if err != nil {
			return CompiledContext{}, err
		}
		artifacts, err = c.store.ListArtifacts(turn.ID)
		if err != nil {
			return CompiledContext{}, err
		}
		childRuns, err = c.store.ListAgentRuns(turn.ID)
		if err != nil {
			return CompiledContext{}, err
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Type == artifacts[j].Type {
			return artifacts[i].Name < artifacts[j].Name
		}
		return artifacts[i].Type < artifacts[j].Type
	})
	statePrompt := buildStatePrompt(turn, policy, checkpoint, artifacts, childRuns, input.AgentRunID)
	if statePrompt != "" {
		prefix = append(prefix, openai.ChatMessage{Role: "system", Content: statePrompt})
	}
	prefixHash := hashValue(struct {
		Messages []openai.ChatMessage    `json:"messages"`
		Tools    []openai.ToolDefinition `json:"tools"`
	}{prefix, definitions})

	messages := append([]openai.ChatMessage{}, prefix...)
	if !policy.FullState {
		if strings.TrimSpace(input.ExtraUser) != "" {
			messages = append(messages, openai.ChatMessage{Role: "user", Content: input.ExtraUser})
		}
		records, runErr := c.store.TimelineByTurn(turn.ID, 0)
		if runErr != nil {
			return CompiledContext{}, runErr
		}
		messages = append(messages, agentRunTimelineMessages(records, input.AgentRunID)...)
	} else {
		previous, err := c.previousTurnProjection(turn)
		if err != nil {
			return CompiledContext{}, err
		}
		messages = append(messages, previous...)
		after := int64(0)
		if checkpoint != nil {
			after = checkpoint.TimelineSeq
		}
		records, err := c.store.TimelineByTurn(turn.ID, after)
		if err != nil {
			return CompiledContext{}, err
		}
		messages = append(messages, timelineMessages(records, turn.SteeringCursor, input.AgentRunID)...)
		pending, err := c.store.PendingSteering(turn.ID, turn.SteeringCursor)
		if err != nil {
			return CompiledContext{}, err
		}
		maxSteer := turn.SteeringCursor
		for _, r := range pending {
			messages = append(messages, openai.ChatMessage{Role: "user", Content: "[用户 Steering，下一安全点优先处理]\n" + r.Content})
			if r.Seq > maxSteer {
				maxSteer = r.Seq
			}
		}
		if strings.TrimSpace(input.ExtraUser) != "" {
			messages = append(messages, openai.ChatMessage{Role: "user", Content: input.ExtraUser})
		}
		messages = openai.NormalizeToolMessages(messages)
		debug := map[string]any{
			"agent": effective.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch,
			"trigger": defaultTrigger(input.Trigger, effective.ID), "task_complexity": turn.TaskComplexity,
			"allow_reconnaissance": turn.AllowReconnaissance, "agents_md_enabled": session.InjectAgents, "agents_md_injected": session.InjectAgents && len(project) > 0,
			"checkpoint_id": turn.ActiveCheckpoint, "active_artifacts": artifactRefs(artifacts), "timeline_after_seq": after,
			"current_objective": turn.CurrentObjective, "user_decisions": turn.UserDecisions,
			"prompt_layout": promptLayout(prefix, messages, definitions), "prompt_layers": promptLayers(kernel, effective, project, statePrompt, session.InjectAgents),
		}
		debugJSON, _ := json.Marshal(debug)
		return CompiledContext{Messages: messages, Tools: definitions, SystemPromptSnapshot: systemSnapshot(prefix), ContextHash: hashValue(messages), PrefixHash: prefixHash, DebugJSON: string(debugJSON), MaxSteeringSeq: maxSteer}, nil
	}
	messages = openai.NormalizeToolMessages(messages)
	debug := map[string]any{
		"agent": effective.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch, "isolated": true,
		"trigger": defaultTrigger(input.Trigger, effective.ID), "task_complexity": turn.TaskComplexity,
		"allow_reconnaissance": turn.AllowReconnaissance, "agents_md_enabled": session.InjectAgents, "agents_md_injected": session.InjectAgents && len(project) > 0,
		"current_objective": turn.CurrentObjective, "user_decisions": turn.UserDecisions,
		"prompt_layout": promptLayout(prefix, messages, definitions), "prompt_layers": promptLayers(kernel, effective, project, statePrompt, session.InjectAgents),
	}
	debugJSON, _ := json.Marshal(debug)
	return CompiledContext{Messages: messages, Tools: definitions, SystemPromptSnapshot: systemSnapshot(prefix), ContextHash: hashValue(messages), PrefixHash: prefixHash, DebugJSON: string(debugJSON), MaxSteeringSeq: turn.SteeringCursor}, nil
}

func (c *ContextCompiler) previousTurnProjection(current *store.Turn) ([]openai.ChatMessage, error) {
	turns, err := c.store.ListTurns(current.SessionID)
	if err != nil {
		return nil, err
	}
	folded, err := c.store.FoldedTurnIDs(current.SessionID)
	if err != nil {
		return nil, err
	}
	all, err := c.store.Timeline(current.SessionID, 0, 5000)
	if err != nil {
		return nil, err
	}
	byTurn := map[string][]store.Record{}
	for _, r := range all {
		byTurn[r.TurnID] = append(byTurn[r.TurnID], r)
	}
	var out []openai.ChatMessage
	for _, turn := range turns {
		if turn.TurnIndex >= current.TurnIndex {
			break
		}
		if folded[turn.ID] {
			_, v, e := c.store.ActiveArtifact(turn.ID, "summary", "final")
			if e != nil {
				return nil, e
			}
			if v != nil {
				facts := string(v.Data)
				if cp, cpErr := c.store.LatestCheckpoint(turn.ID); cpErr == nil && cp != nil && len(cp.Facts) > 0 {
					facts = string(cp.Facts)
				}
				out = append(out, openai.ChatMessage{Role: "system", Content: fmt.Sprintf("<FoldedTurn index=\"%d\">\nSummary:\n%s\nFacts:\n%s\n</FoldedTurn>", turn.TurnIndex, v.Content, facts)})
			}
			continue
		}
		reasoning := reasoningContentByCall(byTurn[turn.ID])
		for _, r := range byTurn[turn.ID] {
			switch r.Kind {
			case store.EventUserMessage, store.EventUserSteering:
				out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
			case store.EventAssistantMessage:
				var meta struct {
					Child            bool   `json:"child"`
					ReasoningContent string `json:"reasoning_content"`
				}
				_ = json.Unmarshal(r.Data, &meta)
				if !meta.Child {
					if meta.ReasoningContent == "" {
						meta.ReasoningContent = reasoning[r.ModelCallID]
					}
					out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
				}
			}
		}
	}
	return out, nil
}

type replayToolCall struct {
	ToolCallID       string
	Name             string
	Arguments        json.RawMessage
	AgentRunID       string
	ReasoningContent string
}

func collectReplayGroups(records []store.Record, accept func(string) bool) (map[string][]replayToolCall, map[string]bool, map[string]string) {
	groups := map[string][]replayToolCall{}
	acceptedIDs := map[string]bool{}
	assistantContent := map[string]string{}
	for _, r := range records {
		if r.Kind == store.EventAssistantMessage && r.ModelCallID != "" {
			var meta struct {
				AgentRunID string `json:"agent_run_id"`
				Child      bool   `json:"child"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if accept(meta.AgentRunID) && !meta.Child {
				assistantContent[r.ModelCallID] = r.Content
			}
			continue
		}
		if r.Kind != store.EventToolRequested || r.ModelCallID == "" {
			continue
		}
		var d struct {
			ToolCallID       string          `json:"tool_call_id"`
			Name             string          `json:"name"`
			Arguments        json.RawMessage `json:"arguments"`
			AgentRunID       string          `json:"agent_run_id"`
			ReasoningContent string          `json:"reasoning_content"`
		}
		if json.Unmarshal(r.Data, &d) != nil || d.ToolCallID == "" || !accept(d.AgentRunID) {
			continue
		}
		groups[r.ModelCallID] = append(groups[r.ModelCallID], replayToolCall{ToolCallID: d.ToolCallID, Name: d.Name, Arguments: d.Arguments, AgentRunID: d.AgentRunID, ReasoningContent: d.ReasoningContent})
		acceptedIDs[d.ToolCallID] = true
	}
	return groups, acceptedIDs, assistantContent
}

func assistantToolReplay(modelCallID string, calls []replayToolCall, content string, reasoning map[string]string) openai.ChatMessage {
	toolCalls := make([]openai.ToolCall, 0, len(calls))
	reason := reasoning[modelCallID]
	for _, call := range calls {
		if reason == "" && call.ReasoningContent != "" {
			reason = call.ReasoningContent
		}
		toolCalls = append(toolCalls, openai.ToolCall{ID: call.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: call.Name, Arguments: string(call.Arguments)}})
	}
	return openai.ChatMessage{Role: "assistant", Content: content, ReasoningContent: reason, ToolCalls: toolCalls}
}

func agentRunTimelineMessages(records []store.Record, agentRunID string) []openai.ChatMessage {
	reasoning := reasoningContentByCall(records)
	groups, acceptedIDs, assistantContent := collectReplayGroups(records, func(runID string) bool { return runID == agentRunID })
	firstRequestSeen := map[string]bool{}
	var out []openai.ChatMessage
	for _, r := range records {
		switch r.Kind {
		case store.EventAssistantMessage:
			var meta struct {
				AgentRunID       string `json:"agent_run_id"`
				ReasoningContent string `json:"reasoning_content"`
				Child            bool   `json:"child"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if meta.AgentRunID != agentRunID || meta.Child || len(groups[r.ModelCallID]) > 0 {
				continue
			}
			if meta.ReasoningContent == "" {
				meta.ReasoningContent = reasoning[r.ModelCallID]
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
		case store.EventToolRequested:
			if len(groups[r.ModelCallID]) == 0 || firstRequestSeen[r.ModelCallID] {
				continue
			}
			firstRequestSeen[r.ModelCallID] = true
			out = append(out, assistantToolReplay(r.ModelCallID, groups[r.ModelCallID], assistantContent[r.ModelCallID], reasoning))
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && acceptedIDs[d.ToolCallID] {
				content := string(d.Result)
				if content == "" || content == "null" {
					content = r.Content
				}
				out = append(out, openai.ChatMessage{Role: "tool", ToolCallID: d.ToolCallID, Content: content})
			}
		}
	}
	return out
}

func timelineMessages(records []store.Record, steeringCursor int64, activeAgentRunID string) []openai.ChatMessage {
	reasoning := reasoningContentByCall(records)
	groups, acceptedIDs, assistantContent := collectReplayGroups(records, func(runID string) bool { return runID == "" || runID == activeAgentRunID })
	firstRequestSeen := map[string]bool{}
	var out []openai.ChatMessage
	for _, r := range records {
		switch r.Kind {
		case store.EventUserMessage:
			out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
		case store.EventUserSteering:
			if r.Seq <= steeringCursor {
				out = append(out, openai.ChatMessage{Role: "user", Content: "[Steering]\n" + r.Content})
			}
		case store.EventAssistantMessage:
			var meta struct {
				AgentRunID       string `json:"agent_run_id"`
				Child            bool   `json:"child"`
				ReasoningContent string `json:"reasoning_content"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if meta.Child || (meta.AgentRunID != "" && meta.AgentRunID != activeAgentRunID) || len(groups[r.ModelCallID]) > 0 {
				continue
			}
			if meta.ReasoningContent == "" {
				meta.ReasoningContent = reasoning[r.ModelCallID]
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
		case store.EventToolRequested:
			if len(groups[r.ModelCallID]) == 0 || firstRequestSeen[r.ModelCallID] {
				continue
			}
			firstRequestSeen[r.ModelCallID] = true
			out = append(out, assistantToolReplay(r.ModelCallID, groups[r.ModelCallID], assistantContent[r.ModelCallID], reasoning))
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && acceptedIDs[d.ToolCallID] {
				content := string(d.Result)
				if content == "" || content == "null" {
					content = r.Content
				}
				out = append(out, openai.ChatMessage{Role: "tool", ToolCallID: d.ToolCallID, Content: content})
			}
		}
	}
	return out
}

func reasoningContentByCall(records []store.Record) map[string]string {
	out := map[string]string{}
	for _, r := range records {
		if r.Kind == store.EventModelReasoning && r.ModelCallID != "" && r.Content != "" {
			out[r.ModelCallID] = r.Content
		}
	}
	return out
}

func buildInstructionPrompt(kernel string, profile AgentProfile, project, scoped []string, injectAgents bool) string {
	var b strings.Builder
	b.WriteString("<WispInstructions>\n")
	b.WriteString("<Core>\n")
	b.WriteString(strings.TrimSpace(kernel))
	b.WriteString("\n</Core>\n")
	b.WriteString(`<RuntimeProtocol>
Timeline 是持久事实，只有 Tool Result 能证明真实副作用。Runtime 允许把互不依赖的只读 ToolCall 批量执行；有副作用或互相依赖的 ToolCall 会被串行化。每个 Tool 批次后 Runtime 会处理 steering/pause/stop/approval。不要伪造 Tool Result、Approval、Artifact 或已完成状态。
</RuntimeProtocol>`)
	b.WriteByte('\n')
	if injectAgents && len(project) > 0 {
		b.WriteString("<ProjectInstructions source=\"AGENTS.md\">\n")
		for _, value := range project {
			b.WriteString(strings.TrimSpace(value))
			b.WriteByte('\n')
		}
		b.WriteString("</ProjectInstructions>\n")
	}
	if len(scoped) > 0 {
		b.WriteString("<ScopedProjectInstructions>\n")
		for i, value := range scoped {
			fmt.Fprintf(&b, "[Scoped %d]\n%s\n", i+1, strings.TrimSpace(value))
		}
		b.WriteString("</ScopedProjectInstructions>\n")
	}
	fmt.Fprintf(&b, "<AgentProfile id=\"%s\">\n%s\n</AgentProfile>\n", profile.ID, strings.TrimSpace(profile.Prompt))
	b.WriteString("</WispInstructions>")
	return b.String()
}

func buildStatePrompt(turn *store.Turn, policy ContextPolicy, checkpoint *store.Checkpoint, artifacts []store.Artifact, childRuns []store.AgentRun, activeAgentRunID string) string {
	var body strings.Builder
	if turn.CurrentObjective != "" && policy.IncludeObjective {
		body.WriteString("<CurrentObjective>\n")
		body.WriteString(turn.CurrentObjective)
		body.WriteString("\n</CurrentObjective>\n")
		if len(turn.UserDecisions) > 0 {
			body.WriteString("<UserDecisions precedence=\"latest-wins\">\n")
			for i, decision := range turn.UserDecisions {
				fmt.Fprintf(&body, "%d. %s\n", i+1, strings.TrimSpace(decision))
			}
			body.WriteString("</UserDecisions>\n")
		}
		fmt.Fprintf(&body, "<TaskPolicy complexity=\"%s\" reconnaissance=\"%t\" />\n", turn.TaskComplexity, turn.AllowReconnaissance)
	}
	if checkpoint != nil {
		fmt.Fprintf(&body, "<Checkpoint kind=\"%s\">\n<Summary>\n%s\n</Summary>\n<Facts>\n%s\n</Facts>\n</Checkpoint>\n", checkpoint.Kind, checkpoint.Summary, string(checkpoint.Facts))
	}
	if len(artifacts) > 0 {
		body.WriteString("<Artifacts>\n")
		for _, artifact := range artifacts {
			var active *store.ArtifactVersion
			for i := range artifact.Versions {
				if artifact.Versions[i].Version == artifact.ActiveVersion {
					value := artifact.Versions[i]
					active = &value
					break
				}
			}
			if active == nil {
				continue
			}
			fmt.Fprintf(&body, "<Artifact type=\"%s\" name=\"%s\" version=\"%d\">\n%s\n<Data>%s</Data>\n</Artifact>\n", artifact.Type, artifact.Name, active.Version, active.Content, string(active.Data))
		}
		body.WriteString("</Artifacts>\n")
	}
	var childBody strings.Builder
	for _, run := range childRuns {
		if run.ParentRunID == "" || run.Status != "completed" || run.ID == activeAgentRunID {
			continue
		}
		fmt.Fprintf(&childBody, "<SubAgentResult profile=\"%s\" run_id=\"%s\">\n%s\n</SubAgentResult>\n", run.ProfileID, run.ID, string(run.Result))
	}
	if childBody.Len() > 0 {
		body.WriteString("<SubAgentResults>\n")
		body.WriteString(childBody.String())
		body.WriteString("</SubAgentResults>\n")
	}
	if body.Len() == 0 {
		return ""
	}
	return "<WispState>\n" + strings.TrimSpace(body.String()) + "\n</WispState>"
}

func defaultTrigger(trigger, profile string) string {
	if strings.TrimSpace(trigger) != "" {
		return strings.TrimSpace(trigger)
	}
	switch profile {
	case "plan":
		return "生成或修订执行计划"
	case "build":
		return "执行 Active Plan"
	case "explore":
		return "定向只读调查"
	case "explain":
		return "解释待审批操作"
	default:
		return "Runtime Action Loop"
	}
}

func promptLayers(kernel string, profile AgentProfile, project []string, statePrompt string, injectAgents bool) []map[string]any {
	layers := []map[string]any{
		{"id": "core", "label": "Core", "chars": len(kernel), "summary": "全局决策优先级、交互和工具规则"},
		{"id": "runtime", "label": "Runtime Protocol", "summary": "Tool Result / approval / safe-point 协议"},
	}
	chars := 0
	for _, item := range project {
		chars += len(item)
	}
	projectSummary := "用户已关闭注入"
	if injectAgents && len(project) == 0 {
		projectSummary = "已启用，但根目录未发现可读取的 AGENTS.md"
	}
	if injectAgents && len(project) > 0 {
		projectSummary = "已注入的项目说明"
	}
	layers = append(layers, map[string]any{"id": "project", "label": "AGENTS.md", "chars": chars, "enabled": injectAgents && len(project) > 0, "summary": projectSummary})
	layers = append(layers,
		map[string]any{"id": "agent", "label": "Agent Profile", "chars": len(profile.Prompt), "summary": profile.DisplayName},
		map[string]any{"id": "state", "label": "Current State", "chars": len(statePrompt), "summary": "Current Objective / Plan / Checkpoint / Artifact"},
	)
	return layers
}

func promptLayout(prefix, messages []openai.ChatMessage, tools []openai.ToolDefinition) map[string]any {
	roles := map[string]int{}
	totalChars := 0
	for _, message := range messages {
		roles[message.Role]++
		totalChars += len(message.Content) + len(message.ReasoningContent)
	}
	systemChars := 0
	for _, message := range prefix {
		systemChars += len(message.Content)
	}
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Function.Name)
	}
	return map[string]any{
		"system_messages": len(prefix), "history_messages": maxInt(0, len(messages)-len(prefix)), "total_messages": len(messages),
		"system_chars": systemChars, "message_chars": totalChars, "roles": roles, "tools": toolNames,
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func artifactRefs(items []store.Artifact) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, a := range items {
		out = append(out, map[string]any{"id": a.ID, "type": a.Type, "name": a.Name, "version": a.ActiveVersion})
	}
	return out
}
func hashValue(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func systemSnapshot(messages []openai.ChatMessage) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(m.Content)
		}
	}
	return b.String()
}
