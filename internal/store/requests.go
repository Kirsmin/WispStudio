package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

type ModelCallDebugSummary struct {
	DebugSeq         int64           `json:"debug_seq"`
	ID               string          `json:"id"`
	TurnID           string          `json:"turn_id"`
	CallIndex        int             `json:"call_index"`
	Provider         string          `json:"provider"`
	Model            string          `json:"model"`
	Thinking         string          `json:"thinking"`
	Status           string          `json:"status"`
	FinishReason     string          `json:"finish_reason,omitempty"`
	AgentRunID       string          `json:"agent_run_id,omitempty"`
	ContextEpoch     int             `json:"context_epoch"`
	ContextHash      string          `json:"context_hash,omitempty"`
	PrefixHash       string          `json:"prefix_hash,omitempty"`
	ContextDebug     json.RawMessage `json:"context_debug"`
	PromptTokens     int             `json:"prompt_tokens"`
	CompletionTokens int             `json:"completion_tokens"`
	CachedTokens     int             `json:"cached_tokens"`
	ReasoningTokens  int             `json:"reasoning_tokens"`
	DurationMs       int             `json:"duration_ms"`
	TTFTMs           int             `json:"ttft_ms"`
	Decision         string          `json:"decision,omitempty"`
	Error            string          `json:"error,omitempty"`
	CreatedAt        string          `json:"created_at"`
	CompletedAt      string          `json:"completed_at,omitempty"`
}

type ModelCallToolAction struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	Result     json.RawMessage `json:"result,omitempty"`
	Status     string          `json:"status,omitempty"`
}

