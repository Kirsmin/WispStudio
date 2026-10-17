package eventlog

import (
	"errors"
	"sync"
)

// ErrHubClosed 表示通知中心已关闭。
var ErrHubClosed = errors.New("eventlog: 通知中心已关闭")

// Hub 是事件写入的 fan-out 通知中心。
//
// 它只做“有新事件”的提醒，不携带事件数据；订阅方收到提醒后必须回读
// 事件日志。通知通道容量为 1，写满时新通知被合并（丢弃），因此通知
// 可能丢失但不会阻塞 writer；只要还有一条提醒，订阅方重新回读即可补齐。
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[int]chan struct{}
	nextID int
	closed bool
}

// NewHub 创建通知中心。
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[int]chan struct{})}
}

// Subscribe 订阅某会话的事件通知。返回的取消函数可重复调用。
// 通知中心关闭后返回 ErrHubClosed。
func (h *Hub) Subscribe(sessionID string) (<-chan struct{}, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, ErrHubClosed
	}
	h.nextID++
	id := h.nextID
	ch := make(chan struct{}, 1)
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = make(map[int]chan struct{})
	}
	h.subs[sessionID][id] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			group := h.subs[sessionID]
			if group == nil {
				return
			}
			if c, ok := group[id]; ok {
				delete(group, id)
				close(c)
			}
			if len(group) == 0 {
				delete(h.subs, sessionID)
			}
		})
	}
	return ch, cancel, nil
}

// Publish 唤醒某会话的全部订阅者；无订阅者或通知中心已关闭时静默返回。
func (h *Hub) Publish(sessionID string) {
	if sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, ch := range h.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default: // 已有未消费的提醒，合并即可
		}
	}
}

// Close 关闭通知中心并唤醒/关闭全部订阅通道，可重复调用。
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for sessionID, group := range h.subs {
		for id, ch := range group {
			delete(group, id)
			close(ch)
		}
		delete(h.subs, sessionID)
	}
}
