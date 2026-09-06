package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
	"wisp/internal/toolcall"
)

const maxToolRounds = 8

type ChatHandler struct {
	cfg          *config.Config
	catalog      *provider.Catalog
	store        *store.Store
	runs         *RunRegistry
	openaiClient *openai.Client
}

func NewChatHandler(cfg *config.Config, catalog *provider.Catalog, st *store.Store, runs *RunRegistry) *ChatHandler {
	return &ChatHandler{
		cfg:          cfg,
		catalog:      catalog,
		store:        st,
		runs:         runs,
		openaiClient: openai.NewClient(&cfg.OpenAI),
	}
}

type chatRequest struct {
	Message  string `json:"message"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

type callOutcome struct {
	Content     string
	Reasoning   string
	Usage       *store.Usage
	Finish      string
	Error       string
	DurationMs  int
	TTFTMs      int
	Tool        *toolcall.Call
	ToolEventID string
}

func (h *ChatHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "缺少会话ID", http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	providerID := strings.TrimSpace(req.Provider)
	if providerID == "" {
		providerID = strings.TrimSpace(sess.Provider)
	}
	provCfg, modelInfo, err := h.catalog.Resolve(r.Context(), providerID, req.Model)
	if err != nil && strings.TrimSpace(req.Provider) == "" && providerID != "" {
		provCfg, modelInfo, err = h.catalog.Resolve(r.Context(), "", req.Model)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Provider = provCfg.ID
	req.Model = modelInfo.ID

	prior, err := h.store.ContextMessages(sessionID)
	if err != nil {
		http.Error(w, "读取历史失败", http.StatusInternalServerError)
		return
	}

	runCtx, ok := h.runs.Begin(sessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "该会话有任务正在执行")
		return
	}
	defer h.runs.End(sessionID)

	turnID, err := h.store.BeginTurn(sessionID)
	if err != nil {
		http.Error(w, "创建 Turn 失败", http.StatusInternalServerError)
		return
	}
	userRecord, err := h.store.AppendUser(sessionID, turnID, req.Message, req.Provider, req.Model, req.Thinking)
	if err != nil {
		_ = h.store.CompleteTurn(turnID, "failed")
		http.Error(w, "保存消息失败", http.StatusInternalServerError)
		return
	}
	_ = h.store.UpdateSelection(sessionID, req.Provider, req.Model)
	if !sess.Renamed && len(prior) == 0 {
		_ = h.store.UpdateAutoTitle(sessionID, store.GenerateTitle(req.Message))
	}

	sse, supported := NewSSEWriter(w)
	if !supported {
		_ = h.store.CompleteTurn(turnID, "failed")
		http.Error(w, "SSE 不支持", http.StatusInternalServerError)
		return
	}
	_ = sse.WriteEvent("ack", mustJSON(map[string]any{
		"message": map[string]any{
			"id":       userRecord.ID,
			"type":     "user",
			"ts":       userRecord.CreatedAt,
			"content":  req.Message,
			"provider": req.Provider,
			"model":    req.Model,
			"thinking": req.Thinking,
		},
		"turn_id": turnID,
	}))

	baseURL, apiKey := h.cfg.OpenAI.BaseURL, h.cfg.OpenAI.APIKey
	if provCfg.BaseURL != "" {
		baseURL = provCfg.BaseURL
	}
	if provCfg.APIKey != "" {
		apiKey = provCfg.APIKey
	}
	systemPrompt := buildSystemPrompt(h.cfg.SystemPrompt)

	for callIndex := 1; callIndex <= maxToolRounds; callIndex++ {
		if runCtx.Err() != nil {
			h.finishCancelled(sse, sessionID, turnID, "")
			return
		}

		history, err := h.store.ContextMessages(sessionID)
		if err != nil {
			h.finishTurnError(sse, sessionID, turnID, "", "读取上下文失败: "+err.Error())
			return
		}
		messages := make([]openai.ChatMessage, 0, len(history)+1)
		messages = append(messages, openai.ChatMessage{Role: "system", Content: systemPrompt})
		for _, msg := range history {
			messages = append(messages, openai.ChatMessage{Role: msg.Role, Content: msg.Content})
		}

		modelCallID, err := h.store.BeginModelCall(sessionID, turnID, callIndex, req.Provider, req.Model, req.Thinking, systemPrompt)
		if err != nil {
			h.finishTurnError(sse, sessionID, turnID, "", "创建 ModelCall 失败: "+err.Error())
			return
		}
		_ = sse.WriteEvent("model.start", mustJSON(map[string]any{
			"call_id":  modelCallID,
			"index":    callIndex,
			"provider": req.Provider,
			"model":    req.Model,
			"thinking": req.Thinking,
		}))

		upReq, err := h.openaiClient.BuildRequest(baseURL, apiKey, req.Model, modelInfo.ThinkingStyle, req.Thinking, messages)
		if err != nil {
			message := "构造请求失败: " + err.Error()
			_ = h.store.AppendError(sessionID, turnID, modelCallID, message)
			_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{Status: "failed", Finish: "error", Error: message})
			h.finishTurnError(sse, sessionID, turnID, modelCallID, message)
			return
		}

		outcome := h.runModelCall(runCtx, sse, upReq)
		if runCtx.Err() != nil {
			h.persistCallBody(sessionID, turnID, modelCallID, outcome)
			_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
				Status: "cancelled", Finish: "aborted", Usage: outcome.Usage,
				DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs, Error: "生成已停止",
			})
			h.finishCancelled(sse, sessionID, turnID, modelCallID)
			return
		}

		h.persistCallBody(sessionID, turnID, modelCallID, outcome)
		if outcome.Error != "" {
			_ = h.store.AppendError(sessionID, turnID, modelCallID, outcome.Error)
			_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
				Status: "failed", Finish: "error", Usage: outcome.Usage,
				DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs, Error: outcome.Error,
			})
			h.finishTurnError(sse, sessionID, turnID, modelCallID, outcome.Error)
			return
		}

		if outcome.Tool != nil {
			if err := h.handleTool(sse, sessionID, turnID, modelCallID, outcome); err != nil {
				message := "保存工具结果失败: " + err.Error()
				_ = h.store.AppendError(sessionID, turnID, modelCallID, message)
				_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
					Status: "failed", Finish: "error", Usage: outcome.Usage,
					DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs, Error: message,
				})
				h.finishTurnError(sse, sessionID, turnID, modelCallID, message)
				return
			}
			_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
				Status: "completed", Finish: "tool_call", Usage: outcome.Usage,
				DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs,
			})
			_ = sse.WriteEvent("model.done", mustJSON(map[string]any{
				"call_id": modelCallID, "finish": "tool_call",
				"duration_ms": outcome.DurationMs, "ttft_ms": outcome.TTFTMs,
			}))
			continue
		}

		finish := outcome.Finish
		if finish == "" {
			finish = "stop"
		}
		if outcome.Content == "" && outcome.Reasoning == "" {
			message := "模型没有返回可显示的内容"
			_ = h.store.AppendError(sessionID, turnID, modelCallID, message)
			_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
				Status: "failed", Finish: "error", Usage: outcome.Usage,
				DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs, Error: message,
			})
			h.finishTurnError(sse, sessionID, turnID, modelCallID, message)
			return
		}
		_ = h.store.FinishModelCall(modelCallID, store.ModelCallResult{
			Status: "completed", Finish: finish, Usage: outcome.Usage,
			DurationMs: outcome.DurationMs, TTFTMs: outcome.TTFTMs,
		})
		_ = h.store.CompleteTurn(turnID, "completed")
		_ = h.store.Touch(sessionID)
		_ = sse.WriteEvent("model.done", mustJSON(map[string]any{
			"call_id": modelCallID, "finish": finish,
			"duration_ms": outcome.DurationMs, "ttft_ms": outcome.TTFTMs,
		}))
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"finish": finish, "turn_id": turnID}))
		return
	}

	message := fmt.Sprintf("工具调用超过上限 (%d)", maxToolRounds)
	_ = h.store.AppendError(sessionID, turnID, "", message)
	_ = h.store.CompleteTurn(turnID, "failed")
	_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": message}))
	_ = sse.WriteEvent("done", mustJSON(map[string]any{"finish": "error", "error": message, "turn_id": turnID}))
}

func (h *ChatHandler) runModelCall(ctx context.Context, sse *SSEWriter, upReq *http.Request) callOutcome {
	startTime := time.Now()
	resp, err := h.openaiClient.DoStream(ctx, upReq)
	if err != nil {
		return callOutcome{Finish: "error", Error: err.Error(), DurationMs: int(time.Since(startTime).Milliseconds())}
	}
	defer resp.Body.Close()

	reader := openai.NewStreamReader(resp.Body)
	ch := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(ch)

	var out callOutcome
	var finalUsage *openai.Usage
	var firstToken time.Time
	framer := &toolcall.Framer{}
	streamStoppedForTool := false

	for evt := range ch {
		if streamStoppedForTool {
			continue
		}
		switch evt.Type {
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()}))
			}
			out.Reasoning += evt.Text
			_ = sse.WriteEvent("reasoning", mustJSON(map[string]any{"text": evt.Text}))
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()}))
			}
			parsed := framer.Feed(evt.Text)
			if parsed.Text != "" {
				out.Content += parsed.Text
				_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": parsed.Text}))
			}
			if parsed.Detected && out.ToolEventID == "" {
				out.ToolEventID = "tc_" + strings.ReplaceAll(uuid.New().String(), "-", "")
				_ = sse.WriteEvent("tool.detecting", mustJSON(map[string]any{"call_id": out.ToolEventID}))
			}
			if parsed.Err != nil {
				out.Error = "工具调用解析失败: " + parsed.Err.Error()
				out.Finish = "error"
				if out.ToolEventID != "" {
					_ = sse.WriteEvent("tool.failed", mustJSON(map[string]any{"call_id": out.ToolEventID, "error": out.Error}))
				}
				_ = reader.Close()
				streamStoppedForTool = true
				continue
			}
			if parsed.Call != nil {
				out.Tool = parsed.Call
				if out.ToolEventID == "" {
					out.ToolEventID = "tc_" + strings.ReplaceAll(uuid.New().String(), "-", "")
					_ = sse.WriteEvent("tool.detecting", mustJSON(map[string]any{"call_id": out.ToolEventID}))
				}
				out.Finish = "tool_call"
				_ = reader.Close()
				streamStoppedForTool = true
			}
		case "usage":
			finalUsage = evt.Usage
			_ = sse.WriteEvent("usage", mustJSON(evt.Usage))
		case "done":
			if out.Finish == "" {
				out.Finish = evt.Finish
			}
		case "error":
			out.Error = evt.Error
			out.Finish = "error"
		}
	}

	if !streamStoppedForTool && out.Error == "" {
		last := framer.Finalize()
		if last.Text != "" {
			out.Content += last.Text
			_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": last.Text}))
		}
		if last.Err != nil {
			out.Error = "工具调用解析失败: " + last.Err.Error()
			out.Finish = "error"
			out.ToolParseErr = true
			if out.ToolEventID != "" {
				_ = sse.WriteEvent("tool.failed", mustJSON(map[string]any{"call_id": out.ToolEventID, "error": out.Error}))
			}
		}
	}

	out.DurationMs = int(time.Since(startTime).Milliseconds())
	if !firstToken.IsZero() {
		out.TTFTMs = int(firstToken.Sub(startTime).Milliseconds())
	}
	if finalUsage != nil {
		out.Usage = &store.Usage{
			PromptTokens:     finalUsage.PromptTokens,
			CompletionTokens: finalUsage.CompletionTokens,
			CachedTokens:     finalUsage.CachedTokens,
			ReasoningTokens:  finalUsage.ReasoningTokens,
		}
	}
	if out.Finish == "" && out.Error == "" {
		out.Finish = "stop"
	}
	return out
}

func (h *ChatHandler) persistCallBody(sessionID, turnID, modelCallID string, out callOutcome) {
	_ = h.store.AppendThinking(sessionID, turnID, modelCallID, out.Reasoning)
	_ = h.store.AppendAssistant(sessionID, turnID, modelCallID, out.Content)
}

func (h *ChatHandler) handleTool(sse *SSEWriter, sessionID, turnID, modelCallID string, out callOutcome) error {
	call := out.Tool
	toolID := out.ToolEventID
	if toolID == "" {
		toolID = "tc_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	if err := h.store.BeginToolCall(toolID, sessionID, turnID, modelCallID, call.Name, call.Raw, call.Body); err != nil {
		return err
	}
	if err := h.store.AppendToolCallRecord(sessionID, turnID, modelCallID, toolID, call.Name, call.Raw); err != nil {
		return err
	}

	status, output, toolErr := executeTool(call)
	if err := h.store.FinishToolCall(toolID, status, output, toolErr); err != nil {
		return err
	}
	if err := h.store.AppendToolOutputRecord(sessionID, turnID, modelCallID, toolID, output); err != nil {
		return err
	}

	if status == "completed" {
		_ = sse.WriteEvent("tool.completed", mustJSON(map[string]any{
			"call_id": toolID, "name": call.Name, "output": output,
		}))
	} else {
		_ = sse.WriteEvent("tool.failed", mustJSON(map[string]any{
			"call_id": toolID, "name": call.Name, "output": output, "error": toolErr,
		}))
	}
	return nil
}

func executeTool(call *toolcall.Call) (status, output, message string) {
	switch call.Name {
	case "Time":
		if strings.TrimSpace(call.Body) != "" {
			message = "Time 不接受参数"
			return "failed", "ToolError: " + message, message
		}
		return "completed", time.Now().Format("2006-1-2 15:04"), ""
	default:
		message = fmt.Sprintf("unknown tool %q", call.Name)
		return "failed", "ToolError: " + message, message
	}
}

func buildSystemPrompt(userPrompt string) string {
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt == "" {
		userPrompt = config.DefaultSystemPrompt
	}
	return fmt.Sprintf(`<System>
%s

你可以在输出时使用 <tc:ToolName /> XML格式来调用工具。
目前可用：
<tc:Time />

一次模型响应最多调用一个工具。
工具调用代表当前模型响应结束。
</System>`, userPrompt)
}

func (h *ChatHandler) finishCancelled(sse *SSEWriter, sessionID, turnID, modelCallID string) {
	_ = h.store.CompleteTurn(turnID, "cancelled")
	_ = h.store.Touch(sessionID)
	if modelCallID != "" {
		_ = sse.WriteEvent("model.done", mustJSON(map[string]any{"call_id": modelCallID, "finish": "aborted", "error": "生成已停止"}))
	}
	_ = sse.WriteEvent("done", mustJSON(map[string]any{"finish": "aborted", "turn_id": turnID, "error": "生成已停止"}))
}

func (h *ChatHandler) finishTurnError(sse *SSEWriter, sessionID, turnID, modelCallID, message string) {
	_ = h.store.CompleteTurn(turnID, "failed")
	_ = h.store.Touch(sessionID)
	_ = sse.WriteEvent("error", mustJSON(map[string]any{"call_id": modelCallID, "message": message}))
	_ = sse.WriteEvent("done", mustJSON(map[string]any{"finish": "error", "turn_id": turnID, "error": message}))
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}
