package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"wisp/internal/config"
	"wisp/internal/provider"
	agentruntime "wisp/internal/runtime"
	"wisp/internal/store"
	"wisp/internal/webui"
)

type Router struct {
	cfg         *config.Config
	mux         *http.ServeMux
	catalog     *provider.Catalog
	runs        *RunRegistry
	hub         *agentruntime.EventHub
	store       *store.Store
	runtime     *agentruntime.Coordinator
	chatHandler *ChatHandler
}

func NewRouter(cfg *config.Config) (*Router, error) {
	st, err := store.Open(cfg.Storage.DataDir)
	if err != nil {
		return nil, err
	}
	catalog := provider.NewCatalog(cfg)
	runs := NewRunRegistry()
	hub := agentruntime.NewEventHub()
	rt := agentruntime.NewCoordinator(cfg, catalog, st, runs, hub, "")
	router := &Router{cfg: cfg, mux: http.NewServeMux(), catalog: catalog, runs: runs, hub: hub, store: st, runtime: rt}
	router.chatHandler = NewChatHandler(cfg, catalog, st, rt)
	router.registerRoutes()
	return router, nil
}

func (r *Router) Close() error                                       { return r.store.Close() }
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

func (r *Router) registerRoutes() {
	r.mux.HandleFunc("/api/health", cors(r.handleHealth))
	r.mux.HandleFunc("/api/catalog", cors(r.handleCatalog))
	r.mux.HandleFunc("/api/providers", cors(r.handleProviders))
	r.mux.HandleFunc("/api/models", cors(r.handleModels))
	r.mux.HandleFunc("/api/runtime/profiles", cors(r.handleProfiles))
	r.mux.HandleFunc("/api/sessions", cors(r.handleSessions))
	r.mux.HandleFunc("/api/sessions/{id}", cors(r.handleSession))
	r.mux.HandleFunc("/api/sessions/{id}/timeline", cors(r.handleTimeline))
	r.mux.HandleFunc("/api/sessions/{id}/runtime", cors(r.handleRuntimeState))
	r.mux.HandleFunc("/api/sessions/{id}/debug", cors(r.handleSessionDebug))
	r.mux.HandleFunc("/api/sessions/{id}/debug/calls/{call}", cors(r.handleDebugCall))
	r.mux.HandleFunc("/api/sessions/{id}/export", cors(r.handleSessionExport))
	r.mux.HandleFunc("/api/sessions/{id}/chat", cors(r.chatHandler.HandleChat))
	r.mux.HandleFunc("/api/sessions/{id}/chat/status", cors(r.handleChatStatus))
	r.mux.HandleFunc("/api/sessions/{id}/chat/cancel", cors(r.handleChatCancel))
	r.mux.HandleFunc("/api/sessions/{id}/turns/{turn}/pause", cors(r.handlePause))
	r.mux.HandleFunc("/api/sessions/{id}/turns/{turn}/resume", cors(r.handleResume))
	r.mux.HandleFunc("/api/sessions/{id}/turns/{turn}/stop", cors(r.handleStop))
	r.mux.HandleFunc("/api/sessions/{id}/turns/{turn}/start-build", cors(r.handleStartBuild))
	r.mux.HandleFunc("/api/sessions/{id}/turns/{turn}/fold", cors(r.handleFoldTurn))
	r.mux.HandleFunc("/api/sessions/{id}/folds/restore", cors(r.handleRestoreFold))
	r.mux.HandleFunc("/api/artifacts/{id}/activate", cors(r.handleActivateArtifact))
	r.mux.HandleFunc("/api/approvals/{id}/approve", cors(r.handleApprove))
	r.mux.HandleFunc("/api/approvals/{id}/reject", cors(r.handleReject))
	r.mux.HandleFunc("/api/approvals/{id}/challenge", cors(r.handleChallenge))
	r.mux.HandleFunc("/api/agent-runs/{id}/timeline", cors(r.handleAgentRunTimeline))
	r.mux.HandleFunc("/api/", cors(func(w http.ResponseWriter, req *http.Request) { http.NotFound(w, req) }))
	r.mux.Handle("/", webui.Handler())
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	version, _ := r.store.SchemaVersion()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ts": time.Now().UTC().Format(time.RFC3339Nano), "schema_version": version})
}
func (r *Router) handleCatalog(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	force, _ := strconv.ParseBool(req.URL.Query().Get("refresh"))
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, 200, r.catalog.Snapshot(ctx, force))
}
func (r *Router) handleProviders(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, 200, r.catalog.Snapshot(ctx, false).Providers)
}
func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	ctx, cancel := contextWithCatalogTimeout(req, 35*time.Second)
	defer cancel()
	writeJSON(w, 200, r.catalog.Snapshot(ctx, false).Models)
}
func (r *Router) handleProfiles(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, r.runtime.Profiles())
}

