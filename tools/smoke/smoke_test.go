// 冒烟测试：用子进程 + 文件事件日志 + 假 provider 端到端验证
// HTTP API、运行时、编译与事件日志的协作。
//
// 流程：
//  1. 子进程启动真实 API（假 provider 按用户消息内容决定行为）；
//  2. 父进程经 HTTP 驱动三个会话：
//     会话 A：正常回合，流式正文 + 一次 read_file 工具调用 + 完成；
//     会话 B：长任务回合，流式开始后 POST /stop 停止；
//     会话 C：长任务回合，流式开始后 SIGKILL 子进程模拟进程崩溃；
//  3. 父进程重新打开事件日志并构造 API，Recover 后验证：
//     会话 C 收敛到完成并补写 assistant 消息；
//     会话 B 保持停止终态、事件不再增长；会话 A 不受影响。
package smoke

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wisp/internal/api"
	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
	"wisp/internal/runtime"
)

// smokeToken 是父进程与子进程约定的访问令牌。
const smokeToken = "smoke-token"

// modelFullID 是冒烟测试使用的模型全称 id。
const modelFullID = "smoke/smoke-model"

// TestMain 在冒烟子进程模式下直接运行子进程逻辑。
func TestMain(m *testing.M) {
	if os.Getenv("WISP_SMOKE_CHILD") == "1" {
		runChild()
		return
	}
	os.Exit(m.Run())
}

// runChild 是子进程入口：启动真实 API 并阻塞，等待父进程 SIGKILL。
func runChild() {
	dir := os.Getenv("WISP_SMOKE_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "冒烟子进程缺少 WISP_SMOKE_DIR")
		os.Exit(1)
	}
	store, err := eventlog.Open(filepath.Join(dir, "eventlog.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开事件日志失败:", err)
		os.Exit(1)
	}
	fake := httptest.NewServer(&childProvider{})
	defer fake.Close()
	catalog := provider.NewCatalog(&config.Config{Providers: []config.ProviderConfig{{
		ProviderID:   "smoke",
		ProviderName: "冒烟",
		BaseURL:      fake.URL,
		Models:       []config.ModelConfig{{ModelID: "smoke-model"}},
	}}}, "")
	srv, err := api.NewServer(api.Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: filepath.Join(dir, "workspace"),
		Token:         smokeToken,
		Logf: func(msg string, args ...any) {
			fmt.Fprintf(os.Stderr, msg+"\n", args...)
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "构造 API 失败:", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "监听失败:", err)
		os.Exit(1)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()
	fmt.Println("http://" + ln.Addr().String())
	select {} // 等待父进程结束子进程
}

// childProvider 是按请求内容决定行为的假 provider：
//   - 用户消息含“长任务”或“崩溃任务”：流式一帧后挂起（供停止/崩溃用例）；
//   - 末尾是 tool 结果：返回最终正文；
//   - 其余：返回一次 read_file 工具调用。
type childProvider struct{}

func (p *childProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "空请求", http.StatusBadRequest)
		return
	}
	last := req.Messages[len(req.Messages)-1]
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	write := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	if last.Role == "user" && (strings.Contains(last.Content, "长任务") || strings.Contains(last.Content, "崩溃任务")) {
		write(map[string]any{"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "思考中…"},
		}}})
		<-r.Context().Done() // 挂起直到客户端断开或进程被杀
		return
	}
	if last.Role == "tool" {
		write(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"content": "工具调用完成。"}, "finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28},
		})
	} else {
		write(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{
					"role": "assistant",
					"tool_calls": []provider.ToolCallDelta{{
						Index: 0, ID: "smoke_call_1", Type: "function",
						Function: provider.ToolCallFunction{Name: "read_file", Arguments: `{"path":"seed.txt"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// textProvider 是恢复阶段使用的假 provider：任何请求都返回一段最终正文。
type textProvider struct{}

func (p *textProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"恢复完成。"}}]}`+"\n\n")
	fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// sseEvent 是 SSE 数据帧的协议投影子集。
type sseEvent struct {
	ID       string          `json:"ID"`
	Seq      int64           `json:"Seq"`
	Type     string          `json:"Type"`
	TurnID   string          `json:"TurnID"`
	Attrs    json.RawMessage `json:"Attrs"`
	BlobText string          `json:"BlobText"`
}

// watcher 收集某个会话的 SSE 事件流，供轮询断言。
type watcher struct {
	mu     sync.Mutex
	events []sseEvent
	cancel context.CancelFunc
}

// watchSession 订阅会话事件流（after_seq=0），返回收集器。
func watchSession(t *testing.T, base, sessionID string) *watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/sessions/"+sessionID+"/events?after_seq=0", nil)
	if err != nil {
		cancel()
		t.Fatalf("构造 SSE 请求失败: %v", err)
	}
	req.Header.Set("X-Token", smokeToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("订阅事件流失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("订阅事件流 = %d", resp.StatusCode)
	}
	w := &watcher{cancel: cancel}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var ev sseEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			w.mu.Lock()
			w.events = append(w.events, ev)
			w.mu.Unlock()
		}
	}()
	return w
}

// waitFor 轮询等待任一事件满足条件。
func (w *watcher) waitFor(t *testing.T, what string, timeout time.Duration, cond func(sseEvent) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		w.mu.Lock()
		hit := false
		for _, ev := range w.events {
			if cond(ev) {
				hit = true
				break
			}
		}
		w.mu.Unlock()
		if hit {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// snapshot 返回已收集事件的副本。
func (w *watcher) snapshot() []sseEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]sseEvent(nil), w.events...)
}

// turnStartedBody 是 POST /messages 的响应。
type turnStartedBody struct {
	SessionID string `json:"SessionID"`
	ContextID string `json:"ContextID"`
	TurnID    string `json:"TurnID"`
}

// sessionBody 是 POST /sessions 的响应。
type sessionBody struct {
	ID string `json:"ID"`
}

// stopBody 是 POST /stop 的响应。
type stopBody struct {
	Stopped bool `json:"Stopped"`
}

// childProc 是冒烟子进程句柄。
type childProc struct {
	addr string
	cmd  *exec.Cmd
}

// startChild 启动冒烟子进程并等待它打印监听地址。
func startChild(t *testing.T, dir string) *childProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试可执行文件失败: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "WISP_SMOKE_CHILD=1", "WISP_SMOKE_DIR="+dir)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("接管子进程输出失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动冒烟子进程失败: %v", err)
	}
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			select {
			case lines <- line:
			default:
			}
		}
	}()
	select {
	case addr := <-lines:
		if !strings.HasPrefix(addr, "http://") {
			_ = cmd.Process.Kill()
			t.Fatalf("子进程启动输出异常: %q", addr)
		}
		return &childProc{addr: addr, cmd: cmd}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("等待冒烟子进程启动超时")
	}
	return nil
}

