package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// Timeline 新事件统一使用 namespace.action。旧 kind 在读取时仍兼容映射。
	EventUserMessage       = "user.message"
	EventUserSteering      = "user.steering"
	EventAssistantMessage  = "assistant.message"
	EventModelReasoning    = "model.reasoning"
	EventRuntimeError      = "runtime.error"
	EventRuntimeStatus     = "runtime.status"
	EventRuntimeCommand    = "runtime.command"
	EventRuntimeHint       = "runtime.hint"
	EventToolRequested     = "tool.requested"
	EventToolStarted       = "tool.started"
	EventToolCompleted     = "tool.completed"
	EventToolFailed        = "tool.failed"
	EventToolRejected      = "tool.rejected"
	EventToolCancelled     = "tool.cancelled"
	EventArtifactCreated   = "artifact.created"
	EventArtifactVersion   = "artifact.version_created"
	EventArtifactActivated = "artifact.version_activated"
	EventApprovalRequested = "approval.requested"
	EventApprovalDecided   = "approval.decided"
	EventAgentStarted      = "agent.started"
	EventAgentCompleted    = "agent.completed"
	EventAgentFailed       = "agent.failed"
	EventCheckpointCreated = "checkpoint.created"
	EventContextFolded     = "context.folded"
	EventContextRestored   = "context.restored"

	// 旧常量保留给兼容 Projection 使用。
	RecordUser      = "user"
	RecordAssistant = "assistant"
	RecordThinking  = "thinking"
	RecordError     = "error"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type Message struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	TS         string `json:"ts,omitempty"`
	Content    string `json:"content"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	Reasoning  string `json:"reasoning,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	TTFTMs     int    `json:"ttft_ms,omitempty"`
	Finish     string `json:"finish,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ContextMessage struct {
	Role    string
	Content string
}

type Record struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	TurnID      string          `json:"turn_id,omitempty"`
	Seq         int64           `json:"seq"`
	ModelCallID string          `json:"model_call_id,omitempty"`
	Kind        string          `json:"kind"`
	Content     string          `json:"content,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

func (s *Store) AppendUser(sessionID, turnID, content, provider, model, thinking string) (Record, error) {
	data, _ := json.Marshal(map[string]string{"provider": provider, "model": model, "thinking": thinking})
	return s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventUserMessage, Content: content, Data: data})
}

func (s *Store) AppendSteering(sessionID, turnID, content string) (Record, error) {
	return s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventUserSteering, Content: content})
}

func (s *Store) AppendThinking(sessionID, turnID, modelCallID, content string) error {
	if content == "" {
		return nil
	}
	_, err := s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: EventModelReasoning, Content: content})
	return err
}

func (s *Store) AppendAssistant(sessionID, turnID, modelCallID, content string) error {
	if content == "" {
		return nil
	}
	_, err := s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: EventAssistantMessage, Content: content})
	return err
}

func (s *Store) AppendError(sessionID, turnID, modelCallID, message string) error {
	_, err := s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: EventRuntimeError, Content: message})
	return err
}

