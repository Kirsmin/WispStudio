package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
)

func (s *Store) SyncJSONL() error {
	target := filepath.Join(s.dataDir, "Sync")
	tmp := target + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "Sessions"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(tmp, "Requests"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(tmp, "Tools"), 0755); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sessions, err := exportSessions(tx)
	if err != nil {
		return err
	}
	if err := writeJSONLines(filepath.Join(tmp, "sessions.jsonl"), sessions); err != nil {
		return err
	}
	for _, session := range sessions {
		if err := exportSessionFiles(tx, tmp, session.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	_ = os.RemoveAll(target)
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	return nil
}

func exportSessions(tx *sql.Tx) ([]Session, error) {
	rows, err := tx.Query(`SELECT id,title,renamed,provider,model,created_at,updated_at FROM sessions ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func exportSessionFiles(tx *sql.Tx, root, sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := exportRecords(tx, filepath.Join(root, "Sessions", sessionID+".jsonl"), sessionID); err != nil {
		return err
	}
	if err := exportModelCalls(tx, filepath.Join(root, "Requests", sessionID+".jsonl"), sessionID); err != nil {
		return err
	}
	return exportToolCalls(tx, filepath.Join(root, "Tools", sessionID+".jsonl"), sessionID)
}

func exportRecords(tx *sql.Tx, path, sessionID string) error {
	rows, err := tx.Query(`SELECT id,session_id,COALESCE(turn_id,''),seq,COALESCE(model_call_id,''),COALESCE(tool_call_id,''),kind,content,data_json,created_at FROM records WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return writeRows(path, rows, func() (any, error) {
		var r Record
		var raw string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.TurnID, &r.Seq, &r.ModelCallID, &r.ToolCallID, &r.Kind, &r.Content, &raw, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Data = json.RawMessage(raw)
		return r, nil
	})
}

type modelCallExport struct {
	ID                   string `json:"id"`
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	CallIndex            int    `json:"call_index"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	Thinking             string `json:"thinking"`
	Status               string `json:"status"`
	FinishReason         string `json:"finish_reason,omitempty"`
	SystemPromptSnapshot string `json:"system_prompt_snapshot"`
	PromptTokens         int    `json:"prompt_tokens"`
	CompletionTokens     int    `json:"completion_tokens"`
	CachedTokens         int    `json:"cached_tokens"`
	ReasoningTokens      int    `json:"reasoning_tokens"`
	DurationMs           int    `json:"duration_ms"`
	TTFTMs               int    `json:"ttft_ms"`
	Error                string `json:"error,omitempty"`
	CreatedAt            string `json:"created_at"`
	CompletedAt          string `json:"completed_at,omitempty"`
}

func exportModelCalls(tx *sql.Tx, path, sessionID string) error {
	rows, err := tx.Query(`SELECT id,session_id,turn_id,call_index,provider,model,thinking,status,finish_reason,system_prompt_snapshot,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,duration_ms,ttft_ms,error,created_at,COALESCE(completed_at,'') FROM model_calls WHERE session_id=? ORDER BY created_at,call_index,id`, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return writeRows(path, rows, func() (any, error) {
		var v modelCallExport
		err := rows.Scan(&v.ID, &v.SessionID, &v.TurnID, &v.CallIndex, &v.Provider, &v.Model, &v.Thinking, &v.Status, &v.FinishReason, &v.SystemPromptSnapshot, &v.PromptTokens, &v.CompletionTokens, &v.CachedTokens, &v.ReasoningTokens, &v.DurationMs, &v.TTFTMs, &v.Error, &v.CreatedAt, &v.CompletedAt)
		return v, err
	})
}

type toolCallExport struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	TurnID      string `json:"turn_id"`
	ModelCallID string `json:"model_call_id"`
	ToolName    string `json:"tool_name"`
	RawCall     string `json:"raw_call"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	CompletedAt string `json:"completed_at,omitempty"`
}

func exportToolCalls(tx *sql.Tx, path, sessionID string) error {
	rows, err := tx.Query(`SELECT id,session_id,turn_id,model_call_id,tool_name,raw_call,input,output,status,error,created_at,COALESCE(completed_at,'') FROM tool_calls WHERE session_id=? ORDER BY created_at,id`, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return writeRows(path, rows, func() (any, error) {
		var v toolCallExport
		err := rows.Scan(&v.ID, &v.SessionID, &v.TurnID, &v.ModelCallID, &v.ToolName, &v.RawCall, &v.Input, &v.Output, &v.Status, &v.Error, &v.CreatedAt, &v.CompletedAt)
		return v, err
	})
}

func writeJSONLines[T any](path string, values []T) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, value := range values {
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeRows(path string, rows *sql.Rows, scan func() (any, error)) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for rows.Next() {
		value, err := scan()
		if err != nil {
			return err
		}
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return nil
}
