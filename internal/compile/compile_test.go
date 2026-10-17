package compile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
)

// 测试辅助：打开内存事件日志。
func openStore(t *testing.T) *eventlog.Store {
	t.Helper()
	s, err := eventlog.Open(":memory:")
	if err != nil {
		t.Fatalf("打开事件日志失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// 测试辅助：维护 parent_id 内容链。
type chain struct {
	last ids.EventID // 链尾事件，空 = 尚未开始
}

// msg 追加一条 message.created 事件。
func (c *chain) msg(t *testing.T, s *eventlog.Store, role, text string) (eventlog.Event, string) {
	t.Helper()
	subject := ids.NewMessageID().String()
	ev, err := s.AppendEvent(context.Background(), eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SubjectID: subject,
		ParentID:  c.last.String(),
		Blob:      []byte(text),
		Attrs:     eventlog.MessageCreatedAttrs{Role: role},
	})
	if err != nil {
		t.Fatalf("追加 message.created 失败: %v", err)
	}
	c.last = ev.ID
	return ev, subject
}

// msgAs 追加一条指定 subject 的 message.created 事件（模拟编辑产生的新版本）。
func (c *chain) msgAs(t *testing.T, s *eventlog.Store, subject, role, text string) eventlog.Event {
	t.Helper()
	ev, err := s.AppendEvent(context.Background(), eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SubjectID: subject,
		ParentID:  c.last.String(),
		Blob:      []byte(text),
		Attrs:     eventlog.MessageCreatedAttrs{Role: role},
	})
	if err != nil {
		t.Fatalf("追加 message.created 失败: %v", err)
	}
	c.last = ev.ID
	return ev
}

// call 追加一条 tool.called 事件（不自动补结果）。
func (c *chain) call(t *testing.T, s *eventlog.Store, name, args, providerID string) (eventlog.Event, string) {
	t.Helper()
	subject := ids.NewToolCallID().String()
	ev, err := s.AppendEvent(context.Background(), eventlog.EventInput{
		Type:      eventlog.TypeToolCalled,
		SubjectID: subject,
		ParentID:  c.last.String(),
		Blob:      []byte(args),
		Attrs:     eventlog.ToolCalledAttrs{Name: name, ProviderID: providerID},
	})
	if err != nil {
		t.Fatalf("追加 tool.called 失败: %v", err)
	}
	c.last = ev.ID
	return ev, subject
}

// result 追加一条 tool.returned 事件并写 result_of 链接。
func (c *chain) result(t *testing.T, s *eventlog.Store, callEv eventlog.Event, subject, text string) (eventlog.Event, string) {
	t.Helper()
	if subject == "" {
		subject = callEv.SubjectID
	}
	ev, err := s.AppendEvent(context.Background(), eventlog.EventInput{
		Type:      eventlog.TypeToolReturned,
		SubjectID: subject,
		ParentID:  c.last.String(),
		Blob:      []byte(text),
		Attrs:     eventlog.ToolReturnedAttrs{},
	})
	if err != nil {
		t.Fatalf("追加 tool.returned 失败: %v", err)
	}
	c.last = ev.ID
	if err := s.AddLink(context.Background(), ev.ID.String(), callEv.ID.String(), "result_of"); err != nil {
		t.Fatalf("写入 result_of 链接失败: %v", err)
	}
	return ev, subject
}

// texts 提取消息列表的 (角色, 正文/工具名) 摘要，便于断言顺序。
func summarize(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		switch {
		case m.ToolCall != nil:
			out = append(out, "assistant:"+m.ToolCall.Name)
		case m.ToolResultFor != "":
			out = append(out, "user:result("+string(m.ToolResultFor)+")="+m.Content)
		default:
			out = append(out, string(m.Role)+":"+m.Content)
		}
	}
	return out
}

// join 拼接摘要。
func join(v []string) string { return strings.Join(v, " | ") }

// readManifest 读回并解析 RequestManifest。
func readManifest(t *testing.T, s *eventlog.Store, hash string) []ManifestItem {
	t.Helper()
	data, err := s.GetBlob(context.Background(), hash)
	if err != nil {
		t.Fatalf("读取 RequestManifest blob 失败: %v", err)
	}
	var items []ManifestItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("解析 RequestManifest 失败: %v", err)
	}
	return items
}

