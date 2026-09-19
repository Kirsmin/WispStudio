package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"wisp/internal/config"
	"wisp/internal/provider"
	agentruntime "wisp/internal/runtime"
	"wisp/internal/store"
)

type ChatHandler struct {
	cfg     *config.Config
	catalog *provider.Catalog
	store   *store.Store
	runtime *agentruntime.Coordinator
}

func NewChatHandler(cfg *config.Config, catalog *provider.Catalog, st *store.Store, runtime *agentruntime.Coordinator) *ChatHandler {
	return &ChatHandler{cfg: cfg, catalog: catalog, store: st, runtime: runtime}
}

type chatRequest struct {
	Message  string `json:"message"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

// HandleChat 只负责传输、校验、持久化用户输入、启动/继续 Runtime 与 SSE 订阅。
// Provider 流读取、Tool Loop、Turn 生命周期等业务规则全部由 Coordinator 管理。
func (h *ChatHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少会话ID")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeJSONError(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "会话不存在")
		return
	}
	providerID := strings.TrimSpace(req.Provider)
	if providerID == "" {
		providerID = strings.TrimSpace(sess.Provider)
	}
	prov, model, err := h.catalog.Resolve(r.Context(), providerID, req.Model)
	if err != nil && strings.TrimSpace(req.Provider) == "" && providerID != "" {
		prov, model, err = h.catalog.Resolve(r.Context(), "", req.Model)
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Provider, req.Model = prov.ID, model.ID
	if req.Thinking == "" {
		req.Thinking = "default"
	}
	selection := agentruntime.Selection{Provider: req.Provider, Model: req.Model, Thinking: req.Thinking}
	_ = h.store.UpdateSelection(sessionID, req.Provider, req.Model)

	turn, err := h.store.GetOpenTurn(sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	isNew := turn == nil
	if isNew {
		turn, err = h.store.BeginTaskTurn(sessionID, req.Message)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "创建 Turn 失败: "+err.Error())
			return
		}
		policy := agentruntime.ClassifyTask(req.Message)
		_ = h.store.SetTurnTaskPolicy(turn.ID, policy.Complexity, policy.AllowReconnaissance)
		turn.TaskComplexity = policy.Complexity
		turn.AllowReconnaissance = policy.AllowReconnaissance
	}

	var userRecord store.Record
	execStatus := h.runtime.Runs().Status(sessionID)
	if !isNew && execStatus.Active {
		userRecord, err = h.store.AppendSteering(sessionID, turn.ID, req.Message)
	} else {
		userRecord, err = h.store.AppendUser(sessionID, turn.ID, req.Message, req.Provider, req.Model, req.Thinking)
	}
	if err != nil {
		if isNew {
			_ = h.store.CompleteTurn(turn.ID, store.TurnFailed)
		}
		writeJSONError(w, http.StatusInternalServerError, "保存消息失败: "+err.Error())
		return
	}
	if !isNew {
		_ = h.store.AppendTurnDecision(turn.ID, req.Message)
		if fresh, loadErr := h.store.GetTurn(turn.ID); loadErr == nil {
			// 只在 Planning 阶段重新分级。Build 中到达的 Steering 可以修订目标，但不能在执行途中静默降级既定策略或移除 Build 工具。
			if fresh.ActiveAgent == "plan" {
				policy := agentruntime.ClassifyTask(fresh.CurrentObjective)
				_ = h.store.SetTurnTaskPolicy(turn.ID, policy.Complexity, policy.AllowReconnaissance)
				fresh.TaskComplexity = policy.Complexity
				fresh.AllowReconnaissance = policy.AllowReconnaissance
			}
			turn = fresh
		}
	}
	if isNew && !sess.Renamed {
		_ = h.store.UpdateAutoTitle(sessionID, store.GenerateTitle(req.Message))
	}

	sse, supported := NewSSEWriter(w)
	if !supported {
		writeJSONError(w, http.StatusInternalServerError, "SSE 不支持")
		return
	}
	_ = sse.WriteEvent("ack", mustJSON(map[string]any{
		"record": userRecord, "turn_id": turn.ID, "steering": userRecord.Kind == store.EventUserSteering,
		"message": map[string]any{"id": userRecord.ID, "type": "user", "ts": userRecord.CreatedAt, "content": req.Message, "provider": req.Provider, "model": req.Model, "thinking": req.Thinking},
	}))

	// 正在执行时该请求只是 Steering：事实已经持久化，当前 Action 到安全点后会消费。
	if userRecord.Kind == store.EventUserSteering {
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"turn_id": turn.ID, "status": store.TurnRunning, "steering": true}))
		return
	}
	if turn.Status == store.TurnPaused {
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"turn_id": turn.ID, "status": store.TurnPaused}))
		return
	}
	if pending, _ := h.store.PendingApproval(turn.ID); pending != nil {
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"turn_id": turn.ID, "status": store.TurnWaitingUser, "approval_id": pending.ID}))
		return
	}

	events, unsubscribe := h.runtime.Hub().Subscribe(sessionID)
	defer unsubscribe()
	if !h.runtime.Start(turn.ID, selection) {
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"turn_id": turn.ID, "status": "running"}))
		return
	}
	for {
		select {
		case <-r.Context().Done():
			// 浏览器断开只结束订阅，Runtime 的独立 Context 继续执行并落盘。
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.TurnID != turn.ID {
				continue
			}
			_ = sse.WriteEvent(event.Type, string(event.Data))
			if event.Type == "runtime.status" {
				var state struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(event.Data, &state)
				switch state.Status {
				case store.TurnWaitingUser, store.TurnPaused, store.TurnCompleted, store.TurnStopped, store.TurnCancelled, store.TurnFailed:
					_ = sse.WriteEvent("done", mustJSON(map[string]any{"turn_id": turn.ID, "status": state.Status}))
					return
				}
			}
		}
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{}`
	}
	return string(data)
}
