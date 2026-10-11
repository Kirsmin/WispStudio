// 包 eventlog 实现 Wisp 的 SQLite 事件日志。
//
// 事件只追加；单 writer goroutine 独占 seq 分配与写入，
// 其他方通过 AppendEvent / AppendEvents 提交。
// schema_v1 只全新建库，发现任何旧版/未知表结构直接报错退出，不做猜测性迁移。
package eventlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"wisp/internal/ids"
)

// SchemaVersion 是当前库结构版本；仅支持全新建库，不支持迁移。
const SchemaVersion = 1

// CurrentVersion 是当前事件版本，新写入事件一律使用该版本。
const CurrentVersion = 1

// 写入通道缓冲大小。
const writeBuffer = 1024

// ErrClosed 表示事件日志已关闭。
var ErrClosed = errors.New("eventlog: 已关闭")

// Event 是已落库的事件。
type Event struct {
	ID        ids.EventID     // 事件 ID
	Seq       int64           // 单调递增序号，由单 writer 分配
	Type      string          // 事件类型
	SessionID string          // 会话 ID
	AgentID   string          // 智能体 ID
	TurnID    string          // 回合 ID
	Actor     string          // 触发者
	ParentID  string          // 父事件 ID
	CauseID   string          // 诱因事件 ID
	SubjectID string          // 关联实体 ID
	Blob      string          // blobs 表哈希引用，空表示无附件
	Attrs     json.RawMessage // 事件属性 JSON
	TS        int64           // 写入时间（Unix 毫秒）
	V         int             // 事件版本
}

// EventInput 是待追加的事件输入。
type EventInput struct {
	Type      string // 必填，事件类型
	SessionID string
	AgentID   string
	TurnID    string
	Actor     string
	ParentID  string
	CauseID   string
	SubjectID string
	Blob      []byte // 可选附件，按内容哈希去重存入 blobs 表
	Attrs     any    // 任意可 JSON 序列化的属性，nil 存 {}
}

// 写入请求类型。
type opKind int

const (
	opAppend  opKind = iota // 批量追加事件（同一 turn 的批量事件在同一事务原子写入）
	opMoveRef               // 更新 refs 并追加 ref.moved 事件
)

// 写入请求，经缓冲 channel 提交给单 writer。
type writeReq struct {
	kind    opKind
	inputs  []EventInput
	refName string
	target  string
	resp    chan writeResp
}

type writeResp struct {
	events []Event
	err    error
}

// Store 是事件日志存储，单 writer 串行写入。
type Store struct {
	db      *sql.DB
	reg     Registry // 事件升级器注册表，可为 nil
	ch      chan writeReq
	closed  atomic.Bool
	wg      sync.WaitGroup
	lastSeq int64 // 仅在 writer goroutine 中访问
}

// Open 打开（必要时全新创建）事件日志数据库。
// registry 用于读取时按 v 逐版本升级旧事件，可为 nil。
func Open(path string, registry Registry) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("eventlog: 打开数据库失败: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, reg: registry, ch: make(chan writeReq, writeBuffer)}
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&s.lastSeq); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventlog: 读取最大 seq 失败: %w", err)
	}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

