package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	closed  bool
}

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, true
}

func (s *SSEWriter) WriteEvent(event, data string) error {
	if s.closed {
		return fmt.Errorf("SSE connection already closed")
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\n", event); err != nil {
		s.closed = true
		return err
	}
	// SSE 要求多行 data 每行都带 data: 前缀；JSON 通常单行，但这里仍正确处理。
	for _, line := range splitLines(data) {
		if _, err := fmt.Fprintf(s.w, "data: %s\n", line); err != nil {
			s.closed = true
			return err
		}
	}
	if _, err := fmt.Fprint(s.w, "\n"); err != nil {
		s.closed = true
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteJSON(event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.WriteEvent(event, string(data))
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
