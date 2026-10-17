package api

import (
	"context"
	"testing"
	"time"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/runtime"
)

// waitTerminal 轮询等待回合到达终态，返回终态指针。
func waitTerminal(t *testing.T, store *eventlog.Store, turnID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := runtime.Status(context.Background(), store, turnID)
		if err == nil && runtime.IsTerminalRef(status.Ref) {
			return status.Ref
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待回合 %s 到终态超时（当前 %q, err=%v）", turnID, status.Ref, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRecoverRunningTurnConverges 验收 F2：重启后 running 指针的回合被重新拉起并收敛到终态。
func TestRecoverRunningTurnConverges(t *testing.T) {
	ctx := context.Background()
	providers := streamingTextServer(t)
	env := newTestEnv(t, providers.URL, "")

	const sessionID = "sess_recover"
	if _, err := env.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeSessionCreated,
		SessionID: sessionID,
		SubjectID: sessionID,
		Attrs:     eventlog.SessionCreatedAttrs{Model: "fake/model-a"},
	}); err != nil {
		t.Fatalf("写 session.created 失败: %v", err)
	}
	msg, err := env.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SessionID: sessionID,
		SubjectID: ids.NewMessageID().String(),
		Blob:      []byte("重启前没答完"),
		Attrs:     eventlog.MessageCreatedAttrs{Role: "user"},
	})
	if err != nil {
		t.Fatalf("写用户消息失败: %v", err)
	}
	ctxRow, err := env.store.CreateContext(ctx, eventlog.ContextInput{ForkAt: msg.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	turnID := ids.NewTurnID().String()
	if _, err := env.store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeTurnStarted,
		SessionID: sessionID,
		TurnID:    turnID,
		SubjectID: turnID,
		Attrs:     eventlog.TurnStartedAttrs{ContextID: ctxRow.ID.String()},
	}); err != nil {
		t.Fatalf("写 turn.started 失败: %v", err)
	}
	if _, err := env.store.MoveRef(ctx, runtime.TurnRefName(turnID), runtime.RefRunning); err != nil {
		t.Fatalf("写 running 指针失败: %v", err)
	}

	if err := env.srv.Recover(ctx); err != nil {
		t.Fatalf("Recover 失败: %v", err)
	}
	ref := waitTerminal(t, env.store, turnID)
	if ref != runtime.RefCompleted {
		t.Fatalf("恢复后回合指针 = %q, 期望 %q", ref, runtime.RefCompleted)
	}
	evs, err := env.store.ListByTurn(ctx, turnID, 0, 0)
	if err != nil {
		t.Fatalf("读取回合事件失败: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == eventlog.TypeMessageCreated {
			found = true
		}
	}
	if !found {
		t.Fatalf("恢复的回合应写 assistant 消息: %v", eventTypes(evs))
	}
}

// TestRecoverSkipsWaitingAndTerminal 验收 F2：等待审批与终态回合不被自动拉起。
func TestRecoverSkipsWaitingAndTerminal(t *testing.T) {
	ctx := context.Background()
	providers := streamingTextServer(t)
	env := newTestEnv(t, providers.URL, "")

	waitingID := ids.NewTurnID().String()
	completedID := ids.NewTurnID().String()
	if _, err := env.store.MoveRef(ctx, runtime.TurnRefName(waitingID), "waiting_approval:tc_x"); err != nil {
		t.Fatalf("写等待指针失败: %v", err)
	}
	if _, err := env.store.MoveRef(ctx, runtime.TurnRefName(completedID), runtime.RefCompleted); err != nil {
		t.Fatalf("写完成指针失败: %v", err)
	}

	if err := env.srv.Recover(ctx); err != nil {
		t.Fatalf("Recover 失败: %v", err)
	}
	// Recover 不阻塞启动，给异步路径一点时间确认没有动作。
	time.Sleep(100 * time.Millisecond)
	if ref, err := env.store.GetRef(ctx, runtime.TurnRefName(waitingID)); err != nil || ref != "waiting_approval:tc_x" {
		t.Fatalf("等待审批的回合被改动: %q (%v)", ref, err)
	}
	if ref, err := env.store.GetRef(ctx, runtime.TurnRefName(completedID)); err != nil || ref != runtime.RefCompleted {
		t.Fatalf("终态回合被改动: %q (%v)", ref, err)
	}
}

// eventTypes 提取事件类型序列（测试断言失败时打印用）。
func eventTypes(evs []eventlog.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}
