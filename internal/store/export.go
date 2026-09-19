package store

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type archiveEntry struct {
	name string
	data []byte
}

// WriteSessionArchive 导出指定 Session 的全部会话级持久化数据。
// 数据先在只读事务里快速快照到内存，随后再压缩，避免长时间占用 SQLite 单连接。
func (s *Store) WriteSessionArchive(ctx context.Context, sessionID string, dst io.Writer) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var title string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM sessions WHERE id=?`, sessionID).Scan(&title); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("会话不存在")
		}
		return err
	}

	tables := []struct {
		name  string
		query string
		args  []any
	}{
		{"session/session.jsonl", `SELECT * FROM sessions WHERE id=?`, []any{sessionID}},
		{"session/turns.jsonl", `SELECT * FROM turns WHERE session_id=? ORDER BY turn_index,id`, []any{sessionID}},
		{"session/timeline.jsonl", `SELECT * FROM records WHERE session_id=? ORDER BY seq,id`, []any{sessionID}},
		{"requests/model_calls.jsonl", `SELECT * FROM model_calls WHERE session_id=? ORDER BY call_index,created_at,id`, []any{sessionID}},
		{"runtime/agent_runs.jsonl", `SELECT * FROM agent_runs WHERE session_id=? ORDER BY created_at,id`, []any{sessionID}},
		{"runtime/approvals.jsonl", `SELECT * FROM approvals WHERE session_id=? ORDER BY created_at,id`, []any{sessionID}},
		{"runtime/approval_grants.jsonl", `SELECT * FROM approval_grants WHERE session_id=? ORDER BY created_at,id`, []any{sessionID}},
		{"runtime/checkpoints.jsonl", `SELECT * FROM checkpoints WHERE session_id=? ORDER BY created_at,id`, []any{sessionID}},
		{"runtime/context_epochs.jsonl", `SELECT * FROM context_epochs WHERE session_id=? ORDER BY turn_id,epoch_index,id`, []any{sessionID}},
		{"runtime/context_folds.jsonl", `SELECT * FROM context_folds WHERE session_id=? ORDER BY stack_index,id`, []any{sessionID}},
		{"artifacts/artifacts.jsonl", `SELECT * FROM artifacts WHERE session_id=? ORDER BY created_at,id`, []any{sessionID}},
		{"artifacts/artifact_versions.jsonl", `SELECT v.* FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id WHERE a.session_id=? ORDER BY a.created_at,v.version,v.id`, []any{sessionID}},
	}

	entries := make([]archiveEntry, 0, len(tables)+16)
	for _, table := range tables {
		data, err := queryJSONLines(ctx, tx, table.query, table.args...)
		if err != nil {
			return fmt.Errorf("导出 %s 失败: %w", table.name, err)
		}
		entries = append(entries, archiveEntry{name: table.name, data: data})
	}

	requestEntries, err := modelCallArchiveEntries(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	entries = append(entries, requestEntries...)

	var schemaVersion int
	var rawVersion string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&rawVersion); err == nil {
		_, _ = fmt.Sscanf(rawVersion, "%d", &schemaVersion)
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	manifest := map[string]any{
		"format":         "wisp-session-export",
		"format_version": 1,
		"session_id":     sessionID,
		"title":          title,
		"schema_version": schemaVersion,
		"exported_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"files":          entryNames(entries),
		"notes":          "包含 Wisp 对该 Session 本地持久化的会话、Timeline、Runtime、Artifact 与模型调用请求数据；Authorization 等未持久化敏感 Header 不在导出中。",
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	entries = append([]archiveEntry{
		{name: "manifest.json", data: append(manifestData, '\n')},
		{name: "README.txt", data: []byte("Wisp Session Export\n\nrequests/model_calls.jsonl 包含每次模型调用的元数据与 request_json。\nrequests/calls/ 下按调用保存最终发往上游的请求正文、系统提示词快照与上下文编译信息。\nsession/timeline.jsonl 是不可变 Timeline。runtime/ 与 artifacts/ 保存其余 Session 级持久事实。\n")},
	}, entries...)

	zw := zip.NewWriter(dst)
	for _, entry := range entries {
		name := filepath.ToSlash(strings.TrimPrefix(entry.name, "/"))
		w, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := w.Write(entry.data); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}

func queryJSONLines(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]byte, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		item := make(map[string]any, len(columns))
		for i, column := range columns {
			item[column] = normalizeExportValue(column, values[i])
		}
		if err := encoder.Encode(item); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func normalizeExportValue(column string, value any) any {
	if value == nil {
		return nil
	}
	var text string
	switch v := value.(type) {
	case []byte:
		text = string(v)
	case string:
		text = v
	default:
		return value
	}
	if strings.HasSuffix(column, "_json") && json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	return text
}

func modelCallArchiveEntries(ctx context.Context, tx *sql.Tx, sessionID string) ([]archiveEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,call_index,request_json,system_prompt_snapshot,context_debug_json FROM model_calls WHERE session_id=? ORDER BY call_index,created_at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []archiveEntry
	for rows.Next() {
		var id, requestJSON, systemPrompt, contextDebug string
		var index int
		if err := rows.Scan(&id, &index, &requestJSON, &systemPrompt, &contextDebug); err != nil {
			return nil, err
		}
		base := fmt.Sprintf("requests/calls/%04d-%s", index, id)
		if !json.Valid([]byte(requestJSON)) {
			encoded, _ := json.Marshal(map[string]string{"raw": requestJSON})
			requestJSON = string(encoded)
		}
		if !json.Valid([]byte(contextDebug)) {
			encoded, _ := json.Marshal(map[string]string{"raw": contextDebug})
			contextDebug = string(encoded)
		}
		var prettyRequest bytes.Buffer
		if err := json.Indent(&prettyRequest, []byte(requestJSON), "", "  "); err != nil {
			prettyRequest.WriteString(requestJSON)
		}
		prettyRequest.WriteByte('\n')
		var prettyDebug bytes.Buffer
		if err := json.Indent(&prettyDebug, []byte(contextDebug), "", "  "); err != nil {
			prettyDebug.WriteString(contextDebug)
		}
		prettyDebug.WriteByte('\n')
		out = append(out,
			archiveEntry{name: base + "/request.json", data: prettyRequest.Bytes()},
			archiveEntry{name: base + "/system-prompt.txt", data: []byte(systemPrompt)},
			archiveEntry{name: base + "/context-debug.json", data: prettyDebug.Bytes()},
		)
	}
	return out, rows.Err()
}

func entryNames(entries []archiveEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, filepath.ToSlash(entry.name))
	}
	return out
}
