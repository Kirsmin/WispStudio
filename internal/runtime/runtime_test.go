// 验收测试：事件驱动运行时的端到端回合（流式增量 + 工具调用 + compile 回放）、
// 审批挂起与恢复、工具调用幂等折叠，以及 securePath 的工作区限制。
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"wisp/internal/compile"
	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
)

// openStore 打开内存事件日志并注册清理。
func openStore(t *testing.T) *eventlog.Store {
	t.Helper()
	s, err := eventlog.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("打开事件日志失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// scriptedProvider 是固定脚本的假 provider：按调用次序回放预设 SSE 帧，
// 并记录每次收到的请求体，供 compile 回放比对。
type scriptedProvider struct {
	mu     sync.Mutex
	rounds [][]string // 第 k 次调用的 data 帧
	got    []provider.ChatRequest
	srv    *httptest.Server
}

// newScriptedProvider 启动假 provider。
func newScriptedProvider(t *testing.T, rounds [][]string) *scriptedProvider {
	t.Helper()
	p := &scriptedProvider{rounds: rounds}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req provider.ChatRequest
		_ = json.Unmarshal(body, &req)

		p.mu.Lock()
		idx := len(p.got)
		p.got = append(p.got, req)
		p.mu.Unlock()

		if idx >= len(p.rounds) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"脚本帧已用尽"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range p.rounds[idx] {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// requests 返回按调用次序记录的请求。
func (p *scriptedProvider) requests() []provider.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.ChatRequest, len(p.got))
	copy(out, p.got)
	return out
}

// eventTypes 提取事件序列的类型列表。
func eventTypes(evs []eventlog.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

// filterTypes 只保留指定类型的事件（保持 seq 升序）。
func filterTypes(evs []eventlog.Event, types ...string) []eventlog.Event {
	keep := map[string]bool{}
	for _, ty := range types {
		keep[ty] = true
	}
	var out []eventlog.Event
	for _, ev := range evs {
		if keep[ev.Type] {
			out = append(out, ev)
		}
	}
	return out
}

// mustEventsOf 取某类型的全部事件。
func mustEventsOf(t *testing.T, evs []eventlog.Event, typ string) []eventlog.Event {
	t.Helper()
	var out []eventlog.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	if len(out) == 0 {
		t.Fatalf("事件序列中缺失 %s: %v", typ, eventTypes(evs))
	}
	return out
}

// lastContentBefore 返回 seq 之前最后一个内容事件 ID（回放 fork 点）。
func lastContentBefore(evs []eventlog.Event, seq int64) ids.EventID {
	tail := ids.EventID("")
	for _, ev := range evs {
		if ev.Seq < seq && isContentEvent(ev.Type) {
			tail = ev.ID
		}
	}
	return tail
}

// 第一轮脚本：推理增量 + 正文增量 + 分片工具调用（read_file a.txt）。
var e2eRound1 = []string{
	`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"先看看"}}]}`,
	`{"choices":[{"index":0,"delta":{"reasoning_content":"工具"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"我来读"}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"文件。"}}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","name":"read_file","arguments":"{\"path\":"}]}}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"arguments":"\"a.txt\"}"}]}}]}`,
	`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
}

// 第二轮脚本：纯正文回复。
var e2eRound2 = []string{
	`{"choices":[{"index":0,"delta":{"content":"a.txt 里是 "}}]}`,
	`{"choices":[{"index":0,"delta":{"content":"hello wisp。"}}]}`,
	`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`,
}

// TestRunTurnEndToEnd 端到端验收：一轮工具调用回合的事件序列完整性，
// 以及用 compile() 回放视图与当时实际发出的请求完全一致。
func TestRunTurnEndToEnd(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello wisp\n"), 0o644); err != nil {
		t.Fatalf("准备工作区文件失败: %v", err)
	}
	fake := newScriptedProvider(t, [][]string{e2eRound1, e2eRound2})
	rt := newTestRuntime(t, store, ws, fake)
	sessionID := "sess_e2e"

	// 预置一条 user 消息（会话内容链起点），上下文 fork 在它上面
	seed, err := store.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SessionID: sessionID,
		SubjectID: ids.NewMessageID().String(),
		Blob:      []byte("请读一下 a.txt"),
		Attrs:     eventlog.MessageCreatedAttrs{Role: string(compile.RoleUser)},
	})
	if err != nil {
		t.Fatalf("写 seed 消息失败: %v", err)
	}
	ctxRow, err := store.CreateContext(ctx, eventlog.ContextInput{ForkAt: seed.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	p := TurnParams{SessionID: sessionID, TurnID: ids.NewTurnID().String(), ContextID: ctxRow.ID}

	if err := rt.RunTurn(ctx, p); err != nil {
		t.Fatalf("RunTurn 失败: %v", err)
	}

	// 指针终态
	if target, err := store.GetRef(ctx, TurnRefName(p.TurnID)); err != nil || target != RefCompleted {
		t.Fatalf("回合指针 = %q (%v), 期望 %q", target, err, RefCompleted)
	}

	evs, err := store.ListByTurn(ctx, p.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("读取回合事件失败: %v", err)
	}

	// 关键事件序列必须完整且有序（message.delta / llm.responded 为附加事件，不参与比对）
	got := eventTypes(filterTypes(evs,
		eventlog.TypeTurnStarted, eventlog.TypeLLMRequested, eventlog.TypeMessageCreated,
		eventlog.TypeToolCalled, eventlog.TypeToolReturned, eventlog.TypeTurnCompleted))
	want := []string{
		eventlog.TypeTurnStarted,
		eventlog.TypeLLMRequested,
		eventlog.TypeMessageCreated,
		eventlog.TypeToolCalled,
		eventlog.TypeToolReturned,
		eventlog.TypeLLMRequested,
		eventlog.TypeMessageCreated,
		eventlog.TypeTurnCompleted,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("事件序列 =\n%v\n期望 =\n%v", got, want)
	}

	// 两轮 LLM：llm.responded 与 llm.requested 一一配对，usage 落盘
	reqs := mustEventsOf(t, evs, eventlog.TypeLLMRequested)
	resps := mustEventsOf(t, evs, eventlog.TypeLLMResponded)
	if len(reqs) != 2 || len(resps) != 2 {
		t.Fatalf("llm.requested/responded 数 = %d/%d, 期望 2/2", len(reqs), len(resps))
	}
	for i, rsp := range resps {
		if rsp.SubjectID != reqs[i].SubjectID {
			t.Fatalf("第 %d 个 llm.responded 未对应请求: %q != %q", i, rsp.SubjectID, reqs[i].SubjectID)
		}
		var attrs eventlog.LLMRespondedAttrs
		if err := json.Unmarshal(rsp.Attrs, &attrs); err != nil {
			t.Fatalf("解析 llm.responded attrs 失败: %v", err)
		}
		if attrs.TotalTokens == 0 || attrs.FinishReason == "" {
			t.Fatalf("第 %d 个 llm.responded 缺少 usage/finish_reason: %+v", i, attrs)
		}
	}

	// message.delta 边收流边落盘：按 kind 聚合后应与 message.created 正文一致
	created := mustEventsOf(t, evs, eventlog.TypeMessageCreated)
	if len(created) != 2 {
		t.Fatalf("message.created 数 = %d, 期望 2", len(created))
	}
	for i, ev := range created {
		content, reasoning := aggregateDeltas(t, ctx, store, evs, ev.SubjectID)
		body, err := store.GetBlob(ctx, ev.Blob)
		if err != nil {
			t.Fatalf("读取 message.created 正文失败: %v", err)
		}
		if content != string(body) {
			t.Fatalf("第 %d 条消息正文 = %q, delta 聚合 = %q", i, body, content)
		}
		if i == 0 && (content != "我来读文件。" || reasoning != "先看看工具") {
			t.Fatalf("第 1 轮增量聚合错误: content=%q reasoning=%q", content, reasoning)
		}
	}

	// tool.called 的 write-ahead 元数据
	called := mustEventsOf(t, evs, eventlog.TypeToolCalled)
	if len(called) != 1 {
		t.Fatalf("tool.called 数 = %d, 期望 1", len(called))
	}
	var calledAttrs eventlog.ToolCalledAttrs
	if err := json.Unmarshal(called[0].Attrs, &calledAttrs); err != nil {
		t.Fatalf("解析 tool.called attrs 失败: %v", err)
	}
	if calledAttrs.Name != "read_file" || calledAttrs.ProviderID != "call_1" || calledAttrs.IdempotencyKey == "" {
		t.Fatalf("tool.called attrs 不完整: %+v", calledAttrs)
	}
	args, err := store.GetBlob(ctx, called[0].Blob)
	if err != nil || string(args) != `{"path":"a.txt"}` {
		t.Fatalf("tool.called 参数 = %q (%v)", args, err)
	}

	// tool.returned 结清：同幂等键、result_of 链接、结果正文
	returned := mustEventsOf(t, evs, eventlog.TypeToolReturned)
	if len(returned) != 1 {
		t.Fatalf("tool.returned 数 = %d, 期望 1", len(returned))
	}
	if returned[0].SubjectID != called[0].SubjectID {
		t.Fatalf("tool.returned subject = %q, 期望 %q", returned[0].SubjectID, called[0].SubjectID)
	}
	var returnedAttrs eventlog.ToolReturnedAttrs
	if err := json.Unmarshal(returned[0].Attrs, &returnedAttrs); err != nil {
		t.Fatalf("解析 tool.returned attrs 失败: %v", err)
	}
	if returnedAttrs.IdempotencyKey != calledAttrs.IdempotencyKey {
		t.Fatalf("tool.returned 幂等键 = %q, 期望 %q", returnedAttrs.IdempotencyKey, calledAttrs.IdempotencyKey)
	}
	links, err := store.ListLinks(ctx, returned[0].ID.String(), called[0].ID.String(), "result_of")
	if err != nil || len(links) != 1 {
		t.Fatalf("result_of 链接缺失: %v (%v)", links, err)
	}
	var result ToolResult
	raw, err := store.GetBlob(ctx, returned[0].Blob)
	if err != nil {
		t.Fatalf("读取 tool.returned 正文失败: %v", err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析工具结果失败: %v", err)
	}
	if result.Status != "success" || !strings.Contains(result.Output, "hello wisp") {
		t.Fatalf("工具结果不正确: %+v", result)
	}

	// compile() 回放：每个 llm.requested 都能由「它之前最后一个内容事件」重新编译出
	// 完全相同的 RequestManifest 与完全相同的外部请求消息。
	sessionEvs, err := store.ListBySession(ctx, sessionID, 0, 0)
	if err != nil {
		t.Fatalf("读取会话事件失败: %v", err)
	}
	requests := fake.requests()
	if len(requests) != len(reqs) {
		t.Fatalf("假 provider 收到 %d 次请求, 期望 %d 次", len(requests), len(reqs))
	}
	for i, reqEv := range reqs {
		forkAt := lastContentBefore(sessionEvs, reqEv.Seq)
		if forkAt == "" {
			t.Fatalf("第 %d 次请求找不到回放 fork 点", i)
		}
		replayCtx, err := store.CreateContext(ctx, eventlog.ContextInput{ForkAt: forkAt})
		if err != nil {
			t.Fatalf("创建回放上下文失败: %v", err)
		}
		res, err := compile.Compile(ctx, store, replayCtx.ID)
		if err != nil {
			t.Fatalf("第 %d 次请求回放编译失败: %v", i, err)
		}
		origManifest, err := store.GetBlob(ctx, reqEv.Blob)
		if err != nil {
			t.Fatalf("读取原始 RequestManifest 失败: %v", err)
		}
		replayManifest, err := store.GetBlob(ctx, res.ManifestHash)
		if err != nil {
			t.Fatalf("读取回放 RequestManifest 失败: %v", err)
		}
		if string(origManifest) != string(replayManifest) {
			t.Fatalf("第 %d 次请求清单不一致:\n原始 %s\n回放 %s", i, origManifest, replayManifest)
		}
		replayReq := BuildChatRequest(res, rt.model, rt.tools.Definitions())
		if !reflect.DeepEqual(replayReq.Messages, requests[i].Messages) {
			t.Fatalf("第 %d 次请求回放消息不一致:\n回放 %+v\n原始 %+v", i, replayReq.Messages, requests[i].Messages)
		}
	}

	// 终态幂等：再次 RunTurn 不写任何新事件
	if err := rt.RunTurn(ctx, p); err != nil {
		t.Fatalf("终态重入失败: %v", err)
	}
	again, err := store.ListByTurn(ctx, p.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("重入后读取回合事件失败: %v", err)
	}
	if len(again) != len(evs) {
		t.Fatalf("终态重入产生了新事件: %d → %d", len(evs), len(again))
	}
}

// aggregateDeltas 按 kind 聚合某条消息的全部 message.delta 正文。
func aggregateDeltas(t *testing.T, ctx context.Context, store *eventlog.Store, evs []eventlog.Event, subject string) (content, reasoning string) {
	t.Helper()
	var cb, rb strings.Builder
	for _, ev := range evs {
		if ev.Type != eventlog.TypeMessageDelta || ev.SubjectID != subject {
			continue
		}
		var attrs eventlog.MessageDeltaAttrs
		if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
			t.Fatalf("解析 message.delta attrs 失败: %v", err)
		}
		data, err := store.GetBlob(ctx, ev.Blob)
		if err != nil {
			t.Fatalf("读取 message.delta 正文失败: %v", err)
		}
		switch attrs.Kind {
		case "content":
			cb.Write(data)
		case "reasoning":
			rb.Write(data)
		default:
			t.Fatalf("未知的 delta 类别: %q", attrs.Kind)
		}
	}
	return cb.String(), rb.String()
}

// TestRunTurnApprovalSuspendResume 验收审批流：需审批工具先挂起、不执行，
// approval.decided 后继续；拒绝时写失败结果且不产生副作用。
func TestRunTurnApprovalSuspendResume(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved bool
	}{
		{"批准后执行", true},
		{"拒绝后不执行", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openStore(t)
			ws := t.TempDir()
			fake := newScriptedProvider(t, [][]string{writeFileRound, e2eRound2})
			rt := newTestRuntime(t, store, ws, fake)
			sessionID := "sess_approval"

			seed, err := store.AppendEvent(ctx, eventlog.EventInput{
				Type:      eventlog.TypeMessageCreated,
				SessionID: sessionID,
				SubjectID: ids.NewMessageID().String(),
				Blob:      []byte("把内容写进 b.txt"),
				Attrs:     eventlog.MessageCreatedAttrs{Role: string(compile.RoleUser)},
			})
			if err != nil {
				t.Fatalf("写 seed 消息失败: %v", err)
			}
			ctxRow, err := store.CreateContext(ctx, eventlog.ContextInput{ForkAt: seed.ID})
			if err != nil {
				t.Fatalf("创建上下文失败: %v", err)
			}
			p := TurnParams{SessionID: sessionID, TurnID: ids.NewTurnID().String(), ContextID: ctxRow.ID}

			if err := rt.RunTurn(ctx, p); err != nil {
				t.Fatalf("首次 RunTurn 失败: %v", err)
			}
			target, err := store.GetRef(ctx, TurnRefName(p.TurnID))
			if err != nil || !strings.HasPrefix(target, refWaitingPrefix) {
				t.Fatalf("回合指针 = %q (%v), 期望挂起等待审批", target, err)
			}

			evs, err := store.ListByTurn(ctx, p.TurnID, 0, 0)
			if err != nil {
				t.Fatalf("读取回合事件失败: %v", err)
			}
			apReq := mustEventsOf(t, evs, eventlog.TypeApprovalRequested)
			if len(apReq) != 1 {
				t.Fatalf("approval.requested 数 = %d, 期望 1", len(apReq))
			}
			var apAttrs eventlog.ApprovalRequestedAttrs
			if err := json.Unmarshal(apReq[0].Attrs, &apAttrs); err != nil {
				t.Fatalf("解析 approval.requested attrs 失败: %v", err)
			}
			if apAttrs.Name != "write_file" || apAttrs.ToolCallID != apReq[0].SubjectID {
				t.Fatalf("approval.requested attrs 不正确: %+v", apAttrs)
			}
			if len(mustEventsOf(t, evs, eventlog.TypeToolCalled)) != 1 {
				t.Fatal("tool.called 应已 write-ahead 落盘")
			}
			if got := filterTypes(evs, eventlog.TypeToolReturned); len(got) != 0 {
				t.Fatal("审批前不得执行工具（不应有 tool.returned）")
			}
			if _, err := os.Stat(filepath.Join(ws, "b.txt")); !os.IsNotExist(err) {
				t.Fatal("审批前不得产生文件副作用")
			}

			// 挂起期间重入不轮询、不写新事件
			if err := rt.RunTurn(ctx, p); err != nil {
				t.Fatalf("挂起重入失败: %v", err)
			}
			again, err := store.ListByTurn(ctx, p.TurnID, 0, 0)
			if err != nil {
				t.Fatalf("重入后读取回合事件失败: %v", err)
			}
			if len(again) != len(evs) {
				t.Fatalf("挂起重入写入了新事件: %d → %d", len(evs), len(again))
			}

			// 记录审批决定并继续
			approvalID := ids.ToolCallID(apReq[0].SubjectID)
			if err := rt.DecideApproval(ctx, p, approvalID, tc.approved, "测试决定"); err != nil {
				t.Fatalf("DecideApproval 失败: %v", err)
			}
			if err := rt.RunTurn(ctx, p); err != nil {
				t.Fatalf("恢复后 RunTurn 失败: %v", err)
			}
			if target, err := store.GetRef(ctx, TurnRefName(p.TurnID)); err != nil || target != RefCompleted {
				t.Fatalf("恢复后回合指针 = %q (%v), 期望 %q", target, err, RefCompleted)
			}

			evs, err = store.ListByTurn(ctx, p.TurnID, 0, 0)
			if err != nil {
				t.Fatalf("恢复后读取回合事件失败: %v", err)
			}
			if len(mustEventsOf(t, evs, eventlog.TypeApprovalDecided)) != 1 {
				t.Fatal("approval.decided 应已落盘")
			}
			returned := mustEventsOf(t, evs, eventlog.TypeToolReturned)
			if len(returned) != 1 {
				t.Fatalf("tool.returned 数 = %d, 期望 1", len(returned))
			}
			var result ToolResult
			raw, err := store.GetBlob(ctx, returned[0].Blob)
			if err != nil {
				t.Fatalf("读取工具结果失败: %v", err)
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatalf("解析工具结果失败: %v", err)
			}

			content, err := os.ReadFile(filepath.Join(ws, "b.txt"))
			if tc.approved {
				if result.Status != "success" || string(content) != "审批后的内容" {
					t.Fatalf("批准后应写入文件: result=%+v content=%q err=%v", result, content, err)
				}
			} else {
				if result.Status != "failure" || !strings.Contains(result.Error, "测试决定") {
					t.Fatalf("拒绝后应写失败结果: %+v", result)
				}
				if _, err := os.Stat(filepath.Join(ws, "b.txt")); !os.IsNotExist(err) {
					t.Fatal("拒绝后不得产生文件副作用")
				}
			}

			// 恢复后仍走完 LLM 步并写 message.created
			if got := filterTypes(evs, eventlog.TypeMessageCreated); len(got) != 2 {
				t.Fatalf("message.created 数 = %d, 期望 2", len(got))
			}
		})
	}
}

// writeFileRound 是审批用例的第一轮脚本：shallow 分片的 write_file 调用。
var writeFileRound = []string{
	`{"choices":[{"index":0,"delta":{"content":"我来写文件。"}}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","name":"write_file","arguments":"{\"path\":\"b.txt\","}]}}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"arguments":"\"content\":\"审批后的内容\"}"}]}}]}`,
	`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
}

