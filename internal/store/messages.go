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
	mu      sync.Mutex
}

func NewMessageStore(dataDir string) *MessageStore {
	_ = os.MkdirAll(filepath.Join(dataDir, "Sessions"), 0755)
	return &MessageStore{dataDir: dataDir}
}

func (s *MessageStore) messagePath(sessionID string) string {
	return filepath.Join(s.dataDir, "Sessions", sessionID+".jsonl")
}

func (s *MessageStore) Append(sessionID string, msg Message) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.ID == "" {
		msg.ID = "m_" + uuid.NewString()
	}
	if msg.TS == "" {
		msg.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return Message{}, err
	}
	f, err := os.OpenFile(s.messagePath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return Message{}, err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func (s *MessageStore) List(sessionID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.messagePath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return []Message{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var msgs []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err == nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs, scanner.Err()
}
