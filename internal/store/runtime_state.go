package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type AgentRun struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	TurnID      string          `json:"turn_id"`
	ParentRunID string          `json:"parent_run_id,omitempty"`
	ProfileID   string          `json:"profile_id"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	CreatedAt   string          `json:"created_at"`
	CompletedAt string          `json:"completed_at,omitempty"`
}

func (s *Store) BeginAgentRun(sessionID, turnID, parentRunID, profileID string) (*AgentRun, error) {
	run := &AgentRun{
		ID: "ar_" + compactUUID(), SessionID: sessionID, TurnID: turnID,
		ParentRunID: parentRunID, ProfileID: profileID, Status: "running", CreatedAt: stamp(time.Now().UTC()),
		Result: json.RawMessage(`{}`),
	}
	_, err := s.db.Exec(`INSERT INTO agent_runs(id,session_id,turn_id,parent_run_id,profile_id,status,result_json,created_at)
		VALUES(?,?,?,NULLIF(?,''),?,?,?,?)`, run.ID, sessionID, turnID, parentRunID, profileID, run.Status, string(run.Result), run.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, _ = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventAgentStarted, Data: eventData(map[string]any{
		"agent_run_id": run.ID, "parent_run_id": parentRunID, "profile_id": profileID,
	})})
	return run, nil
}

func (s *Store) FinishAgentRun(runID, status string, result any) error {
	if status == "" {
		status = "completed"
	}
	data := eventData(result)
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage(`{}`)
	}
	var sessionID, turnID, profileID string
	if err := s.db.QueryRow(`SELECT session_id,turn_id,profile_id FROM agent_runs WHERE id=?`, runID).Scan(&sessionID, &turnID, &profileID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE agent_runs SET status=?,result_json=?,completed_at=? WHERE id=?`, status, string(data), stamp(time.Now().UTC()), runID)
	if err != nil {
		return err
	}
	kind := EventAgentCompleted
	if status == "failed" {
		kind = EventAgentFailed
	}
	_, err = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: kind, Data: eventData(map[string]any{
		"agent_run_id": runID, "profile_id": profileID, "status": status, "result": json.RawMessage(data),
	})})
	return err
}

func (s *Store) GetAgentRun(runID string) (*AgentRun, error) {
	var run AgentRun
	var raw string
	err := s.db.QueryRow(`SELECT id,session_id,turn_id,COALESCE(parent_run_id,''),profile_id,status,result_json,created_at,COALESCE(completed_at,'') FROM agent_runs WHERE id=?`, runID).
		Scan(&run.ID, &run.SessionID, &run.TurnID, &run.ParentRunID, &run.ProfileID, &run.Status, &raw, &run.CreatedAt, &run.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("AgentRun 不存在")
	}
	if err != nil {
		return nil, err
	}
	run.Result = json.RawMessage(raw)
	return &run, nil
}

func (s *Store) ListAgentRuns(turnID string) ([]AgentRun, error) {
	rows, err := s.db.Query(`SELECT id,session_id,turn_id,COALESCE(parent_run_id,''),profile_id,status,result_json,created_at,COALESCE(completed_at,'') FROM agent_runs WHERE turn_id=? ORDER BY created_at,id`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRun
	for rows.Next() {
		var run AgentRun
		var raw string
		if err := rows.Scan(&run.ID, &run.SessionID, &run.TurnID, &run.ParentRunID, &run.ProfileID, &run.Status, &raw, &run.CreatedAt, &run.CompletedAt); err != nil {
			return nil, err
		}
		run.Result = json.RawMessage(raw)
		out = append(out, run)
	}
	return out, rows.Err()
}

type Approval struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	TurnID     string          `json:"turn_id"`
	AgentRunID string          `json:"agent_run_id,omitempty"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Args       json.RawMessage `json:"args"`
	Risk       string          `json:"risk"`
	RiskClass  string          `json:"risk_class,omitempty"`
	Status     string          `json:"status"`
	Decision   json.RawMessage `json:"decision,omitempty"`
	CreatedAt  string          `json:"created_at"`
	DecidedAt  string          `json:"decided_at,omitempty"`
}

func (s *Store) CreateApproval(sessionID, turnID, agentRunID, toolCallID, toolName string, args json.RawMessage, risk, riskClass string) (*Approval, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	a := &Approval{
		ID: "ap_" + compactUUID(), SessionID: sessionID, TurnID: turnID, AgentRunID: agentRunID,
		ToolCallID: toolCallID, ToolName: toolName, Args: args, Risk: risk, RiskClass: riskClass, Status: "pending", CreatedAt: stamp(time.Now().UTC()), Decision: json.RawMessage(`{}`),
	}
	_, err := s.db.Exec(`INSERT INTO approvals(id,session_id,turn_id,agent_run_id,tool_call_id,tool_name,args_json,risk,risk_class,status,decision_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, sessionID, turnID, agentRunID, toolCallID, toolName, string(args), risk, riskClass, a.Status, `{}`, a.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, _ = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventApprovalRequested, Data: eventData(a)})
	return a, nil
}

