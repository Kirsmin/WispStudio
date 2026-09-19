package store

import (
	"encoding/json"
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

type ModelCallContext struct {
	AgentRunID  string
	Epoch       int
	ContextHash string
	PrefixHash  string
	DebugJSON   string
}

type ModelCallDebug struct {
	ID                   string          `json:"id"`
	TurnID               string          `json:"turn_id"`
	CallIndex            int             `json:"call_index"`
	Provider             string          `json:"provider"`
	Model                string          `json:"model"`
	Thinking             string          `json:"thinking"`
	Status               string          `json:"status"`
	FinishReason         string          `json:"finish_reason,omitempty"`
	SystemPromptSnapshot string          `json:"system_prompt_snapshot"`
	AgentRunID           string          `json:"agent_run_id,omitempty"`
	ContextEpoch         int             `json:"context_epoch"`
	ContextHash          string          `json:"context_hash,omitempty"`
	PrefixHash           string          `json:"prefix_hash,omitempty"`
	ContextDebug         json.RawMessage `json:"context_debug"`
	RequestURL           string          `json:"request_url,omitempty"`
	Request              json.RawMessage `json:"request"`
	PromptTokens         int             `json:"prompt_tokens"`
	CompletionTokens     int             `json:"completion_tokens"`
	CachedTokens         int             `json:"cached_tokens"`
	ReasoningTokens      int             `json:"reasoning_tokens"`
	DurationMs           int             `json:"duration_ms"`
	TTFTMs               int             `json:"ttft_ms"`
	Error                string          `json:"error,omitempty"`
	CreatedAt            string          `json:"created_at"`
	CompletedAt          string          `json:"completed_at,omitempty"`
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

// SetModelCallRequest 保存真正发往上游的请求正文。Authorization 等敏感 Header 不落盘。
func (s *Store) SetModelCallRequest(id, requestURL, requestJSON string) error {
	if requestJSON == "" {
		requestJSON = "{}"
	}
	_, err := s.db.Exec(`UPDATE model_calls SET request_url=?, request_json=? WHERE id=?`, requestURL, requestJSON, id)
	return err
}

func (s *Store) SessionModelCallDebug(sessionID string) ([]ModelCallDebug, error) {
	rows, err := s.db.Query(`SELECT id,turn_id,call_index,provider,model,thinking,status,finish_reason,
		system_prompt_snapshot,agent_run_id,context_epoch,context_hash,prefix_hash,context_debug_json,
		request_url,request_json,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,
		duration_ms,ttft_ms,error,created_at,COALESCE(completed_at,'')
		FROM model_calls WHERE session_id=? ORDER BY created_at DESC,call_index DESC,id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCallDebug
	for rows.Next() {
		var item ModelCallDebug
		var contextDebug, requestJSON string
		if err := rows.Scan(&item.ID, &item.TurnID, &item.CallIndex, &item.Provider, &item.Model, &item.Thinking,
			&item.Status, &item.FinishReason, &item.SystemPromptSnapshot, &item.AgentRunID, &item.ContextEpoch,
			&item.ContextHash, &item.PrefixHash, &contextDebug, &item.RequestURL, &requestJSON,
			&item.PromptTokens, &item.CompletionTokens, &item.CachedTokens, &item.ReasoningTokens,
			&item.DurationMs, &item.TTFTMs, &item.Error, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		item.ContextDebug = validRawJSON(contextDebug)
		item.Request = validRawJSON(requestJSON)
		out = append(out, item)
	}
	return out, rows.Err()
}

func validRawJSON(value string) json.RawMessage {
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
