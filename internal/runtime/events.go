package runtime

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt string          `json:"created_at"`
}

func NewEvent(eventType, sessionID, turnID string, value any) Event {
	data, _ := json.Marshal(value)
	return Event{Type: eventType, SessionID: sessionID, TurnID: turnID, Data: data, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

type EventHub struct {
	mu     sync.Mutex
	nextID int
	subs   map[string]map[int]chan Event
}

func NewEventHub() *EventHub { return &EventHub{subs: map[string]map[int]chan Event{}} }

func (h *EventHub) Publish(event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[event.SessionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *EventHub) Subscribe(sessionID string) (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan Event, 128)
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = map[int]chan Event{}
	}
	h.subs[sessionID][id] = ch
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if group := h.subs[sessionID]; group != nil {
			if c, ok := group[id]; ok {
				delete(group, id)
				close(c)
			}
			if len(group) == 0 {
				delete(h.subs, sessionID)
			}
		}
	}
	return ch, cancel
}