// AppendEvent 是 Timeline 唯一追加入口。已有 Record 永不更新；状态变化通过新事件 + 规范化状态表表达。
func (s *Store) AppendEvent(record Record) (Record, error) {
	if strings.TrimSpace(record.SessionID) == "" {
		return record, fmt.Errorf("Timeline Event 缺少 session_id")
	}
	if strings.TrimSpace(record.Kind) == "" {
		return record, fmt.Errorf("Timeline Event 缺少 kind")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return record, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM records WHERE session_id=?`, record.SessionID).Scan(&record.Seq); err != nil {
		return record, err
	}
	record.ID = "r_" + compactUUID()
	record.CreatedAt = stamp(time.Now().UTC())
	if len(record.Data) == 0 {
		record.Data = json.RawMessage(`{}`)
	}
	if _, err = tx.Exec(`INSERT INTO records(id,session_id,turn_id,seq,model_call_id,kind,content,data_json,created_at)
		VALUES(?,?,?,?,NULLIF(?,''),?,?,?,?)`,
		record.ID, record.SessionID, nullString(record.TurnID), record.Seq,
		record.ModelCallID, record.Kind, record.Content, string(record.Data), record.CreatedAt); err != nil {
		return record, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, record.CreatedAt, record.SessionID); err != nil {
		return record, err
	}
	if err := tx.Commit(); err != nil {
		return record, err
	}
	return record, nil
}

// Timeline 返回 Session 的不可变事件流。afterSeq 用于增量订阅；limit<=0 时使用安全上限。
func (s *Store) Timeline(sessionID string, afterSeq int64, limit int) ([]Record, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.Query(`SELECT id,session_id,COALESCE(turn_id,''),seq,COALESCE(model_call_id,''),kind,content,data_json,created_at
		FROM records WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *Store) TimelineByTurn(turnID string, afterSeq int64) ([]Record, error) {
	rows, err := s.db.Query(`SELECT id,session_id,COALESCE(turn_id,''),seq,COALESCE(model_call_id,''),kind,content,data_json,created_at
		FROM records WHERE turn_id=? AND seq>? ORDER BY seq`, turnID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

type recordRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanRecords(rows recordRows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		var r Record
		var raw string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.TurnID, &r.Seq, &r.ModelCallID, &r.Kind, &r.Content, &raw, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Kind = NormalizeRecordKind(r.Kind)
		r.Data = json.RawMessage(raw)
		out = append(out, r)
	}
	return out, rows.Err()
}

func NormalizeRecordKind(kind string) string {
	switch kind {
	case RecordUser:
		return EventUserMessage
	case RecordAssistant:
		return EventAssistantMessage
	case RecordThinking:
		return EventModelReasoning
	case RecordError:
		return EventRuntimeError
	default:
		return kind
	}
}

// ContextMessages 仅保留旧 API 兼容，不再供 Agent Runtime 直接构造上下文。
func (s *Store) ContextMessages(sessionID string) ([]ContextMessage, error) {
	records, err := s.Timeline(sessionID, 0, 5000)
	if err != nil {
		return nil, err
	}
	var out []ContextMessage
	for _, record := range records {
		switch record.Kind {
		case EventUserMessage, EventUserSteering:
			out = append(out, ContextMessage{Role: "user", Content: record.Content})
		case EventAssistantMessage:
			out = append(out, ContextMessage{Role: "assistant", Content: record.Content})
		}
	}
	return out, nil
}

// ListMessages 是 message-only 旧前端的兼容 Projection；事实来源始终是 Timeline。
func (s *Store) ListMessages(sessionID string) ([]Message, error) {
	calls, err := s.loadModelCalls(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,kind,content,COALESCE(model_call_id,''),data_json,created_at FROM records WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	assistantIndex := map[string]int{}
	for rows.Next() {
		var recordID, rawKind, content, modelCallID, rawData, created string
		if err := rows.Scan(&recordID, &rawKind, &content, &modelCallID, &rawData, &created); err != nil {
			return nil, err
		}
		kind := NormalizeRecordKind(rawKind)
		if kind == EventUserMessage || kind == EventUserSteering {
			var data struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Thinking string `json:"thinking"`
			}
			_ = json.Unmarshal([]byte(rawData), &data)
			out = append(out, Message{ID: recordID, Type: "user", TS: created, Content: content, Provider: data.Provider, Model: data.Model, Thinking: data.Thinking})
			continue
		}
		if modelCallID == "" || (kind != EventModelReasoning && kind != EventAssistantMessage && kind != EventRuntimeError) {
			continue
		}
		idx, exists := assistantIndex[modelCallID]
		if !exists {
			meta := calls[modelCallID]
			msg := Message{
				ID: modelCallID, Type: "assistant", TS: meta.CreatedAt,
				Provider: meta.Provider, Model: meta.Model, Thinking: meta.Thinking,
				Usage: meta.Usage, DurationMs: meta.DurationMs, TTFTMs: meta.TTFTMs,
				Finish: meta.Finish, Error: meta.Error,
			}
			out = append(out, msg)
			idx = len(out) - 1
			assistantIndex[modelCallID] = idx
		}
		switch kind {
		case EventModelReasoning:
			out[idx].Reasoning += content
		case EventAssistantMessage:
			out[idx].Content += content
		case EventRuntimeError:
			if out[idx].Error == "" {
				out[idx].Error = content
			}
		}
	}
	return out, rows.Err()
}

type modelCallView struct {
	Provider, Model, Thinking, Finish, Error, CreatedAt string
	DurationMs, TTFTMs                                  int
	Usage                                               *Usage
}

func (s *Store) loadModelCalls(sessionID string) (map[string]modelCallView, error) {
	rows, err := s.db.Query(`SELECT id,provider,model,thinking,finish_reason,error,created_at,duration_ms,ttft_ms,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens FROM model_calls WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]modelCallView{}
	for rows.Next() {
		var id string
		var v modelCallView
		var u Usage
		if err := rows.Scan(&id, &v.Provider, &v.Model, &v.Thinking, &v.Finish, &v.Error, &v.CreatedAt, &v.DurationMs, &v.TTFTMs, &u.PromptTokens, &u.CompletionTokens, &u.CachedTokens, &u.ReasoningTokens); err != nil {
			return nil, err
		}
		if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.CachedTokens != 0 || u.ReasoningTokens != 0 {
			v.Usage = &u
		}
		out[id] = v
	}
	return out, rows.Err()
}

func compactUUID() string { return strings.ReplaceAll(uuid.New().String(), "-", "") }
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
