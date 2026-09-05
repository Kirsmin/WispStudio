package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
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
	ModelKey  string `json:"model_key,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}
type Message struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	Reasoning  string `json:"reasoning,omitempty"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	TTFTMs     int    `json:"ttft_ms,omitempty"`
	Finish     string `json:"finish,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  string `json:"created_at"`
}
type Store struct {
	mu           sync.RWMutex
	dataDir      string
	sessionsPath string
}

func New(dataDir string) *Store {
	_ = os.MkdirAll(filepath.Join(dataDir, "Sessions"), 0o755)
	_ = os.MkdirAll(filepath.Join(dataDir, "Requests"), 0o755)
	return &Store{dataDir: dataDir, sessionsPath: filepath.Join(dataDir, "sessions.json")}
}
func (s *Store) ListSessions() ([]Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, e := s.readSessions()
	sort.SliceStable(v, func(i, j int) bool { return v[i].UpdatedAt > v[j].UpdatedAt })
	return v, e
}
func (s *Store) GetSession(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, e := s.readSessions()
	if e != nil {
		return nil, e
	}
	for _, x := range v {
		if x.ID == id {
			y := x
			return &y, nil
		}
	}
	return nil, errors.New("会话不存在")
}
func (s *Store) CreateSession(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.readSessions()
	if e != nil {
		return nil, e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(title) == "" {
		title = "新会话"
	}
	x := Session{ID: uuid.NewString(), Title: title, CreatedAt: now, UpdatedAt: now}
	v = append(v, x)
	if e = s.writeSessions(v); e != nil {
		return nil, e
	}
	return &x, nil
}
func (s *Store) Touch(id, title, modelKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.readSessions()
	if e != nil {
		return e
	}
	for i := range v {
		if v[i].ID == id {
			if title != "" && !v[i].Renamed {
				v[i].Title = title
			}
			if modelKey != "" {
				v[i].ModelKey = modelKey
			}
			v[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			return s.writeSessions(v)
		}
	}
	return errors.New("会话不存在")
}
func (s *Store) Rename(id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.readSessions()
	if e != nil {
		return e
	}
	for i := range v {
		if v[i].ID == id {
			v[i].Title = title
			v[i].Renamed = true
			v[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			return s.writeSessions(v)
		}
	}
	return errors.New("会话不存在")
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.readSessions()
	if e != nil {
		return e
	}
	out := v[:0]
	found := false
	for _, x := range v {
		if x.ID == id {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return errors.New("会话不存在")
	}
	if e = s.writeSessions(out); e != nil {
		return e
	}
	_ = os.Remove(s.messagePath(id))
	_ = os.Remove(filepath.Join(s.dataDir, "Requests", id+".jsonl"))
	return nil
}
func (s *Store) AppendMessage(id string, m Message) (*Message, error) {
	if !safeID(id) {
		return nil, errors.New("非法会话ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(s.messagePath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	_, e = f.Write(append(b, '\n'))
	return &m, e
}
func (s *Store) Messages(id string) ([]Message, error) {
	if !safeID(id) {
		return nil, errors.New("非法会话ID")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, e := os.Open(s.messagePath(id))
	if errors.Is(e, os.ErrNotExist) {
		return []Message{}, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := []Message{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}
func (s *Store) WriteRequest(id string, v any) {
	if !safeID(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, e := json.Marshal(v)
	if e != nil {
		return
	}
	f, e := os.OpenFile(filepath.Join(s.dataDir, "Requests", id+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if e != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
func (s *Store) readSessions() ([]Session, error) {
	b, e := os.ReadFile(s.sessionsPath)
	if errors.Is(e, os.ErrNotExist) {
		return []Session{}, nil
	}
	if e != nil {
		return nil, e
	}
	if len(b) == 0 {
		return []Session{}, nil
	}
	var v []Session
	e = json.Unmarshal(b, &v)
	return v, e
}
func (s *Store) writeSessions(v []Session) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp := s.sessionsPath + ".tmp"
	if e = os.WriteFile(tmp, b, 0o644); e != nil {
		return e
	}
	return os.Rename(tmp, s.sessionsPath)
}
func (s *Store) messagePath(id string) string {
	return filepath.Join(s.dataDir, "Sessions", id+".jsonl")
}
func safeID(id string) bool {
	return id != "" && !strings.Contains(id, "..") && !strings.ContainsAny(id, "/\\")
}
func Title(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 28 {
		return string(r[:28]) + "…"
	}
	if text == "" {
		return "新会话"
	}
	return text
}
