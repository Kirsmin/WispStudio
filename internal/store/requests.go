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
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
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

// WriteRequest 记录一次完整的原始请求：方法、URL、完整请求体（raw 原文 + 结构化 body）
func (s *RequestStore) WriteRequest(sessionID string, method, url, raw string) error {
	var body map[string]any
	if raw != "" {
		// 解析出结构化 body 便于检索；raw 保留字节级原文实现"全量留痕"
		_ = json.Unmarshal([]byte(raw), &body)
		body = maskSensitive(body)
	}
	return s.Write(sessionID, RequestLog{
		Kind:   "request",
		Method: method,
		URL:    url,
		Body:   body,
		Raw:    raw,
	})
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

// maskSensitive 递归脱敏：键名含 api_key / secret 等字段的值一律打码
func maskSensitive(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		low := strings.ToLower(k)
		if strings.Contains(low, "api_key") || strings.Contains(low, "apikey") || strings.Contains(low, "secret") {
			out[k] = "sk-***"
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
