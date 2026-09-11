package store

import (
	"time"
)

type ModelCallResult struct {
	Status     string
	Finish     string
	Usage      *Usage
	DurationMs int
	TTFTMs     int
	Error      string
}

func (s *Store) BeginModelCall(sessionID, turnID string, callIndex int, provider, model, thinking, systemPrompt string) (string, error) {
	id := "mc_" + compactUUID()
	_, err := s.db.Exec(`INSERT INTO model_calls(
		id,session_id,turn_id,call_index,provider,model,thinking,status,system_prompt_snapshot,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, sessionID, turnID, callIndex, provider, model, thinking, "running", systemPrompt, stamp(time.Now().UTC()))
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
