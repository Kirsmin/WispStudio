package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"wisp/internal/ids"
)

// TestConcurrentAppend 并发追加 1000 个事件，校验 seq 全局单调且连续。
func TestConcurrentAppend(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "eventlog.db"))
	if err != nil {
		t.Fatalf("Open 出错: %v", err)
	}
	defer s.Close()

	const total = 1000
	const workers = 16
	seqCh := make(chan int64, total)
	errCh := make(chan error, total)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// 整除会丢掉余数，前 total%workers 个 worker 各多追加一个，保证总数恰为 total
			n := total / workers
			if w < total%workers {
				n++
			}
			for i := 0; i < n; i++ {
				ev, err := s.AppendEvent(context.Background(), EventInput{
					Type:      "test.append",
					SessionID: "sess-concurrent",
					Actor:     fmt.Sprintf("worker-%d", w),
					Attrs:     map[string]int{"n": i},
				})
				if err != nil {
					errCh <- err
					return
				}
				seqCh <- ev.Seq
			}
		}(w)
	}
	wg.Wait()
	close(seqCh)
	close(errCh)

	for err := range errCh {
		t.Fatalf("追加事件出错: %v", err)
	}

	seqs := make([]int64, 0, total)
	for seq := range seqCh {
		seqs = append(seqs, seq)
	}
	if len(seqs) != total {
		t.Fatalf("追加事件数 = %d, 期望 %d", len(seqs), total)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	// seq 必须从 1 开始连续无洞，证明分配与写入全局单调
	for i, seq := range seqs {
		if want := int64(i + 1); seq != want {
			t.Fatalf("seq 不连续: seqs[%d] = %d, 期望 %d", i, seq, want)
		}
	}

	// 按 seq 顺序读回，也应严格单调递增
	events, err := s.ListBySession(context.Background(), "sess-concurrent", 0, 0)
	if err != nil {
		t.Fatalf("ListBySession 出错: %v", err)
	}
	if len(events) != total {
		t.Fatalf("读回事件数 = %d, 期望 %d", len(events), total)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("seq 非单调递增: events[%d].Seq=%d, events[%d].Seq=%d", i, events[i].Seq, i-1, events[i-1].Seq)
		}
	}
}

// TestBatchAtomic 校验同一 turn 的批量事件在同一事务原子写入且 seq 连续。
func TestBatchAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "eventlog.db"))
	if err != nil {
		t.Fatalf("Open 出错: %v", err)
	}
	defer s.Close()

	turn := ids.NewTurnID().String()
	ins := make([]EventInput, 5)
	for i := range ins {
		ins[i] = EventInput{
			Type:   "test.batch",
			TurnID: turn,
			Attrs:  map[string]int{"i": i},
		}
	}
	events, err := s.AppendEvents(context.Background(), ins)
	if err != nil {
		t.Fatalf("AppendEvents 出错: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("批量写入数 = %d, 期望 5", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("批量事件 seq 不连续: %d → %d", events[i-1].Seq, events[i].Seq)
		}
	}

	// Blob 去重：两个相同内容的附件只存一份
	ev1, err := s.AppendEvent(context.Background(), EventInput{Type: "test.blob", Blob: []byte("hello")})
	if err != nil {
		t.Fatalf("追加 blob 事件出错: %v", err)
	}
	ev2, err := s.AppendEvent(context.Background(), EventInput{Type: "test.blob", Blob: []byte("hello")})
	if err != nil {
		t.Fatalf("追加 blob 事件出错: %v", err)
	}
	if ev1.Blob == "" || ev1.Blob != ev2.Blob {
		t.Fatalf("相同内容 blob 哈希应一致: %q vs %q", ev1.Blob, ev2.Blob)
	}
}

// TestUnknownSchemaRejected 校验发现未知表结构时直接报错，不做猜测性迁移。
func TestUnknownSchemaRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库出错: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_messages(id TEXT);`); err != nil {
		t.Fatalf("建旧表出错: %v", err)
	}
	db.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("发现旧版/未知表结构应报错退出")
	}
}

// TestMoveRef 校验 refs 更新同时追加 ref.moved 事件。
func TestMoveRef(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "eventlog.db"))
	if err != nil {
		t.Fatalf("Open 出错: %v", err)
	}
	defer s.Close()

	ev, err := s.MoveRef(context.Background(), "head", "evt_target")
	if err != nil {
		t.Fatalf("MoveRef 出错: %v", err)
	}
	if ev.Type != "ref.moved" || ev.SubjectID != "head" {
		t.Fatalf("ref.moved 事件不正确: %+v", ev)
	}
	var attrs map[string]string
	if err := json.Unmarshal(ev.Attrs, &attrs); err != nil {
		t.Fatalf("解析 attrs 出错: %v", err)
	}
	if attrs["name"] != "head" || attrs["target"] != "evt_target" {
		t.Fatalf("ref.moved attrs 不正确: %v", attrs)
	}

	// 更新同一 ref，事件仍追加且 seq 单调
	ev2, err := s.MoveRef(context.Background(), "head", "evt_new")
	if err != nil {
		t.Fatalf("MoveRef 出错: %v", err)
	}
	if ev2.Seq <= ev.Seq {
		t.Fatalf("ref.moved 事件 seq 非递增: %d → %d", ev.Seq, ev2.Seq)
	}
}