// TestFoldTurnSettledByIdempotencyKey 验收崩溃恢复语义：tool.returned 以
// 幂等键结清调用，即使事件 subject 不同也不得重复执行（不轮询 pending 表）。
func TestFoldTurnSettledByIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const turnID = "tn_fold_test"

	appendEv := func(in eventlog.EventInput) eventlog.Event {
		t.Helper()
		in.TurnID = turnID
		ev, err := store.AppendEvent(ctx, in)
		if err != nil {
			t.Fatalf("追加事件失败: %v", err)
		}
		return ev
	}
	appendEv(eventlog.EventInput{Type: eventlog.TypeTurnStarted, SubjectID: turnID})
	sameKey := appendEv(eventlog.EventInput{
		Type: eventlog.TypeToolCalled, SubjectID: ids.NewToolCallID().String(),
		Attrs: eventlog.ToolCalledAttrs{Name: "read_file", IdempotencyKey: "k1"},
	})
	// 已有结果：subject 与 called 一致
	appendEv(eventlog.EventInput{
		Type: eventlog.TypeToolReturned, SubjectID: sameKey.SubjectID,
		Attrs: eventlog.ToolReturnedAttrs{IdempotencyKey: "k1"},
	})
	otherKey := appendEv(eventlog.EventInput{
		Type: eventlog.TypeToolCalled, SubjectID: ids.NewToolCallID().String(),
		Attrs: eventlog.ToolCalledAttrs{Name: "read_file", IdempotencyKey: "k2"},
	})

	evs, err := store.ListByTurn(ctx, turnID, 0, 0)
	if err != nil {
		t.Fatalf("读取回合事件失败: %v", err)
	}
	st, err := foldTurn(ctx, store, evs)
	if err != nil {
		t.Fatalf("折叠失败: %v", err)
	}
	call, ok := st.nextPending()
	if !ok || call.idempotencyKey != "k2" {
		t.Fatalf("pending 调用 = %+v (%v), 期望只剩 k2", call, ok)
	}
	if st.chainTail != otherKey.ID {
		t.Fatalf("内容链尾 = %s, 期望最后一个内容事件 %s", st.chainTail, otherKey.ID)
	}

	// 换一个 subject、但幂等键相同的 tool.returned：应按幂等键结清
	appendEv(eventlog.EventInput{
		Type: eventlog.TypeToolReturned, SubjectID: ids.NewToolCallID().String(),
		Attrs: eventlog.ToolReturnedAttrs{IdempotencyKey: "k2"},
	})
	evs, err = store.ListByTurn(ctx, turnID, 0, 0)
	if err != nil {
		t.Fatalf("二次读取回合事件失败: %v", err)
	}
	st, err = foldTurn(ctx, store, evs)
	if err != nil {
		t.Fatalf("二次折叠失败: %v", err)
	}
	if call, ok := st.nextPending(); ok {
		t.Fatalf("幂等键已结清的调用不应再 pending: %+v", call)
	}
}

