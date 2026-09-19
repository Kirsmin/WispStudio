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
		for _, r := range byTurn[turn.ID] {
			switch r.Kind {
			case store.EventUserMessage, store.EventUserSteering:
				out = append(out, openai.ChatMessage{Role: "user", Content: r.Content})
			case store.EventAssistantMessage:
				var meta struct {
					Child bool `json:"child"`
				}
				_ = json.Unmarshal(r.Data, &meta)
				if !meta.Child {
					out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content})
				}
			}
		}
	}
	return out, nil
}

func agentRunTimelineMessages(records []store.Record, agentRunID string) []openai.ChatMessage {
	toolCalls := map[string]bool{}
	for _, r := range records {
		if r.Kind != store.EventToolRequested {
			continue
		}
		var d struct {
			ToolCallID string `json:"tool_call_id"`
			AgentRunID string `json:"agent_run_id"`
		}
		if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == agentRunID {
			toolCalls[d.ToolCallID] = true
		}
	}
	var out []openai.ChatMessage
	for _, r := range records {
		switch r.Kind {
		case store.EventAssistantMessage:
			var meta struct {
				AgentRunID string `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &meta) == nil && meta.AgentRunID == agentRunID {
				out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content})
			}
		case store.EventToolRequested:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Name       string          `json:"name"`
				Arguments  json.RawMessage `json:"arguments"`
				AgentRunID string          `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == agentRunID {
				out = append(out, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)}}}})
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
	for _, r := range records {
		if r.Kind != store.EventToolRequested {
			continue
		}
		var d struct {
			ToolCallID string `json:"tool_call_id"`
			AgentRunID string `json:"agent_run_id"`
		}
		if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID != "" && d.AgentRunID != activeAgentRunID {
			childCalls[d.ToolCallID] = true
		}
	}
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
				Child bool `json:"child"`
			}
			_ = json.Unmarshal(r.Data, &meta)
			if meta.Child {
				continue
			}
			out = append(out, openai.ChatMessage{Role: "assistant", Content: r.Content})
		case store.EventToolRequested:
			var d struct {
				ToolCallID string          `json:"tool_call_id"`
				Name       string          `json:"name"`
				Arguments  json.RawMessage `json:"arguments"`
				AgentRunID string          `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.ToolCallID != "" && !childCalls[d.ToolCallID] {
				out = append(out, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: d.ToolCallID, Type: "function", Function: openai.ToolFunction{Name: d.Name, Arguments: string(d.Arguments)}}}})
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
