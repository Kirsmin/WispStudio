package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RequestLog struct {
	TS      string         `json:"ts"`
	Kind    string         `json:"kind"`
	Method  string         `json:"method,omitempty"`
	URL     string         `json:"url,omitempty"`
	Body    map[string]any `json:"body,omitempty"`
	Raw     string         `json:"raw,omitempty"`
	Usage   map[string]int `json:"usage,omitempty"`
	Finish  string         `json:"finish,omitempty"`
	Message string         `json:"message,omitempty"`
}

type RequestStore struct {
	dataDir string
	mu      sync.Mutex
}

func NewRequestStore(dataDir string) *RequestStore {
	_ = os.MkdirAll(filepath.Join(dataDir, "Requests"), 0755)
	return &RequestStore{dataDir: dataDir}
}

func (s *RequestStore) logPath(sessionID string) string {
	return filepath.Join(s.dataDir, "Requests", sessionID+".jsonl")
}

func (s *RequestStore) Write(sessionID string, log RequestLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	log.TS = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(log)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *RequestStore) WriteRequest(sessionID, method, url, raw string) error {
	var body map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &body)
		body = maskSensitive(body)
	}
	return s.Write(sessionID, RequestLog{Kind: "request", Method: method, URL: url, Body: body, Raw: raw})
}
func (s *RequestStore) WriteDone(sessionID string, usage map[string]int, finish string) error {
	return s.Write(sessionID, RequestLog{Kind: "done", Usage: usage, Finish: finish})
}
func (s *RequestStore) WriteError(sessionID, message string) error {
	return s.Write(sessionID, RequestLog{Kind: "error", Message: message})
}
func (s *RequestStore) WriteAborted(sessionID string) error {
	return s.Write(sessionID, RequestLog{Kind: "aborted"})
}

func maskSensitive(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		low := strings.ToLower(k)
		if strings.Contains(low, "api_key") || strings.Contains(low, "apikey") || strings.Contains(low, "secret") || strings.Contains(low, "token") {
			out[k] = "***"
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			out[k] = maskSensitive(nested)
			continue
		}
		out[k] = v
	}
	return out
}