// kill 强制结束子进程（模拟进程崩溃）。
func (c *childProc) kill(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatalf("结束子进程失败: %v", err)
	}
	_, _ = c.cmd.Process.Wait()
}

// postJSON 发送 JSON 请求并解析响应体；非 2xx 直接失败。
func postJSON(t *testing.T, endpoint string, body, out any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", smokeToken)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s 失败: %v", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		t.Fatalf("POST %s = %d: %s", endpoint, resp.StatusCode, text)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("解析 %s 响应失败: %v", endpoint, err)
		}
	}
}

// createSession 创建一个指定标题的会话。
func createSession(t *testing.T, base, title string) string {
	t.Helper()
	var body sessionBody
	postJSON(t, base+"/api/sessions", map[string]string{"Title": title, "Model": modelFullID}, &body)
	if body.ID == "" {
		t.Fatalf("创建会话 %q 未返回 ID", title)
	}
	return body.ID
}

// sendMessage 发送一条用户消息并返回回合信息。
func sendMessage(t *testing.T, base, sessionID, text string) turnStartedBody {
	t.Helper()
	var body turnStartedBody
	postJSON(t, base+"/api/sessions/"+sessionID+"/messages",
		map[string]string{"Text": text, "Model": modelFullID}, &body)
	if body.TurnID == "" {
		t.Fatalf("发送消息未返回 TurnID")
	}
	return body
}

