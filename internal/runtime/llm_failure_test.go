package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wisp/internal/compile"
	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
)

// errorRound 是 5 帧正文增量后接一个非法帧的脚本：
// 客户端已收到增量后一定会以流错误结束。
var errorRound = []string{
	`{"choices":[{"index":0,"delta":{"content":"一"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"二"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"三"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"四"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"五"}}]}`,
	`{"choices":[`,
}

// seededTurn 预置一条 user 消息与上下文，返回会话 ID、上下文行与回合参数。
func seededTurn(t *testing.T, store *eventlog.Store, sessionID, text string) (eventlog.ContextRow, TurnParams) {
	t.Helper()
	ctx := context.Background()
	seed, err := store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SessionID: sessionID,
		SubjectID: ids.NewMessageID().String(),
		Blob:      []byte(text),
		Attrs:     eventlog.MessageCreatedAttrs{Role: string(compile.RoleUser)},
	})
	if err != nil {
		t.Fatalf("写 seed 消息失败: %v", err)
	}
	ctxRow, err := store.CreateContext(ctx, eventlog.ContextInput{
		ForkAt: seed.ID,
	})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	return ctxRow, TurnParams{SessionID: sessionID, TurnID: ids.NewTurnID().String(), ContextID: ctxRow.ID}
}

// assistantCreated 返回回合内 role=assistant 的 message.created 事件（含截断标记）。
func assistantCreated(t *testing.T, evs []eventlog.Event) (eventlog.Event, eventlog.MessageCreatedAttrs) {
	t.Helper()
	for _, ev := range evs {
		if ev.Type != eventlog.TypeMessageCreated {
			continue
		}
		var attrs eventlog.MessageCreatedAttrs
		if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
			t.Fatalf("解析 message.created attrs 失败: %v", err)
		}
		if attrs.Role == string(compile.RoleAssistant) {
			return ev, attrs
		}
	}
	t.Fatalf("回合事件中缺少 assistant 的 message.created: %v", eventTypes(evs))
	return eventlog.Event{}, eventlog.MessageCreatedAttrs{}
}

// TestStepLLMPartialContentOnProviderError 验收 F1：
// provider 流出内容后报错必须补写 message.created(truncated=true) 并追加到上下文，
// 使下一回合 compile 不再看到相邻两条 user 消息。
func TestStepLLMPartialContentOnProviderError(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	fake := newScriptedProvider(t, [][]string{errorRound, e2eRound2})
	rt := newTestRuntime(t, store, t.TempDir(), fake)

	ctxRow, p := seededTurn(t, store, "sess_truncated", "请回答")

	if err := rt.RunTurn(ctx, p); err == nil {
		t.Fatal("provider 报错应让回合以错误结束")
	}
	ref, err := store.GetRef(ctx, TurnRefName(p.TurnID))
	if err != nil || !strings.HasPrefix(ref, refFailedPrefix) {
		t.Fatalf("回合指针 = %q (%v), 期望失败终态", ref, err)
	}

	evs, err := store.ListByTurn(ctx, p.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("读取回合事件失败: %v", err)
	}
	msgEv, attrs := assistantCreated(t, evs)
	if !attrs.Truncated {
		t.Fatalf("assistant message.created 应带 truncated 标记: %+v", attrs)
	}
	body, err := store.GetBlob(ctx, msgEv.Blob)
	if err != nil {
		t.Fatalf("读取截断消息正文失败: %v", err)
	}
	content, _ := aggregateDeltas(t, ctx, store, evs, msgEv.SubjectID)
	if string(body) != content || content != "一二三四五" {
		t.Fatalf("截断消息正文 = %q, delta 聚合 = %q, 期望 一二三四五", body, content)
	}

	// 下一回合：同一上下文追加新 user 消息后成功完成
	if _, err := AppendUserMessage(ctx, store, UserMessage{
		SessionID: p.SessionID,
		TurnID:    ids.NewTurnID().String(),
		ContextID: ctxRow.ID,
		Text:      "继续",
	}); err != nil {
		t.Fatalf("追加用户消息失败: %v", err)
	}
	p2 := TurnParams{SessionID: p.SessionID, TurnID: ids.NewTurnID().String(), ContextID: ctxRow.ID}
	if err := rt.RunTurn(ctx, p2); err != nil {
		t.Fatalf("下一回合应成功完成: %v", err)
	}
	if ref, err := store.GetRef(ctx, TurnRefName(p2.TurnID)); err != nil || ref != RefCompleted {
		t.Fatalf("下一回合指针 = %q (%v), 期望 %q", ref, err, RefCompleted)
	}
}

