package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Artifact struct {
	ID            string            `json:"id"`
	SessionID     string            `json:"session_id"`
	TurnID        string            `json:"turn_id"`
	Type          string            `json:"type"`
	Name          string            `json:"name"`
	ActiveVersion int               `json:"active_version"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	Versions      []ArtifactVersion `json:"versions,omitempty"`
}

type ArtifactVersion struct {
	ID         string          `json:"id"`
	ArtifactID string          `json:"artifact_id"`
	Version    int             `json:"version"`
	Content    string          `json:"content"`
	Data       json.RawMessage `json:"data,omitempty"`
	CreatedBy  string          `json:"created_by,omitempty"`
	CreatedAt  string          `json:"created_at"`
}

func (s *Store) CreateArtifactVersion(sessionID, turnID, artifactType, name, content string, data json.RawMessage, createdBy string) (*Artifact, *ArtifactVersion, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	now := stamp(time.Now().UTC())
	var artifact Artifact
	err = tx.QueryRow(`SELECT id,session_id,turn_id,type,name,active_version,created_at,updated_at FROM artifacts WHERE turn_id=? AND type=? AND name=?`, turnID, artifactType, name).
		Scan(&artifact.ID, &artifact.SessionID, &artifact.TurnID, &artifact.Type, &artifact.Name, &artifact.ActiveVersion, &artifact.CreatedAt, &artifact.UpdatedAt)
	created := false
	if err == sql.ErrNoRows {
		artifact = Artifact{ID: "a_" + compactUUID(), SessionID: sessionID, TurnID: turnID, Type: artifactType, Name: name, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.Exec(`INSERT INTO artifacts(id,session_id,turn_id,type,name,active_version,created_at,updated_at) VALUES(?,?,?,?,?,0,?,?)`,
			artifact.ID, sessionID, turnID, artifactType, name, now, now); err != nil {
			return nil, nil, err
		}
		created = true
	} else if err != nil {
		return nil, nil, err
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM artifact_versions WHERE artifact_id=?`, artifact.ID).Scan(&next); err != nil {
		return nil, nil, err
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	version := &ArtifactVersion{
		ID: "av_" + compactUUID(), ArtifactID: artifact.ID, Version: next,
		Content: content, Data: data, CreatedBy: createdBy, CreatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO artifact_versions(id,artifact_id,version,content,data_json,created_by,created_at) VALUES(?,?,?,?,?,?,?)`,
		version.ID, artifact.ID, next, content, string(data), createdBy, now); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE artifacts SET active_version=?,updated_at=? WHERE id=?`, next, now, artifact.ID); err != nil {
		return nil, nil, err
	}
	// Active Plan 和有效目标必须在同一事务中更新，避免 Build 看到半更新状态。
	if artifactType == "plan" && name == "active" {
		if _, err := tx.Exec(`UPDATE turns SET current_objective=?,user_decisions_json='[]' WHERE id=?`, content, turnID); err != nil {
			return nil, nil, err
		}
	}
	artifact.ActiveVersion = next
	artifact.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	if created {
		_, _ = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventArtifactCreated, Data: eventData(map[string]any{
			"artifact_id": artifact.ID, "type": artifactType, "name": name,
		})})
	}
	_, _ = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventArtifactVersion, Data: eventData(map[string]any{
		"artifact_id": artifact.ID, "type": artifactType, "name": name, "version": next, "version_id": version.ID,
	})})
	return &artifact, version, nil
}

func (s *Store) ActivateArtifactVersion(artifactID string, version int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, turnID, artifactType, name string
	if err := tx.QueryRow(`SELECT session_id,turn_id,type,name FROM artifacts WHERE id=?`, artifactID).Scan(&sessionID, &turnID, &artifactType, &name); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("Artifact 不存在")
		}
		return err
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id=? AND version=?`, artifactID, version).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("Artifact Version 不存在")
	}
	if _, err := tx.Exec(`UPDATE artifacts SET active_version=?,updated_at=? WHERE id=?`, version, stamp(time.Now().UTC()), artifactID); err != nil {
		return err
	}
	if artifactType == "plan" && name == "active" {
		var content string
		if err := tx.QueryRow(`SELECT content FROM artifact_versions WHERE artifact_id=? AND version=?`, artifactID, version).Scan(&content); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE turns SET current_objective=?,user_decisions_json='[]' WHERE id=?`, content, turnID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = s.AppendEvent(Record{SessionID: sessionID, TurnID: turnID, Kind: EventArtifactActivated, Data: eventData(map[string]any{
		"artifact_id": artifactID, "type": artifactType, "name": name, "version": version,
	})})
	return err
}

func (s *Store) GetArtifact(artifactID string) (*Artifact, error) {
	var a Artifact
	err := s.db.QueryRow(`SELECT id,session_id,turn_id,type,name,active_version,created_at,updated_at FROM artifacts WHERE id=?`, artifactID).
		Scan(&a.ID, &a.SessionID, &a.TurnID, &a.Type, &a.Name, &a.ActiveVersion, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Artifact 不存在")
	}
	if err != nil {
		return nil, err
	}
	versions, err := s.ListArtifactVersions(artifactID)
	if err != nil {
		return nil, err
	}
	a.Versions = versions
	return &a, nil
}

func (s *Store) ListArtifacts(turnID string) ([]Artifact, error) {
	rows, err := s.db.Query(`SELECT id,session_id,turn_id,type,name,active_version,created_at,updated_at FROM artifacts WHERE turn_id=? ORDER BY created_at,id`, turnID)
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.SessionID, &a.TurnID, &a.Type, &a.Name, &a.ActiveVersion, &a.CreatedAt, &a.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		versions, err := s.ListArtifactVersions(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Versions = versions
	}
	return out, nil
}

func (s *Store) ListArtifactVersions(artifactID string) ([]ArtifactVersion, error) {
	rows, err := s.db.Query(`SELECT id,artifact_id,version,content,data_json,created_by,created_at FROM artifact_versions WHERE artifact_id=? ORDER BY version`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactVersion
	for rows.Next() {
		var v ArtifactVersion
		var raw string
		if err := rows.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Content, &raw, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Data = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ActiveArtifact(turnID, artifactType, name string) (*Artifact, *ArtifactVersion, error) {
	var a Artifact
	err := s.db.QueryRow(`SELECT id,session_id,turn_id,type,name,active_version,created_at,updated_at FROM artifacts WHERE turn_id=? AND type=? AND name=?`, turnID, artifactType, name).
		Scan(&a.ID, &a.SessionID, &a.TurnID, &a.Type, &a.Name, &a.ActiveVersion, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var v ArtifactVersion
	var raw string
	err = s.db.QueryRow(`SELECT id,artifact_id,version,content,data_json,created_by,created_at FROM artifact_versions WHERE artifact_id=? AND version=?`, a.ID, a.ActiveVersion).
		Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Content, &raw, &v.CreatedBy, &v.CreatedAt)
	if err != nil {
		return nil, nil, err
	}
	v.Data = json.RawMessage(raw)
	return &a, &v, nil
}
