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
	Provider  string    `json:"provider,omitempty"`
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
	mu      sync.RWMutex
}

func NewSessionStore(dataDir string) *SessionStore {
	_ = os.MkdirAll(dataDir, 0755)
	return &SessionStore{dataDir: dataDir}
}

func (s *SessionStore) sessionsPath() string {
	return filepath.Join(s.dataDir, "sessions.json")
}

func (s *SessionStore) readSessions() (*SessionsFile, error) {
	data, err := os.ReadFile(s.sessionsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &SessionsFile{Version: 2, Sessions: []Session{}}, nil
		}
		return nil, err
	}
	var file SessionsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Version < 2 {
		file.Version = 2
	}
	if file.Sessions == nil {
		file.Sessions = []Session{}
	}
	return &file, nil
}

func (s *SessionStore) writeSessions(file *SessionsFile) error {
	file.Version = 2
	data, err := json.MarshalIndent(file, "", "  ")
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	result := append([]Session(nil), file.Sessions...)
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *SessionStore) Get(id string) (*Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	for i := range file.Sessions {
		if file.Sessions[i].ID == id {
			copy := file.Sessions[i]
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("会话不存在")
}

func (s *SessionStore) Create(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readSessions()
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	now := time.Now().UTC()
	session := Session{
		ID:        "s_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	file.Sessions = append(file.Sessions, session)
	if err := s.writeSessions(file); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionStore) UpdateTitle(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	return s.update(id, func(session *Session) {
		session.Title = title
		session.Renamed = true
	})
}

func (s *SessionStore) UpdateAutoTitle(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	return s.update(id, func(session *Session) {
		if !session.Renamed {
			session.Title = title
		}
	})
}

func (s *SessionStore) UpdateSelection(id, provider, model string) error {
	return s.update(id, func(session *Session) {
		session.Provider = provider
		session.Model = model
	})
}

func (s *SessionStore) Touch(id string) error {
	return s.update(id, func(*Session) {})
}

func (s *SessionStore) update(id string, mutate func(*Session)) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readSessions()
	if err != nil {
		return err
	}
	for i := range file.Sessions {
		if file.Sessions[i].ID != id {
			continue
		}
		mutate(&file.Sessions[i])
		file.Sessions[i].UpdatedAt = time.Now().UTC()
		return s.writeSessions(file)
	}
	return fmt.Errorf("会话不存在")
}

func (s *SessionStore) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readSessions()
	if err != nil {
		return err
	}
	found := false
	filtered := file.Sessions[:0]
	for _, session := range file.Sessions {
		if session.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, session)
	}
	if !found {
		return fmt.Errorf("会话不存在")
	}
	file.Sessions = filtered
	if err := s.writeSessions(file); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dataDir, "Sessions", id+".jsonl"))
	_ = os.Remove(filepath.Join(s.dataDir, "Requests", id+".jsonl"))
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
