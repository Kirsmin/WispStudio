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

// Session 会话元数据
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
	return &SessionStore{dataDir: dataDir}
}

func (s *SessionStore) sessionsPath() string {
	return filepath.Join(s.dataDir, "sessions.json")
}

func (s *SessionStore) readSessions() (*SessionsFile, error) {
	path := s.sessionsPath()
	data, err := os.ReadFile(path)
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
	path := s.sessionsPath()
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
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
	// updated_at 倒序
	sort.Slice(sf.Sessions, func(i, j int) bool {
		return sf.Sessions[i].UpdatedAt.After(sf.Sessions[j].UpdatedAt)
	})
	return sf.Sessions, nil
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
			return &sf.Sessions[i], nil
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

	sess := Session{
		ID:        "s_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Title:     title,
		Renamed:   false,
		Model:     "",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	sf.Sessions = append(sf.Sessions, sess)
	if err := s.writeSessions(sf); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SessionStore) UpdateTitle(id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sf, err := s.readSessions()
	if err != nil {
		return err
	}
	for i := range sf.Sessions {
		if sf.Sessions[i].ID == id {
			sf.Sessions[i].Title = title
			sf.Sessions[i].Renamed = true
			sf.Sessions[i].UpdatedAt = time.Now().UTC()
			return s.writeSessions(sf)
		}
	}
	return fmt.Errorf("会话不存在")
}

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
	found := false
	filtered := sf.Sessions[:0]
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

	// 删除关联文件
	_ = os.Remove(filepath.Join(s.dataDir, "Sessions", id+".jsonl"))
	_ = os.Remove(filepath.Join(s.dataDir, "Requests", id+".jsonl"))
	return nil
}

// GenerateTitle 按 rune 截前 10 个字符
func GenerateTitle(firstMessage string) string {
	r := []rune(firstMessage)
	if len(r) > 10 {
		return string(r[:10]) + "…"
	}
	return string(r)
}
