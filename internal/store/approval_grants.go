package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func approvalFingerprint(name string, args json.RawMessage) (string, error) {
	if name == "write_file" {
		return "workspace-writes", nil
	}
	if name != "run_command" {
		return "", fmt.Errorf("该工具不支持批量授权")
	}
	var command struct {
		Command string `json:"command"`
		CWD     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &command); err != nil {
		return "", err
	}
	if command.Command == "" {
		return "", fmt.Errorf("没有可授权命令")
	}
	data, _ := json.Marshal(command)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (s *Store) HasApprovalGrant(turnID, tool string, args json.RawMessage) (bool, error) {
	fingerprint, err := approvalFingerprint(tool, args)
	if err != nil {
		return false, nil
	}
	var count int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM approval_grants WHERE turn_id=? AND tool_name=? AND fingerprint=?`, turnID, tool, fingerprint).Scan(&count)
	return count > 0, err
}

// Approval 的作用域仅限当前 Turn。Shell 仅能重放完全相同的命令/cwd。
func (s *Store) GrantApproval(approval *Approval) error {
	fingerprint, err := approvalFingerprint(approval.ToolName, approval.Args)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO approval_grants(id,session_id,turn_id,tool_name,fingerprint,created_at) VALUES(?,?,?,?,?,?)`,
		"grant_"+compactUUID(), approval.SessionID, approval.TurnID, approval.ToolName, fingerprint, stamp(time.Now().UTC()))
	return err
}
