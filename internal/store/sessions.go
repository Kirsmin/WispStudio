package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Renamed   bool      `json:"renamed"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SessionsFile struct {
	Version  int       `json:"version"`
	Sessions []Session `json:"sessions"`
}

type SessionStore struct {
	dataDir string
	mu      sync.Mutex
}

func NewSessionStore(dataDir string) *SessionStore {
	_ = os.MkdirAll(dataDir, 0755)
	return &SessionStore{dataDir: dataDir}
}

func (s *SessionStore) sessionsPath() string { return filepath.Join(s.dataDir, "sessions.json") }

func (s *SessionStore) readSessions() (*SessionsFile, error) {
	data, err := os.ReadFile(s.sessionsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &SessionsFile{Version: 1, Sessions: []Session{}}, nil
		}
		return nil, err
	}
	var sf SessionsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return &sf, nil
}

func (s *SessionStore) writeSessions(sf *SessionsFile) error {
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	path := s.sessionsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *SessionStore) List() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	result := append([]Session(nil), sf.Sessions...)
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *SessionStore) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	for i := range sf.Sessions {
		if sf.Sessions[i].ID == id {
			copy := sf.Sessions[i]
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("会话不存在")
}

func (s *SessionStore) Create(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := Session{
		ID: "s_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Title: strings.TrimSpace(title),
		CreatedAt: now, UpdatedAt: now,
	}
	if sess.Title == "" {
		sess.Title = "新会话"
	}
	sf.Sessions = append(sf.Sessions, sess)
	if err := s.writeSessions(sf); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SessionStore) setTitle(id, title string, renamed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return err
	}
	for i := range sf.Sessions {
		if sf.Sessions[i].ID == id {
			sf.Sessions[i].Title = title
			sf.Sessions[i].Renamed = renamed
			sf.Sessions[i].UpdatedAt = time.Now().UTC()
			return s.writeSessions(sf)
		}
	}
	return fmt.Errorf("会话不存在")
}

func (s *SessionStore) UpdateTitle(id, title string) error  { return s.setTitle(id, title, true) }
func (s *SessionStore) SetAutoTitle(id, title string) error { return s.setTitle(id, title, false) }

func (s *SessionStore) UpdateModel(id, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return err
	}
	for i := range sf.Sessions {
		if sf.Sessions[i].ID == id {
			sf.Sessions[i].Model = model
			sf.Sessions[i].UpdatedAt = time.Now().UTC()
			return s.writeSessions(sf)
		}
	}
	return fmt.Errorf("会话不存在")
}

func (s *SessionStore) Touch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return err
	}
	for i := range sf.Sessions {
		if sf.Sessions[i].ID == id {
			sf.Sessions[i].UpdatedAt = time.Now().UTC()
			return s.writeSessions(sf)
		}
	}
	return fmt.Errorf("会话不存在")
}

func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.readSessions()
	if err != nil {
		return err
	}
	filtered := make([]Session, 0, len(sf.Sessions))
	found := false
	for _, sess := range sf.Sessions {
		if sess.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, sess)
	}
	if !found {
		return fmt.Errorf("会话不存在")
	}
	sf.Sessions = filtered
	if err := s.writeSessions(sf); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dataDir, "Sessions", id+".jsonl"))
	_ = os.Remove(filepath.Join(s.dataDir, "Requests", id+".jsonl"))
	return nil
}

func GenerateTitle(firstMessage string) string {
	r := []rune(strings.TrimSpace(firstMessage))
	if len(r) > 18 {
		return string(r[:18]) + "…"
	}
	if len(r) == 0 {
		return "新会话"
	}
	return string(r)
}
