// 包 eventlog 实现 Wisp 的 SQLite 事件日志。
//
// 事件只追加；单 writer goroutine 独占 seq 分配与写入，
// 其他方通过 AppendEvent / AppendEvents 提交。
// 只支持当前 schema：全新建库，或打开版本完全一致的库；
// 其他版本直接报错，不做迁移或向前兼容。
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

// SchemaVersion 是当前库结构版本。只接受全新库或完全一致的版本。
const SchemaVersion = 3

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
	TurnID    string          // 回合 ID
	Actor     string          // 触发者
	ParentID  string          // 父事件 ID
	CauseID   string          // 诱因事件 ID
	SubjectID string          // 关联实体 ID
	Blob      string          // blobs 表哈希引用，空表示无附件
	Attrs     json.RawMessage // 事件属性 JSON
	TS        int64           // 写入时间（Unix 毫秒）
}

// EventInput 是待追加的事件输入。
type EventInput struct {
	Type      string // 必填，事件类型
	SessionID string
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
	opAppend        opKind = iota // 批量追加事件（同一 turn 的批量事件在同一事务原子写入）
	opMoveRef                     // 更新 refs 并追加 ref.moved 事件
	opCreateContext               // 创建上下文：写 contexts 表并追加 ctx.created 事件
	opAppendCtxOp                 // 追加上下文编辑操作：写 ctx_ops 表并追加 ctx.op 事件
	opAddLink                     // 写入一条 links 关系（INSERT OR IGNORE）
	opPutBlob                     // 按内容哈希去重写入 blobs 表（供 RequestManifest 等引用）
)

// 写入请求，经缓冲 channel 提交给单 writer。
type writeReq struct {
	kind    opKind
	inputs  []EventInput
	refName string
	target  string
	ctxIn   ContextInput // opCreateContext 专用
	opIn    CtxOpInput   // opAppendCtxOp 专用
	linkSrc string       // opAddLink 专用
	linkDst string       // opAddLink 专用
	linkRel string       // opAddLink 专用
	blob    []byte       // opPutBlob 专用
	resp    chan writeResp
}

type writeResp struct {
	events []Event
	hash   string     // opPutBlob 返回的 blob 哈希
	ctxRow ContextRow // opCreateContext 返回的行
	opRow  CtxOpRow   // opAppendCtxOp 返回的行
	err    error
}

// Store 是事件日志存储：读取接口 + Writer 的单 writer 实现。
type Store struct {
	db       *sql.DB
	ch       chan writeReq
	hub      *Hub
	closed   atomic.Bool
	submitMu sync.RWMutex // 保证 Close 与 submit 不竞态
	wg       sync.WaitGroup
	lastSeq  int64 // 仅在 writer goroutine 中访问
}

// Open 打开（必要时全新创建）事件日志数据库。
//
// path 为 ":memory:" 时限制连接数为 1：内存库按连接隔离，多连接会各自
// 看到空库（仅测试使用内存库；文件库为 WAL + busy_timeout，不受影响）。
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: 打开数据库失败: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{
		db:  db,
		ch:  make(chan writeReq, writeBuffer),
		hub: NewHub(),
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&s.lastSeq); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventlog: 读取最大 seq 失败: %w", err)
	}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

// Hub 返回事件通知中心。订阅方只在收到通知后回读日志。
func (s *Store) Hub() *Hub { return s.hub }

