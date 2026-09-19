package runtime

import (
	"context"
	"sync"
	"time"
)

type ExecutionStatus struct {
	Active    bool      `json:"active"`
	SessionID string    `json:"session_id,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

type runState struct {
	turnID  string
	cancel  context.CancelFunc
	started time.Time
}

// RunRegistry 是内存实时控制面；持久事实仍由 Store/Timeline 保存。
type RunRegistry struct {
	mu   sync.Mutex
	runs map[string]runState
}

func NewRunRegistry() *RunRegistry { return &RunRegistry{runs: map[string]runState{}} }

func (r *RunRegistry) Begin(sessionID, turnID string) (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runs[sessionID]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.runs[sessionID] = runState{turnID: turnID, cancel: cancel, started: time.Now().UTC()}
	return ctx, true
}
func (r *RunRegistry) End(sessionID, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.runs[sessionID]; ok && (turnID == "" || state.turnID == turnID) {
		delete(r.runs, sessionID)
	}
}
func (r *RunRegistry) Cancel(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[sessionID]
	if !ok {
		return false
	}
	state.cancel()
	return true
}
func (r *RunRegistry) Status(sessionID string) ExecutionStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[sessionID]
	if !ok {
		return ExecutionStatus{}
	}
	return ExecutionStatus{Active: true, SessionID: sessionID, TurnID: state.turnID, StartedAt: state.started}
}
