package store

import (
	"database/sql"
	"encoding/json"
)

type PendingToolCall struct {
	ToolCallID string          `json:"tool_call_id"`
	AgentRunID string          `json:"agent_run_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
}

// 只查未有终态 Result 的 requested event。批准、暂停与重启后继续按原序执行。
func (s *Store) NextPendingToolCall(turnID string) (*PendingToolCall, error) {
	var raw string
	err := s.db.QueryRow(`SELECT r.data_json FROM records r WHERE r.turn_id=? AND r.kind=?
        AND NOT EXISTS (SELECT 1 FROM records done WHERE done.turn_id=r.turn_id
            AND done.kind IN (?,?,?,?) AND json_extract(done.data_json,'$.tool_call_id')=json_extract(r.data_json,'$.tool_call_id'))
        ORDER BY r.seq LIMIT 1`, turnID, EventToolRequested, EventToolCompleted, EventToolFailed, EventToolRejected, EventToolCancelled).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var item PendingToolCall
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, err
	}
	return &item, nil
}