// migrate 校验并初始化库结构：只接受全新库或与 SchemaVersion 完全一致的库。
func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("eventlog: 读取 schema 版本失败: %w", err)
	}
	switch v {
	case 0:
		// 全新库才允许建表；存在任何未知表结构直接报错。
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
			return fmt.Errorf("eventlog: 检查已有表失败: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("eventlog: 数据库中已存在未知表结构，请更换数据库文件")
		}
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("eventlog: 创建 schema_v%d 失败: %w", SchemaVersion, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
			return fmt.Errorf("eventlog: 写入 schema 版本失败: %w", err)
		}
		return nil
	case SchemaVersion:
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`).Scan(&n); err != nil || n != 1 {
			return fmt.Errorf("eventlog: schema_v%d 缺少 events 表，库结构不一致", SchemaVersion)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('contexts', 'ctx_ops')`).Scan(&n); err != nil || n != 2 {
			return fmt.Errorf("eventlog: schema_v%d 缺少 contexts/ctx_ops 表，库结构不一致", SchemaVersion)
		}
		return nil
	default:
		return fmt.Errorf("eventlog: 不支持的 schema 版本 v%d（当前 v%d），请更换数据库文件", v, SchemaVersion)
	}
}

// schema 建表语句：表名与列名一律小写下划线。
const schema = `
CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    seq        INTEGER NOT NULL UNIQUE,
    type       TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    turn_id    TEXT NOT NULL DEFAULT '',
    actor      TEXT NOT NULL DEFAULT '',
    parent_id  TEXT NOT NULL DEFAULT '',
    cause_id   TEXT NOT NULL DEFAULT '',
    subject_id TEXT NOT NULL DEFAULT '',
    blob       TEXT NOT NULL DEFAULT '',
    attrs      TEXT NOT NULL DEFAULT '{}',
    ts         INTEGER NOT NULL
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

CREATE TABLE contexts (
    id          TEXT PRIMARY KEY,
    base_ctx    TEXT NOT NULL DEFAULT '',
    fork_at     TEXT NOT NULL DEFAULT '',
    created_seq INTEGER NOT NULL
);
CREATE TABLE ctx_ops (
    id     TEXT PRIMARY KEY,
    ctx    TEXT NOT NULL,
    seq    INTEGER NOT NULL,
    op     TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    anchor TEXT NOT NULL DEFAULT '',
    okey   TEXT NOT NULL DEFAULT '',
    new_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_ctx_ops_ctx ON ctx_ops(ctx, seq);
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
// 读锁保证请求入队期间 Close 不会关闭 channel。
func (s *Store) submit(ctx context.Context, req writeReq) error {
	s.submitMu.RLock()
	defer s.submitMu.RUnlock()
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

// GetRef 读取命名引用的当前目标；不存在返回 sql.ErrNoRows。
func (s *Store) GetRef(ctx context.Context, name string) (string, error) {
	var target string
	err := s.db.QueryRowContext(ctx, `SELECT target FROM refs WHERE name = ?`, name).Scan(&target)
	if err != nil {
		return "", err
	}
	return target, nil
}

// Close 关闭事件日志：停止接收新请求，排空 writer 队列，关闭通知中心，
// 最后关闭数据库。先排空队列再关库，避免在途事件丢失。
func (s *Store) Close() error {
	s.submitMu.Lock()
	if !s.closed.CompareAndSwap(false, true) {
		s.submitMu.Unlock()
		return nil
	}
	close(s.ch)
	s.submitMu.Unlock()

	s.wg.Wait()
	s.hub.Close()
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
		if err == nil {
			// 事务已提交，逐会话发出“有新事件”通知；通知不携带数据。
			for _, ev := range events {
				s.hub.Publish(ev.SessionID)
			}
		}
		req.resp <- writeResp{events: events, err: err}
	case opMoveRef:
		ev, err := s.applyMoveRef(req.refName, req.target)
		req.resp <- writeResp{events: []Event{ev}, err: err}
	case opCreateContext:
		row, err := s.applyCreateContext(req.ctxIn)
		req.resp <- writeResp{ctxRow: row, err: err}
	case opAppendCtxOp:
		row, err := s.applyAppendCtxOp(req.opIn)
		req.resp <- writeResp{opRow: row, err: err}
	case opAddLink:
		req.resp <- writeResp{err: s.applyAddLink(req.linkSrc, req.linkDst, req.linkRel)}
	case opPutBlob:
		req.resp <- writeResp{hash: blobHash(req.blob), err: s.applyPutBlob(req.blob)}
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
			TurnID:    in.TurnID,
			Actor:     in.Actor,
			ParentID:  in.ParentID,
			CauseID:   in.CauseID,
			SubjectID: in.SubjectID,
			TS:        now,
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
			(id, seq, type, session_id, turn_id, actor, parent_id, cause_id, subject_id, blob, attrs, ts)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID.String(), ev.Seq, ev.Type, ev.SessionID, ev.TurnID,
			ev.Actor, ev.ParentID, ev.CauseID, ev.SubjectID, ev.Blob, ev.Attrs, ev.TS); err != nil {
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
	}
	attrs, err := json.Marshal(map[string]string{"name": name, "target": target})
	if err != nil {
		return Event{}, err
	}
	ev.Attrs = attrs
	if _, err := tx.Exec(`INSERT INTO events
		(id, seq, type, actor, subject_id, attrs, ts)
		VALUES(?, ?, ?, '', ?, ?, ?)`,
		ev.ID.String(), ev.Seq, ev.Type, ev.SubjectID, ev.Attrs, ev.TS); err != nil {
		return Event{}, fmt.Errorf("eventlog: 写入 ref.moved 事件失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// applyCreateContext 在单个事务中写 contexts 表并追加 ctx.created 事件。
func (s *Store) applyCreateContext(in ContextInput) (ContextRow, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return ContextRow{}, err
	}
	defer tx.Rollback()

	row := ContextRow{
		ID:         ids.NewContextID(),
		BaseCtx:    in.BaseCtx,
		ForkAt:     in.ForkAt,
		CreatedSeq: s.lastSeq + 1,
	}
	raw, err := marshalAttrs(nil)
	if err != nil {
		return ContextRow{}, err
	}
	if _, err := tx.Exec(`INSERT INTO events
		(id, seq, type, actor, subject_id, attrs, ts)
		VALUES(?, ?, ?, '', '', ?, ?)`,
		ids.NewEventID().String(), s.nextSeq(), TypeCtxCreated, raw, now); err != nil {
		return ContextRow{}, fmt.Errorf("eventlog: 写入 ctx.created 事件失败: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO contexts(id, base_ctx, fork_at, created_seq) VALUES(?, ?, ?, ?)`,
		row.ID.String(), row.BaseCtx.String(), row.ForkAt.String(), row.CreatedSeq); err != nil {
		return ContextRow{}, fmt.Errorf("eventlog: 写入 contexts 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ContextRow{}, err
	}
	return row, nil
}

