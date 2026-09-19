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
func (r *ConstraintResolver) Resolve() (hard []string, scoped []string) {
	// 第一版将项目根 AGENTS.md 作为稳定 Hard Constraint；Scoped 保留独立出口。
	path := filepath.Join(r.workspaceRoot, "AGENTS.md")
	if data, err := os.ReadFile(path); err == nil && len(data) <= 128*1024 {
		hard = append(hard, string(data))
	}
	return hard, nil
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
	var prefix []openai.ChatMessage
	kernel := strings.TrimSpace(c.cfg.SystemPrompt)
	if kernel == "" {
		kernel = config.DefaultSystemPrompt
	}
	prefix = append(prefix, openai.ChatMessage{Role: "system", Content: "<WispKernel>\n" + kernel + "\n</WispKernel>"})
	prefix = append(prefix, openai.ChatMessage{Role: "system", Content: "<AgentProfile id=\"" + input.Profile.ID + "\">\n" + input.Profile.Prompt + "\n</AgentProfile>"})
	prefix = append(prefix, openai.ChatMessage{Role: "system", Content: `<RuntimeProtocol>
Timeline 是持久事实，Tool Result 才代表真实副作用。每轮最多提出一个 ToolCall，等待结果后再决定下一步；Runtime 按串行 Action Loop 执行。每个 Tool 完成后 Runtime 会在安全点处理 steering/pause/stop/approval。不要伪造工具结果、Approval 或 Artifact 版本。
</RuntimeProtocol>`})
	hard, scoped := c.resolver.Resolve()
	if len(hard) > 0 || len(scoped) > 0 {
		var b strings.Builder
		b.WriteString("<Constraints>\n")
		for i, v := range hard {
			fmt.Fprintf(&b, "[Hard %d]\n%s\n", i+1, strings.TrimSpace(v))
		}
		for i, v := range scoped {
			fmt.Fprintf(&b, "[Scoped %d]\n%s\n", i+1, strings.TrimSpace(v))
		}
		b.WriteString("</Constraints>")
		prefix = append(prefix, openai.ChatMessage{Role: "system", Content: b.String()})
	}
	policy, ok := c.profiles.ContextPolicy(input.Profile.ContextPolicy)
	if !ok {
		return CompiledContext{}, fmt.Errorf("未知 ContextPolicy: %s", input.Profile.ContextPolicy)
	}
	if turn.Objective != "" && policy.IncludeObjective {
		prefix = append(prefix, openai.ChatMessage{Role: "system", Content: "<Objective>\n" + turn.Objective + "\n</Objective>"})
	}

	var checkpoint *store.Checkpoint
	var artifacts []store.Artifact
	var err error
	fullState := policy.FullState
	if fullState {
		checkpoint, err = c.store.LatestCheckpoint(turn.ID)
	}
	if err != nil {
		return CompiledContext{}, err
	}
	if checkpoint != nil {
		prefix = append(prefix, openai.ChatMessage{Role: "system", Content: fmt.Sprintf("<Checkpoint kind=\"%s\">\nSummary:\n%s\nFacts:\n%s\n</Checkpoint>", checkpoint.Kind, checkpoint.Summary, string(checkpoint.Facts))})
	}
	if fullState {
		artifacts, err = c.store.ListArtifacts(turn.ID)
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
	for _, a := range artifacts {
		var active *store.ArtifactVersion
		for i := range a.Versions {
			if a.Versions[i].Version == a.ActiveVersion {
				v := a.Versions[i]
				active = &v
				break
			}
		}
		if active == nil {
			continue
		}
		prefix = append(prefix, openai.ChatMessage{Role: "system", Content: fmt.Sprintf("<Artifact type=\"%s\" name=\"%s\" version=\"%d\">\n%s\nData: %s\n</Artifact>", a.Type, a.Name, active.Version, active.Content, string(active.Data))})
	}
	if fullState {
		childRuns, runErr := c.store.ListAgentRuns(turn.ID)
		if runErr != nil {
			return CompiledContext{}, runErr
		}
		for _, run := range childRuns {
			if run.ParentRunID == "" || run.Status != "completed" || run.ID == input.AgentRunID {
				continue
			}
			prefix = append(prefix, openai.ChatMessage{Role: "system", Content: fmt.Sprintf("<SubAgentResult profile=\"%s\" run_id=\"%s\">\n%s\n</SubAgentResult>", run.ProfileID, run.ID, string(run.Result))})
		}
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
		debug := map[string]any{"agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch, "checkpoint_id": turn.ActiveCheckpoint, "active_artifacts": artifactRefs(artifacts), "timeline_after_seq": after}
		debugJSON, _ := json.Marshal(debug)
		return CompiledContext{Messages: messages, Tools: definitions, SystemPromptSnapshot: systemSnapshot(prefix), ContextHash: hashValue(messages), PrefixHash: prefixHash, DebugJSON: string(debugJSON), MaxSteeringSeq: maxSteer}, nil
	}
	messages = openai.NormalizeToolMessages(messages)
	debug := map[string]any{"agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch, "isolated": true}
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

		// 非 Fold 的历史 Turn 保留真实 Tool 对话，而不是只投影 user/assistant 文本。
		// DeepSeek 思考模式在请求携带 tools 时要求把历史 assistant 的
		// reasoning_content 原样回传；完整投影也能保证 reasoning/tool_calls/tool
		// 三者仍然处于同一协议序列中。
		// activeAgentRunID 传空表示历史 Turn：保留所有非 Child 的顶层 Agent 调用，
		// 包括 Plan -> Build transition 后产生的新顶层 AgentRun。
		out = append(out, timelineMessages(byTurn[turn.ID], turn.SteeringCursor, "")...)
	}
	return out, nil
}

type replayToolRequest struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	AgentRunID string          `json:"agent_run_id"`
}

type replayModelMeta struct {
	AgentRunID string `json:"agent_run_id"`
	Child      bool   `json:"child"`
}

type replayModelCall struct {
	Content   string
	Reasoning string
	ToolCalls []openai.ToolCall
}

type replayState struct {
	Calls              map[string]*replayModelCall
	IncludedModelCalls map[string]bool
	ToolCallModel      map[string]string
	ToolCallIDs        map[string]bool
}

func buildReplayState(records []store.Record, includeModel func(store.Record) bool, includeTool func(replayToolRequest) bool) replayState {
	state := replayState{
		Calls:              map[string]*replayModelCall{},
		IncludedModelCalls: map[string]bool{},
		ToolCallModel:      map[string]string{},
		ToolCallIDs:        map[string]bool{},
	}
	ensure := func(id string) *replayModelCall {
		call := state.Calls[id]
		if call == nil {
			call = &replayModelCall{}
			state.Calls[id] = call
		}
		return call
	}

	// 先收集模型文本与 reasoning，再收集 ToolCall。这样即使 Timeline 中
	// assistant.message 位于 tool.requested 之前，也能在回放时还原为同一条消息。
	for _, r := range records {
		if r.ModelCallID == "" || !includeModel(r) {
			continue
		}
		if r.Kind != store.EventModelReasoning && r.Kind != store.EventAssistantMessage {
			continue
		}
		state.IncludedModelCalls[r.ModelCallID] = true
		switch r.Kind {
		case store.EventModelReasoning:
			ensure(r.ModelCallID).Reasoning += r.Content
		case store.EventAssistantMessage:
			ensure(r.ModelCallID).Content += r.Content
		}
	}
	for i, r := range records {
		if r.Kind != store.EventToolRequested {
			continue
		}
		d, ok := parseReplayToolRequest(r)
		if !ok || d.ToolCallID == "" {
			continue
		}
		modelCallID := r.ModelCallID
		if modelCallID == "" {
			modelCallID = inferToolModelCallID(records, i, includeModel)
		}
		// 若能关联到一个已确认属于当前投影的 model_call，就以 model_call 为准。
		// 这能正确保留 Plan -> Build transition 后的顶层 ToolCall，同时排除
		// Explorer/Explain 等 Child Agent 的 ToolCall。无法关联时才退回 agent_run_id。
		if modelCallID == "" || !state.IncludedModelCalls[modelCallID] {
			if !includeTool(d) {
				continue
			}
		}
		state.ToolCallIDs[d.ToolCallID] = true
		state.ToolCallModel[d.ToolCallID] = modelCallID
		if modelCallID != "" {
			ensure(modelCallID).ToolCalls = append(ensure(modelCallID).ToolCalls, openai.ToolCall{
				ID: d.ToolCallID, Type: "function",
				Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)},
			})
		}
	}
	return state
}