// refOf 读取回合指针；不存在视为空串。
func refOf(t *testing.T, store *eventlog.Store, turnID string) string {
	t.Helper()
	ref, err := store.GetRef(context.Background(), runtime.TurnRefName(turnID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		t.Fatalf("读取指针失败: %v", err)
	}
	return ref
}

// waitRef 轮询等待回合指针到达目标值。
func waitRef(t *testing.T, store *eventlog.Store, turnID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ref := refOf(t, store, turnID); ref == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待回合 %s 到 %q 超时（当前 %q）", turnID, want, refOf(t, store, turnID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSmoke 是端到端冒烟测试。
func TestSmoke(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "seed.txt"), []byte("wisp smoke seed\n"), 0o644); err != nil {
		t.Fatalf("写 seed.txt 失败: %v", err)
	}

	proc := startChild(t, dir)
	t.Cleanup(func() {
		if proc.cmd.Process != nil {
			_ = proc.cmd.Process.Kill()
			_, _ = proc.cmd.Process.Wait()
		}
	})
	base := proc.addr

	// ---- 会话 A：流式正文 + 一次 read_file 工具调用 + 正常完成 ----
	sessA := createSession(t, base, "常规会话")
	wa := watchSession(t, base, sessA)
	defer wa.cancel()
	turnA := sendMessage(t, base, sessA, "常规任务：读取 seed.txt")
	wa.waitFor(t, "会话 A 回合完成", 30*time.Second, func(ev sseEvent) bool {
		return ev.Type == eventlog.TypeTurnCompleted && ev.TurnID == turnA.TurnID
	})
	assertStreamingTurn(t, wa.snapshot(), turnA.TurnID)

	// ---- 会话 B：流式开始后停止 ----
	sessB := createSession(t, base, "停止会话")
	wb := watchSession(t, base, sessB)
	defer wb.cancel()
	turnB := sendMessage(t, base, sessB, "长任务：慢慢来")
	wb.waitFor(t, "会话 B 进入流式", 15*time.Second, func(ev sseEvent) bool {
		return ev.Type == eventlog.TypeMessageDelta && ev.TurnID == turnB.TurnID && ev.BlobText != ""
	})
	var stopped stopBody
	postJSON(t, base+"/api/sessions/"+sessB+"/stop", nil, &stopped)
	if !stopped.Stopped {
		t.Fatal("停止会话 B 未返回 Stopped=true")
	}
	wb.waitFor(t, "会话 B 停止终态", 15*time.Second, func(ev sseEvent) bool {
		return ev.Type == eventlog.TypeTurnCompleted && ev.TurnID == turnB.TurnID
	})

	// ---- 会话 C：流式开始后杀掉子进程，模拟崩溃 ----
	sessC := createSession(t, base, "崩溃会话")
	wc := watchSession(t, base, sessC)
	defer wc.cancel()
	turnC := sendMessage(t, base, sessC, "崩溃任务：挂起")
	wc.waitFor(t, "会话 C 进入流式", 15*time.Second, func(ev sseEvent) bool {
		return ev.Type == eventlog.TypeMessageDelta && ev.TurnID == turnC.TurnID && ev.BlobText != ""
	})
	proc.kill(t)
	for _, snap := range [][]sseEvent{wa.snapshot(), wb.snapshot(), wc.snapshot()} {
		for _, ev := range snap {
			if ev.Type == eventlog.TypeTurnCompleted && ev.TurnID == turnC.TurnID {
				t.Fatal("崩溃前会话 C 不应写入 turn.completed")
			}
		}
	}

	// ---- 重启：重新打开事件日志并 Recover ----
	store, err := eventlog.Open(filepath.Join(dir, "eventlog.db"))
	if err != nil {
		t.Fatalf("重新打开事件日志失败: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if ref := refOf(t, store, turnA.TurnID); ref != runtime.RefCompleted {
		t.Fatalf("崩溃前会话 A 应已完成，实际指针 %q", ref)
	}
	if ref := refOf(t, store, turnB.TurnID); ref != "failed:stopped" {
		t.Fatalf("崩溃前会话 B 应为停止终态，实际指针 %q", ref)
	}
	if ref := refOf(t, store, turnC.TurnID); ref != runtime.RefRunning {
		t.Fatalf("崩溃遗留的会话 C 应为 running，实际指针 %q", ref)
	}
	assertSessionA(t, store, turnA.TurnID)

	eventsB, err := store.ListByTurn(ctx, turnB.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("读取会话 B 事件失败: %v", err)
	}

	recoverSrv := httptest.NewServer(&textProvider{})
	defer recoverSrv.Close()
	catalog := provider.NewCatalog(&config.Config{Providers: []config.ProviderConfig{{
		ProviderID:   "smoke",
		ProviderName: "冒烟",
		BaseURL:      recoverSrv.URL,
		Models:       []config.ModelConfig{{ModelID: "smoke-model"}},
	}}}, "")
	srv, err := api.NewServer(api.Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: workspace,
		Token:         smokeToken,
	})
	if err != nil {
		t.Fatalf("构造恢复 API 失败: %v", err)
	}
	if err := srv.Recover(ctx); err != nil {
		t.Fatalf("Recover 失败: %v", err)
	}
	waitRef(t, store, turnC.TurnID, runtime.RefCompleted, 30*time.Second)

	if ref := refOf(t, store, turnB.TurnID); ref != "failed:stopped" {
		t.Fatalf("恢复后会话 B 指针被改动: %q", ref)
	}
	afterB, err := store.ListByTurn(ctx, turnB.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("恢复后读取会话 B 事件失败: %v", err)
	}
	if len(afterB) != len(eventsB) {
		t.Fatalf("恢复后会话 B 事件数从 %d 变为 %d", len(eventsB), len(afterB))
	}

	evsC, err := store.ListByTurn(ctx, turnC.TurnID, 0, 0)
	if err != nil {
		t.Fatalf("读取会话 C 事件失败: %v", err)
	}
	found := false
	for _, ev := range evsC {
		if ev.Type != eventlog.TypeMessageCreated {
			continue
		}
		var attrs eventlog.MessageCreatedAttrs
		if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
			t.Fatalf("解析 message.created attrs 失败: %v", err)
		}
		if attrs.Role != "assistant" {
			continue
		}
		text, err := store.GetBlob(ctx, ev.Blob)
		if err != nil {
			t.Fatalf("读取恢复消息正文失败: %v", err)
		}
		if string(text) != "恢复完成。" {
			t.Fatalf("恢复的消息正文 = %q", text)
		}
		found = true
	}
	if !found {
		t.Fatal("恢复后的会话 C 缺少 assistant 消息")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("关闭恢复 API 失败: %v", err)
	}
}

