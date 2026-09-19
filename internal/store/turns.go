package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	TurnRunning     = "running"
	TurnWaitingUser = "waiting_user"
	TurnPaused      = "paused"
	TurnCompleted   = "completed"
	TurnStopped     = "stopped"
	TurnCancelled   = "cancelled"
	TurnFailed      = "failed"
)

type Turn struct {
	ID               string   `json:"id"`
	SessionID        string   `json:"session_id"`
	TurnIndex        int      `json:"turn_index"`
	Status           string   `json:"status"`
	Objective        string   `json:"objective"` // 首条请求保留存档，不再直接注入 Build
	CurrentObjective string   `json:"current_objective"`
	UserDecisions    []string `json:"user_decisions"`
	TaskMode         string   `json:"task_mode"`
	Stage            string   `json:"stage"`
	ActiveAgent      string   `json:"active_agent"`
	ContextEpoch     int      `json:"context_epoch"`
	SteeringCursor   int64    `json:"steering_cursor"`
	RootAgentRunID   string   `json:"root_agent_run_id,omitempty"`
	ActiveAgentRunID string   `json:"active_agent_run_id,omitempty"`
	ActiveCheckpoint string   `json:"active_checkpoint_id,omitempty"`
	PauseRequested   bool     `json:"pause_requested"`
	StopRequested    bool     `json:"stop_requested"`
	CreatedAt        string   `json:"created_at"`
	CompletedAt      string   `json:"completed_at,omitempty"`
}

func (s *Store) BeginTaskTurn(sessionID, objective string) (*Turn, error) {
	if _, err := s.GetSession(sessionID); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var index int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(turn_index),0)+1 FROM turns WHERE session_id=?`, sessionID).Scan(&index); err != nil {
		return nil, err
	}
	turn := &Turn{
		ID:               "t_" + compactUUID(),
		SessionID:        sessionID,
		TurnIndex:        index,
		Status:           TurnRunning,
		Objective:        objective,
		CurrentObjective: objective,
		TaskMode:         "standard",
		Stage:            "plan",
		ActiveAgent:      "plan",
		ContextEpoch:     1,
		CreatedAt:        stamp(time.Now().UTC()),
	}
	if _, err := tx.Exec(`INSERT INTO turns(
		id,session_id,turn_index,status,created_at,objective,current_objective,active_agent,context_epoch
	) VALUES(?,?,?,?,?,?,?,?,?)`, turn.ID, sessionID, index, turn.Status, turn.CreatedAt, objective, objective, turn.ActiveAgent, turn.ContextEpoch); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO context_epochs(id,session_id,turn_id,epoch_index,reason,created_at) VALUES(?,?,?,?,?,?)`,
		"ep_"+compactUUID(), sessionID, turn.ID, 1, "turn.started", turn.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return turn, nil
}