func (s *Store) DecideApproval(id, status string, decision any) (*Approval, error) {
	data := eventData(decision)
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage(`{}`)
	}
	now := stamp(time.Now().UTC())
	res, err := s.db.Exec(`UPDATE approvals SET status=?,decision_json=?,decided_at=? WHERE id=? AND status='pending'`, status, string(data), now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("Approval 不存在或已处理")
	}
	a, err := s.GetApproval(id)
	if err != nil {
		return nil, err
	}
	_, _ = s.AppendEvent(Record{SessionID: a.SessionID, TurnID: a.TurnID, Kind: EventApprovalDecided, Data: eventData(map[string]any{
		"approval_id": id, "status": status, "decision": json.RawMessage(data),
	})})
	return a, nil
}

func (s *Store) GetApproval(id string) (*Approval, error) {
	var a Approval
	var args, decision string
	err := s.db.QueryRow(`SELECT id,session_id,turn_id,agent_run_id,tool_call_id,tool_name,args_json,risk,risk_class,status,decision_json,created_at,COALESCE(decided_at,'') FROM approvals WHERE id=?`, id).
		Scan(&a.ID, &a.SessionID, &a.TurnID, &a.AgentRunID, &a.ToolCallID, &a.ToolName, &args, &a.Risk, &a.RiskClass, &a.Status, &decision, &a.CreatedAt, &a.DecidedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Approval 不存在")
	}
	if err != nil {
		return nil, err
	}
	a.Args = json.RawMessage(args)
	a.Decision = json.RawMessage(decision)
	return &a, nil
}