// assertInvariants 校验 validate 的全部不变量。
func assertInvariants(t *testing.T, msgs []Message) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("消息列表为空")
	}
	if msgs[0].Role == RoleAssistant {
		t.Fatal("首条消息不允许为 assistant")
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Fatalf("角色未交替: 第 %d 与第 %d 条均为 %s", i-1, i, msgs[i].Role)
		}
	}
	calls := map[ids.ToolCallID]int{}
	for i, m := range msgs {
		if m.ToolCall != nil {
			if _, dup := calls[m.ToolCall.ID]; dup {
				t.Fatalf("工具调用 ID %s 重复出现", m.ToolCall.ID)
			}
			calls[m.ToolCall.ID] = i
		}
	}
	results := map[ids.ToolCallID]int{}
	for i, m := range msgs {
		if m.ToolResultFor != "" {
			if _, dup := results[m.ToolResultFor]; dup {
				t.Fatalf("工具结果 ID %s 重复出现", m.ToolResultFor)
			}
			results[m.ToolResultFor] = i
		}
	}
	for id, ci := range calls {
		ri, ok := results[id]
		if !ok {
			t.Fatalf("工具调用 %s 缺少配对结果", id)
		}
		if ri < ci {
			t.Fatalf("工具调用 %s 的结果出现在调用之前", id)
		}
	}
	for id := range results {
		if _, ok := calls[id]; !ok {
			t.Fatalf("工具结果 %s 没有对应调用", id)
		}
	}
}

// TestCompileAppendAndManifest 覆盖根上下文纯 append 场景与 RequestManifest。
func TestCompileAppendAndManifest(t *testing.T) {
	s := openStore(t)
	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}

	var c chain
	e1, sub1 := c.msg(t, s, "user", "你好")
	e2, sub2 := c.msg(t, s, "assistant", "你好！有什么可以帮你？")

	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpAppend, NewID: e1.ID}); err != nil {
		t.Fatalf("追加 append 失败: %v", err)
	}
	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpAppend, NewID: e2.ID}); err != nil {
		t.Fatalf("追加 append 失败: %v", err)
	}

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:你好 | assistant:你好！有什么可以帮你？"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}

	// RequestManifest：条目 (id, blob_hash, revision) 与内容事件一一对应
	items := readManifest(t, s, res.ManifestHash)
	if len(items) != 2 {
		t.Fatalf("清单条目数 = %d, 期望 2", len(items))
	}
	if items[0].ID != sub1 || items[0].BlobHash != e1.Blob || items[0].Revision != e1.Seq {
		t.Fatalf("清单第一条不正确: %+v, 期望 id=%s blob=%s revision=%d", items[0], sub1, e1.Blob, e1.Seq)
	}
	if items[1].ID != sub2 || items[1].BlobHash != e2.Blob || items[1].Revision != e2.Seq {
		t.Fatalf("清单第二条不正确: %+v", items[1])
	}
}

// TestCompileFork 覆盖 fork_at 截断历史的场景：子上下文看不到 fork 点之后的内容。
func TestCompileFork(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "第一条")
	e2, _ := c.msg(t, s, "assistant", "第二条")
	e3, sub3 := c.msg(t, s, "user", "fork 之后")

	// 根上下文取全量历史
	root, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e3.ID})
	if err != nil {
		t.Fatalf("创建根上下文失败: %v", err)
	}
	// 子上下文 fork 在第二条处
	child, err := s.CreateContext(context.Background(), eventlog.ContextInput{
		BaseCtx: root.ID,
		ForkAt:  e2.ID,
	})
	if err != nil {
		t.Fatalf("创建子上下文失败: %v", err)
	}

	res, err := Compile(context.Background(), s, child.ID)
	if err != nil {
		t.Fatalf("编译子上下文失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:第一条 | assistant:第二条"; got != want {
		t.Fatalf("子上下文消息 = %q, 期望 %q（不应包含 fork 点之后的内容）", got, want)
	}
	items := readManifest(t, s, res.ManifestHash)
	for _, it := range items {
		if it.ID == sub3 {
			t.Fatal("子上下文清单不应包含 fork 点之后的内容")
		}
	}

	resRoot, err := Compile(context.Background(), s, root.ID)
	if err != nil {
		t.Fatalf("编译根上下文失败: %v", err)
	}
	if got, want := join(summarize(resRoot.Messages)),
		"user:第一条 | assistant:第二条 | user:fork 之后"; got != want {
		t.Fatalf("根上下文消息 = %q, 期望 %q", got, want)
	}
}

// TestCompileLatestVersion 覆盖同 subject 多版本取 seq 最大者的场景。
func TestCompileLatestVersion(t *testing.T) {
	s := openStore(t)
	var c chain
	e1, sub := c.msg(t, s, "user", "旧内容")
	// 同一 subject 的新版本（模拟消息编辑），位于链上更靠后处
	e3 := c.msgAs(t, s, sub, "user", "新内容")
	c.msg(t, s, "assistant", "收到")
	_ = e1

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c.last})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if len(res.Messages) != 2 {
		t.Fatalf("消息数 = %d, 期望 2（多版本应去重）", len(res.Messages))
	}
	if res.Messages[0].Content != "新内容" {
		t.Fatalf("应取最新版本内容, 实际 %q", res.Messages[0].Content)
	}
	items := readManifest(t, s, res.ManifestHash)
	if len(items) != 2 || items[0].Revision != e3.Seq || items[0].BlobHash != e3.Blob {
		t.Fatalf("清单应指向最新版本: %+v", items)
	}
}

