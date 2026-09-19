package store

import (
	"context"
	"fmt"
	"strconv"
)

const schemaVersion = 8

// 仅保留当前结构以及上一发布版 v7 的一次性数据升级；不猜测旧库结构。
const currentSchema = `
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

CREATE TABLE approval_grants (id TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,tool_name TEXT NOT NULL,fingerprint TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(turn_id,tool_name,fingerprint));

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

CREATE TABLE model_calls (
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
    agent_run_id TEXT NOT NULL DEFAULT '',
    context_epoch INTEGER NOT NULL DEFAULT 1,
    context_hash TEXT NOT NULL DEFAULT '',
    prefix_hash TEXT NOT NULL DEFAULT '',
    context_debug_json TEXT NOT NULL DEFAULT '{}',
    request_url TEXT NOT NULL DEFAULT '',
    request_json TEXT NOT NULL DEFAULT '{}',
    UNIQUE(turn_id, call_index)
);

CREATE TABLE records (
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

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    renamed INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    agents_enabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE turns (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT,
    objective TEXT NOT NULL DEFAULT '',
    active_agent TEXT NOT NULL DEFAULT 'plan',
    context_epoch INTEGER NOT NULL DEFAULT 1,
    steering_cursor INTEGER NOT NULL DEFAULT 0,
    root_agent_run_id TEXT NOT NULL DEFAULT '',
    active_agent_run_id TEXT NOT NULL DEFAULT '',
    active_checkpoint_id TEXT NOT NULL DEFAULT '',
    pause_requested INTEGER NOT NULL DEFAULT 0,
    stop_requested INTEGER NOT NULL DEFAULT 0,
    current_objective TEXT NOT NULL DEFAULT '',
    user_decisions_json TEXT NOT NULL DEFAULT '[]',
    task_mode TEXT NOT NULL DEFAULT 'standard',
    stage TEXT NOT NULL DEFAULT 'plan',
    UNIQUE(session_id, turn_index)
);

CREATE INDEX idx_agent_runs_turn ON agent_runs(turn_id, created_at);

CREATE INDEX idx_approval_grants_turn ON approval_grants(turn_id,tool_name);

CREATE INDEX idx_approvals_turn_status ON approvals(turn_id, status, created_at);

CREATE INDEX idx_artifact_versions_artifact ON artifact_versions(artifact_id, version);

CREATE INDEX idx_artifacts_turn ON artifacts(turn_id, type);

CREATE INDEX idx_checkpoints_turn_epoch ON checkpoints(turn_id, context_epoch);

CREATE INDEX idx_context_folds_session_active ON context_folds(session_id, active, stack_index);

CREATE INDEX idx_model_calls_agent_run ON model_calls(agent_run_id, call_index);

CREATE INDEX idx_model_calls_session ON model_calls(session_id, created_at);

CREATE INDEX idx_model_calls_turn_index ON model_calls(turn_id, call_index);

CREATE INDEX idx_records_session_seq ON records(session_id, seq);

CREATE INDEX idx_records_turn_seq ON records(turn_id, seq);

CREATE INDEX idx_turns_session_status ON turns(session_id, status, turn_index);
`

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tableCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
			return err
		}
		if tableCount != 0 {
			return fmt.Errorf("发现无版本数据库；请先导出并使用干净的数据目录，不会擅自重建")
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, currentSchema); err != nil {
			return fmt.Errorf("初始化数据库失败: %w", err)
		}
	} else {
		var version string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
			return err
		}
		switch version {
		case "8":
			return tx.Commit()
		case "7":
			for _, query := range []string{
				`ALTER TABLE sessions ADD COLUMN agents_enabled INTEGER NOT NULL DEFAULT 0`,
				`ALTER TABLE turns ADD COLUMN current_objective TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE turns ADD COLUMN user_decisions_json TEXT NOT NULL DEFAULT '[]'`,
				`ALTER TABLE turns ADD COLUMN task_mode TEXT NOT NULL DEFAULT 'standard'`,
				`ALTER TABLE turns ADD COLUMN stage TEXT NOT NULL DEFAULT 'plan'`,
				`CREATE TABLE approval_grants (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE, tool_name TEXT NOT NULL, fingerprint TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(turn_id,tool_name,fingerprint))`,
				`CREATE INDEX idx_approval_grants_turn ON approval_grants(turn_id,tool_name)`,
				`UPDATE turns SET current_objective=COALESCE((SELECT v.content FROM artifacts a JOIN artifact_versions v ON v.artifact_id=a.id AND v.version=a.active_version WHERE a.turn_id=turns.id AND a.type='plan' AND a.name='active'), objective), stage=CASE WHEN active_agent='build' THEN 'build' ELSE 'plan' END`,
			} {
				if _, err := tx.ExecContext(ctx, query); err != nil {
					return fmt.Errorf("升级 v7→v8 失败: %w", err)
				}
			}
		default:
			return fmt.Errorf("数据库版本 %s 不受当前源码支持（仅支持 v7→v8）；请保留原数据并先导出", version)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SchemaVersion() (int, error) {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&value); err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}
