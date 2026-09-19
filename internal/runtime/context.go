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
}

type ConstraintResolver struct{ workspaceRoot string }

func NewConstraintResolver(root string) *ConstraintResolver {
	if root == "" {
		root, _ = os.Getwd()
	}
	abs, _ := filepath.Abs(root)
	return &ConstraintResolver{workspaceRoot: abs}
}
func (r *ConstraintResolver) Resolve(enabled bool) (project []string, err error) {
	if !enabled {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(r.workspaceRoot, "AGENTS.md"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 128*1024 {
		return nil, fmt.Errorf("AGENTS.md 超过 128 KiB，建议精简或关闭注入")
	}
	return []string{string(data)}, nil
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
	definitions := openai.NormalizeToolDefinitions(c.tools.Definitions(input.Profile.Tools))
	kernel := strings.TrimSpace(c.cfg.SystemPrompt)
	if kernel == "" {
		kernel = config.DefaultSystemPrompt
	}
	session, err := c.store.GetSession(turn.SessionID)
	if err != nil {
		return CompiledContext{}, err
	}
	project, err := c.resolver.Resolve(session.AgentsEnabled)
	if err != nil {
		return CompiledContext{}, err
	}
	instructions := buildInstructionPrompt(kernel, input.Profile, project)
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
		trigger := "继续任务"
		if len(pending) > 0 {
			trigger = "用户补充要求"
		} else if turn.Stage == "build" && checkpoint != nil && checkpoint.Kind == "plan_to_build" && len(records) == 0 {
			trigger = "开始执行"
		} else if len(records) > 0 {
			last := records[len(records)-1].Kind
			if strings.HasPrefix(last, "tool.") {
				trigger = "工具结果"
			} else if turn.Stage == "plan" && len(records) == 1 {
				trigger = "初次规划"
			}
		}
		debug := map[string]any{
			"trigger": trigger, "active_user_override": len(pending) > 0, "agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch,
			"checkpoint_id": turn.ActiveCheckpoint, "active_artifacts": artifactRefs(artifacts), "timeline_after_seq": after,
			"prompt_layout":  promptLayout(prefix, messages, definitions),
			"agents_enabled": session.AgentsEnabled, "agents_injected": len(project) > 0,
			"objective_source": "current_plan_or_initial", "pending_user_decisions": len(turn.UserDecisions), "task_mode": turn.TaskMode, "stage": turn.Stage,
		}
		debugJSON, _ := json.Marshal(debug)
		return CompiledContext{Messages: messages, Tools: definitions, SystemPromptSnapshot: systemSnapshot(prefix), ContextHash: hashValue(messages), PrefixHash: prefixHash, DebugJSON: string(debugJSON), MaxSteeringSeq: maxSteer}, nil
	}
	messages = openai.NormalizeToolMessages(messages)
	debug := map[string]any{
		"agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch, "isolated": true, "trigger": "子任务",
		"prompt_layout":  promptLayout(prefix, messages, definitions),
		"agents_enabled": session.AgentsEnabled, "agents_injected": len(project) > 0,
		"objective_source": "current_plan_or_initial", "pending_user_decisions": len(turn.UserDecisions), "task_mode": turn.TaskMode, "stage": turn.Stage,
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

// replayTimeline 将同一次模型响应的 ToolCalls 聚合为一条 assistant 消息，
// 紧随其后回放全部匹配的 tool results；兼容多次只读调用与暂停审批后的批次恢复。
func agentRunTimelineMessages(records []store.Record, agentRunID string) []openai.ChatMessage {
	return replayTimeline(records, agentRunID, 0, true)
}

func timelineMessages(records []store.Record, steeringCursor int64, activeAgentRunID string) []openai.ChatMessage {
	return replayTimeline(records, activeAgentRunID, steeringCursor, false)
}

func replayTimeline(records []store.Record, agentRunID string, steeringCursor int64, isolated bool) []openai.ChatMessage {
	type requested struct {
		ToolCallID       string          `json:"tool_call_id"`
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		AgentRunID       string          `json:"agent_run_id"`
		ReasoningContent string          `json:"reasoning_content"`
	}
	toolGroups := map[string][]requested{}
	results := map[string]string{}
	assistantText := map[string]string{}
	reasoning := reasoningContentByCall(records)
	for _, r := range records {
		if r.Kind == store.EventToolRequested {
			var item requested
			if json.Unmarshal(r.Data, &item) == nil && item.ToolCallID != "" &&
				((isolated && item.AgentRunID == agentRunID) || (!isolated && item.AgentRunID == agentRunID)) {
				toolGroups[r.ModelCallID] = append(toolGroups[r.ModelCallID], item)
			}
		}
		if r.Kind == store.EventAssistantMessage {
			var meta struct {
				AgentRunID string `json:"agent_run_id"`
				Child      bool   `json:"child"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if (isolated && meta.AgentRunID == agentRunID) || (!isolated && !meta.Child) {
				assistantText[r.ModelCallID] = r.Content
			}
		}
		if r.Kind == store.EventToolCompleted || r.Kind == store.EventToolFailed || r.Kind == store.EventToolRejected || r.Kind == store.EventToolCancelled {
			var result struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &result) == nil && result.ToolCallID != "" {
				results[result.ToolCallID] = string(result.Result)
			}
		}
	}
	emitted := map[string]bool{}
	var out []openai.ChatMessage
	for _, r := range records {
		switch r.Kind {
		case store.EventUserMessage:
			if !isolated {
				out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
			}
		case store.EventUserSteering:
			if !isolated && r.Seq <= steeringCursor {
				out = append(out, openai.ChatMessage{Role: "user", Content: "[用户最新修改]\n" + r.Content})
			}
		case store.EventAssistantMessage:
			if len(toolGroups[r.ModelCallID]) > 0 {
				continue
			}
			var meta struct {
				AgentRunID       string `json:"agent_run_id"`
				Child            bool   `json:"child"`
				ReasoningContent string `json:"reasoning_content"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if (isolated && meta.AgentRunID != agentRunID) || (!isolated && meta.Child) {
				continue
			}
			if meta.ReasoningContent == "" {
				meta.ReasoningContent = reasoning[r.ModelCallID]
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
		case store.EventToolRequested:
			group := toolGroups[r.ModelCallID]
			if len(group) == 0 || emitted[r.ModelCallID] {
				continue
			}
			emitted[r.ModelCallID] = true
			calls := make([]openai.ToolCall, 0, len(group))
			for _, call := range group {
				calls = append(calls, openai.ToolCall{ID: call.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: call.Name, Arguments: string(call.Arguments)}})
			}
			why := group[0].ReasoningContent
			if why == "" {
				why = reasoning[r.ModelCallID]
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: assistantText[r.ModelCallID], ReasoningContent: why, ToolCalls: calls})
			for _, call := range group {
				if result, ok := results[call.ToolCallID]; ok {
					out = append(out, openai.ChatMessage{Role: "tool", ToolCallID: call.ToolCallID, Content: result})
				}
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

func buildInstructionPrompt(kernel string, profile AgentProfile, project []string) string {
	var b strings.Builder
	b.WriteString("<RuntimeCore>\n")
	b.WriteString(strings.TrimSpace(kernel))
	b.WriteString("\n</RuntimeCore>\n")
	b.WriteString(`<RuntimeSafety>用户的最新明确修改优先于 Plan 和项目指令，但不能跳过文件写入、命令执行等真实权限检查。Runtime 独占阶段迁移、批准与 Checkpoint；工具结果才是真实副作用。一次最多四个独立只读 ToolCall；跨依赖动作等待结果。禁止用 Tool 维护阶段。</RuntimeSafety>`)
	b.WriteByte('\n')
	for i, value := range project {
		fmt.Fprintf(&b, "<ProjectInstructions source=\"AGENTS.md\" index=\"%d\">\n%s\n</ProjectInstructions>\n", i+1, strings.TrimSpace(value))
	}
	fmt.Fprintf(&b, "<AgentProfile id=\"%s\">\n%s\n</AgentProfile>", profile.ID, strings.TrimSpace(profile.Prompt))
	return b.String()
}

func buildStatePrompt(turn *store.Turn, policy ContextPolicy, checkpoint *store.Checkpoint, artifacts []store.Artifact, childRuns []store.AgentRun, activeAgentRunID string) string {
	var body strings.Builder
	if turn.CurrentObjective != "" && policy.IncludeObjective {
		body.WriteString("<CurrentObjective source=\"active-plan-or-user-request\">\n")
		body.WriteString(turn.CurrentObjective)
		body.WriteString("\n</CurrentObjective>\n")
		if len(turn.UserDecisions) > 0 {
			body.WriteString("<UserOverrides priority=\"above-plan-and-project-instructions\">\n")
			for _, text := range turn.UserDecisions {
				body.WriteString("- " + text + "\n")
			}
			body.WriteString("</UserOverrides>\n")
		}
	}
	fmt.Fprintf(&body, "<Execution mode=\"%s\" stage=\"%s\"/>\n", turn.TaskMode, turn.Stage)
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
			if artifact.Type == "plan" && artifact.Name == "active" {
				fmt.Fprintf(&body, "<ActivePlan version=\"%d\" source=\"CurrentObjective\"/>\n", active.Version)
			} else {
				fmt.Fprintf(&body, "<Artifact type=\"%s\" name=\"%s\" version=\"%d\">\n%s\n<Data>%s</Data>\n</Artifact>\n", artifact.Type, artifact.Name, active.Version, active.Content, string(active.Data))
			}
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