// TestCompileInsertOkey 覆盖 insert(anchor+okey) 分数索引场景：
// 乱序提交的 okey 按字典序归位，结果条目锚定调用条目紧随其后。
func TestCompileInsertOkey(t *testing.T) {
	s := openStore(t)
	var c chain
	_, m1 := c.msg(t, s, "user", "锚点消息")
	e2, _ := c.msg(t, s, "assistant", "结尾消息")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e2.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	add := func(in eventlog.CtxOpInput) {
		t.Helper()
		if _, err := s.AppendCtxOp(context.Background(), in); err != nil {
			t.Fatalf("追加 ctx_op 失败: %v", err)
		}
	}

	// 先提交 okey="b" 的调用，再提交 okey="a" 的调用：最终 a 应在 b 前
	callB, tcB := c.call(t, s, "tool_b", `{}`, "toolu_b")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: m1, OKey: "b", NewID: callB.ID})
	callA, tcA := c.call(t, s, "tool_a", `{}`, "toolu_a")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: m1, OKey: "a", NewID: callA.ID})
	// 结果条目锚定各自的调用条目
	resB, _ := c.result(t, s, callB, tcB, "结果b")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: tcB, NewID: resB.ID})
	resA, _ := c.result(t, s, callA, tcA, "结果a")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: tcA, NewID: resA.ID})

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	want := "user:锚点消息 | assistant:tool_a | user:result(" + tcA + ")=结果a | assistant:tool_b | user:result(" + tcB + ")=结果b | assistant:结尾消息"
	if got := join(summarize(res.Messages)); got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}

	// 空 okey 排最后
	callC, tcC := c.call(t, s, "tool_c", `{}`, "toolu_c")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: m1, NewID: callC.ID})
	resC, _ := c.result(t, s, callC, tcC, "结果c")
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: tcC, NewID: resC.ID})
	res, err = Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("二次编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if msgs := res.Messages; msgs[5].ToolCall.Name != "tool_c" {
		t.Fatalf("空 okey 应排最后, 实际第 6 条 = %v", summarize(res.Messages))
	}
}

// TestCompileHideIdempotent 覆盖 hide 占位符与幂等场景。
func TestCompileHideIdempotent(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "保留")
	_, m2 := c.msg(t, s, "assistant", "隐藏我")
	_, m3 := c.msg(t, s, "user", "也隐藏我")
	e4, _ := c.msg(t, s, "assistant", "结尾")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e4.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	add := func(in eventlog.CtxOpInput) {
		t.Helper()
		if _, err := s.AppendCtxOp(context.Background(), in); err != nil {
			t.Fatalf("追加 ctx_op 失败: %v", err)
		}
	}
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpHide, Target: m2})
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpHide, Target: m2}) // 重复 hide
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpHide, Target: m3})

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	got := join(summarize(res.Messages))
	if want := "user:保留 | assistant:[已隐藏] | user:[已隐藏] | assistant:结尾"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q（重复 hide 不得产生第二个占位符）", got, want)
	}

	// 编译确定性：重复编译输出一致
	res2, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("二次编译失败: %v", err)
	}
	if join(summarize(res2.Messages)) != got {
		t.Fatal("重复编译输出不一致")
	}
}

