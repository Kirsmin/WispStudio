package api

import (
	"fmt"
	"net/http"
)

type SSE struct {
	w http.ResponseWriter
	f http.Flusher
}

func NewSSE(w http.ResponseWriter) (*SSE, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f.Flush()
	return &SSE{w, f}, true
}
func (s *SSE) Event(name, data string) error {
	_, e := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data)
	if e == nil {
		s.f.Flush()
	}
	return e
}
