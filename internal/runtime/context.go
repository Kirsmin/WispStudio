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
	kernel := strings.TrimSpace(c.cfg.SystemPrompt)
	if kernel == "" {
		kernel = config.DefaultSystemPrompt
	}
	hard, scoped := c.resolver.Resolve()
	instructions := buildInstructionPrompt(kernel, input.Profile, hard, scoped)
	prefix := []openai.ChatMessage{{Role: "system", Content: instructions}}

	policy, ok := c.profiles.ContextPolicy(input.Profile.ContextPolicy)
	if !ok {
		return CompiledContext{}, fmt.Errorf("未知 ContextPolicy: %s", input.Profile.ContextPolicy)
	}

	var checkpoint *store.Checkpoint
	var artifacts []store.Artifact
	var childRuns []store.AgentRun
	var err error
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
			"agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch,
			"checkpoint_id": turn.ActiveCheckpoint, "active_artifacts": artifactRefs(artifacts), "timeline_after_seq": after,
			"prompt_layout": promptLayout(prefix, messages, definitions),
		}
		debugJSON, _ := json.Marshal(debug)
		return CompiledContext{Messages: messages, Tools: definitions, SystemPromptSnapshot: systemSnapshot(prefix), ContextHash: hashValue(messages), PrefixHash: prefixHash, DebugJSON: string(debugJSON), MaxSteeringSeq: maxSteer}, nil
	}
	messages = openai.NormalizeToolMessages(messages)
	debug := map[string]any{
		"agent": input.Profile.ID, "agent_run_id": input.AgentRunID, "context_epoch": turn.ContextEpoch, "isolated": true,
		"prompt_layout": promptLayout(prefix, messages, definitions),
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

func agentRunTimelineMessages(records []store.Record, agentRunID string) []openai.ChatMessage {
	toolCalls := map[string]bool{}
	toolCallsByModel := map[string]bool{}
	reasoning := reasoningContentByCall(records)
	assistantContent := map[string]string{}
	for _, r := range records {
		if r.Kind == store.EventAssistantMessage && r.ModelCallID != "" {
			var meta struct {
				AgentRunID string `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &meta) == nil && meta.AgentRunID == agentRunID {
				assistantContent[r.ModelCallID] = r.Content
			}
		}
		if r.Kind == store.EventToolRequested {
			var d struct {
				ToolCallID string `json:"tool_call_id"`
				AgentRunID string `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == agentRunID {
				toolCalls[d.ToolCallID] = true
				if r.ModelCallID != "" {
					toolCallsByModel[r.ModelCallID] = true
				}
			}
		}
	}
	var out []openai.ChatMessage
	lastReasoning := ""
	for _, r := range records {
		switch r.Kind {
		case store.EventModelReasoning:
			lastReasoning = r.Content
		case store.EventAssistantMessage:
			var meta struct {
				AgentRunID       string `json:"agent_run_id"`
				ReasoningContent string `json:"reasoning_content"`
			}
			if json.Unmarshal(r.Data, &meta) == nil && meta.AgentRunID == agentRunID {
				if toolCallsByModel[r.ModelCallID] {
					continue
				}
				if meta.ReasoningContent == "" {
					meta.ReasoningContent = reasoning[r.ModelCallID]
				}
				out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
			}
		case store.EventToolRequested:
			var d struct {
				ToolCallID       string          `json:"tool_call_id"`
				Name             string          `json:"name"`
				Arguments        json.RawMessage `json:"arguments"`
				AgentRunID       string          `json:"agent_run_id"`
				ReasoningContent string          `json:"reasoning_content"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == agentRunID {
				if d.ReasoningContent == "" {
					d.ReasoningContent = reasoning[r.ModelCallID]
				}
				if d.ReasoningContent == "" {
					d.ReasoningContent = lastReasoning
				}
				out = append(out, openai.ChatMessage{Role: "assistant", Content: assistantContent[r.ModelCallID], ReasoningContent: d.ReasoningContent, ToolCalls: []openai.ToolCall{{ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)}}}})
			}
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && toolCalls[d.ToolCallID] {
				out = append(out, openai.ChatMessage{Role: "tool", ToolCallID: d.ToolCallID, Content: string(d.Result)})
			}
		}
	}
	return out
}

func timelineMessages(records []store.Record, steeringCursor int64, activeAgentRunID string) []openai.ChatMessage {
	var out []openai.ChatMessage
	childCalls := map[string]bool{}
	toolCallsByModel := map[string]bool{}
	reasoning := reasoningContentByCall(records)
	assistantContent := map[string]string{}
	for _, r := range records {
		if r.Kind == store.EventAssistantMessage && r.ModelCallID != "" {
			var meta struct {
				Child bool `json:"child"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if !meta.Child {
				assistantContent[r.ModelCallID] = r.Content
			}
		}
		if r.Kind != store.EventToolRequested {
			continue
		}
		var d struct {
			ToolCallID string `json:"tool_call_id"`
			AgentRunID string `json:"agent_run_id"`
		}
		if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID != "" && d.AgentRunID != activeAgentRunID {
			childCalls[d.ToolCallID] = true
		} else if d.ToolCallID != "" && r.ModelCallID != "" {
			toolCallsByModel[r.ModelCallID] = true
		}
	}
	lastReasoning := ""
	for _, r := range records {
		switch r.Kind {
		case store.EventUserMessage:
			out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
		case store.EventUserSteering:
			if r.Seq <= steeringCursor {
				out = append(out, openai.ChatMessage{Role: "user", Content: "[Steering]\n" + r.Content})
			}
		case store.EventModelReasoning:
			lastReasoning = r.Content
		case store.EventAssistantMessage:
			var meta struct {
				Child            bool   `json:"child"`
				ReasoningContent string `json:"reasoning_content"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if meta.Child {
				continue
			}
			if toolCallsByModel[r.ModelCallID] {
				continue
			}
			if meta.ReasoningContent == "" {
				meta.ReasoningContent = reasoning[r.ModelCallID]
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content, ReasoningContent: meta.ReasoningContent})
		case store.EventToolRequested:
			var d struct {
				ToolCallID       string          `json:"tool_call_id"`
				Name             string          `json:"name"`
				Arguments        json.RawMessage `json:"arguments"`
				AgentRunID       string          `json:"agent_run_id"`
				ReasoningContent string          `json:"reasoning_content"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.ToolCallID != "" && !childCalls[d.ToolCallID] {
				if d.ReasoningContent == "" {
					d.ReasoningContent = reasoning[r.ModelCallID]
				}
				if d.ReasoningContent == "" {
					d.ReasoningContent = lastReasoning
				}
				out = append(out, openai.ChatMessage{Role: "assistant", Content: assistantContent[r.ModelCallID], ReasoningContent: d.ReasoningContent, ToolCalls: []openai.ToolCall{{ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)}}}})
			}
		case store.EventToolCompleted, store.EventToolFailed, store.EventToolRejected, store.EventToolCancelled:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.ToolCallID != "" && !childCalls[d.ToolCallID] {
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

func buildInstructionPrompt(kernel string, profile AgentProfile, hard, scoped []string) string {
	var b strings.Builder
	b.WriteString("<WispInstructions>\n")
	b.WriteString("<Core>\n")
	b.WriteString(strings.TrimSpace(kernel))
	b.WriteString("\n</Core>\n")
	b.WriteString(`<RuntimeProtocol>
Timeline 是持久事实，只有 Tool Result 能证明真实副作用。一次模型响应最多提出一个 ToolCall，拿到结果后再规划下一步。每个 Tool 完成后 Runtime 会处理 steering/pause/stop/approval。不要伪造 Tool Result、Approval、Artifact 或已完成状态。
</RuntimeProtocol>`)
	b.WriteByte('\n')
	if len(hard) > 0 || len(scoped) > 0 {
		b.WriteString("<Constraints>\n")
		for i, value := range hard {
			fmt.Fprintf(&b, "[Hard %d]\n%s\n", i+1, strings.TrimSpace(value))
		}
		for i, value := range scoped {
			fmt.Fprintf(&b, "[Scoped %d]\n%s\n", i+1, strings.TrimSpace(value))
		}
		b.WriteString("</Constraints>\n")
	}
	fmt.Fprintf(&b, "<AgentProfile id=\"%s\">\n%s\n</AgentProfile>\n", profile.ID, strings.TrimSpace(profile.Prompt))
	b.WriteString("</WispInstructions>")
	return b.String()
}

func buildStatePrompt(turn *store.Turn, policy ContextPolicy, checkpoint *store.Checkpoint, artifacts []store.Artifact, childRuns []store.AgentRun, activeAgentRunID string) string {
	var body strings.Builder
	if turn.Objective != "" && policy.IncludeObjective {
		body.WriteString("<Objective>\n")
		body.WriteString(turn.Objective)
		body.WriteString("\n</Objective>\n")
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