// TestCompileReplace 覆盖 replace 场景：原 target 不再出现，新内容占据原位置。
func TestCompileReplace(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "前")
	_, m2 := c.msg(t, s, "assistant", "被替换")
	c.msg(t, s, "user", "后")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c.last})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}

	eNew, _ := c.msg(t, s, "assistant", "替换后")
	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
		Ctx: ctx.ID, Op: eventlog.CtxOpReplace, Target: m2, NewID: eNew.ID,
	}); err != nil {
		t.Fatalf("追加 replace 失败: %v", err)
	}

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:前 | assistant:替换后 | user:后"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}
	for _, item := range readManifest(t, s, res.ManifestHash) {
		if item.ID == m2 {
			t.Fatalf("被替换的 target %s 不得出现在清单中", m2)
		}
	}
}

// TestCompilePin 覆盖 pin 场景：置顶区按 pin 的 seq 排序，且保持角色交替。
func TestCompilePin(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "一")
	c.msg(t, s, "assistant", "二")
	_, m3 := c.msg(t, s, "user", "三")
	_, m4 := c.msg(t, s, "assistant", "四")
	c.msg(t, s, "user", "五")
	e6, _ := c.msg(t, s, "assistant", "六")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e6.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	add := func(in eventlog.CtxOpInput) {
		t.Helper()
		if _, err := s.AppendCtxOp(context.Background(), in); err != nil {
			t.Fatalf("追加 ctx_op 失败: %v", err)
		}
	}
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpPin, Target: m3})
	add(eventlog.CtxOpInput{Ctx: ctx.ID, Op: eventlog.CtxOpPin, Target: m4})

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:三 | assistant:四 | user:一 | assistant:二 | user:五 | assistant:六"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}
}

// TestCompileBundleGroupHide 覆盖 bundle 整组隐藏场景。
func TestCompileBundleGroupHide(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "前")
	callEv, tc := c.call(t, s, "read_file", `{"path":"a"}`, "toolu_read")
	_, _ = c.result(t, s, callEv, tc, "文件内容")
	e4, _ := c.msg(t, s, "assistant", "后")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e4.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	// 只 hide 结果：整组（调用+结果）一起消失，且不留占位符
	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
		Ctx: ctx.ID, Op: eventlog.CtxOpHide, Target: tc,
	}); err != nil {
		t.Fatalf("追加 hide 失败: %v", err)
	}

	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:前 | assistant:后"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}
	if _, ok := res.ProviderMap[ids.ToolCallID(tc)]; ok {
		t.Fatal("整组隐藏后 provider 映射不应保留")
	}
	for _, item := range readManifest(t, s, res.ManifestHash) {
		if item.ID == tc {
			t.Fatal("整组隐藏后清单不应包含该工具调用")
		}
	}
}

// TestCompileOrphanCalled 覆盖孤儿 called 补占位结果并告警的场景。
func TestCompileOrphanCalled(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "前")
	_, tc := c.call(t, s, "read_file", `{"path":"a"}`, "toolu_orphan")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c.last})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if len(res.Warnings) == 0 {
		t.Fatal("孤儿 called 应产生 warning")
	}
	if got, want := join(summarize(res.Messages)), "user:前 | assistant:read_file | user:result("+tc+")=omitted"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}
}

// TestCompileRejectsMissingProviderID 验收 F6：tool.called 缺少 provider_id
// 映射时编译直接失败，不允许把空 id 发给服务商。
func TestCompileRejectsMissingProviderID(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "前")
	callEv, tc := c.call(t, s, "read_file", `{"path":"a"}`, "")
	c.result(t, s, callEv, tc, "文件内容")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c.last})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	_, err = Compile(context.Background(), s, ctx.ID)
	if err == nil {
		t.Fatal("缺少 provider_id 映射应编译失败")
	}
	if want := "工具调用 " + tc + " 缺少 provider_id 映射"; !strings.Contains(err.Error(), want) {
		t.Fatalf("错误信息 = %q, 应包含 %q", err, want)
	}
}

