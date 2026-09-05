package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Renamed   bool   `json:"renamed"`
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type SessionStore struct {
	mu      sync.RWMutex
	dataDir string
	path    string
}

func NewSessionStore(dataDir string) *SessionStore {
	_ = os.MkdirAll(dataDir, 0o755)
	return &SessionStore{dataDir: dataDir, path: filepath.Join(dataDir, "sessions.json")}
}

func (s *SessionStore) List() ([]Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	return list, nil
}

func (s *SessionStore) Get(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			copy := list[i]
			return &copy, nil
		}
	}
	return nil, errors.New("会话不存在")
}

func (s *SessionStore) Create(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	session := Session{ID: newID(), Title: title, CreatedAt: now, UpdatedAt: now}
	list = append(list, session)
	if err := s.writeLocked(list); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionStore) UpdateTitle(id, title string) error {
	return s.update(id, func(session *Session) {
		session.Title = strings.TrimSpace(title)
		session.Renamed = true
	})
}

func (s *SessionStore) UpdateAutoTitle(id, title string) error {
	return s.update(id, func(session *Session) {
		if !session.Renamed {
			session.Title = strings.TrimSpace(title)
		}
	})
}

func (s *SessionStore) UpdateModel(id, model string) error {
	return s.update(id, func(session *Session) { session.Model = model })
}

func (s *SessionStore) Touch(id string) error {
	return s.update(id, func(_ *Session) {})
}

func (s *SessionStore) update(id string, mutate func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		mutate(&list[i])
		list[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return s.writeLocked(list)
	}
	return errors.New("会话不存在")
}

func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		return err
	}
	found := false
	filtered := list[:0]
	for _, session := range list {
		if session.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, session)
	}
	if !found {
		return errors.New("会话不存在")
	}
	if err := s.writeLocked(filtered); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dataDir, "Sessions", id+".jsonl"))
	_ = os.Remove(filepath.Join(s.dataDir, "Requests", id+".jsonl"))
	return nil
}

func (s *SessionStore) readLocked() ([]Session, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []Session{}, nil
	}
	var list []Session
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *SessionStore) writeLocked(list []Session) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func GenerateTitle(message string) string {
	message = strings.TrimSpace(strings.Join(strings.Fields(message), " "))
	runes := []rune(message)
	if len(runes) > 28 {
		return string(runes[:28]) + "…"
	}
	if message == "" {
		return "新会话"
	}
	return message
}
