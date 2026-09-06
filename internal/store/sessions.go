package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Renamed   bool      `json:"renamed"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`SELECT id,title,renamed,provider,model,created_at,updated_at FROM sessions ORDER BY updated_at DESC, id`)
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

func (s *Store) GetSession(id string) (*Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT id,title,renamed,provider,model,created_at,updated_at FROM sessions WHERE id=?`, id)
	item, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("会话不存在")
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) CreateSession(title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	now := time.Now().UTC()
	item := Session{
		ID:        "s_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.db.Exec(`INSERT INTO sessions(id,title,renamed,provider,model,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		item.ID, item.Title, 0, "", "", stamp(now), stamp(now))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) UpdateTitle(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	return s.updateSession(id, `title=?, renamed=1`, title)
}

func (s *Store) UpdateAutoTitle(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	res, err := s.db.Exec(`UPDATE sessions SET title=?, updated_at=? WHERE id=? AND renamed=0`, title, stamp(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetSession(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpdateSelection(id, provider, model string) error {
	return s.updateSession(id, `provider=?, model=?`, provider, model)
}

func (s *Store) Touch(id string) error {
	res, err := s.db.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, stamp(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话不存在")
	}
	return nil
}

func (s *Store) updateSession(id, fields string, args ...any) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	args = append(args, stamp(time.Now().UTC()), id)
	res, err := s.db.Exec(`UPDATE sessions SET `+fields+`, updated_at=? WHERE id=?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话不存在")
	}
	return nil
}

func (s *Store) DeleteSession(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话不存在")
	}
	return nil
}

func GenerateTitle(firstMessage string) string {
	firstMessage = strings.Join(strings.Fields(strings.TrimSpace(firstMessage)), " ")
	if firstMessage == "" {
		return "新会话"
	}
	runes := []rune(firstMessage)
	if len(runes) > 20 {
		return string(runes[:20]) + "…"
	}
	return firstMessage
}

func validateSessionID(id string) error {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("非法会话ID")
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanSession(row rowScanner) (Session, error) {
	var item Session
	var renamed int
	var created, updated string
	err := row.Scan(&item.ID, &item.Title, &renamed, &item.Provider, &item.Model, &created, &updated)
	if err != nil {
		return item, err
	}
	item.Renamed = renamed != 0
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return item, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