// TestCompileOrphanReturnedDropped 覆盖孤儿 returned 被丢弃的场景。
func TestCompileOrphanReturnedDropped(t *testing.T) {
	s := openStore(t)
	// 另一条链上的“外部调用”
	var foreign chain
	fc, _ := foreign.call(t, s, "ext", `{}`, "")

	var c2 chain
	c2.msg(t, s, "user", "唯一消息")
	// 孤儿结果：链接指向不在本编译列表中的调用
	ev, err := s.AppendEvent(context.Background(), eventlog.EventInput{
		Type:      eventlog.TypeToolReturned,
		SubjectID: ids.NewToolCallID().String(),
		ParentID:  c2.last.String(),
		Blob:      []byte("没人要的结果"),
	})
	if err != nil {
		t.Fatalf("追加孤儿 returned 失败: %v", err)
	}
	c2.last = ev.ID
	if err := s.AddLink(context.Background(), ev.ID.String(), fc.ID.String(), "result_of"); err != nil {
		t.Fatalf("写链接失败: %v", err)
	}

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c2.last})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	// 孤儿 returned 被丢弃：列表只剩唯一消息
	if got, want := join(summarize(res.Messages)), "user:唯一消息"; got != want {
		t.Fatalf("消息列表 = %q, 期望 %q", got, want)
	}
}

// TestCompileLayeredOps 覆盖逐层继承场景：父上下文的 hide 影响子上下文。
func TestCompileLayeredOps(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "保留")
	e2, m2 := c.msg(t, s, "assistant", "父级隐藏")

	root, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e2.ID})
	if err != nil {
		t.Fatalf("创建根上下文失败: %v", err)
	}
	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
		Ctx: root.ID, Op: eventlog.CtxOpHide, Target: m2,
	}); err != nil {
		t.Fatalf("追加 hide 失败: %v", err)
	}

	e3, _ := c.msg(t, s, "user", "子级新增")
	child, err := s.CreateContext(context.Background(), eventlog.ContextInput{
		BaseCtx: root.ID,
		ForkAt:  e2.ID, // fork 在父级历史内，e3 由子级 append 引入
	})
	if err != nil {
		t.Fatalf("创建子上下文失败: %v", err)
	}
	if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
		Ctx: child.ID, Op: eventlog.CtxOpAppend, NewID: e3.ID,
	}); err != nil {
		t.Fatalf("追加 append 失败: %v", err)
	}

	// 子上下文：父级 hide 生效，子级 append 生效
	res, err := Compile(context.Background(), s, child.ID)
	if err != nil {
		t.Fatalf("编译子上下文失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got, want := join(summarize(res.Messages)), "user:保留 | assistant:[已隐藏] | user:子级新增"; got != want {
		t.Fatalf("子上下文消息 = %q, 期望 %q", got, want)
	}

	// 父上下文自身视图不变
	resRoot, err := Compile(context.Background(), s, root.ID)
	if err != nil {
		t.Fatalf("编译根上下文失败: %v", err)
	}
	if got, want := join(summarize(resRoot.Messages)), "user:保留 | assistant:[已隐藏]"; got != want {
		t.Fatalf("根上下文消息 = %q, 期望 %q", got, want)
	}
}

// TestCompileProviderMap 覆盖 render 阶段的 provider ID 映射。
func TestCompileProviderMap(t *testing.T) {
	s := openStore(t)
	var c chain
	c.msg(t, s, "user", "调用工具")
	callEv, tc := c.call(t, s, "read_file", `{"path":"a"}`, "toolu_01ABC")
	e3, _ := c.result(t, s, callEv, tc, "内容")

	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e3.ID})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	assertInvariants(t, res.Messages)
	if got := res.ProviderMap[ids.ToolCallID(tc)]; got != "toolu_01ABC" {
		t.Fatalf("ProviderMap[%s] = %q, 期望 toolu_01ABC", tc, got)
	}
	var found *ToolCall
	for i := range res.Messages {
		if res.Messages[i].ToolCall != nil {
			found = res.Messages[i].ToolCall
		}
	}
	if found == nil || found.ProviderID != "toolu_01ABC" {
		t.Fatalf("ToolCall.ProviderID 映射丢失: %+v", found)
	}
}