func (s *Store) GetTurn(turnID string) (*Turn, error) {
	row := s.db.QueryRow(`SELECT id,session_id,turn_index,status,objective,current_objective,user_decisions_json,task_mode,stage,active_agent,context_epoch,steering_cursor,
		root_agent_run_id,active_agent_run_id,active_checkpoint_id,pause_requested,stop_requested,created_at,COALESCE(completed_at,'')
		FROM turns WHERE id=?`, turnID)
	turn, err := scanTurn(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Turn 不存在")
	}
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

func (s *Store) GetOpenTurn(sessionID string) (*Turn, error) {
	row := s.db.QueryRow(`SELECT id,session_id,turn_index,status,objective,current_objective,user_decisions_json,task_mode,stage,active_agent,context_epoch,steering_cursor,
		root_agent_run_id,active_agent_run_id,active_checkpoint_id,pause_requested,stop_requested,created_at,COALESCE(completed_at,'')
		FROM turns WHERE session_id=? AND status IN ('running','waiting_user','paused') ORDER BY turn_index DESC LIMIT 1`, sessionID)
	turn, err := scanTurn(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

func (s *Store) ListTurns(sessionID string) ([]Turn, error) {
	rows, err := s.db.Query(`SELECT id,session_id,turn_index,status,objective,current_objective,user_decisions_json,task_mode,stage,active_agent,context_epoch,steering_cursor,
		root_agent_run_id,active_agent_run_id,active_checkpoint_id,pause_requested,stop_requested,created_at,COALESCE(completed_at,'')
		FROM turns WHERE session_id=? ORDER BY turn_index`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		turn, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, turn)
	}
	return out, rows.Err()
}

func scanTurn(row rowScanner) (Turn, error) {
	var turn Turn
	var pause, stop int
	var decisions string
	err := row.Scan(&turn.ID, &turn.SessionID, &turn.TurnIndex, &turn.Status, &turn.Objective, &turn.CurrentObjective, &decisions, &turn.TaskMode, &turn.Stage, &turn.ActiveAgent,
		&turn.ContextEpoch, &turn.SteeringCursor, &turn.RootAgentRunID, &turn.ActiveAgentRunID,
		&turn.ActiveCheckpoint, &pause, &stop, &turn.CreatedAt, &turn.CompletedAt)
	if err == nil {
		err = json.Unmarshal([]byte(decisions), &turn.UserDecisions)
	}
	turn.PauseRequested = pause != 0
	turn.StopRequested = stop != 0
	return turn, err
}

func (s *Store) CompleteTurn(turnID, status string) error {
	if status == "" {
		status = TurnCompleted
	}
	return s.SetTurnStatus(turnID, status)
}

func (s *Store) SetTurnStatus(turnID, status string) error {
	terminal := status == TurnCompleted || status == TurnStopped || status == TurnCancelled || status == TurnFailed
	var completed any
	if terminal {
		completed = stamp(time.Now().UTC())
	}
	_, err := s.db.Exec(`UPDATE turns SET status=?, completed_at=? WHERE id=?`, status, completed, turnID)
	return err
}

func (s *Store) SetTurnAgent(turnID, agent, agentRunID string) error {
	_, err := s.db.Exec(`UPDATE turns SET active_agent=?, active_agent_run_id=? WHERE id=?`, agent, agentRunID, turnID)
	return err
}

func (s *Store) SetTurnRootRun(turnID, runID string) error {
	_, err := s.db.Exec(`UPDATE turns SET root_agent_run_id=?, active_agent_run_id=? WHERE id=?`, runID, runID, turnID)
	return err
}

func (s *Store) SetPauseRequested(turnID string, value bool) error {
	v := 0
	if value {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE turns SET pause_requested=? WHERE id=?`, v, turnID)
	return err
}

func (s *Store) SetStopRequested(turnID string, value bool) error {
	v := 0
	if value {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE turns SET stop_requested=? WHERE id=?`, v, turnID)
	return err
}

func (s *Store) ClearControlRequests(turnID string) error {
	_, err := s.db.Exec(`UPDATE turns SET pause_requested=0,stop_requested=0 WHERE id=?`, turnID)
	return err
}

func (s *Store) ConsumeSteering(turnID string, seq int64) error {
	_, err := s.db.Exec(`UPDATE turns SET steering_cursor=CASE WHEN steering_cursor<? THEN ? ELSE steering_cursor END WHERE id=?`, seq, seq, turnID)
	return err
}

func (s *Store) PendingSteering(turnID string, afterSeq int64) ([]Record, error) {
	rows, err := s.db.Query(`SELECT id,session_id,COALESCE(turn_id,''),seq,COALESCE(model_call_id,''),kind,content,data_json,created_at
		FROM records WHERE turn_id=? AND seq>? AND kind=? ORDER BY seq`, turnID, afterSeq, EventUserSteering)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *Store) NextModelCallIndex(turnID string) (int, error) {
	var index int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(call_index),0)+1 FROM model_calls WHERE turn_id=?`, turnID).Scan(&index)
	return index, err
}

func (s *Store) AdvanceContextEpoch(turnID, reason, checkpointID, prefixHash string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var sessionID string
	var current int
	if err := tx.QueryRow(`SELECT session_id,context_epoch FROM turns WHERE id=?`, turnID).Scan(&sessionID, &current); err != nil {
		return 0, err
	}
	next := current + 1
	now := stamp(time.Now().UTC())
	if _, err := tx.Exec(`INSERT INTO context_epochs(id,session_id,turn_id,epoch_index,reason,checkpoint_id,prefix_hash,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, "ep_"+compactUUID(), sessionID, turnID, next, reason, checkpointID, prefixHash, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE turns SET context_epoch=?,active_checkpoint_id=? WHERE id=?`, next, checkpointID, turnID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) UpdateEpochPrefixHash(turnID string, epoch int, prefixHash string) error {
	_, err := s.db.Exec(`UPDATE context_epochs SET prefix_hash=? WHERE turn_id=? AND epoch_index=?`, prefixHash, turnID, epoch)
	return err
}

func (s *Store) TurnCapabilities(turn *Turn, executionActive bool, hasApproval bool, hasPlan bool) map[string]bool {
	caps := map[string]bool{
		"can_pause":       false,
		"can_resume":      false,
		"can_stop":        false,
		"can_cancel":      false,
		"can_steer":       false,
		"can_start_build": false,
		"can_approve":     false,
		"can_reject":      false,
		"can_challenge":   false,
	}
	if turn == nil {
		return caps
	}
	caps["can_cancel"] = executionActive
	caps["can_pause"] = executionActive && turn.Status == TurnRunning
	caps["can_stop"] = turn.Status == TurnRunning || turn.Status == TurnPaused || turn.Status == TurnWaitingUser
	caps["can_steer"] = executionActive && (turn.Status == TurnRunning || turn.Status == TurnWaitingUser)
	caps["can_resume"] = turn.Status == TurnPaused
	caps["can_start_build"] = !executionActive && turn.Status == TurnWaitingUser && hasPlan && !hasApproval
	caps["can_approve"] = hasApproval && !executionActive
	caps["can_reject"] = hasApproval && !executionActive
	caps["can_challenge"] = hasApproval && !executionActive
	return caps
}

func eventData(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

// RecordUserDecision 保存尚未被新 Plan 吸收的后续明确输入；不猜测其语义，也不覆盖原始归档。
func (s *Store) RecordUserDecision(turnID, content string) error {
	turn, err := s.GetTurn(turnID)
	if err != nil {
		return err
	}
	turn.UserDecisions = append(turn.UserDecisions, content)
	if len(turn.UserDecisions) > 20 {
		turn.UserDecisions = turn.UserDecisions[len(turn.UserDecisions)-20:]
	}
	data, err := json.Marshal(turn.UserDecisions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE turns SET user_decisions_json=? WHERE id=?`, string(data), turnID)
	return err
}

// ReplaceCurrentObjective 由 Plan Artifact 的真实 active version 驱动，不靠提示词猜测覆盖顺序。
func (s *Store) ReplaceCurrentObjective(turnID, objective string) error {
	_, err := s.db.Exec(`UPDATE turns SET current_objective=?,user_decisions_json='[]' WHERE id=?`, objective, turnID)
	return err
}

// TransitionStage 由 Runtime 根据已执行操作推进，禁止模型写入虚假阶段。
func (s *Store) TransitionStage(turnID, stage string) error {
	var current string
	if err := s.db.QueryRow(`SELECT stage FROM turns WHERE id=?`, turnID).Scan(&current); err != nil {
		return err
	}
	allowed := map[string]map[string]bool{
		"plan": {"build": true}, "build": {"verify": true, "complete": true},
		"verify": {"build": true, "complete": true}, "complete": {},
	}
	if current == stage {
		return nil
	}
	if !allowed[current][stage] {
		return fmt.Errorf("非法状态迁移 %s → %s", current, stage)
	}
	_, err := s.db.Exec(`UPDATE turns SET stage=? WHERE id=? AND stage=?`, stage, turnID, current)
	return err
}

func (s *Store) SetTaskMode(turnID, mode string) error {
	if mode != "trivial" && mode != "standard" && mode != "complex" {
		return fmt.Errorf("非法任务模式")
	}
	_, err := s.db.Exec(`UPDATE turns SET task_mode=? WHERE id=?`, mode, turnID)
	return err
}
