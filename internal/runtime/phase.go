package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"wisp/internal/store"
)

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

// Phase 由 Runtime 在复杂任务开始及成功完成时自动管理，不暴露 LLM 状态工具。
func (t *ToolRuntime) beginAutomaticPhase(sessionID, turnID, runID string) error {
	state, err := t.loadTodo(turnID)
	if err != nil {
		return err
	}
	if len(state.Phases) > 0 {
		return nil
	}
	state.Phases = []phase{{ID: "execution", Title: "实现与验证", Status: "processing"}}
	_, err = t.saveTodo(ToolContext{SessionID: sessionID, TurnID: turnID, AgentRunID: runID}, state)
	return err
}

func (t *ToolRuntime) finishAutomaticPhase(sessionID, turnID, runID string) error {
	state, err := t.loadTodo(turnID)
	if err != nil {
		return err
	}
	if len(state.Phases) == 0 {
		return nil
	}
	for i := range state.Phases {
		state.Phases[i].Status = "completed"
	}
	_, err = t.saveTodo(ToolContext{SessionID: sessionID, TurnID: turnID, AgentRunID: runID}, state)
	return err
}
