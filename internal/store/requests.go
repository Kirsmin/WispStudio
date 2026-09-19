package store

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
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

// ModelCallSummary 只携带列表需要的字段；原始请求仅通过单次详情接口读取。
type ModelCallSummary struct {
	Cursor           int64    `json:"cursor"`
	ID               string   `json:"id"`
	TurnID           string   `json:"turn_id"`
	CallIndex        int      `json:"call_index"`
	Model            string   `json:"model"`
	Status           string   `json:"status"`
	Agent            string   `json:"agent"`
	Trigger          string   `json:"trigger"`
	ContextEpoch     int      `json:"context_epoch"`
	PrefixHash       string   `json:"prefix_hash"`
	ContextHash      string   `json:"context_hash"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	DurationMs       int      `json:"duration_ms"`
	TTFTMs           int      `json:"ttft_ms"`
	Tools            []string `json:"tools"`
	Error            string   `json:"error,omitempty"`
	CreatedAt        string   `json:"created_at"`
}

func (s *Store) ListModelCallSummaries(sessionID string, after, before int64, limit int) ([]ModelCallSummary, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	query := `SELECT rowid,id,turn_id,call_index,model,status,agent_run_id,context_epoch,context_hash,prefix_hash,
        context_debug_json,prompt_tokens,completion_tokens,duration_ms,ttft_ms,error,created_at
        FROM model_calls WHERE session_id=?`
	args := []any{sessionID}
	// 首次/向前翻页：最近一页；after：真正的增量，只取新调用。
	if after > 0 {
		query += ` AND rowid>? ORDER BY rowid ASC`
		args = append(args, after)
	}
	if after == 0 {
		if before > 0 {
			query += ` AND rowid<?`
			args = append(args, before)
		}
		query += ` ORDER BY rowid DESC`
	}
	query += ` LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]ModelCallSummary, 0, limit)
	for rows.Next() {
		var item ModelCallSummary
		var raw, run string
		if err := rows.Scan(&item.Cursor, &item.ID, &item.TurnID, &item.CallIndex, &item.Model, &item.Status, &run,
			&item.ContextEpoch, &item.ContextHash, &item.PrefixHash, &raw, &item.PromptTokens, &item.CompletionTokens, &item.DurationMs, &item.TTFTMs, &item.Error, &item.CreatedAt); err != nil {
			return nil, false, err
		}
		var debug struct {
			Agent   string `json:"agent"`
			Trigger string `json:"trigger"`
		}
		_ = json.Unmarshal([]byte(raw), &debug)
		item.Agent = debug.Agent
		item.Trigger = debug.Trigger
		item.Tools = []string{}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	if after == 0 {
		sort.Slice(out, func(i, j int) bool { return out[i].Cursor < out[j].Cursor })
	}
	if len(out) == 0 {
		return out, more, nil
	}
	// 一次查询拿当前页的工具名称，避免每张卡片再触发一次数据库/网络请求。
	ids := make([]any, len(out))
	placeholders := make([]string, len(out))
	byID := map[string]int{}
	for i, item := range out {
		ids[i] = item.ID
		placeholders[i] = "?"
		byID[item.ID] = i
	}
	query = `SELECT model_call_id,json_extract(data_json,'$.name') FROM records WHERE kind=? AND model_call_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY seq`
	args = append([]any{EventToolRequested}, ids...)
	toolRows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var id string
		var name sql.NullString
		if err := toolRows.Scan(&id, &name); err != nil {
			return nil, false, err
		}
		if index, ok := byID[id]; ok && name.Valid {
			out[index].Tools = append(out[index].Tools, name.String)
		}
	}
	return out, more, toolRows.Err()
}

type ModelCallStatus struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	DurationMs       int    `json:"duration_ms"`
	TTFTMs           int    `json:"ttft_ms"`
	Error            string `json:"error,omitempty"`
}

func (s *Store) RecentModelCallStatuses(sessionID string) ([]ModelCallStatus, error) {
	rows, err := s.db.Query(`SELECT id,status,prompt_tokens,completion_tokens,duration_ms,ttft_ms,error FROM model_calls WHERE session_id=? ORDER BY rowid DESC LIMIT 60`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCallStatus
	for rows.Next() {
		var v ModelCallStatus
		if err := rows.Scan(&v.ID, &v.Status, &v.PromptTokens, &v.CompletionTokens, &v.DurationMs, &v.TTFTMs, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// 详情按 Session + Call 双重过滤，避免跨 Session 访问请求快照。
func (s *Store) ModelCallDetail(sessionID, callID string) (*ModelCallDebug, error) {
	row := s.db.QueryRow(`SELECT id,turn_id,call_index,provider,model,thinking,status,finish_reason,
        system_prompt_snapshot,agent_run_id,context_epoch,context_hash,prefix_hash,context_debug_json,
        request_url,request_json,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,
        duration_ms,ttft_ms,error,created_at,COALESCE(completed_at,'')
        FROM model_calls WHERE session_id=? AND id=?`, sessionID, callID)
	var item ModelCallDebug
	var contextDebug, requestJSON string
	if err := row.Scan(&item.ID, &item.TurnID, &item.CallIndex, &item.Provider, &item.Model, &item.Thinking,
		&item.Status, &item.FinishReason, &item.SystemPromptSnapshot, &item.AgentRunID, &item.ContextEpoch,
		&item.ContextHash, &item.PrefixHash, &contextDebug, &item.RequestURL, &requestJSON,
		&item.PromptTokens, &item.CompletionTokens, &item.CachedTokens, &item.ReasoningTokens,
		&item.DurationMs, &item.TTFTMs, &item.Error, &item.CreatedAt, &item.CompletedAt); err != nil {
		return nil, err
	}
	item.ContextDebug = validRawJSON(contextDebug)
	item.Request = validRawJSON(requestJSON)
	return &item, nil
}

// 调试详情的底层轨迹与模型请求分别按需读取；只选与该调用匹配的 Tool 结果。
func (s *Store) ModelCallTrace(sessionID, callID string) ([]Record, error) {
	rows, err := s.db.Query(`SELECT r.id,r.session_id,COALESCE(r.turn_id,''),r.seq,COALESCE(r.model_call_id,''),r.kind,r.content,r.data_json,r.created_at
        FROM records r WHERE r.session_id=? AND (r.model_call_id=? OR (
            r.kind IN (?,?,?,?) AND EXISTS (
                SELECT 1 FROM records requested WHERE requested.session_id=? AND requested.model_call_id=? AND requested.kind=?
                AND json_extract(requested.data_json,'$.tool_call_id')=json_extract(r.data_json,'$.tool_call_id'))))
        ORDER BY r.seq`, sessionID, callID, EventToolCompleted, EventToolFailed, EventToolRejected, EventToolCancelled, sessionID, callID, EventToolRequested)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func validRawJSON(value string) json.RawMessage {
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
