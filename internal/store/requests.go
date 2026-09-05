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
	_ = os.MkdirAll(dataDir, 0755)
	return &RequestStore{dataDir: dataDir}
}

func (s *RequestStore) logPath(sessionID string) string {
	return filepath.Join(s.dataDir, "Requests", sessionID+".jsonl")
}

func (s *RequestStore) ensureDir(sessionID string) error {
	dir := filepath.Join(s.dataDir, "Requests")
	return os.MkdirAll(dir, 0755)
}

func (s *RequestStore) Write(sessionID string, log RequestLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureDir(sessionID); err != nil {
		return err
	}

	log.TS = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(log)
	if err != nil {
		return err
	}

	path := s.logPath(sessionID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(string(data) + "\n")
	return err
}

func (s *RequestStore) WriteRequest(sessionID string, method, url string, body map[string]any) error {
	// 脱敏 api_key
	if body != nil {
		body = deepCopyMap(body)
		// 如果 body 里有 api_key 相关字段，替换
		for k := range body {
			if strings.Contains(strings.ToLower(k), "api_key") {
				body[k] = "sk-***"
			}
		}
	}
	return s.Write(sessionID, RequestLog{
		Kind:   "request",
		Method: method,
		URL:    url,
		Body:   body,
	})
}

func (s *RequestStore) WriteUpstream(sessionID, raw string) error {
	return s.Write(sessionID, RequestLog{Kind: "upstream", Raw: raw})
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

func deepCopyMap(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(data, &out)
	return out
}
