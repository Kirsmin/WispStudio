package eventlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"wisp/internal/ids"
)

// ContextRow 是 contexts 表的一行，即一个上下文。
// 上下文构成 base_ctx 链：子上下文继承基线的历史与全部 ctx_ops。
type ContextRow struct {
	ID ids.ContextID // 上下文 ID（ctx_<ulid>）

	// BaseCtx 是基上下文 ID，空 = 根上下文。
	BaseCtx ids.ContextID

	// ForkAt 是 fork 起点事件 ID，空 = 从空历史开始。
	// 编译基线内容时从该事件沿 parent_id 链回溯到根。
	ForkAt ids.EventID

	// CreatedSeq 是对应 ctx.created 事件的 seq。
	CreatedSeq int64
}

// ContextInput 是创建上下文的输入。
type ContextInput struct {
	BaseCtx ids.ContextID // 基上下文，零值 = 根上下文
	ForkAt  ids.EventID   // fork 起点事件，零值 = 从空历史开始
}

// CreateContext 创建上下文：追加 ctx.created 事件并写入 contexts 表（同事务）。
func (s *Store) CreateContext(ctx context.Context, in ContextInput) (ContextRow, error) {
	req := writeReq{kind: opCreateContext, ctxIn: in, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return ContextRow{}, err
	}
	resp := <-req.resp
	if resp.err != nil {
		return ContextRow{}, resp.err
	}
	return resp.ctxRow, nil
}

// ctx_ops.op 列的合法取值。
const (
	// CtxOpAppend 追加内容：new_id 引用的内容事件排到末尾。
	CtxOpAppend = "append"

	// CtxOpInsert 插入内容：new_id 插入 anchor 组（anchor 为已有条目的
	// subject_id，空 = 队首组），组内按 okey 字典序排列，空 okey 排最后。
	CtxOpInsert = "insert"

	// CtxOpHide 隐藏 target 条目（对 tool bundle 整组生效），幂等。
	CtxOpHide = "hide"

	// CtxOpReplace 替换 target 条目为 new_id 引用的新内容事件，位置不变。
	CtxOpReplace = "replace"

	// CtxOpPin 置顶 target 条目（对 tool bundle 整组生效），幂等。
	CtxOpPin = "pin"
)

// CtxOpRow 是 ctx_ops 表的一行，即一次上下文编辑操作。
// 同一 ctx 内按 seq（全局事件 seq）升序应用。
type CtxOpRow struct {
	ID  ids.ContextOpID // 操作 ID（op_<ulid>）
	Ctx ids.ContextID   // 目标上下文
	Seq int64           // 全局事件 seq，同一 ctx 内单调递增

	// Op 是操作名，取值见 CtxOp* 常量。
	Op string

	// Target 是作用对象 subject_id（hide/replace/pin 必填）。
	Target string

	// Anchor 是 insert 的锚点 subject_id，空 = 队首组。
	Anchor string

	// OKey 是 anchor 组内分数索引（TEXT），空 = 排最后，字典序比较。
	OKey string

	// NewID 是 append/insert/replace 引用的内容事件 ID，其余操作为空。
	NewID ids.EventID
}

// CtxOpInput 是追加上下文操作的输入。
type CtxOpInput struct {
	Ctx    ids.ContextID
	Op     string
	Target string
	Anchor string
	OKey   string
	NewID  ids.EventID
}

// validCtxOp 校验操作输入的字段约束，失败返回错误。
func validCtxOp(in CtxOpInput) error {
	switch in.Op {
	case CtxOpAppend, CtxOpInsert, CtxOpReplace:
		if in.NewID == "" {
			return fmt.Errorf("eventlog: %s 操作缺少 new_id", in.Op)
		}
	case CtxOpHide, CtxOpPin:
		if in.Target == "" {
			return fmt.Errorf("eventlog: %s 操作缺少 target", in.Op)
		}
	default:
		return fmt.Errorf("eventlog: 未知的 ctx.op 操作 %q", in.Op)
	}
	if in.Op == CtxOpReplace && in.Target == "" {
		return fmt.Errorf("eventlog: replace 操作缺少 target")
	}
	return nil
}

// AppendCtxOp 追加一次上下文编辑操作：追加 ctx.op 事件并写入 ctx_ops 表（同事务）。
func (s *Store) AppendCtxOp(ctx context.Context, in CtxOpInput) (CtxOpRow, error) {
	if err := validCtxOp(in); err != nil {
		return CtxOpRow{}, err
	}
	req := writeReq{kind: opAppendCtxOp, opIn: in, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return CtxOpRow{}, err
	}
	resp := <-req.resp
	if resp.err != nil {
		return CtxOpRow{}, resp.err
	}
	return resp.opRow, nil
}