// TestCompileValidateErrors 覆盖 validate 失败即报错的场景。
func TestCompileValidateErrors(t *testing.T) {
	// 首条为 assistant
	t.Run("首条非assistant被违反", func(t *testing.T) {
		s := openStore(t)
		var c chain
		c.msg(t, s, "assistant", "我先说")
		e2, _ := c.msg(t, s, "user", "后说")

		ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: e2.ID})
		if err != nil {
			t.Fatalf("创建上下文失败: %v", err)
		}
		if _, err := Compile(context.Background(), s, ctx.ID); err == nil {
			t.Fatal("首条为 assistant 应编译失败")
		}
	})

	// 角色未交替（结尾连续两条 assistant）
	t.Run("角色交替被违反", func(t *testing.T) {
		s := openStore(t)
		var c chain
		c.msg(t, s, "user", "一")
		c.msg(t, s, "assistant", "二")
		c.msg(t, s, "assistant", "三")

		ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: c.last})
		if err != nil {
			t.Fatalf("创建上下文失败: %v", err)
		}
		if _, err := Compile(context.Background(), s, ctx.ID); err == nil {
			t.Fatal("角色未交替应编译失败")
		}
	})

	// insert 锚点不存在
	t.Run("insert锚点缺失", func(t *testing.T) {
		s := openStore(t)
		ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{})
		if err != nil {
			t.Fatalf("创建上下文失败: %v", err)
		}
		var c chain
		e1, _ := c.msg(t, s, "user", "内容")
		if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
			Ctx: ctx.ID, Op: eventlog.CtxOpInsert, Anchor: "msg_不存在", NewID: e1.ID,
		}); err != nil {
			t.Fatalf("追加 insert 失败: %v", err)
		}
		if _, err := Compile(context.Background(), s, ctx.ID); err == nil {
			t.Fatal("insert 锚点不存在应编译失败")
		}
	})

	// hide 目标不存在
	t.Run("hide目标缺失", func(t *testing.T) {
		s := openStore(t)
		ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{})
		if err != nil {
			t.Fatalf("创建上下文失败: %v", err)
		}
		if _, err := s.AppendCtxOp(context.Background(), eventlog.CtxOpInput{
			Ctx: ctx.ID, Op: eventlog.CtxOpHide, Target: "msg_不存在",
		}); err != nil {
			t.Fatalf("追加 hide 失败: %v", err)
		}
		if _, err := Compile(context.Background(), s, ctx.ID); err == nil {
			t.Fatal("hide 目标不存在应编译失败")
		}
	})

	// 上下文不存在
	t.Run("上下文缺失", func(t *testing.T) {
		s := openStore(t)
		if _, err := Compile(context.Background(), s, ids.NewContextID()); err == nil {
			t.Fatal("编译不存在的上下文应失败")
		}
	})
}

// byteStream 把 fuzz 输入当作字节流，耗尽后返回 0，保证生成过程不 panic。
type byteStream struct {
	b []byte
	i int
}

func (s *byteStream) next() byte {
	if s.i >= len(s.b) {
		return 0
	}
	v := s.b[s.i]
	s.i++
	return v
}

// pick 取模选号，n<=0 时返回 0。
func (s *byteStream) pick(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.next()) % n
}