// migrate 校验并初始化库结构：只允许 schema_v1 全新建库。
func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("eventlog: 读取 schema 版本失败: %w", err)
	}
	switch {
	case v == 0:
		// 全新库才允许建表；存在任何未知表结构直接报错。
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
			return fmt.Errorf("eventlog: 检查已有表失败: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("eventlog: 数据库中已存在未知表结构（schema_v0），不做猜测性迁移，请更换数据库文件")
		}
		if _, err := db.Exec(schemaV1); err != nil {
			return fmt.Errorf("eventlog: 创建 schema_v1 失败: %w", err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
			return fmt.Errorf("eventlog: 写入 schema 版本失败: %w", err)
		}
		return nil
	case v == SchemaVersion:
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`).Scan(&n); err != nil || n != 1 {
			return fmt.Errorf("eventlog: schema_v1 缺少 events 表，库结构不一致")
		}
		return nil
	default:
		return fmt.Errorf("eventlog: 未知的 schema 版本 v%d，不支持迁移", v)
	}
}

// schemaV1 建表语句：五张表，表名与列名一律小写下划线。
const schemaV1 = `
CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    seq        INTEGER NOT NULL UNIQUE,
    type       TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    agent_id   TEXT NOT NULL DEFAULT '',
    turn_id    TEXT NOT NULL DEFAULT '',
    actor      TEXT NOT NULL DEFAULT '',
    parent_id  TEXT NOT NULL DEFAULT '',
    cause_id   TEXT NOT NULL DEFAULT '',
    subject_id TEXT NOT NULL DEFAULT '',
    blob       TEXT NOT NULL DEFAULT '',
    attrs      TEXT NOT NULL DEFAULT '{}',
    ts         INTEGER NOT NULL,
    v          INTEGER NOT NULL
);
CREATE INDEX idx_events_session ON events(session_id, seq);
CREATE INDEX idx_events_turn ON events(turn_id, seq);

CREATE TABLE blobs (
    hash     TEXT PRIMARY KEY,
    data     BLOB NOT NULL,
    redacted INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE links (
    src TEXT NOT NULL,
    dst TEXT NOT NULL,
    rel TEXT NOT NULL,
    PRIMARY KEY (src, dst, rel)
);

CREATE TABLE refs (
    name   TEXT PRIMARY KEY,
    target TEXT NOT NULL
);
`

// AppendEvent 提交单个事件，阻塞直至写入完成或 ctx 取消。
func (s *Store) AppendEvent(ctx context.Context, in EventInput) (Event, error) {
	events, err := s.AppendEvents(ctx, []EventInput{in})
	if err != nil {
		return Event{}, err
	}
	return events[0], nil
}

// AppendEvents 批量提交事件，整批在同一事务中原子写入
// （用于同一 turn 的批量事件）。阻塞直至写入完成或 ctx 取消。
func (s *Store) AppendEvents(ctx context.Context, ins []EventInput) ([]Event, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	req := writeReq{kind: opAppend, inputs: ins, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return nil, err
	}
	resp := <-req.resp
	return resp.events, resp.err
}

// MoveRef 更新命名引用，并在同一事务中追加一条 ref.moved 事件。
func (s *Store) MoveRef(ctx context.Context, name, target string) (Event, error) {
	req := writeReq{kind: opMoveRef, refName: name, target: target, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return Event{}, err
	}
	resp := <-req.resp
	if resp.err != nil {
		return Event{}, resp.err
	}
	return resp.events[0], nil
}

// submit 把写入请求送入缓冲 channel。
func (s *Store) submit(ctx context.Context, req writeReq) error {
	if s.closed.Load() {
		return ErrClosed
	}
	select {
	case s.ch <- req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 关闭事件日志：停止接收新请求，排空队列后关闭数据库。
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.ch)
	s.wg.Wait()
	return s.db.Close()
}

// loop 是单 writer 主循环：独占 seq 分配与写入。
func (s *Store) loop() {
	defer s.wg.Done()
	for req := range s.ch {
		s.apply(req)
	}
}

// apply 执行一个写入请求。
func (s *Store) apply(req writeReq) {
	switch req.kind {
	case opAppend:
		events, err := s.applyAppend(req.inputs)
		req.resp <- writeResp{events: events, err: err}
	case opMoveRef:
		ev, err := s.applyMoveRef(req.refName, req.target)
		req.resp <- writeResp{events: []Event{ev}, err: err}
	}
}

// applyAppend 在单个事务中原子写入一批事件。
func (s *Store) applyAppend(ins []EventInput) ([]Event, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	events := make([]Event, 0, len(ins))
	for _, in := range ins {
		ev := Event{
			ID:        ids.NewEventID(),
			Seq:       s.nextSeq(),
			Type:      in.Type,
			SessionID: in.SessionID,
			AgentID:   in.AgentID,
			TurnID:    in.TurnID,
			Actor:     in.Actor,
			ParentID:  in.ParentID,
			CauseID:   in.CauseID,
			SubjectID: in.SubjectID,
			TS:        now,
			V:         CurrentVersion,
		}
		// 附件按内容哈希去重存入 blobs，events.blob 只存哈希引用
		if len(in.Blob) > 0 {
			sum := sha256.Sum256(in.Blob)
			ev.Blob = hex.EncodeToString(sum[:])
			if _, err := tx.Exec(`INSERT OR IGNORE INTO blobs(hash, data, redacted) VALUES(?, ?, 0)`,
				ev.Blob, in.Blob); err != nil {
				return nil, fmt.Errorf("eventlog: 写入 blob 失败: %w", err)
			}
		}
		attrs := in.Attrs
		if attrs == nil {
			attrs = map[string]any{}
		}
		raw, err := json.Marshal(attrs)
		if err != nil {
			return nil, fmt.Errorf("eventlog: 序列化 attrs 失败: %w", err)
		}
		ev.Attrs = raw
		if _, err := tx.Exec(`INSERT INTO events
			(id, seq, type, session_id, agent_id, turn_id, actor, parent_id, cause_id, subject_id, blob, attrs, ts, v)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID.String(), ev.Seq, ev.Type, ev.SessionID, ev.AgentID, ev.TurnID,
			ev.Actor, ev.ParentID, ev.CauseID, ev.SubjectID, ev.Blob, ev.Attrs, ev.TS, ev.V); err != nil {
			return nil, fmt.Errorf("eventlog: 写入事件失败: %w", err)
		}
		events = append(events, ev)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

// applyMoveRef 在单个事务中更新 refs 并追加 ref.moved 事件。
func (s *Store) applyMoveRef(name, target string) (Event, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO refs(name, target) VALUES(?, ?)
		ON CONFLICT(name) DO UPDATE SET target = excluded.target`, name, target); err != nil {
		return Event{}, fmt.Errorf("eventlog: 更新 ref 失败: %w", err)
	}
	ev := Event{
		ID:        ids.NewEventID(),
		Seq:       s.nextSeq(),
		Type:      "ref.moved",
		SubjectID: name,
		TS:        now,
		V:         CurrentVersion,
	}
	attrs, err := json.Marshal(map[string]string{"name": name, "target": target})
	if err != nil {
		return Event{}, err
	}
	ev.Attrs = attrs
	if _, err := tx.Exec(`INSERT INTO events
		(id, seq, type, session_id, agent_id, turn_id, actor, parent_id, cause_id, subject_id, blob, attrs, ts, v)
		VALUES(?, ?, ?, '', '', '', '', '', '', '', '', ?, ?, ?)`,
		ev.ID.String(), ev.Seq, ev.Type, ev.Attrs, ev.TS, ev.V); err != nil {
		return Event{}, fmt.Errorf("eventlog: 写入 ref.moved 事件失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// nextSeq 分配下一个 seq，仅在 writer goroutine 中调用。
func (s *Store) nextSeq() int64 {
	s.lastSeq++
	return s.lastSeq
}

// scanEvent 把一行扫描为事件。
func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var ev Event
	var id, attrs string
	err := row.Scan(&id, &ev.Seq, &ev.Type, &ev.SessionID, &ev.AgentID, &ev.TurnID,
		&ev.Actor, &ev.ParentID, &ev.CauseID, &ev.SubjectID, &ev.Blob, &attrs, &ev.TS, &ev.V)
	if err != nil {
		return Event{}, err
	}
	ev.ID = ids.EventID(id)
	ev.Attrs = json.RawMessage(attrs)
	return ev, nil
}

const columns = `id, seq, type, session_id, agent_id, turn_id, actor, parent_id, cause_id, subject_id, blob, attrs, ts, v`

// Get 按事件 ID 读取事件，读取时按 v 逐版本升级。
func (s *Store) Get(ctx context.Context, id ids.EventID) (Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM events WHERE id = ?`, id.String())
	ev, err := scanEvent(row)
	if err != nil {
		return Event{}, err
	}
	if err := s.upcast(&ev); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// ListBySession 按会话读取 seq 大于 afterSeq 的事件（按 seq 升序，最多 limit 条）。
// limit <= 0 表示不限制。读取时按 v 逐版本升级。
func (s *Store) ListBySession(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, error) {
	q := `SELECT ` + columns + ` FROM events WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{sessionID, afterSeq}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		if err := s.upcast(&ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