// applyAppendCtxOp 在单个事务中写 ctx_ops 表并追加 ctx.op 事件。
func (s *Store) applyAppendCtxOp(in CtxOpInput) (CtxOpRow, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return CtxOpRow{}, err
	}
	defer tx.Rollback()

	row := CtxOpRow{
		ID:     ids.NewContextOpID(),
		Ctx:    in.Ctx,
		Seq:    s.lastSeq + 1,
		Op:     in.Op,
		Target: in.Target,
		Anchor: in.Anchor,
		OKey:   in.OKey,
		NewID:  in.NewID,
	}
	raw, err := marshalAttrs(nil)
	if err != nil {
		return CtxOpRow{}, err
	}
	if _, err := tx.Exec(`INSERT INTO events
		(id, seq, type, actor, subject_id, attrs, ts)
		VALUES(?, ?, ?, '', ?, ?, ?)`,
		ids.NewEventID().String(), s.nextSeq(), TypeCtxOp, row.ID.String(), raw, now); err != nil {
		return CtxOpRow{}, fmt.Errorf("eventlog: 写入 ctx.op 事件失败: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO ctx_ops(id, ctx, seq, op, target, anchor, okey, new_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID.String(), row.Ctx.String(), row.Seq, row.Op, row.Target, row.Anchor, row.OKey, row.NewID.String()); err != nil {
		return CtxOpRow{}, fmt.Errorf("eventlog: 写入 ctx_ops 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CtxOpRow{}, err
	}
	return row, nil
}

// applyAddLink 写入一条 links 关系，重复写入幂等。
func (s *Store) applyAddLink(src, dst, rel string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO links(src, dst, rel) VALUES(?, ?, ?)`, src, dst, rel)
	if err != nil {
		return fmt.Errorf("eventlog: 写入 link 失败: %w", err)
	}
	return nil
}

// applyPutBlob 按内容哈希去重写入 blobs 表。
func (s *Store) applyPutBlob(data []byte) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO blobs(hash, data, redacted) VALUES(?, ?, 0)`, blobHash(data), data)
	if err != nil {
		return fmt.Errorf("eventlog: 写入 blob 失败: %w", err)
	}
	return nil
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
	err := row.Scan(&id, &ev.Seq, &ev.Type, &ev.SessionID, &ev.TurnID,
		&ev.Actor, &ev.ParentID, &ev.CauseID, &ev.SubjectID, &ev.Blob, &attrs, &ev.TS)
	if err != nil {
		return Event{}, err
	}
	ev.ID = ids.EventID(id)
	ev.Attrs = json.RawMessage(attrs)
	return ev, nil
}

