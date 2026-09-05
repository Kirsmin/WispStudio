package api

import (
	"context"
	"sync"
	"time"
)

type runState struct {
	cancel  context.CancelFunc
	started time.Time
}

type RunRegistry struct {
	mu   sync.Mutex
	runs map[string]runState
}

func NewRunRegistry() *RunRegistry {
	return &RunRegistry{runs: make(map[string]runState)}
}

// Begin 使用独立于浏览器连接的 Context。
// 即使用户刷新/切换会话，服务端也会继续生成并最终落盘；只有显式停止才会取消。
func (r *RunRegistry) Begin(sessionID string) (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runs[sessionID]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.runs[sessionID] = runState{cancel: cancel, started: time.Now().UTC()}
	return ctx, true
}

func (r *RunRegistry) End(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, sessionID)
}

func (r *RunRegistry) Cancel(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[sessionID]
	if !exists {
		return false
	}
	state.cancel()
	return true
}

func (r *RunRegistry) Status(sessionID string) (bool, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[sessionID]
	return exists, state.started
}
