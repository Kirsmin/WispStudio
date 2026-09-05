package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RequestStore struct {
	mu  sync.Mutex
	dir string
}

func NewRequestStore(dataDir string) *RequestStore {
	dir := filepath.Join(dataDir, "Requests")
	_ = os.MkdirAll(dir, 0o755)
	return &RequestStore{dir: dir}
}

func (s *RequestStore) WriteRequest(sessionID, method, url, raw string) {
	s.write(sessionID, map[string]any{"type": "request", "method": method, "url": url, "body": raw})
}
func (s *RequestStore) WriteError(sessionID, message string) {
	s.write(sessionID, map[string]any{"type": "error", "error": message})
}
func (s *RequestStore) WriteAborted(sessionID string) {
	s.write(sessionID, map[string]any{"type": "aborted"})
}
func (s *RequestStore) WriteDone(sessionID string, usage map[string]int, finish string) {
	s.write(sessionID, map[string]any{"type": "done", "usage": usage, "finish": finish})
}
func (s *RequestStore) write(sessionID string, payload map[string]any) {
	if validateSessionID(sessionID) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	payload["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(s.dir, sessionID+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}