// GetContext 按 ID 读取上下文。
func (s *Store) GetContext(ctx context.Context, id ids.ContextID) (ContextRow, error) {
	var row ContextRow
	var base, fork string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, base_ctx, fork_at, created_seq FROM contexts WHERE id = ?`, id.String()).
		Scan(&row.ID, &base, &fork, &row.CreatedSeq)
	if err != nil {
		return ContextRow{}, fmt.Errorf("eventlog: 读取上下文 %s 失败: %w", id, err)
	}
	row.BaseCtx = ids.ContextID(base)
	row.ForkAt = ids.EventID(fork)
	return row, nil
}

// ListCtxOps 读取某上下文的全部操作，按 seq 升序。
func (s *Store) ListCtxOps(ctx context.Context, ctxID ids.ContextID) ([]CtxOpRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ctx, seq, op, target, anchor, okey, new_id FROM ctx_ops WHERE ctx = ? ORDER BY seq ASC`,
		ctxID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CtxOpRow
	for rows.Next() {
		var r CtxOpRow
		var c, nid string
		if err := rows.Scan(&r.ID, &c, &r.Seq, &r.Op, &r.Target, &r.Anchor, &r.OKey, &nid); err != nil {
			return nil, err
		}
		r.Ctx = ids.ContextID(c)
		r.NewID = ids.EventID(nid)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LinkRow 是 links 表的一行（src, dst, rel）。
type LinkRow struct {
	Src string
	Dst string
	Rel string
}

// AddLink 写入一条 links 关系（INSERT OR IGNORE，幂等）。
func (s *Store) AddLink(ctx context.Context, src, dst, rel string) error {
	req := writeReq{kind: opAddLink, linkSrc: src, linkDst: dst, linkRel: rel, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return err
	}
	return (<-req.resp).err
}

// ListLinks 按 src/dst/rel 查询 links，空字符串表示通配。
func (s *Store) ListLinks(ctx context.Context, src, dst, rel string) ([]LinkRow, error) {
	q := `SELECT src, dst, rel FROM links WHERE 1=1`
	var args []any
	if src != "" {
		q += ` AND src = ?`
		args = append(args, src)
	}
	if dst != "" {
		q += ` AND dst = ?`
		args = append(args, dst)
	}
	if rel != "" {
		q += ` AND rel = ?`
		args = append(args, rel)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var r LinkRow
		if err := rows.Scan(&r.Src, &r.Dst, &r.Rel); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetBlob 按哈希读取附件内容。
func (s *Store) GetBlob(ctx context.Context, hash string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE hash = ?`, hash).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("eventlog: 读取 blob %s 失败: %w", hash, err)
	}
	return data, nil
}

// GetBlobs 批量读取附件内容；按批查询，避免编译时逐条读取。
// 不存在的哈希不会出现在返回 map 中，由调用方决定是否报错。
func (s *Store) GetBlobs(ctx context.Context, hashes []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(hashes))
	const batch = 500
	for start := 0; start < len(hashes); start += batch {
		end := min(start+batch, len(hashes))
		args := make([]any, 0, end-start)
		marks := make([]string, 0, end-start)
		for _, hash := range hashes[start:end] {
			if hash == "" {
				continue
			}
			marks = append(marks, "?")
			args = append(args, hash)
		}
		if len(args) == 0 {
			continue
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT hash, data FROM blobs WHERE hash IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("eventlog: 批量读取 blob 失败: %w", err)
		}
		for rows.Next() {
			var hash string
			var data []byte
			if err := rows.Scan(&hash, &data); err != nil {
				rows.Close()
				return nil, err
			}
			out[hash] = data
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PutBlob 把内容按 sha256 去重写入 blobs 表，返回哈希引用。
// 经单 writer 通道执行，与事件写入互不竞态。
func (s *Store) PutBlob(ctx context.Context, data []byte) (string, error) {
	req := writeReq{kind: opPutBlob, blob: data, resp: make(chan writeResp, 1)}
	if err := s.submit(ctx, req); err != nil {
		return "", err
	}
	resp := <-req.resp
	if resp.err != nil {
		return "", resp.err
	}
	return resp.hash, nil
}

// blobHash 计算内容的 sha256 十六进制哈希（与 applyAppend 的去重逻辑一致）。
func blobHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// marshalAttrs 把 attrs 结构序列化为 JSON；nil 存 {}。
func marshalAttrs(attrs any) (json.RawMessage, error) {
	if attrs == nil {
		attrs = map[string]any{}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return nil, fmt.Errorf("eventlog: 序列化 attrs 失败: %w", err)
	}
	return raw, nil
}