func (r *Router) handleSessions(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		list, err := r.store.ListSessions()
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求体解析失败")
			return
		}
		item, err := r.store.CreateSession(body.Title)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, item)
	default:
		http.Error(w, "方法不允许", 405)
	}
}
func (r *Router) handleSession(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if id == "" {
		writeJSONError(w, 400, "缺少会话ID")
		return
	}
	switch req.Method {
	case http.MethodPatch:
		var body struct {
			Title         *string `json:"title"`
			AgentsEnabled *bool   `json:"agents_enabled"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求体解析失败")
			return
		}
		if body.Title == nil && body.AgentsEnabled == nil {
			writeJSONError(w, 400, "没有可更新的配置")
			return
		}
		if body.Title != nil {
			if err := r.store.UpdateTitle(id, *body.Title); err != nil {
				writeJSONError(w, 404, err.Error())
				return
			}
		}
		if body.AgentsEnabled != nil {
			if err := r.store.SetAgentsEnabled(id, *body.AgentsEnabled); err != nil {
				writeJSONError(w, 404, err.Error())
				return
			}
		}
		writeJSON(w, 200, map[string]any{"agents_enabled": body.AgentsEnabled})
	case http.MethodDelete:
		if r.runs.Status(id).Active {
			writeJSONError(w, 409, "会话正在执行，请先停止")
			return
		}
		if err := r.store.DeleteSession(id); err != nil {
			writeJSONError(w, 404, err.Error())
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "方法不允许", 405)
	}
}
func (r *Router) handleTimeline(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
	items, err := r.store.Timeline(req.PathValue("id"), after, 5000)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

func (r *Router) handleSessionDebug(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	id := req.PathValue("id")
	if _, err := r.store.GetSession(id); err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
	before, _ := strconv.ParseInt(req.URL.Query().Get("before"), 10, 64)
	calls, more, err := r.store.ListModelCallSummaries(id, after, before, 40)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	next := after
	if len(calls) > 0 {
		next = calls[len(calls)-1].Cursor
	}
	statuses, err := r.store.RecentModelCallStatuses(id)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"calls": calls, "next_cursor": next, "has_more": more, "updates": statuses})
}

func (r *Router) handleDebugCall(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	id := req.PathValue("id")
	if _, err := r.store.GetSession(id); err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	detail, err := r.store.ModelCallDetail(id, req.PathValue("call"))
	if err != nil {
		writeJSONError(w, 404, "模型调用不存在")
		return
	}
	trace, err := r.store.ModelCallTrace(id, detail.ID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"call": detail, "trace": trace})
}

func (r *Router) handleSessionExport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	sessionID := req.PathValue("id")
	if _, err := r.store.GetSession(sessionID); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var archive bytes.Buffer
	if err := r.store.WriteSessionArchive(req.Context(), sessionID, &archive); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "导出 Session 失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="wisp-session-`+sessionID+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(archive.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive.Bytes())
}

func (r *Router) handleRuntimeState(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	sessionID := req.PathValue("id")
	if _, err := r.store.GetSession(sessionID); err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	turns, err := r.store.ListTurns(sessionID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	open, err := r.store.GetOpenTurn(sessionID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	var artifacts []store.Artifact
	var approvals []store.Approval
	var agentRuns []store.AgentRun
	for _, turn := range turns {
		a, _ := r.store.ListArtifacts(turn.ID)
		artifacts = append(artifacts, a...)
		p, _ := r.store.ListApprovals(turn.ID)
		approvals = append(approvals, p...)
		runs, _ := r.store.ListAgentRuns(turn.ID)
		agentRuns = append(agentRuns, runs...)
	}
	execution := r.runs.Status(sessionID)
	pending := false
	hasPlan := false
	if open != nil {
		if p, _ := r.store.PendingApproval(open.ID); p != nil {
			pending = true
		}
		_, v, _ := r.store.ActiveArtifact(open.ID, "plan", "active")
		hasPlan = v != nil
	}
	caps := r.runtime.Capabilities(open, execution.Active, pending, hasPlan)
	folded, _ := r.store.FoldedTurnIDs(sessionID)
	writeJSON(w, 200, map[string]any{"turn": open, "turns": turns, "execution": execution, "capabilities": caps, "artifacts": artifacts, "approvals": approvals, "agent_runs": agentRuns, "can_restore_fold": len(folded) > 0})
}
func (r *Router) handleChatStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	writeJSON(w, 200, r.runs.Status(req.PathValue("id")))
}
func (r *Router) handleChatCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	sessionID := req.PathValue("id")
	turn, _ := r.store.GetOpenTurn(sessionID)
	if turn != nil {
		r.persistCommand(turn, "hard_cancel", nil)
	}
	cancelled := r.runs.Cancel(sessionID)
	if !cancelled && turn != nil {
		_ = r.store.SetTurnStatus(turn.ID, store.TurnCancelled)
		if turn.ActiveAgentRunID != "" {
			_ = r.store.FinishAgentRun(turn.ActiveAgentRunID, "cancelled", map[string]any{"reason": "hard_cancel"})
		}
	}
	writeJSON(w, 200, map[string]bool{"cancelled": cancelled || turn != nil})
}

func (r *Router) handlePause(w http.ResponseWriter, req *http.Request) {
	turn, ok := r.controlTurn(w, req)
	if !ok {
		return
	}
	r.persistCommand(turn, "pause", nil)
	if r.runs.Status(turn.SessionID).Active {
		_ = r.store.SetPauseRequested(turn.ID, true)
	} else {
		_ = r.store.SetTurnStatus(turn.ID, store.TurnPaused)
	}
	writeJSON(w, 200, map[string]any{"status": "pause_requested"})
}
func (r *Router) handleResume(w http.ResponseWriter, req *http.Request) {
	turn, ok := r.controlTurn(w, req)
	if !ok {
		return
	}
	if turn.Status != store.TurnPaused {
		writeJSONError(w, 409, "Turn 未处于 paused")
		return
	}
	sel := r.decodeSelection(req)
	r.persistCommand(turn, "resume", nil)
	_ = r.store.ClearControlRequests(turn.ID)
	_ = r.store.SetTurnStatus(turn.ID, store.TurnRunning)
	started := r.runtime.Start(turn.ID, sel)
	writeJSON(w, 200, map[string]any{"started": started})
}
func (r *Router) handleStop(w http.ResponseWriter, req *http.Request) {
	turn, ok := r.controlTurn(w, req)
	if !ok {
		return
	}
	r.persistCommand(turn, "stop", nil)
	if r.runs.Status(turn.SessionID).Active {
		_ = r.store.SetStopRequested(turn.ID, true)
		writeJSON(w, 200, map[string]any{"status": "stop_requested"})
		return
	}
	_ = r.store.SetTurnStatus(turn.ID, store.TurnStopped)
	if turn.ActiveAgentRunID != "" {
		_ = r.store.FinishAgentRun(turn.ActiveAgentRunID, "stopped", map[string]any{"reason": "user_stop"})
	}
	writeJSON(w, 200, map[string]any{"status": store.TurnStopped})
}
func (r *Router) handleStartBuild(w http.ResponseWriter, req *http.Request) {
	turn, ok := r.controlTurn(w, req)
	if !ok {
		return
	}
	if err := r.runtime.StartBuild(turn.ID, r.decodeSelection(req)); err != nil {
		writeJSONError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"started": true})
}
func (r *Router) controlTurn(w http.ResponseWriter, req *http.Request) (*store.Turn, bool) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return nil, false
	}
	turn, err := r.store.GetTurn(req.PathValue("turn"))
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return nil, false
	}
	if turn.SessionID != req.PathValue("id") {
		writeJSONError(w, 404, "Turn 不属于该会话")
		return nil, false
	}
	return turn, true
}
func (r *Router) persistCommand(turn *store.Turn, name string, data any) {
	_, _ = r.store.AppendEvent(store.Record{SessionID: turn.SessionID, TurnID: turn.ID, Kind: store.EventRuntimeCommand, Content: name, Data: marshalRaw(data)})
}

