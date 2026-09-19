package store

import "time"

type ModelCallResult struct {
	Status     string
	Finish     string
	Usage      *Usage
	DurationMs int
	TTFTMs     int
	Error      string
}

type ModelCallContext struct {
	AgentRunID  string
	Epoch       int
	ContextHash string
	PrefixHash  string
	DebugJSON   string
}

// BeginModelCall 保留旧签名；新 Runtime 使用 BeginModelCallWithContext。
func (s *Store) BeginModelCall(sessionID, turnID string, callIndex int, provider, model, thinking, systemPrompt string) (string, error) {
	return s.BeginModelCallWithContext(sessionID, turnID, callIndex, provider, model, thinking, systemPrompt, ModelCallContext{})
}

func (s *Store) BeginModelCallWithContext(sessionID, turnID string, callIndex int, provider, model, thinking, systemPrompt string, ctx ModelCallContext) (string, error) {
	id := "mc_" + compactUUID()
	if ctx.Epoch <= 0 {
		ctx.Epoch = 1
	}
	if ctx.DebugJSON == "" {
		ctx.DebugJSON = "{}"
	}
	_, err := s.db.Exec(`INSERT INTO model_calls(
		id,session_id,turn_id,call_index,provider,model,thinking,status,system_prompt_snapshot,created_at,
		agent_run_id,context_epoch,context_hash,prefix_hash,context_debug_json
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sessionID, turnID, callIndex, provider, model, thinking, "running", systemPrompt, stamp(time.Now().UTC()),
		ctx.AgentRunID, ctx.Epoch, ctx.ContextHash, ctx.PrefixHash, ctx.DebugJSON)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) FinishModelCall(id string, result ModelCallResult) error {
	if result.Status == "" {
		result.Status = "completed"
	}
	var usage Usage
	if result.Usage != nil {
		usage = *result.Usage
	}
	_, err := s.db.Exec(`UPDATE model_calls SET
		status=?, finish_reason=?, prompt_tokens=?, completion_tokens=?, cached_tokens=?, reasoning_tokens=?,
		duration_ms=?, ttft_ms=?, error=?, completed_at=?
		WHERE id=?`,
		result.Status, result.Finish,
		usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, usage.ReasoningTokens,
		result.DurationMs, result.TTFTMs, result.Error, stamp(time.Now().UTC()), id)
	return err
}
