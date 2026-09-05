package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MessageTypeUser      = "user"
	MessageTypeAssistant = "assistant"
)

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
	Thinking   string `json:"thinking,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	TTFTMs     int    `json:"ttft_ms,omitempty"`
	Finish     string `json:"finish,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type MessageStore struct {
	mu  sync.RWMutex
	dir string
}

func NewMessageStore(dataDir string) *MessageStore {
	dir := filepath.Join(dataDir, "Sessions")
	_ = os.MkdirAll(dir, 0o755)
	return &MessageStore{dir: dir}
}

func (s *MessageStore) Append(sessionID string, message Message) (*Message, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if message.ID == "" {
		message.ID = newID()
	}
	if message.CreatedAt == "" {
		message.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path(sessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	return &message, nil
}

func (s *MessageStore) List(sessionID string) ([]Message, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := os.Open(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]Message, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var message Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func validateSessionID(sessionID string) error {
	if sessionID == "" || strings.Contains(sessionID, "..") || strings.ContainsAny(sessionID, "/\\") {
		return errors.New("非法会话ID")
	}
	return nil
}

func (s *MessageStore) path(sessionID string) string { return filepath.Join(s.dir, sessionID+".jsonl") }