func (r *Router) handleActivateArtifact(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	var body struct {
		Version int `json:"version"`
	}
	if json.NewDecoder(req.Body).Decode(&body) != nil || body.Version <= 0 {
		writeJSONError(w, 400, "缺少 version")
		return
	}
	artifact, err := r.store.GetArtifact(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	turn, err := r.store.GetTurn(artifact.TurnID)
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	if !r.runtime.ArtifactVersionMutable(turn, artifact) {
		writeJSONError(w, 409, "当前 Runtime 阶段已冻结该 Artifact Version")
		return
	}
	if err := r.store.ActivateArtifactVersion(artifact.ID, body.Version); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"active_version": body.Version})
}

func (r *Router) handleApprove(w http.ResponseWriter, req *http.Request) {
	r.decideApproval(w, req, "approved")
}
func (r *Router) handleReject(w http.ResponseWriter, req *http.Request) {
	r.decideApproval(w, req, "rejected")
}
func (r *Router) decideApproval(w http.ResponseWriter, req *http.Request, status string) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	var body struct {
		Note     string `json:"note"`
		Scope    string `json:"scope"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Thinking string `json:"thinking"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	current, err := r.store.GetApproval(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	if r.runs.Status(current.SessionID).Active {
		writeJSONError(w, 409, "当前 Approval 的解释 Action 仍在执行")
		return
	}
	if body.Scope != "" && body.Scope != "once" && body.Scope != "turn" {
		writeJSONError(w, 400, "未知授权作用域")
		return
	}
	if body.Scope == "turn" && status != "approved" {
		writeJSONError(w, 400, "拒绝操作不能授予权限")
		return
	}
	if body.Scope == "turn" && current.ToolName != "write_file" && current.ToolName != "run_command" {
		writeJSONError(w, 400, "此工具不支持本轮授权")
		return
	}
	approval, err := r.store.DecideApproval(req.PathValue("id"), status, map[string]any{"note": body.Note, "scope": body.Scope})
	if err != nil {
		writeJSONError(w, 409, err.Error())
		return
	}
	if status == "approved" && body.Scope == "turn" {
		if err := r.store.GrantApproval(approval); err != nil {
			writeJSONError(w, 500, "保存作用域授权失败: "+err.Error())
			return
		}
	}
	_ = r.store.SetTurnStatus(approval.TurnID, store.TurnRunning)
	started := r.runtime.Start(approval.TurnID, agentruntime.Selection{Provider: body.Provider, Model: body.Model, Thinking: body.Thinking})
	writeJSON(w, 200, map[string]any{"status": status, "started": started})
}
func (r *Router) handleChallenge(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	var body struct {
		Question string `json:"question"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Thinking string `json:"thinking"`
	}
	if err := decodeOptionalJSON(req, &body); err != nil {
		writeJSONError(w, 400, "请求体解析失败")
		return
	}
	approval, err := r.store.GetApproval(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	turn, err := r.store.GetTurn(approval.TurnID)
	if err == nil {
		r.persistCommand(turn, "approval.challenge", map[string]any{"approval_id": approval.ID, "question": body.Question})
	}
	if err := r.runtime.ChallengeApproval(approval.ID, body.Question, agentruntime.Selection{Provider: body.Provider, Model: body.Model, Thinking: body.Thinking}); err != nil {
		writeJSONError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"started": true})
}

func (r *Router) handleFoldTurn(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	if err := r.store.FoldTurn(req.PathValue("id"), req.PathValue("turn")); err != nil {
		writeJSONError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"folded": true})
}
func (r *Router) handleRestoreFold(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "方法不允许", 405)
		return
	}
	turnID, err := r.store.RestoreLatestFold(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"restored_turn_id": turnID})
}
func (r *Router) handleAgentRunTimeline(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "方法不允许", 405)
		return
	}
	records, err := r.store.AgentRunTimeline(req.PathValue("id"))
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, records)
}

func (r *Router) decodeSelection(req *http.Request) agentruntime.Selection {
	var body agentruntime.Selection
	_ = decodeOptionalJSON(req, &body)
	return body
}
func decodeOptionalJSON(req *http.Request, target any) error {
	if req.Body == nil {
		return nil
	}
	err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(target)
	if err == io.EOF {
		return nil
	}
	return err
}
func marshalRaw(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage(`{}`)
	}
	data, _ := json.Marshal(value)
	return data
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		if req.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next(w, req)
	}
}
func contextWithCatalogTimeout(req *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(req.Context(), timeout)
}
