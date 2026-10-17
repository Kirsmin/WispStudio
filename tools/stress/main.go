// 压测工具：单会话连续 N 轮对话，每轮 2 次只读工具调用。
//
// 它用内存假 provider 驱动真实 HTTP API + runtime + compile + eventlog，
// 输出 seq 连续性、编译耗时、事件/ blob / 数据库体积统计，作为发布前验收。
//
// 用法：
//
//	go run ./tools/stress -turns 200 -dir Data/stress
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"wisp/internal/api"
	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
	"wisp/internal/runtime"
)

// modelFullID 是压测使用的模型全称 id。
const modelFullID = "stress/stress-model"

// stressToken 是压测使用的访问令牌。
const stressToken = "stress-token"

// fakeProvider 按请求末尾角色决定返回工具调用还是最终正文。
// 同一回合第一次调用（末尾是 user）返回 2 个工具调用，第二次（末尾是 tool）返回正文。
type fakeProvider struct {
	calls atomic.Int64
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	callNo := f.calls.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	write := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "tool" {
		write(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []provider.ToolCallDelta{
						{
							Index: 0, ID: fmt.Sprintf("call_%d_1", callNo), Type: "function",
							Function: provider.ToolCallFunction{Name: "read_file", Arguments: `{"path":"seed.txt"}`},
						},
						{
							Index: 1, ID: fmt.Sprintf("call_%d_2", callNo), Type: "function",
							Function: provider.ToolCallFunction{Name: "list_files", Arguments: `{"path":"."}`},
						},
					},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	} else {
		write(map[string]any{
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": "本轮完成。"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28},
		})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// turnStarted 是 POST /messages 的响应。
type turnStarted struct {
	SessionID string `json:"SessionID"`
	TurnID    string `json:"TurnID"`
}

// compilationTimes 收集 runtime 内真实 compile 耗时。
type compilationTimes struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (c *compilationTimes) add(d time.Duration) {
	c.mu.Lock()
	c.durations = append(c.durations, d)
	c.mu.Unlock()
}

func (c *compilationTimes) snapshot() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durations...)
}

func main() {
	turns := flag.Int("turns", 200, "连续对话轮数")
	dir := flag.String("dir", "", "数据目录；空则使用系统临时目录")
	flag.Parse()
	if err := run(*turns, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "压测失败:", err)
		os.Exit(1)
	}
}

func run(turns int, dir string) error {
	if turns <= 0 {
		return fmt.Errorf("turns 必须为正数")
	}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "wisp-stress-")
		if err != nil {
			return err
		}
		dir = tmp
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workspace, "seed.txt"), []byte("wisp stress seed\n"), 0o644); err != nil {
		return err
	}
	dbPath := filepath.Join(base, "eventlog.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	fake := &fakeProvider{}
	fsrv := httptest.NewServer(fake)
	defer fsrv.Close()

	catalog := provider.NewCatalog(&config.Config{Providers: []config.ProviderConfig{{
		ProviderID:   "stress",
		ProviderName: "压测",
		BaseURL:      fsrv.URL,
		Models:       []config.ModelConfig{{ModelID: "stress-model"}},
	}}}, "")
	store, err := eventlog.Open(dbPath)
	if err != nil {
		return err
	}
	times := &compilationTimes{}
	apiSrv, err := api.NewServer(api.Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: workspace,
		Token:         stressToken,
		OnCompile:     times.add,
	})
	if err != nil {
		_ = store.Close()
		return err
	}
	hsrv := httptest.NewServer(apiSrv.Handler())
	client := &http.Client{Timeout: 60 * time.Second}

	var session struct {
		ID string `json:"ID"`
	}
	if err := postJSON(client, hsrv.URL+"/api/sessions", map[string]string{
		"Title": "压测会话",
		"Model": modelFullID,
	}, &session); err != nil {
		return err
	}
	if session.ID == "" {
		return fmt.Errorf("创建会话未返回 ID")
	}

	ctx := context.Background()
	for i := 1; i <= turns; i++ {
		var started turnStarted
		if err := postJSON(client,
			hsrv.URL+"/api/sessions/"+url.PathEscape(session.ID)+"/messages",
			map[string]string{
				"Text":  fmt.Sprintf("第 %d 轮：读取 seed.txt", i),
				"Model": modelFullID,
			}, &started); err != nil {
			return fmt.Errorf("第 %d 轮请求失败: %w", i, err)
		}
		if err := waitCompleted(ctx, store, started.TurnID); err != nil {
			return fmt.Errorf("第 %d 轮未完成: %w", i, err)
		}
		if i%20 == 0 {
			fmt.Printf("已完成 %d/%d 轮\n", i, turns)
		}
	}

	hsrv.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	if got, want := fake.calls.Load(), int64(turns*2); got != want {
		return fmt.Errorf("provider 请求数 %d，期望 %d", got, want)
	}
	fmt.Printf("\n数据目录: %s\n", base)
	fmt.Printf("对话轮数: %d（每轮 2 次工具调用）\n", turns)
	fmt.Printf("provider 请求数: %d\n", fake.calls.Load())
	return printStats(dbPath, times.snapshot(), turns)
}

