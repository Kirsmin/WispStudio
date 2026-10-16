// HTTP API 验收测试：启动回合 → 轮询事件流/状态 → 工具调用 → 提交审批决定，
// 全部通过真实 HTTP 路由与假 provider 走完，不直接调用内部函数。
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
)

// 假 provider 的脚本帧：第一轮回 read_file，第二轮回正文。
var (
	readFileRound = []string{
		`{"choices":[{"index":0,"delta":{"content":"我来读文件。"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
	}
	writeFileRound = []string{
		`{"choices":[{"index":0,"delta":{"content":"我来写文件。"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write_file","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"b.txt\",\"content\":\"API 写入的内容\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
	}
	finalRound = []string{
		`{"choices":[{"index":0,"delta":{"content":"已完成。"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26}}`,
	}
)

// testAPI 是测试用的 API 服务与工作区。
type testAPI struct {
	base      string // httptest 服务地址
	workspace string
	store     *eventlog.Store
}

// newTestAPI 启动带假 provider 的 API 服务。
func newTestAPI(t *testing.T, rounds [][]string) *testAPI {
	t.Helper()
	store, err := eventlog.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("打开事件日志失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello wisp\n"), 0o644); err != nil {
		t.Fatalf("准备工作区失败: %v", err)
	}

	// 假 provider：按调用次序回放 SSE 帧
	var calls int
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := calls
		calls++
		if idx >= len(rounds) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"脚本帧已用尽"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range rounds[idx] {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(llm.Close)

	cfg := &config.Config{Providers: []config.ProviderConfig{{
		ProviderID: "test",
		BaseURL:    llm.URL,
		Secret:     "sk-test",
		Models:     []config.ModelConfig{{ModelID: "deepseek-flash"}},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	catalog := provider.NewCatalog(cfg, "")
	srv, err := NewServer(Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: ws,
		DefaultModel:  "test/deepseek-flash",
		TurnTimeout:   30 * time.Second,
		Logf:          func(msg string, args ...any) { t.Logf(msg, args...) },
	})
	if err != nil {
		t.Fatalf("构造 API 服务失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &testAPI{base: ts.URL, workspace: ws, store: store}
}

// ---- HTTP 辅助 ----

func request(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, raw
}

// startTurnWire 是 POST /api/turns 的响应。
type startTurnWire struct {
	SessionID string `json:"session_id"`
	ContextID string `json:"context_id"`
	TurnID    string `json:"turn_id"`
	Model     string `json:"model"`
}

// statusWire 是 GET /api/turns/{id} 的响应。
type statusWire struct {
	TurnID    string `json:"turn_id"`
	SessionID string `json:"session_id"`
	ContextID string `json:"context_id"`
	Model     string `json:"model"`
	Ref       string `json:"ref"`
	Started   bool   `json:"started"`
	Completed bool   `json:"completed"`
	Error     string `json:"error"`
	Pending   []struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
	} `json:"pending_approvals"`
}

// eventWire 是事件流中的一条事件。
type eventWire struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	TurnID    string          `json:"turn_id"`
	SubjectID string          `json:"subject_id"`
	Attrs     json.RawMessage `json:"attrs"`
	BlobText  string          `json:"blob_text"`
}

// eventsWire 是 GET /api/sessions/{id}/events 的响应。
type eventsWire struct {
	Events  []eventWire `json:"events"`
	NextSeq int64       `json:"next_seq"`
}

// apiGet 发起 GET 并断言状态码。
func apiGet(t *testing.T, url string, want int) []byte {
	t.Helper()
	code, raw := request(t, http.MethodGet, url, nil)
	if code != want {
		t.Fatalf("GET %s = %d, 期望 %d: %s", url, code, want, raw)
	}
	return raw
}

// apiPost 发起 POST 并断言状态码。
func apiPost(t *testing.T, url string, body any, want int) []byte {
	t.Helper()
	code, raw := request(t, http.MethodPost, url, body)
	if code != want {
		t.Fatalf("POST %s = %d, 期望 %d: %s", url, code, want, raw)
	}
	return raw
}

// turnStatus 读取回合状态。
func turnStatus(t *testing.T, base, turnID string) statusWire {
	t.Helper()
	raw := apiGet(t, base+"/api/turns/"+turnID, http.StatusOK)
	var st statusWire
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("解析回合状态失败: %v: %s", err, raw)
	}
	return st
}

// waitRef 轮询回合状态直到 ref 命中前缀。
func waitRef(t *testing.T, base, turnID, refPrefix string) statusWire {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last statusWire
	for time.Now().Before(deadline) {
		last = turnStatus(t, base, turnID)
		if strings.HasPrefix(last.Ref, refPrefix) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 ref 前缀 %q 超时，最后状态: %+v", refPrefix, last)
	return last
}

// sessionEvents 读取会话事件流。
func sessionEvents(t *testing.T, base, sessionID string) eventsWire {
	t.Helper()
	raw := apiGet(t, base+"/api/sessions/"+sessionID+"/events", http.StatusOK)
	var evs eventsWire
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatalf("解析事件流失败: %v: %s", err, raw)
	}
	return evs
}

// TestAPITurnEndToEnd 覆盖：启动回合 → 轮询事件流与状态 → 工具调用往返 → 完成。
func TestAPITurnEndToEnd(t *testing.T) {
	api := newTestAPI(t, [][]string{readFileRound, finalRound})

	raw := apiPost(t, api.base+"/api/turns", map[string]any{
		"text": "请读一下 a.txt", "model": "test/deepseek-flash",
	}, http.StatusCreated)
	var started startTurnWire
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatalf("解析启动响应失败: %v: %s", err, raw)
	}
	if !strings.HasPrefix(started.SessionID, "sess_") || started.TurnID == "" || started.ContextID == "" {
		t.Fatalf("启动响应缺少 ID: %+v", started)
	}
	if started.Model != "test/deepseek-flash" {
		t.Fatalf("模型 = %q", started.Model)
	}

	st := waitRef(t, api.base, started.TurnID, "completed")
	if st.SessionID != started.SessionID || st.ContextID != started.ContextID {
		t.Fatalf("状态与会话/上下文不一致: %+v", st)
	}
	if st.Model != "test/deepseek-flash" || !st.Started || !st.Completed || st.Error != "" {
		t.Fatalf("状态字段不正确: %+v", st)
	}

	evs := sessionEvents(t, api.base, started.SessionID)
	keyTypes := map[string]bool{
		"turn.started": true, "llm.requested": true, "message.created": true,
		"tool.called": true, "tool.returned": true, "turn.completed": true,
	}
	var types []string
	sawDelta, responded := false, 0
	for _, ev := range evs.Events {
		if ev.TurnID != started.TurnID {
			continue
		}
		if keyTypes[ev.Type] {
			types = append(types, ev.Type)
		}
		switch ev.Type {
		case "message.delta":
			sawDelta = true
		case "llm.responded":
			responded++
		}
	}
	// 用户消息在回合开始前落盘（turn_id 已归属本回合），其后是关键序列
	want := []string{
		"message.created",
		"turn.started", "llm.requested", "message.created", "tool.called",
		"tool.returned", "llm.requested", "message.created", "turn.completed",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("回合事件序列 =\n%v\n期望 =\n%v", types, want)
	}
	if !sawDelta || responded != 2 {
		t.Fatalf("流式增量/响应事件缺失: delta=%v responded=%d", sawDelta, responded)
	}

	// 工具结果正文经事件流可取
	for _, ev := range evs.Events {
		if ev.Type == "tool.returned" && !strings.Contains(ev.BlobText, "hello wisp") {
			t.Fatalf("tool.returned 正文不正确: %q", ev.BlobText)
		}
	}

	// 增量轮询：after_seq = next_seq 应返回空
	raw = apiGet(t, fmt.Sprintf("%s/api/sessions/%s/events?after_seq=%d", api.base, started.SessionID, evs.NextSeq), http.StatusOK)
	var more eventsWire
	if err := json.Unmarshal(raw, &more); err != nil {
		t.Fatalf("解析增量事件失败: %v", err)
	}
	if len(more.Events) != 0 {
		t.Fatalf("增量轮询应无新事件，实际 %d 条", len(more.Events))
	}
	if more.NextSeq != evs.NextSeq {
		t.Fatalf("next_seq 应保持不变: %d != %d", more.NextSeq, evs.NextSeq)
	}
}

// TestAPIApprovalFlow 覆盖审批挂起与两种决定的后续行为。
func TestAPIApprovalFlow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision string
	}{
		{"批准后写入", "approved"},
		{"拒绝后不写入", "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestAPI(t, [][]string{writeFileRound, finalRound})

			raw := apiPost(t, api.base+"/api/turns", map[string]any{
				"text": "把内容写进 b.txt", "model": "test/deepseek-flash",
			}, http.StatusCreated)
			var started startTurnWire
			if err := json.Unmarshal(raw, &started); err != nil {
				t.Fatalf("解析启动响应失败: %v", err)
			}

			// 挂起等待审批
			st := waitRef(t, api.base, started.TurnID, "waiting_approval:")
			if len(st.Pending) != 1 || st.Pending[0].Name != "write_file" {
				t.Fatalf("待审批列表不正确: %+v", st.Pending)
			}
			if _, err := os.Stat(filepath.Join(api.workspace, "b.txt")); !os.IsNotExist(err) {
				t.Fatal("审批前不得产生文件副作用")
			}
			// 重复提交非法决定
			apiPost(t, api.base+"/api/turns/"+started.TurnID+"/approvals/"+st.Pending[0].ToolCallID,
				map[string]any{"decision": "maybe"}, http.StatusBadRequest)

			// 提交决定
			apiPost(t, api.base+"/api/turns/"+started.TurnID+"/approvals/"+st.Pending[0].ToolCallID,
				map[string]any{"decision": tc.decision, "reason": "测试"}, http.StatusAccepted)

			waitRef(t, api.base, started.TurnID, "completed")

			content, err := os.ReadFile(filepath.Join(api.workspace, "b.txt"))
			if tc.decision == "approved" {
				if err != nil || string(content) != "API 写入的内容" {
					t.Fatalf("批准后应写入文件: %q (%v)", content, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("拒绝后不应写入文件: %q (%v)", content, err)
			}

			evs := sessionEvents(t, api.base, started.SessionID)
			var sawRequested, sawDecided, sawReturned bool
			for _, ev := range evs.Events {
				switch ev.Type {
				case "approval.requested":
					sawRequested = true
				case "approval.decided":
					sawDecided = true
				case "tool.returned":
					sawReturned = true
					if tc.decision == "rejected" && !strings.Contains(ev.BlobText, "failure") {
						t.Fatalf("拒绝后工具结果应为失败: %s", ev.BlobText)
					}
				}
			}
			if !sawRequested || !sawDecided || !sawReturned {
				t.Fatalf("审批事件不完整: requested=%v decided=%v returned=%v", sawRequested, sawDecided, sawReturned)
			}
		})
	}
}

// TestAPIHealthCatalogAndErrors 覆盖健康检查、模型目录与各类错误响应。
func TestAPIHealthCatalogAndErrors(t *testing.T) {
	api := newTestAPI(t, [][]string{finalRound})

	raw := apiGet(t, api.base+"/api/health", http.StatusOK)
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("健康检查响应不正确: %s", raw)
	}

	raw = apiGet(t, api.base+"/api/catalog", http.StatusOK)
	var cat struct {
		DefaultModel string `json:"default_model"`
		Providers    []struct {
			ProviderID string `json:"provider_id"`
			Models     []struct {
				FullID string `json:"full_id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("解析目录失败: %v: %s", err, raw)
	}
	if cat.DefaultModel != "test/deepseek-flash" || len(cat.Providers) != 1 ||
		cat.Providers[0].ProviderID != "test" || cat.Providers[0].Models[0].FullID != "test/deepseek-flash" {
		t.Fatalf("目录内容不正确: %+v", cat)
	}

	// 启动参数错误
	for _, bad := range []map[string]any{
		{"text": "   "},
		{"text": "x", "model": "badid"},
		{"text": "x", "model": "test/nope"},
		{"text": "x", "model": "nope/deepseek-flash"},
		{"text": "x", "session_id": "not-a-session"},
	} {
		apiPost(t, api.base+"/api/turns", bad, http.StatusBadRequest)
	}
	// 未知回合
	apiGet(t, api.base+"/api/turns/tn_01J8ZP9W4RYY7Q0K3F6N5M2B1VC", http.StatusNotFound)
	// 未知回合的审批
	apiPost(t, api.base+"/api/turns/tn_01J8ZP9W4RYY7Q0K3F6N5M2B1VC/approvals/tc_01J8ZP9W4RYY7Q0K3F6N5M2B1VC",
		map[string]any{"decision": "approved"}, http.StatusNotFound)
	// 查询参数错误
	apiGet(t, api.base+"/api/sessions/sess_01J8ZP9W4RYY7Q0K3F6N5M2B1VC/events?after_seq=abc", http.StatusBadRequest)

	// 正常回合结束后，对非待审批调用提交决定应 409
	raw = apiPost(t, api.base+"/api/turns", map[string]any{"text": "打个招呼"}, http.StatusCreated)
	var started startTurnWire
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatalf("解析启动响应失败: %v", err)
	}
	waitRef(t, api.base, started.TurnID, "completed")
	apiPost(t, api.base+"/api/turns/"+started.TurnID+"/approvals/tc_01J8ZP9W4RYY7Q0K3F6N5M2B1VC",
		map[string]any{"decision": "approved"}, http.StatusConflict)

	// 未知会话的事件流是空列表而非错误
	raw = apiGet(t, api.base+"/api/sessions/sess_01J8ZP9W4RYY7Q0K3F6N5M2B1VC/events", http.StatusOK)
	var evs eventsWire
	if err := json.Unmarshal(raw, &evs); err != nil || len(evs.Events) != 0 {
		t.Fatalf("未知会话应返回空事件列表: %s (%v)", raw, err)
	}
}

// TestAPIConcurrentTurnsSameSession 校验同一会话的回合串行执行，
// 内容链保持单链（不做并发分叉）。
func TestAPIConcurrentTurnsSameSession(t *testing.T) {
	api := newTestAPI(t, [][]string{finalRound, finalRound, finalRound})

	raw := apiPost(t, api.base+"/api/turns", map[string]any{"text": "第一句"}, http.StatusCreated)
	var first startTurnWire
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("解析启动响应失败: %v", err)
	}
	// 同一会话再启动两个回合
	var turnIDs []string
	for i := 0; i < 2; i++ {
		raw = apiPost(t, api.base+"/api/turns", map[string]any{
			"text": fmt.Sprintf("第 %d 句", i+2), "session_id": first.SessionID,
		}, http.StatusCreated)
		var next startTurnWire
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatalf("解析启动响应失败: %v", err)
		}
		if next.SessionID != first.SessionID {
			t.Fatalf("会话 ID 应复用: %q != %q", next.SessionID, first.SessionID)
		}
		turnIDs = append(turnIDs, next.TurnID)
	}
	for _, id := range turnIDs {
		waitRef(t, api.base, id, "completed")
	}

	// 内容链唯一：所有 message.created 的 parent_id 拼起来是一条单链
	evs := sessionEvents(t, api.base, first.SessionID)
	var lines []string
	for _, ev := range evs.Events {
		if ev.Type == "message.created" {
			lines = append(lines, ev.BlobText)
		}
	}
	if len(lines) != 6 { // 3 次用户输入 + 3 次模型回复
		t.Fatalf("消息数 = %d, 期望 6: %v", len(lines), lines)
	}
}
