package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RecordUser       = "user"
	RecordAssistant  = "assistant"
	RecordThinking   = "thinking"
	RecordToolCall   = "tool_call"
	RecordToolOutput = "tool_output"
	RecordError      = "error"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type ToolView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Message struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	TS         string     `json:"ts,omitempty"`
	Content    string     `json:"content"`
	Provider   string     `json:"provider,omitempty"`
	Model      string     `json:"model,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	Tools      []ToolView `json:"tools,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	DurationMs int        `json:"duration_ms,omitempty"`
	TTFTMs     int        `json:"ttft_ms,omitempty"`
	Finish     string     `json:"finish,omitempty"`
	Error      string     `json:"error,omitempty"`
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
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	Kind        string          `json:"kind"`
	Content     string          `json:"content,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

func (s *Store) BeginTurn(sessionID string) (string, error) {
	if _, err := s.GetSession(sessionID); err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var index int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(turn_index),0)+1 FROM turns WHERE session_id=?`, sessionID).Scan(&index); err != nil {
		return "", err
	}
	id := "t_" + compactUUID()
	now := stamp(time.Now().UTC())
	if _, err := tx.Exec(`INSERT INTO turns(id,session_id,turn_index,status,created_at) VALUES(?,?,?,?,?)`, id, sessionID, index, "running", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) CompleteTurn(turnID, status string) error {
	if status == "" {
		status = "completed"
	}
	_, err := s.db.Exec(`UPDATE turns SET status=?, completed_at=? WHERE id=?`, status, stamp(time.Now().UTC()), turnID)
	return err
}

func (s *Store) AppendUser(sessionID, turnID, content, provider, model, thinking string) (Record, error) {
	data, _ := json.Marshal(map[string]string{"provider": provider, "model": model, "thinking": thinking})
	return s.appendRecord(Record{SessionID: sessionID, TurnID: turnID, Kind: RecordUser, Content: content, Data: data})
}

func (s *Store) AppendThinking(sessionID, turnID, modelCallID, content string) error {
	if content == "" {
		return nil
	}
	_, err := s.appendRecord(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: RecordThinking, Content: content})
	return err
}

func (s *Store) AppendAssistant(sessionID, turnID, modelCallID, content string) error {
	if content == "" {
		return nil
	}
	_, err := s.appendRecord(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: RecordAssistant, Content: content})
	return err
}

func (s *Store) AppendError(sessionID, turnID, modelCallID, message string) error {
	_, err := s.appendRecord(Record{SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, Kind: RecordError, Content: message})
	return err
}

func (s *Store) AppendToolCallRecord(sessionID, turnID, modelCallID, toolCallID, name, raw string) error {
	data, _ := json.Marshal(map[string]string{"name": name, "raw_call": raw})
	_, err := s.appendRecord(Record{
		SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, ToolCallID: toolCallID,
		Kind: RecordToolCall, Data: data,
	})
	return err
}

func (s *Store) AppendToolOutputRecord(sessionID, turnID, modelCallID, toolCallID, output string) error {
	_, err := s.appendRecord(Record{
		SessionID: sessionID, TurnID: turnID, ModelCallID: modelCallID, ToolCallID: toolCallID,
		Kind: RecordToolOutput, Content: output,
	})
	return err
}

func (s *Store) appendRecord(record Record) (Record, error) {
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
	_, err = tx.Exec(`INSERT INTO records(id,session_id,turn_id,seq,model_call_id,tool_call_id,kind,content,data_json,created_at)
		VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?)`,
		record.ID, record.SessionID, nullString(record.TurnID), record.Seq,
		record.ModelCallID, record.ToolCallID, record.Kind, record.Content, string(record.Data), record.CreatedAt)
	if err != nil {
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

func (s *Store) ContextMessages(sessionID string) ([]ContextMessage, error) {
	rows, err := s.db.Query(`SELECT kind,content,COALESCE(model_call_id,''),COALESCE(tool_call_id,''),data_json FROM records WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextMessage
	lastAssistantCall := ""
	for rows.Next() {
		var kind, content, modelCallID, toolCallID, rawData string
		if err := rows.Scan(&kind, &content, &modelCallID, &toolCallID, &rawData); err != nil {
			return nil, err
		}
		switch kind {
		case RecordUser:
			out = append(out, ContextMessage{Role: "user", Content: content})
			lastAssistantCall = ""
		case RecordAssistant:
			out = append(out, ContextMessage{Role: "assistant", Content: content})
			lastAssistantCall = modelCallID
		case RecordToolCall:
			var data struct {
				RawCall string `json:"raw_call"`
			}
			_ = json.Unmarshal([]byte(rawData), &data)
			if data.RawCall == "" {
				continue
			}
			if len(out) > 0 && out[len(out)-1].Role == "assistant" && lastAssistantCall == modelCallID {
				if strings.TrimSpace(out[len(out)-1].Content) != "" {
					out[len(out)-1].Content += "\n\n"
				}
				out[len(out)-1].Content += data.RawCall
			} else {
				out = append(out, ContextMessage{Role: "assistant", Content: data.RawCall})
			}
			lastAssistantCall = modelCallID
		case RecordToolOutput:
			out = append(out, ContextMessage{Role: "system", Content: "<Output>" + content + "</Output>"})
			lastAssistantCall = ""
		}
	}
	return out, rows.Err()
}

func (s *Store) ListMessages(sessionID string) ([]Message, error) {
	calls, err := s.loadModelCalls(sessionID)
	if err != nil {
		return nil, err
	}
	tools, err := s.loadToolViews(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,kind,content,COALESCE(model_call_id,''),COALESCE(tool_call_id,''),data_json,created_at FROM records WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	assistantIndex := map[string]int{}
	for rows.Next() {
		var recordID, kind, content, modelCallID, toolCallID, rawData, created string
		if err := rows.Scan(&recordID, &kind, &content, &modelCallID, &toolCallID, &rawData, &created); err != nil {
			return nil, err
		}
		if kind == RecordUser {
			var data struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Thinking string `json:"thinking"`
			}
			_ = json.Unmarshal([]byte(rawData), &data)
			out = append(out, Message{ID: recordID, Type: "user", TS: created, Content: content, Provider: data.Provider, Model: data.Model, Thinking: data.Thinking})
			continue
		}
		if modelCallID == "" || (kind != RecordThinking && kind != RecordAssistant && kind != RecordToolCall && kind != RecordError) {
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
		case RecordThinking:
			out[idx].Reasoning += content
		case RecordAssistant:
			out[idx].Content += content
		case RecordToolCall:
			if tool, ok := tools[toolCallID]; ok {
				out[idx].Tools = append(out[idx].Tools, tool)
			}
		case RecordError:
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

func (s *Store) loadToolViews(sessionID string) (map[string]ToolView, error) {
	rows, err := s.db.Query(`SELECT id,tool_name,status,output,error FROM tool_calls WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ToolView{}
	for rows.Next() {
		var v ToolView
		if err := rows.Scan(&v.ID, &v.Name, &v.Status, &v.Output, &v.Error); err != nil {
			return nil, err
		}
		out[v.ID] = v
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
