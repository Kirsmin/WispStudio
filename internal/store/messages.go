package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MessageType 消息类型
type MessageType string

const (
	MessageTypeUser      MessageType = "user"
	MessageTypeAssistant MessageType = "assistant"
)

// Usage 用量信息
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// Message 单条消息
type Message struct {
	Type     MessageType `json:"type"`
	ID       string      `json:"id"`
	TS       string      `json:"ts"`
	Content  string      `json:"content"`
	Model    string      `json:"model,omitempty"`
	Thinking string      `json:"thinking,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	Usage    *Usage      `json:"usage,omitempty"`
	DurationMs int      `json:"duration_ms,omitempty"`
	Finish   string      `json:"finish,omitempty"`
}

type MessageStore struct {
	dataDir string
	mu      sync.Mutex
}

func NewMessageStore(dataDir string) *MessageStore {
	return &MessageStore{dataDir: dataDir}
}

func (s *MessageStore) messagePath(sessionID string) string {
	return filepath.Join(s.dataDir, "Sessions", sessionID+".jsonl")
}

func (s *MessageStore) ensureDir(sessionID string) error {
	dir := filepath.Join(s.dataDir, "Sessions")
	return os.MkdirAll(dir, 0755)
}

func (s *MessageStore) Append(sessionID string, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureDir(sessionID); err != nil {
		return err
	}
	if msg.ID == "" {
		msg.ID = "m_" + uuid.New().String()
	}
	if msg.TS == "" {
		msg.TS = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	path := s.messagePath(sessionID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(string(data) + "\n")
	return err
}

func (s *MessageStore) List(sessionID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.messagePath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Message{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, scanner.Err()
}
