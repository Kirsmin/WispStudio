package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// migration 表示一次递增的 Schema 版本升级。
// 所有迁移在 migrate 的同一个事务中按版本升序执行；
// 中途任何一步失败都会整体回滚，不会留下“版本已升但结构只做了一半”的状态。
type migration struct {
	version int
	name    string
	up      func(ctx context.Context, tx *sql.Tx) error
}

// migrations 按版本升序登记全部 Schema 迁移。
// 新增结构变更时的规范：
//  1. 在末尾追加新的 migration，version 严格递增，name 用中文简述用途；
//  2. 不允许修改或删除已发布的迁移（旧库升级依赖其顺序与幂等性）；
//  3. DDL 必须对“从上一版本升级过来的库”安全生效，必要时使用 IF NOT EXISTS；
//  4. 只放当前阶段需要的结构，不为未来需求提前建表。
var migrations = []migration{
	{
		version: 1,
		name:    "初始结构：会话/轮次/模型调用/记录",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    renamed INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS turns (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE(session_id, turn_index)
);
CREATE TABLE IF NOT EXISTS model_calls (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    call_index INTEGER NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    thinking TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    finish_reason TEXT NOT NULL DEFAULT '',
    system_prompt_snapshot TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE(turn_id, call_index)
);
CREATE TABLE IF NOT EXISTS records (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT REFERENCES turns(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    model_call_id TEXT REFERENCES model_calls(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    data_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    UNIQUE(session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_records_session_seq ON records(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_model_calls_session ON model_calls(session_id, created_at);
`
			_, err := tx.ExecContext(ctx, schema)
			if err != nil {
				return fmt.Errorf("初始化 SQLite 失败: %w", err)
			}
			return nil
		},
	},
}

// migrate 执行所有尚未应用的迁移。整个流程在一个事务内完成：
// 读取当前版本 -> 顺序执行缺失迁移 -> 逐条记录版本与审计信息 -> 提交。
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// meta 保存兼容用的当前 schema_version。
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("初始化 meta 表失败: %w", err)
	}
	// schema_migrations 逐条记录已应用的迁移，便于 debug 与审计。
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("初始化 schema_migrations 表失败: %w", err)
	}

	current, err := s.currentSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	// 补记历史版本审计：从旧机制升级过来的库可能已处于较新版本，
	// 但 schema_migrations 中没有对应记录。
	for _, m := range migrations {
		if m.version > current {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, stamp(time.Now().UTC())); err != nil {
			return fmt.Errorf("补记迁移 v%d 失败: %w", m.version, err)
		}
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := m.up(ctx, tx); err != nil {
			return fmt.Errorf("迁移 v%d (%s) 失败: %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES ('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			strconv.Itoa(m.version)); err != nil {
			return fmt.Errorf("记录 schema_version=%d 失败: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, stamp(time.Now().UTC())); err != nil {
			return fmt.Errorf("记录迁移 v%d 失败: %w", m.version, err)
		}
		current = m.version
	}
	return tx.Commit()
}

// currentSchemaVersion 读取 meta 中的 schema_version。
// 兼容极旧数据库：结构已存在但没有版本记录时，按 v1 处理。
func (s *Store) currentSchemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&raw)
	if err == sql.ErrNoRows {
		var tables int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('sessions','turns','model_calls','records')`).Scan(&tables); err != nil {
			return 0, err
		}
		if tables == 4 {
			return 1, nil
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("非法的 schema_version: %q", raw)
	}
	return version, nil
}

// AppliedMigration 是一次已应用迁移的审计记录。
type AppliedMigration struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at"`
}

// AppliedMigrations 返回全部已应用的迁移，便于 debug。
func (s *Store) AppliedMigrations() ([]AppliedMigration, error) {
	rows, err := s.db.Query(`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedMigration
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Version, &m.Name, &m.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SchemaVersion 返回当前数据库结构版本，便于 debug。
func (s *Store) SchemaVersion() (int, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&raw); err != nil {
		return 0, err
	}
	return strconv.Atoi(raw)
}