type ModelCallDebugDetail struct {
	ModelCallDebugSummary
	SystemPromptSnapshot string                `json:"system_prompt_snapshot"`
	RequestURL           string                `json:"request_url,omitempty"`
	Request              json.RawMessage       `json:"request"`
	Reasoning            string                `json:"reasoning,omitempty"`
	AssistantOutput      string                `json:"assistant_output,omitempty"`
	ToolActions          []ModelCallToolAction `json:"tool_actions"`
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
		duration_ms=?, ttft_ms=?, error=?, completed_at=? WHERE id=?`,
		result.Status, result.Finish, usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, usage.ReasoningTokens,
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

// SessionModelCallDebugSummaries 只返回轻量摘要：新增记录外加极短尾部，让运行中的调用能刷新为完成态，同时避免每次轮询重传全部历史请求正文。
func (s *Store) SessionModelCallDebugSummaries(sessionID string, after int64) ([]ModelCallDebugSummary, int64, error) {
	rows, err := s.db.Query(`SELECT mc.rowid,mc.id,mc.turn_id,mc.call_index,mc.provider,mc.model,mc.thinking,mc.status,mc.finish_reason,
		mc.agent_run_id,mc.context_epoch,mc.context_hash,mc.prefix_hash,mc.context_debug_json,
		mc.prompt_tokens,mc.completion_tokens,mc.cached_tokens,mc.reasoning_tokens,mc.duration_ms,mc.ttft_ms,mc.error,mc.created_at,COALESCE(mc.completed_at,''),
		COALESCE((SELECT GROUP_CONCAT(json_extract(r.data_json,'$.name'), ', ') FROM records r WHERE r.model_call_id=mc.id AND r.kind='tool.requested'),'')
		FROM model_calls mc WHERE mc.session_id=? AND (
			mc.rowid>? OR mc.rowid IN (SELECT rowid FROM model_calls WHERE session_id=? ORDER BY rowid DESC LIMIT 3)
		) ORDER BY mc.rowid ASC`, sessionID, after, sessionID)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	var out []ModelCallDebugSummary
	next := after
	for rows.Next() {
		var item ModelCallDebugSummary
		var debug string
		if err := rows.Scan(&item.DebugSeq, &item.ID, &item.TurnID, &item.CallIndex, &item.Provider, &item.Model, &item.Thinking,
			&item.Status, &item.FinishReason, &item.AgentRunID, &item.ContextEpoch, &item.ContextHash, &item.PrefixHash, &debug,
			&item.PromptTokens, &item.CompletionTokens, &item.CachedTokens, &item.ReasoningTokens, &item.DurationMs, &item.TTFTMs,
			&item.Error, &item.CreatedAt, &item.CompletedAt, &item.Decision); err != nil {
			return nil, next, err
		}
		item.ContextDebug = validRawJSON(debug)
		if item.Decision == "" && item.Status == "completed" {
			item.Decision = "回复 / 等待下一步"
		}
		if item.DebugSeq > next {
			next = item.DebugSeq
		}
		out = append(out, item)
	}
	return out, next, rows.Err()
}

func (s *Store) GetModelCallDebug(sessionID, callID string) (*ModelCallDebugDetail, error) {
	var item ModelCallDebugDetail
	var debug, requestJSON string
	err := s.db.QueryRow(`SELECT rowid,id,turn_id,call_index,provider,model,thinking,status,finish_reason,
		agent_run_id,context_epoch,context_hash,prefix_hash,context_debug_json,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,
		duration_ms,ttft_ms,error,created_at,COALESCE(completed_at,''),system_prompt_snapshot,request_url,request_json
		FROM model_calls WHERE session_id=? AND id=?`, sessionID, callID).Scan(
		&item.DebugSeq, &item.ID, &item.TurnID, &item.CallIndex, &item.Provider, &item.Model, &item.Thinking, &item.Status, &item.FinishReason,
		&item.AgentRunID, &item.ContextEpoch, &item.ContextHash, &item.PrefixHash, &debug, &item.PromptTokens, &item.CompletionTokens,
		&item.CachedTokens, &item.ReasoningTokens, &item.DurationMs, &item.TTFTMs, &item.Error, &item.CreatedAt, &item.CompletedAt,
		&item.SystemPromptSnapshot, &item.RequestURL, &requestJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Model Call 不存在")
	}
	if err != nil {
		return nil, err
	}
	item.ContextDebug = validRawJSON(debug)
	item.Request = validRawJSON(requestJSON)

	records, err := s.db.Query(`SELECT kind,content,data_json FROM records WHERE model_call_id=? OR (
		turn_id=? AND kind IN ('tool.completed','tool.failed','tool.rejected','tool.cancelled') AND
		json_extract(data_json,'$.tool_call_id') IN (SELECT json_extract(data_json,'$.tool_call_id') FROM records WHERE model_call_id=? AND kind='tool.requested')
	) ORDER BY seq`, callID, item.TurnID, callID)
	if err != nil {
		return nil, err
	}
	defer records.Close()
	actionIndex := map[string]int{}
	for records.Next() {
		var kind, content, raw string
		if err := records.Scan(&kind, &content, &raw); err != nil {
			return nil, err
		}
		switch kind {
		case EventModelReasoning:
			item.Reasoning += content
		case EventAssistantMessage:
			item.AssistantOutput += content
		case EventToolRequested:
			var data struct {
				ToolCallID string          `json:"tool_call_id"`
				Name       string          `json:"name"`
				Arguments  json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal([]byte(raw), &data) == nil {
				actionIndex[data.ToolCallID] = len(item.ToolActions)
				item.ToolActions = append(item.ToolActions, ModelCallToolAction{ToolCallID: data.ToolCallID, Name: data.Name, Arguments: data.Arguments})
			}
		case EventToolCompleted, EventToolFailed, EventToolRejected, EventToolCancelled:
			var data struct {
				ToolCallID string          `json:"tool_call_id"`
				Result     json.RawMessage `json:"result"`
			}
			if json.Unmarshal([]byte(raw), &data) == nil {
				if index, ok := actionIndex[data.ToolCallID]; ok {
					item.ToolActions[index].Result = data.Result
					switch kind {
					case EventToolCompleted:
						item.ToolActions[index].Status = "completed"
					case EventToolFailed:
						item.ToolActions[index].Status = "failed"
					case EventToolRejected:
						item.ToolActions[index].Status = "rejected"
					case EventToolCancelled:
						item.ToolActions[index].Status = "cancelled"
					}
				}
			}
		}
	}
	if len(item.ToolActions) > 0 {
		names := make([]string, 0, len(item.ToolActions))
		for _, action := range item.ToolActions {
			names = append(names, action.Name)
		}
		item.Decision = joinStrings(names, ", ")
	} else if item.Status == "completed" {
		item.Decision = "回复 / 等待下一步"
	}
	return &item, records.Err()
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += sep
		}
		out += item
	}
	return out
}

func validRawJSON(value string) json.RawMessage {
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