// assertStreamingTurn 断言会话 A 的完整回合事件：流式增量、一次 read_file
// 工具调用、结果与最终正文、正常完成。
func assertStreamingTurn(t *testing.T, evs []sseEvent, turnID string) {
	t.Helper()
	var deltas, calls, results int
	var completed bool
	for _, ev := range evs {
		if ev.TurnID != turnID {
			continue
		}
		switch ev.Type {
		case eventlog.TypeMessageDelta:
			if ev.BlobText != "" {
				deltas++
			}
		case eventlog.TypeToolCalled:
			calls++
			var attrs eventlog.ToolCalledAttrs
			if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
				t.Fatalf("解析 tool.called attrs 失败: %v", err)
			}
			if attrs.Name != "read_file" {
				t.Fatalf("工具调用名 = %q, 期望 read_file", attrs.Name)
			}
			if attrs.ProviderID == "" {
				t.Fatal("工具调用缺少 provider_id")
			}
		case eventlog.TypeToolReturned:
			results++
		case eventlog.TypeTurnCompleted:
			completed = true
			var attrs eventlog.TurnCompletedAttrs
			if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
				t.Fatalf("解析 turn.completed attrs 失败: %v", err)
			}
			if attrs.Error != "" {
				t.Fatalf("会话 A 回合失败: %s", attrs.Error)
			}
		}
	}
	if deltas == 0 {
		t.Fatal("会话 A 未观察到流式增量")
	}
	if calls != 1 || results != 1 {
		t.Fatalf("会话 A 工具调用/结果数 = %d/%d, 期望 1/1", calls, results)
	}
	if !completed {
		t.Fatal("会话 A 未到达 turn.completed")
	}
	// 最终正文：工具结果后的 assistant 消息
	for _, ev := range evs {
		if ev.Type == eventlog.TypeMessageCreated && ev.TurnID == turnID && ev.BlobText == "工具调用完成。" {
			return
		}
	}
	t.Fatal("会话 A 缺少工具调用后的最终正文")
}

// assertSessionA 在重新打开的事件日志上复核会话 A 的轨迹。
func assertSessionA(t *testing.T, store *eventlog.Store, turnID string) {
	t.Helper()
	ctx := context.Background()
	evs, err := store.ListByTurn(ctx, turnID, 0, 0)
	if err != nil {
		t.Fatalf("读取会话 A 事件失败: %v", err)
	}
	var calls, results int
	var final string
	for _, ev := range evs {
		switch ev.Type {
		case eventlog.TypeToolCalled:
			calls++
			var attrs eventlog.ToolCalledAttrs
			if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
				t.Fatalf("解析 tool.called attrs 失败: %v", err)
			}
			if attrs.Name != "read_file" || attrs.ProviderID == "" {
				t.Fatalf("工具调用属性不完整: %+v", attrs)
			}
		case eventlog.TypeToolReturned:
			results++
		case eventlog.TypeMessageCreated:
			var attrs eventlog.MessageCreatedAttrs
			if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
				t.Fatalf("解析 message.created attrs 失败: %v", err)
			}
			if attrs.Role != "assistant" {
				continue
			}
			if ev.Blob == "" {
				continue // 纯工具调用消息没有正文
			}
			blob, err := store.GetBlob(ctx, ev.Blob)
			if err != nil {
				t.Fatalf("读取正文失败: %v", err)
			}
			if string(blob) == "工具调用完成。" {
				final = string(blob)
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("会话 A 工具调用/结果数 = %d/%d, 期望 1/1", calls, results)
	}
	if final == "" {
		t.Fatal("会话 A 缺少最终正文")
	}
}
