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

type MessageType string

const (
	MessageTypeUser      MessageType = "user"
	MessageTypeAssistant MessageType = "assistant"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type Message struct {
	Type       MessageType `json:"type"`
	ID         string      `json:"id"`
	TS         string      `json:"ts"`
	Content    string      `json:"content"`
	Provider   string      `json:"provider,omitempty"`
	Model      string      `json:"model,omitempty"`
	Thinking   string      `json:"thinking,omitempty"`
	Reasoning  string      `json:"reasoning,omitempty"`
	Usage      *Usage      `json:"usage,omitempty"`
	DurationMs int         `json:"duration_ms,omitempty"`
	TTFTMs     int         `json:"ttft_ms,omitempty"`
	Finish     string      `json:"finish,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type MessageStore struct {
	dataDir string
	mu      sync.RWMutex
}

func NewMessageStore(dataDir string) *MessageStore {
	_ = os.MkdirAll(filepath.Join(dataDir, "Sessions"), 0755)
	return &MessageStore{dataDir: dataDir}
}

func (s *MessageStore) messagePath(sessionID string) string {
	return filepath.Join(s.dataDir, "Sessions", sessionID+".jsonl")
}

func (s *MessageStore) Append(sessionID string, message Message) (*Message, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if message.ID == "" {
		message.ID = "m_" + uuid.New().String()
	}
	if message.TS == "" {
		message.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.messagePath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
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

	file, err := os.Open(s.messagePath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return []Message{}, nil
		}
		return nil, err
	}
	defer file.Close()

	messages := make([]Message, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var message Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			// 单条历史损坏不应该让整个会话打不开。
			continue
		}
		messages = append(messages, message)
	}
	return messages, scanner.Err()
}
