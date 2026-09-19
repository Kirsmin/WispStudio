package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// migration 表示一次递增的 Schema 版本升级。
// 所有迁移在 migrate 的同一个事务中按版本升序执行；任一步失败都会整体回滚。
type migration struct {
	version int
	name    string
	up      func(ctx context.Context, tx *sql.Tx) error
}

// migrations 只追加、不改写已发布版本。每个版本只建立当时 Runtime 已经需要的数据结构。
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
			if _, err := tx.ExecContext(ctx, schema); err != nil {
				return fmt.Errorf("初始化 SQLite 失败: %w", err)
			}
			return nil
		},
	},
	{
		version: 2,
		name:    "Turn 与 Timeline 任务语义",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
ALTER TABLE turns ADD COLUMN objective TEXT NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN active_agent TEXT NOT NULL DEFAULT 'plan';
ALTER TABLE turns ADD COLUMN context_epoch INTEGER NOT NULL DEFAULT 1;
ALTER TABLE turns ADD COLUMN steering_cursor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN root_agent_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN active_agent_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN active_checkpoint_id TEXT NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN pause_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN stop_requested INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_turns_session_status ON turns(session_id, status, turn_index);
CREATE INDEX IF NOT EXISTS idx_records_turn_seq ON records(turn_id, seq);
`
			_, err := tx.ExecContext(ctx, schema)
			return err
		},
	},
	{
		version: 3,
		name:    "ModelCall Context 可观测信息",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
ALTER TABLE model_calls ADD COLUMN agent_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN context_epoch INTEGER NOT NULL DEFAULT 1;
ALTER TABLE model_calls ADD COLUMN context_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN prefix_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN context_debug_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_model_calls_turn_index ON model_calls(turn_id, call_index);
CREATE INDEX IF NOT EXISTS idx_model_calls_agent_run ON model_calls(agent_run_id, call_index);
`
			_, err := tx.ExecContext(ctx, schema)
			return err
		},
	},
	{
		version: 4,
		name:    "AgentRun 与版本化 Artifact",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
CREATE TABLE agent_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    parent_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,
    profile_id TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX idx_agent_runs_turn ON agent_runs(turn_id, created_at);

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    active_version INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(turn_id, type, name)
);
CREATE INDEX idx_artifacts_turn ON artifacts(turn_id, type);

CREATE TABLE artifact_versions (
    id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    data_json TEXT NOT NULL DEFAULT '{}',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(artifact_id, version)
);
CREATE INDEX idx_artifact_versions_artifact ON artifact_versions(artifact_id, version);
`
			_, err := tx.ExecContext(ctx, schema)
			return err
		},
	},
	{
		version: 5,
		name:    "Approval/Checkpoint/Context Epoch",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    agent_run_id TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    args_json TEXT NOT NULL DEFAULT '{}',
    risk TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    decision_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    decided_at TEXT,
    UNIQUE(turn_id, tool_call_id)
);
CREATE INDEX idx_approvals_turn_status ON approvals(turn_id, status, created_at);

CREATE TABLE checkpoints (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    context_epoch INTEGER NOT NULL,
    kind TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    facts_json TEXT NOT NULL DEFAULT '{}',
    timeline_seq INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_checkpoints_turn_epoch ON checkpoints(turn_id, context_epoch);

CREATE TABLE context_epochs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    epoch_index INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    checkpoint_id TEXT NOT NULL DEFAULT '',
    prefix_hash TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(turn_id, epoch_index)
);
`
			_, err := tx.ExecContext(ctx, schema)
			return err
		},
	},
	{
		version: 6,
		name:    "Turn Context Fold/Restore",
		up: func(ctx context.Context, tx *sql.Tx) error {
			const schema = `
CREATE TABLE context_folds (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    stack_index INTEGER NOT NULL,
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    restored_at TEXT,
    UNIQUE(session_id, stack_index)
);
CREATE INDEX idx_context_folds_session_active ON context_folds(session_id, active, stack_index);
`
			_, err := tx.ExecContext(ctx, schema)
			return err
		},
	},
}

// migrate 执行所有尚未应用的迁移。整个流程在一个事务内完成。
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("初始化 meta 表失败: %w", err)
	}
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
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	if current > latest {
		return fmt.Errorf("数据库 schema_version=%d 高于当前程序支持的 v%d", current, latest)
	}

	for _, m := range migrations {
		if m.version > current {
			break
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, stamp(time.Now().UTC())); err != nil {
			return fmt.Errorf("补记迁移 v%d 失败: %w", m.version, err)
		}
	}

	// 兼容旧 v1：如果版本是通过表结构识别出来的，也必须立即写回 meta。
	if current > 0 {
		if err := setSchemaVersion(ctx, tx, current); err != nil {
			return err
		}
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := m.up(ctx, tx); err != nil {
			return fmt.Errorf("迁移 v%d (%s) 失败: %w", m.version, m.name, err)
		}
		if err := setSchemaVersion(ctx, tx, m.version); err != nil {
			return fmt.Errorf("记录 schema_version=%d 失败: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, stamp(time.Now().UTC())); err != nil {
			return fmt.Errorf("记录迁移 v%d 失败: %w", m.version, err)
		}
		current = m.version
	}
	return tx.Commit()
}

func setSchemaVersion(ctx context.Context, tx *sql.Tx, version int) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(version))
	return err
}

// currentSchemaVersion 兼容极旧数据库：四张 v1 核心表完整存在但没有版本记录时按 v1 处理。
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

type AppliedMigration struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at"`
}

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

func (s *Store) SchemaVersion() (int, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&raw); err != nil {
		return 0, err
	}
	return strconv.Atoi(raw)
}