// TestStepLLMPartialContentOnCancel 验收 F1 的取消路径：
// 用户停止导致 ctx 取消时，已累积的正文仍要落盘，会话保持可继续。
func TestStepLLMPartialContentOnCancel(t *testing.T) {
	baseCtx := context.Background()
	store := openStore(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req provider.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if len(req.Messages) <= 1 {
			fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"半"}}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"截"}}]}`+"\n\n")
			flusher.Flush()
			close(started)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"好的。"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	rt, err := New(store, Options{
		WorkspaceRoot: t.TempDir(),
		ProviderID:    "deepseek",
		Model:         "deepseek/deepseek-chat",
		Client:        &provider.Client{BaseURL: srv.URL, MaxRetries: 0},
	})
	if err != nil {
		t.Fatalf("构造运行时失败: %v", err)
	}

	ctxRow, p := seededTurn(t, store, "sess_cancel", "请回答")
	runCtx, cancel := context.WithCancel(baseCtx)
	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(runCtx, p) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("假 provider 未收到请求")
	}
	// 等已落盘的增量到达客户端内存后再停止，模拟用户看到输出后点停止。
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := store.ListByTurn(baseCtx, p.TurnID, 0, 0)
		if err != nil {
			t.Fatalf("读取回合事件失败: %v", err)
		}
		if len(filterTypes(evs, eventlog.TypeMessageDelta)) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 message.delta 超时: %v", eventTypes(evs))
		}
		time.Sleep(10 * time.Millisecond)
	}
	rt.Interrupt("stopped")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("停止路径 RunTurn 不应报错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunTurn 未在取消后返回")
	}
	ref, err := store.GetRef(baseCtx, TurnRefName(p.TurnID))
	if err != nil || ref != refFailedPrefix+"stopped" {
		t.Fatalf("回合指针 = %q (%v), 期望 %q", ref, err, refFailedPrefix+"stopped")
	}

	evs, err := store.ListByTurn(baseCtx, p.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("读取回合事件失败: %v", err)
	}
	msgEv, attrs := assistantCreated(t, evs)
	if !attrs.Truncated {
		t.Fatalf("assistant message.created 应带 truncated 标记: %+v", attrs)
	}
	body, err := store.GetBlob(baseCtx, msgEv.Blob)
	if err != nil {
		t.Fatalf("读取截断消息正文失败: %v", err)
	}
	if string(body) != "半截" {
		t.Fatalf("截断消息正文 = %q, 期望 半截", body)
	}

	// 下一回合：会话可以从停止点继续
	if _, err := AppendUserMessage(baseCtx, store, UserMessage{
		SessionID: p.SessionID,
		TurnID:    ids.NewTurnID().String(),
		ContextID: ctxRow.ID,
		Text:      "继续",
	}); err != nil {
		t.Fatalf("追加用户消息失败: %v", err)
	}
	p2 := TurnParams{SessionID: p.SessionID, TurnID: ids.NewTurnID().String(), ContextID: ctxRow.ID}
	if err := rt.RunTurn(baseCtx, p2); err != nil {
		t.Fatalf("下一回合应成功完成: %v", err)
	}
	if ref, err := store.GetRef(baseCtx, TurnRefName(p2.TurnID)); err != nil || ref != RefCompleted {
		t.Fatalf("下一回合指针 = %q (%v), 期望 %q", ref, err, RefCompleted)
	}
}
