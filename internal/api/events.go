package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
)

// SSE 参数。
const (
	eventReplayBatch = 500              // 每次从日志回读的批大小
	eventHeartbeat   = 15 * time.Second // 心跳间隔，防止代理超时断流
)

// eventDTO 是事件日志的协议投影：字段与 eventlog.Event 一一对应，
// JSON tag 使用 PascalCase。BlobText 是 blob 正文的只读解码结果。
type eventDTO struct {
	ID        string          `json:"ID"`
	Seq       int64           `json:"Seq"`
	Type      string          `json:"Type"`
	SessionID string          `json:"SessionID"`
	TurnID    string          `json:"TurnID"`
	Actor     string          `json:"Actor"`
	ParentID  string          `json:"ParentID"`
	CauseID   string          `json:"CauseID"`
	SubjectID string          `json:"SubjectID"`
	Blob      string          `json:"Blob"`
	Attrs     json.RawMessage `json:"Attrs"`
	TS        int64           `json:"TS"`
	BlobText  string          `json:"BlobText,omitempty"`
}

// projectEvent 把已落库事件投影为协议消息；数据源永远是事件日志。
func (s *Server) projectEvent(ctx context.Context, ev eventlog.Event) eventDTO {
	dto := eventDTO{
		ID:        ev.ID.String(),
		Seq:       ev.Seq,
		Type:      ev.Type,
		SessionID: ev.SessionID,
		TurnID:    ev.TurnID,
		Actor:     ev.Actor,
		ParentID:  ev.ParentID,
		CauseID:   ev.CauseID,
		SubjectID: ev.SubjectID,
		Blob:      ev.Blob,
		Attrs:     ev.Attrs,
		TS:        ev.TS,
	}
	if dto.Attrs == nil {
		dto.Attrs = json.RawMessage(`{}`)
	}
	if ev.Blob != "" {
		if data, err := s.cfg.Store.GetBlob(ctx, ev.Blob); err == nil && utf8.Valid(data) {
			dto.BlobText = string(data)
		}
	}
	return dto
}

// handleEventsSSE 是 SSE 端点：
//  1. 先订阅通知；
//  2. 从日志回放 seq > after_seq 的事件；
//  3. 之后每次通知都回读日志补齐，通知丢失也不会丢数据；
//  4. 断线重连带 after_seq（或 Last-Event-ID）即可完整补发且不重复。
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if _, err := ids.ParseSessionID(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "会话 ID 格式不合法: %v", err)
		return
	}

	afterSeq, ok := int64Param(w, r, "after_seq", 0)
	if !ok {
		return
	}
	// 浏览器 EventSource 断线重连会自动带 Last-Event-ID；与 after_seq 二选一。
	if r.URL.Query().Get("after_seq") == "" {
		if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
			v, err := parseNonNegativeInt64(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "Last-Event-ID 不合法: %v", err)
				return
			}
			afterSeq = v
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前连接不支持流式响应")
		return
	}

	// 先订阅再回放：回放与订阅之间写入的事件不会漏。
	notify, cancel, err := s.hub.Subscribe(sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "事件通知中心已关闭")
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	lastSeq := afterSeq
	writeEvent := func(dto eventDTO) error {
		data, err := json.Marshal(dto)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", dto.Seq, dto.Type, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// 回读并发送 seq > lastSeq 的事件，直到当前日志末尾。
	catchUp := func() error {
		for {
			evs, err := s.cfg.Store.ListBySession(ctx, sessionID, lastSeq, eventReplayBatch)
			if err != nil {
				return err
			}
			for _, ev := range evs {
				if ev.Seq <= lastSeq {
					continue
				}
				if err := writeEvent(s.projectEvent(ctx, ev)); err != nil {
					return err
				}
				lastSeq = ev.Seq
			}
			if len(evs) < eventReplayBatch {
				return nil
			}
		}
	}
	if err := catchUp(); err != nil {
		s.cfg.Logf("api: SSE 回放失败 session=%s: %v", sessionID, err)
		return
	}

	ticker := time.NewTicker(eventHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-notify:
			if !open {
				return // 通知中心关闭（服务退出）
			}
			// 合并短时间内的多次通知，然后一次性回读补齐。
			for {
				select {
				case <-notify:
				default:
					goto drained
				}
			}
		drained:
			if err := catchUp(); err != nil {
				s.cfg.Logf("api: SSE 补发失败 session=%s: %v", sessionID, err)
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleTrace 沿 cause_id 回溯因果链，返回从根因到目标事件的投影数组。
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	eventID := strings.TrimSpace(r.PathValue("eventID"))
	parsed, err := ids.ParseEventID(eventID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "事件 ID 格式不合法: %v", err)
		return
	}

	ctx := r.Context()
	ev, err := s.cfg.Store.Get(ctx, parsed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "事件不存在: %s", eventID)
			return
		}
		writeError(w, http.StatusInternalServerError, "读取事件失败: %v", err)
		return
	}

	// 从目标事件沿 cause_id 往根走，最后反转成“根 → 目标”。
	chain := []eventlog.Event{ev}
	seen := map[string]bool{ev.ID.String(): true}
	for cur := ev; cur.CauseID != ""; {
		if seen[cur.CauseID] {
			break // 防御异常数据成环
		}
		seen[cur.CauseID] = true
		cause, err := s.cfg.Store.Get(ctx, ids.EventID(cur.CauseID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break // cause 指向未落库事件，链路到此为止
			}
			writeError(w, http.StatusInternalServerError, "读取因果链失败: %v", err)
			return
		}
		chain = append(chain, cause)
		cur = cause
	}
	out := make([]eventDTO, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, s.projectEvent(ctx, chain[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseNonNegativeInt64 解析非负整数。
func parseNonNegativeInt64(raw string) (int64, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("必须是非负整数: %q", raw)
	}
	return v, nil
}

// truncateRunes 按 rune 截断，避免拆坏中文。
func truncateRunes(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" || max <= 0 {
		return text
	}
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max]) + "…"
}