// TestSecurePathBlocksEscape 验收文件操作一律 securePath 限制在 workspaceRoot 内。
func TestSecurePathBlocksEscape(t *testing.T) {
	ctx := context.Background()
	outer := t.TempDir()
	ws := filepath.Join(outer, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}
	secret := filepath.Join(outer, "secret.txt")
	if err := os.WriteFile(secret, []byte("绝密内容"), 0o644); err != nil {
		t.Fatalf("准备工作区外文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("工作区内"), 0o644); err != nil {
		t.Fatalf("准备工作区内文件失败: %v", err)
	}
	// 指向工作区外的符号链接：词法上在工作区内，必须被真实路径检查拦住
	if err := os.Symlink(outer, filepath.Join(ws, "link")); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}
	reg, err := NewRegistry(ws)
	if err != nil {
		t.Fatalf("构造注册表失败: %v", err)
	}

	if res, err := reg.Execute(ctx, "read_file", []byte(`{"path":"a.txt"}`)); err != nil || res.Status != "success" {
		t.Fatalf("工作区内读取应成功: %+v (%v)", res, err)
	}

	cases := []struct {
		name string
		tool string
		args string
	}{
		{"读 ../ 逃逸", "read_file", `{"path":"../secret.txt"}`},
		{"读绝对路径", "read_file", `{"path":"/etc/passwd"}`},
		{"读符号链接逃逸", "read_file", `{"path":"link/secret.txt"}`},
		{"列出符号链接逃逸", "list_files", `{"path":"link"}`},
		{"搜索符号链接逃逸", "search_text", `{"query":"绝密","path":"link"}`},
		{"写 ../ 逃逸", "write_file", `{"path":"../evil.txt","content":"x"}`},
		{"命令 cwd 逃逸", "run_command", `{"command":"true","cwd":"../"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := reg.Execute(ctx, tc.tool, []byte(tc.args))
			if err != nil {
				t.Fatalf("执行不应返回 error（应作为失败结果）: %v", err)
			}
			if res.Status != "failure" {
				t.Fatalf("越界路径应失败, 实际 %+v", res)
			}
			if strings.Contains(res.Output, "绝密内容") || strings.Contains(res.Stdout, "绝密内容") {
				t.Fatal("越界路径泄漏了工作区外内容")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outer, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("越界写入产生了工作区外文件")
	}
}

// TestBuiltinToolDefinitions 验收内置工具集合完整。
func TestBuiltinToolDefinitions(t *testing.T) {
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("构造注册表失败: %v", err)
	}
	var names []string
	for _, def := range reg.Definitions() {
		names = append(names, def.Function.Name)
	}
	want := []string{"list_files", "read_file", "run_command", "search_text", "write_file"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("内置工具 = %v, 期望 %v", names, want)
	}
	if !reg.NeedsApproval("write_file") || !reg.NeedsApproval("run_command") {
		t.Fatal("write_file / run_command 应需要审批")
	}
	if reg.NeedsApproval("read_file") || reg.NeedsApproval("list_files") || reg.NeedsApproval("search_text") {
		t.Fatal("只读工具不应需要审批")
	}
}

// newTestRuntime 构造指向假 provider 的运行时。
func newTestRuntime(t *testing.T, store *eventlog.Store, workspace string, fake *scriptedProvider) *Runtime {
	t.Helper()
	rt, err := New(store, Options{
		WorkspaceRoot: workspace,
		ProviderID:    "deepseek",
		Model:         "deepseek/deepseek-chat",
		Client:        &provider.Client{BaseURL: fake.srv.URL, MaxRetries: 0},
	})
	if err != nil {
		t.Fatalf("构造运行时失败: %v", err)
	}
	return rt
}