func inferToolModelCallID(records []store.Record, index int, includeModel func(store.Record) bool) string {
	for i := index - 1; i >= 0; i-- {
		r := records[i]
		if (r.Kind == store.EventAssistantMessage || r.Kind == store.EventModelReasoning) && r.ModelCallID != "" && includeModel(r) {
			return r.ModelCallID
		}
		// 一次新的 Tool 请求若前面已经跨过上一轮 Tool 结果或新的用户输入，
		// 就不能再把更早的 model_call_id 猜到当前请求上。
		if isTerminalToolRecord(r.Kind) || r.Kind == store.EventToolRequested || r.Kind == store.EventUserMessage || r.Kind == store.EventUserSteering {
			break
		}
	}
	return ""
}

func parseReplayToolRequest(r store.Record) (replayToolRequest, bool) {
	var d replayToolRequest
	if json.Unmarshal(r.Data, &d) != nil {
		return d, false
	}
	return d, true
}

func parseReplayModelMeta(r store.Record) replayModelMeta {
	var meta replayModelMeta
	_ = json.Unmarshal(r.Data, &meta)
	return meta
}

func isTerminalToolRecord(kind string) bool {
	return kind == store.EventToolCompleted || kind == store.EventToolFailed || kind == store.EventToolRejected || kind == store.EventToolCancelled
}