func (s *Store) PendingApproval(turnID string) (*Approval, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM approvals WHERE turn_id=? AND status='pending' ORDER BY created_at LIMIT 1`, turnID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetApproval(id)
}

func (s *Store) NextResolvedApproval(turnID string) (*Approval, error) {
	var id string
	err := s.db.QueryRow(`SELECT a.id FROM approvals a WHERE a.turn_id=? AND a.status IN ('approved','rejected')
		AND NOT EXISTS (
			SELECT 1 FROM records r
			WHERE r.turn_id=a.turn_id AND r.kind IN (?,?,?,?)
			AND json_extract(r.data_json,'$.tool_call_id')=a.tool_call_id
		) ORDER BY a.decided_at LIMIT 1`, turnID, EventToolCompleted, EventToolFailed, EventToolRejected, EventToolCancelled).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetApproval(id)
}

func (s *Store) ListApprovals(turnID string) ([]Approval, error) {
	rows, err := s.db.Query(`SELECT id,session_id,turn_id,agent_run_id,tool_call_id,tool_name,args_json,risk,risk_class,status,decision_json,created_at,COALESCE(decided_at,'') FROM approvals WHERE turn_id=? ORDER BY created_at`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var a Approval
		var args, decision string
		if err := rows.Scan(&a.ID, &a.SessionID, &a.TurnID, &a.AgentRunID, &a.ToolCallID, &a.ToolName, &args, &a.Risk, &a.RiskClass, &a.Status, &decision, &a.CreatedAt, &a.DecidedAt); err != nil {
			return nil, err
		}
		a.Args = json.RawMessage(args)
		a.Decision = json.RawMessage(decision)
		out = append(out, a)
	}
	return out, rows.Err()
}

type approvalDecision struct {
	Scope     string `json:"scope"`
	RiskClass string `json:"risk_class"`
	PhaseID   string `json:"phase_id"`
}

// ApprovalGrantAllows 只在用户明确声明的授权作用域内复用批准。
// phase 绑定 Runtime Phase ID；turn 授权持续到当前 Turn 结束。
func (s *Store) ApprovalGrantAllows(turnID, toolName, riskClass, phaseID string) (bool, error) {
	rows, err := s.db.Query(`SELECT tool_name,risk_class,decision_json FROM approvals WHERE turn_id=? AND status='approved' ORDER BY decided_at DESC`, turnID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, class, raw string
		if err := rows.Scan(&name, &class, &raw); err != nil {
			return false, err
		}
		if name != toolName || class != riskClass {
			continue
		}
		var decision approvalDecision
		if json.Unmarshal([]byte(raw), &decision) != nil {
			continue
		}
		switch decision.Scope {
		case "turn":
			return true, nil
		case "phase":
			if phaseID != "" && decision.PhaseID == phaseID {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

type Checkpoint struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	TurnID       string          `json:"turn_id"`
	ContextEpoch int             `json:"context_epoch"`
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	Facts        json.RawMessage `json:"facts"`
	TimelineSeq  int64           `json:"timeline_seq"`
	CreatedAt    string          `json:"created_at"`
}

func (s *Store) CreateCheckpoint(sessionID, turnID, kind, summary string, facts any) (*Checkpoint, error) {
	turn, err := s.GetTurn(turnID)
	if err != nil {
		return nil, err
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM records WHERE turn_id=?`, turnID).Scan(&seq); err != nil {
		return nil, err
	}
	data := eventData(facts)
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage(`{}`)
	}
	cp := &Checkpoint{ID: "cp_" + compactUUID(), SessionID: sessionID, TurnID: turnID, ContextEpoch: turn.ContextEpoch, Kind: kind, Summary: summary, Facts: data, TimelineSeq: seq, CreatedAt: stamp(time.Now().UTC())}
	_, err = s.db.Exec(`INSERT INTO checkpoints(id,session_id,turn_id,context_epoch,kind,summary,facts_json,timeline_seq,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		cp.ID, sessionID, turnID, cp.ContextEpoch, kind, summary, string(data), seq, cp.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, _ = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventCheckpointCreated, Data: eventData(cp)})
	return cp, nil
}

func (s *Store) LatestCheckpoint(turnID string) (*Checkpoint, error) {
	var cp Checkpoint
	var raw string
	err := s.db.QueryRow(`SELECT id,session_id,turn_id,context_epoch,kind,summary,facts_json,timeline_seq,created_at FROM checkpoints WHERE turn_id=? ORDER BY context_epoch DESC,created_at DESC LIMIT 1`, turnID).
		Scan(&cp.ID, &cp.SessionID, &cp.TurnID, &cp.ContextEpoch, &cp.Kind, &cp.Summary, &raw, &cp.TimelineSeq, &cp.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cp.Facts = json.RawMessage(raw)
	return &cp, nil
}

func (s *Store) FoldTurn(sessionID, turnID string) error {
	turn, err := s.GetTurn(turnID)
	if err != nil {
		return err
	}
	if turn.SessionID != sessionID || turn.Status != TurnCompleted {
		return fmt.Errorf("只能 Fold 已完成的 Turn")
	}
	if _, summary, err := s.ActiveArtifact(turnID, "summary", "final"); err != nil {
		return err
	} else if summary == nil {
		return fmt.Errorf("Turn 缺少 Final Summary")
	}
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_folds WHERE session_id=? AND turn_id=? AND active=1`, sessionID, turnID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return nil
	}
	var next int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(stack_index),0)+1 FROM context_folds WHERE session_id=?`, sessionID).Scan(&next); err != nil {
		return err
	}
	id := "fold_" + compactUUID()
	if _, err := s.db.Exec(`INSERT INTO context_folds(id,session_id,turn_id,stack_index,active,created_at) VALUES(?,?,?,?,1,?)`, id, sessionID, turnID, next, stamp(time.Now().UTC())); err != nil {
		return err
	}
	_, err = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventContextFolded, Data: eventData(map[string]any{"fold_id": id, "stack_index": next})})
	return err
}

func (s *Store) RestoreLatestFold(sessionID string) (string, error) {
	var id, turnID string
	err := s.db.QueryRow(`SELECT id,turn_id FROM context_folds WHERE session_id=? AND active=1 ORDER BY stack_index DESC LIMIT 1`, sessionID).Scan(&id, &turnID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("没有可 Restore 的 Fold")
	}
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`UPDATE context_folds SET active=0,restored_at=? WHERE id=?`, stamp(time.Now().UTC()), id); err != nil {
		return "", err
	}
	_, err = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventContextRestored, Data: eventData(map[string]any{"fold_id": id})})
	return turnID, err
}

func (s *Store) FoldedTurnIDs(sessionID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT turn_id FROM context_folds WHERE session_id=? AND active=1`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// AgentRunTimeline 返回某个 AgentRun 的可展开 Timeline，不把 sibling/parent transcript 混入。
func (s *Store) AgentRunTimeline(runID string) ([]Record, error) {
	run, err := s.GetAgentRun(runID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id FROM model_calls WHERE agent_run_id=?`, runID)
	if err != nil {
		return nil, err
	}
	callIDs := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		callIDs[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records, err := s.TimelineByTurn(run.TurnID, 0)
	if err != nil {
		return nil, err
	}
	toolIDs := map[string]bool{}
	var out []Record
	for _, r := range records {
		belongsToModelCall := callIDs[r.ModelCallID]
		belongsToToolCall := false
		if r.Kind == EventToolRequested {
			var d struct {
				AgentRunID string `json:"agent_run_id"`
				ToolCallID string `json:"tool_call_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == runID {
				toolIDs[d.ToolCallID] = true
				belongsToToolCall = true
			}
		}
		// 新版 tool.requested 会携带 model_call_id；仍需先记录 tool_call_id，
		// 否则后续 started/completed 事件会从 Child Timeline 中丢失。
		if belongsToModelCall || belongsToToolCall {
			out = append(out, r)
		}
	}
	for _, r := range records {
		if r.Kind == EventToolCompleted || r.Kind == EventToolFailed || r.Kind == EventToolRejected || r.Kind == EventToolCancelled || r.Kind == EventToolStarted {
			var d struct {
				ToolCallID string `json:"tool_call_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && toolIDs[d.ToolCallID] {
				out = append(out, r)
			}
		}
		if r.Kind == EventAgentStarted || r.Kind == EventAgentCompleted || r.Kind == EventAgentFailed {
			var d struct {
				AgentRunID string `json:"agent_run_id"`
			}
			if json.Unmarshal(r.Data, &d) == nil && d.AgentRunID == runID {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