// FuzzCompile 属性测试：随机构造交替角色的消息链 + 可选工具对 +
// hide/replace/pin 操作序列，编译成功后校验以下不变量：
//   - 角色交替、工具调用与结果同进同出（assertInvariants）；
//   - hide 幂等：把每条 hide 重复一次后编译结果不变；
//   - replace 后原 target 不得以任何形式出现（消息、清单均不得含其 subject）；
//   - 编译确定性：同一上下文重复编译输出一致。
func FuzzCompile(f *testing.F) {
	// 语料：覆盖不同消息数、是否带工具对、各类操作组合
	f.Add([]byte{1, 2, 3, 4, 5})
	f.Add([]byte{0, 255, 7, 7, 3, 1})
	f.Add([]byte{9, 1, 1, 2, 3, 5, 8, 13})
	f.Add([]byte{4, 0, 2, 0, 0, 1, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		st := &byteStream{b: data}
		s := openStore(t)
		var c chain

		// 消息链：角色强制交替，首条必为 user
		n := 2 + st.pick(6)
		msgSubjects := make([]string, 0, n)
		for i := 0; i < n; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			_, sub := c.msg(t, s, role, fmt.Sprintf("m%d", i))
			msgSubjects = append(msgSubjects, sub)
		}
		// 链尾为 user 时可选追加一对工具调用+结果（保持角色交替）
		if n%2 == 1 && st.pick(2) == 0 {
			callEv, tc := c.call(t, s, "fuzz_tool", `{}`, "toolu_fuzz")
			c.result(t, s, callEv, tc, "fuzz结果")
		}
		forkAt := c.last

		// 随机操作序列（记录 replace 目标供事后校验）
		var ops []eventlog.CtxOpInput
		var replaced []string
		k := st.pick(4)
		for i := 0; i < k; i++ {
			switch st.pick(3) {
			case 0: // hide
				ops = append(ops, eventlog.CtxOpInput{
					Op: eventlog.CtxOpHide, Target: msgSubjects[st.pick(n)],
				})
			case 1: // replace：新事件用全新 subject、与原目标同角色同类型，保持角色交替
				idx := st.pick(n)
				replaced = append(replaced, msgSubjects[idx])
				e := c.msgAs(t, s, ids.NewMessageID().String(), msgSubjectsRole(idx), fmt.Sprintf("r%d", i))
				msgSubjects[idx] = e.SubjectID // 后续操作指向新版本
				ops = append(ops, eventlog.CtxOpInput{
					Op: eventlog.CtxOpReplace, Target: replaced[len(replaced)-1], NewID: e.ID,
				})
			case 2: // pin
				ops = append(ops, eventlog.CtxOpInput{
					Op: eventlog.CtxOpPin, Target: msgSubjects[st.pick(n)],
				})
			}
		}

		// 编译两次：一次原样，一次把每条 hide 重复一遍（验证幂等）
		resA, sumA, errA := compileWithOps(t, s, forkAt, ops, false)
		_, sumB, errB := compileWithOps(t, s, forkAt, ops, true)
		if errA != nil || errB != nil {
			if (errA == nil) != (errB == nil) {
				t.Fatalf("重复 hide 改变了编译成败: 单次=%v, 重复=%v", errA, errB)
			}
			return // 无效场景（如 replace 后又指向旧 subject）允许报错
		}

		// 不变量 1：角色交替 + 工具调用与结果同进同出
		assertInvariants(t, resA.Messages)

		// 不变量 2：hide 幂等——重复 hide 编译结果逐条一致
		if sumA != sumB {
			t.Fatalf("hide 幂等被违反: 单次=%q, 重复=%q", sumA, sumB)
		}

		// 不变量 3：replace 后原 target 消失
		for _, target := range replaced {
			for _, item := range readManifest(t, s, resA.ManifestHash) {
				if item.ID == target {
					t.Fatalf("被替换的 target %s 仍出现在清单中", target)
				}
			}
			for _, m := range resA.Messages {
				if m.ToolCall != nil && m.ToolCall.ID == ids.ToolCallID(target) {
					t.Fatalf("被替换的 target %s 仍出现在消息中", target)
				}
			}
		}

		// 不变量 4：编译确定性
		resA2, sumA2, err := compileWithOps(t, s, forkAt, ops, false)
		if err != nil {
			t.Fatalf("同上下文二次编译失败: %v", err)
		}
		if sumA != sumA2 || resA.ManifestHash != resA2.ManifestHash {
			t.Fatal("重复编译输出不一致")
		}
	})
}

// msgSubjectsRole 按交替规则返回消息下标的角色。
func msgSubjectsRole(idx int) string {
	if idx%2 == 1 {
		return "assistant"
	}
	return "user"
}

// compileWithOps 用同一 fork 点建上下文并应用操作序列后编译；
// dupHide 时把每条 hide 操作再追加一遍。返回编译结果与消息摘要。
func compileWithOps(t *testing.T, s *eventlog.Store, forkAt ids.EventID,
	ops []eventlog.CtxOpInput, dupHide bool) (*Result, string, error) {
	t.Helper()
	ctx, err := s.CreateContext(context.Background(), eventlog.ContextInput{ForkAt: forkAt})
	if err != nil {
		t.Fatalf("创建上下文失败: %v", err)
	}
	for _, op := range ops {
		op.Ctx = ctx.ID
		if _, err := s.AppendCtxOp(context.Background(), op); err != nil {
			t.Fatalf("追加 ctx_op 失败: %v", err)
		}
		if dupHide && op.Op == eventlog.CtxOpHide {
			if _, err := s.AppendCtxOp(context.Background(), op); err != nil {
				t.Fatalf("追加重复 hide 失败: %v", err)
			}
		}
	}
	res, err := Compile(context.Background(), s, ctx.ID)
	if err != nil {
		return nil, "", err
	}
	return res, join(summarize(res.Messages)), nil
}