func replayAssistantMessage(call *replayModelCall) openai.ChatMessage {
	if call == nil {
		return openai.ChatMessage{Role: "assistant"}
	}
	return openai.ChatMessage{
		Role:             "assistant",
		Content:          call.Content,
		ReasoningContent: call.Reasoning,
		ToolCalls:        append([]openai.ToolCall(nil), call.ToolCalls...),
	}
}

func agentRunTimelineMessages(records []store.Record, agentRunID string) []openai.ChatMessage {
	includeModel := func(r store.Record) bool {
		meta := parseReplayModelMeta(r)
		return meta.AgentRunID == agentRunID
	}
	includeTool := func(d replayToolRequest) bool { return d.AgentRunID == agentRunID }
	state := buildReplayState(records, includeModel, includeTool)
	emittedCalls := map[string]bool{}
	var out []openai.ChatMessage

	for _, r := range records {
		switch r.Kind {
		case store.EventModelReasoning:
			// reasoning_content 与 assistant 消息一起回放，不单独生成协议消息。
			continue
		case store.EventAssistantMessage:
			if !includeModel(r) {
				continue
			}
			if r.ModelCallID == "" {
				out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content})
				continue
			}
			if emittedCalls[r.ModelCallID] {
				continue
			}
			out = append(out, replayAssistantMessage(state.Calls[r.ModelCallID]))
			emittedCalls[r.ModelCallID] = true
		case store.EventToolRequested:
			d, ok := parseReplayToolRequest(r)
			if !ok || !includeTool(d) || d.ToolCallID == "" {
				continue
			}
			modelCallID := state.ToolCallModel[d.ToolCallID]
			if modelCallID != "" {
				if emittedCalls[modelCallID] {
					continue
				}
				out = append(out, replayAssistantMessage(state.Calls[modelCallID]))
				emittedCalls[modelCallID] = true
				continue
			}
			// 兼容没有 model_call_id 的旧 Timeline。此时至少保持 Tool 协议完整；
			// 新版本记录的 ToolRequested 都会带 model_call_id。
			out = append(out, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)},
			}}})
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && state.ToolCallIDs[d.ToolCallID] {
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
	includeModel := func(r store.Record) bool {
		meta := parseReplayModelMeta(r)
		if meta.Child {
			return false
		}
		return meta.AgentRunID == "" || activeAgentRunID == "" || meta.AgentRunID == activeAgentRunID
	}
	includeTool := func(d replayToolRequest) bool {
		if activeAgentRunID == "" {
			return d.AgentRunID == ""
		}
		return d.AgentRunID == "" || d.AgentRunID == activeAgentRunID
	}
	state := buildReplayState(records, includeModel, includeTool)
	emittedCalls := map[string]bool{}
	var out []openai.ChatMessage

	for _, r := range records {
		switch r.Kind {
		case store.EventUserMessage:
			out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
		case store.EventUserSteering:
			if r.Seq <= steeringCursor {
				out = append(out, openai.ChatMessage{Role: "user", Content: "[Steering]\n" + r.Content})
			}
		case store.EventModelReasoning:
			continue
		case store.EventAssistantMessage:
			if !includeModel(r) {
				continue
			}
			if r.ModelCallID == "" {
				out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content})
				continue
			}
			if emittedCalls[r.ModelCallID] {
				continue
			}
			out = append(out, replayAssistantMessage(state.Calls[r.ModelCallID]))
			emittedCalls[r.ModelCallID] = true
		case store.EventToolRequested:
			d, ok := parseReplayToolRequest(r)
			if !ok || !includeTool(d) || d.ToolCallID == "" {
				continue
			}
			modelCallID := state.ToolCallModel[d.ToolCallID]
			if modelCallID != "" {
				if emittedCalls[modelCallID] {
					continue
				}
				out = append(out, replayAssistantMessage(state.Calls[modelCallID]))
				emittedCalls[modelCallID] = true
				continue
			}
			out = append(out, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)},
			}}})
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.ToolCallID != "" && state.ToolCallIDs[d.ToolCallID] {
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