// waitCompleted 轮询 refs 指针直到回合正常完成。
func waitCompleted(ctx context.Context, store *eventlog.Store, turnID string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		target, err := store.GetRef(ctx, runtime.TurnRefName(turnID))
		if err == nil {
			if target == runtime.RefCompleted {
				return nil
			}
			if runtime.IsTerminalRef(target) {
				return fmt.Errorf("回合停在 %q", target)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待回合超时")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// postJSON 以 JSON 发送请求并解析响应；非 2xx 返回错误。
func postJSON(client *http.Client, endpoint string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", stressToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, text)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// printStats 校验 seq 连续性并打印编译耗时、事件分布与体积。
func printStats(dbPath string, durations []time.Duration, turns int) error {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT seq FROM events ORDER BY seq ASC`)
	if err != nil {
		return err
	}
	var count, maxSeq, expected int64 = 0, 0, 1
	gaps := 0
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			return err
		}
		if seq != expected {
			gaps++
			fmt.Printf("seq 空洞: 期望 %d，实际 %d\n", expected, seq)
		}
		count++
		maxSeq = seq
		expected = seq + 1
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("事件日志为空")
	}

	type eventCount struct {
		Type  string
		Count int64
	}
	typeRows, err := db.Query(`SELECT type, COUNT(*) FROM events GROUP BY type ORDER BY COUNT(*) DESC, type ASC`)
	if err != nil {
		return err
	}
	var types []eventCount
	counts := map[string]int64{}
	for typeRows.Next() {
		var item eventCount
		if err := typeRows.Scan(&item.Type, &item.Count); err != nil {
			typeRows.Close()
			return err
		}
		types = append(types, item)
		counts[item.Type] = item.Count
	}
	if err := typeRows.Close(); err != nil {
		return err
	}
	if got, want := counts[eventlog.TypeToolCalled], int64(turns*2); got != want {
		return fmt.Errorf("tool.called 事件数 %d，期望 %d", got, want)
	}
	if got, want := counts[eventlog.TypeTurnCompleted], int64(turns); got != want {
		return fmt.Errorf("turn.completed 事件数 %d，期望 %d", got, want)
	}

	var blobCount, blobBytes int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(data)), 0) FROM blobs`).Scan(&blobCount, &blobBytes); err != nil {
		return err
	}

	if len(durations) == 0 {
		return fmt.Errorf("没有采集到编译耗时")
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	avg := time.Duration(0)
	if len(durations) > 0 {
		avg = sum / time.Duration(len(durations))
	}
	p95 := durations[(len(durations)-1)*95/100]
	maxCompile := durations[len(durations)-1]

	fmt.Printf("事件总数: %d，seq: 1..%d，空洞: %d\n", count, maxSeq, gaps)
	for _, item := range types {
		fmt.Printf("  %-18s %6d\n", item.Type, item.Count)
	}
	fmt.Printf("blob 数: %d，blob 总量: %d 字节\n", blobCount, blobBytes)
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(dbPath + suffix); err == nil {
			fmt.Printf("数据库文件 %s: %d 字节\n", filepath.Base(dbPath+suffix), info.Size())
		}
	}
	fmt.Printf("工具调用: %d（期望 %d）\n", counts[eventlog.TypeToolCalled], turns*2)
	fmt.Printf("编译样本: %d，耗时 avg=%s p95=%s max=%s（上限 100ms）\n", len(durations),
		avg.Round(time.Microsecond), p95.Round(time.Microsecond), maxCompile.Round(time.Microsecond))

	if gaps > 0 {
		return fmt.Errorf("seq 存在 %d 个空洞", gaps)
	}
	if maxCompile >= 100*time.Millisecond {
		return fmt.Errorf("编译耗时 %s 超过 100ms", maxCompile)
	}
	return nil
}