// columns 带表别名 e，读取事件的查询必须给 events 表起别名 e。
const columns = `e.id, e.seq, e.type, e.session_id, e.turn_id, e.actor, e.parent_id, e.cause_id, e.subject_id, e.blob, e.attrs, e.ts`

// Get 按事件 ID 读取事件。
func (s *Store) Get(ctx context.Context, id ids.EventID) (Event, error) {
	return scanEvent(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM events e WHERE e.id = ?`, id.String()))
}

// LoadChain 从 forkAt 沿 parent_id 回溯，按 forkAt→根的顺序返回整条链。
// 使用递归 CTE 一次查询完成；UNION 去重同时防御异常数据成环。
func (s *Store) LoadChain(ctx context.Context, forkAt ids.EventID) ([]Event, error) {
	if forkAt == "" {
		return nil, nil
	}
	q := `WITH RECURSIVE chain(id) AS (
		SELECT e.id FROM events e WHERE e.id = ?
		UNION
		SELECT e.parent_id FROM events e JOIN chain c ON e.id = c.id WHERE e.parent_id <> ''
	)
	SELECT ` + columns + ` FROM events e JOIN chain c ON e.id = c.id ORDER BY e.seq DESC`
	return s.queryEvents(ctx, q, forkAt.String())
}

// ListBySession 按会话读取 seq 大于 afterSeq 的事件（按 seq 升序，最多 limit 条）。
// limit <= 0 表示不限制。
func (s *Store) ListBySession(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, error) {
	q := `SELECT ` + columns + ` FROM events e WHERE e.session_id = ? AND e.seq > ? ORDER BY e.seq ASC`
	args := []any{sessionID, afterSeq}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryEvents(ctx, q, args...)
}

// ListByTurn 按回合读取 seq 大于 afterSeq 的事件（按 seq 升序，最多 limit 条）。
// limit <= 0 表示不限制。
func (s *Store) ListByTurn(ctx context.Context, turnID string, afterSeq int64, limit int) ([]Event, error) {
	q := `SELECT ` + columns + ` FROM events e WHERE e.turn_id = ? AND e.seq > ? ORDER BY e.seq ASC`
	args := []any{turnID, afterSeq}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryEvents(ctx, q, args...)
}

// ListSessionIDs 返回日志中出现过的全部非空会话 ID（升序）。
// API 层据此投影会话列表，不直接执行 SQL。
func (s *Store) ListSessionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT session_id FROM events WHERE session_id <> '' ORDER BY session_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// queryEvents 执行事件查询并逐行扫描。
func (s *Store) queryEvents(ctx context.Context, q string, args ...any) ([]Event, error) {
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
		out = append(out, ev)
	}
	return out, rows.Err()
}
