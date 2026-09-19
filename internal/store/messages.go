package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
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
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
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
		r.Data = json.RawMessage(raw)
		out = append(out, r)
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
