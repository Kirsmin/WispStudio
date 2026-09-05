package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type ChatHandler struct {
	cfg          *config.Config
	sessionStore *store.SessionStore
	messageStore *store.MessageStore
	requestStore *store.RequestStore
	openaiClient *openai.Client
	modelsMu     sync.RWMutex
	models       []provider.ModelInfo
	runsMu       sync.Mutex
	runs         map[string]context.CancelFunc
}

func NewChatHandler(cfg *config.Config, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore, models []provider.ModelInfo) *ChatHandler {
	return &ChatHandler{cfg: cfg, sessionStore: ss, messageStore: ms, requestStore: rs, openaiClient: openai.NewClient(&cfg.OpenAI), models: append([]provider.ModelInfo(nil), models...), runs: map[string]context.CancelFunc{}}
}

func (h *ChatHandler) SetModels(models []provider.ModelInfo) {
	h.modelsMu.Lock()
	defer h.modelsMu.Unlock()
	h.models = append([]provider.ModelInfo(nil), models...)
}
func (h *ChatHandler) findModel(id string) *provider.ModelInfo {
	h.modelsMu.RLock()
	defer h.modelsMu.RUnlock()
	return provider.FindModel(h.models, id)
}
func (h *ChatHandler) beginRun(sessionID string) (context.Context, bool) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if _, exists := h.runs[sessionID]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.runs[sessionID] = cancel
	return ctx, true
}
func (h *ChatHandler) endRun(sessionID string) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	delete(h.runs, sessionID)
}
func (h *ChatHandler) IsRunning(sessionID string) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	_, ok := h.runs[sessionID]
	return ok
}
func (h *ChatHandler) Cancel(sessionID string) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	cancel, ok := h.runs[sessionID]
	if ok {
		cancel()
	}
	return ok
}

type chatRequest struct {
	Message  string `json:"message"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
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
	if req.Message == "" {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}
	model := h.findModel(req.Model)
	if model == nil {
		http.Error(w, "模型不存在或模型列表已变化，请刷新模型列表", http.StatusBadRequest)
		return
	}
	if err := provider.ValidateThinking(*model, req.Thinking); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := h.sessionStore.Get(sessionID); err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	ctx, ok := h.beginRun(sessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "该会话有任务正在执行")
		return
	}
	defer h.endRun(sessionID)

	history, err := h.messageStore.List(sessionID)
	if err != nil {
		http.Error(w, "读取历史失败", http.StatusInternalServerError)
		return
	}
	messages := make([]openai.ChatMessage, 0, len(history)+1)
	for _, msg := range history {
		role := "user"
		if msg.Type == store.MessageTypeAssistant {
			role = "assistant"
		}
		messages = append(messages, openai.ChatMessage{Role: role, Content: msg.Content})
	}
	messages = append(messages, openai.ChatMessage{Role: "user", Content: req.Message})

	userMessage, err := h.messageStore.Append(sessionID, store.Message{Type: store.MessageTypeUser, Content: req.Message, Model: req.Model, Thinking: req.Thinking})
	if err != nil {
		http.Error(w, "保存消息失败", http.StatusInternalServerError)
		return
	}
	_ = h.sessionStore.UpdateModel(sessionID, req.Model)
	if len(history) == 0 {
		_ = h.sessionStore.UpdateAutoTitle(sessionID, store.GenerateTitle(req.Message))
	}

	sse, ok := NewSSEWriter(w)
	if !ok {
		return
	}
	ack, _ := json.Marshal(map[string]any{"message": userMessage})
	_ = sse.WriteEvent("ack", string(ack))

	upReq, err := h.openaiClient.BuildRequest(model.BaseURL, model.APIKey, req.Model, model.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		h.finishWithError(sessionID, req, sse, "构造上游请求失败: "+err.Error(), 0, 0)
		return
	}
	if upReq.GetBody != nil {
		if rc, getErr := upReq.GetBody(); getErr == nil {
			if raw, readErr := io.ReadAll(rc); readErr == nil {
				h.requestStore.WriteRequest(sessionID, http.MethodPost, upReq.URL.String(), string(raw))
			}
			_ = rc.Close()
		}
	}

	start := time.Now()
	resp, err := h.openaiClient.DoStream(ctx, upReq)
	if err != nil {
		h.requestStore.WriteError(sessionID, err.Error())
		h.finishWithError(sessionID, req, sse, err.Error(), int(time.Since(start).Milliseconds()), 0)
		return
	}
	defer resp.Body.Close()

	reader := openai.NewStreamReader(resp.Body)
	events := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(events)
	var fullContent, fullReasoning, finishReason, streamError string
	var finalUsage *openai.Usage
	var firstToken time.Time
	for event := range events {
		switch event.Type {
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", fmt.Sprintf(`{"ms":%d}`, firstToken.Sub(start).Milliseconds()))
			}
			fullContent += event.Text
			_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": event.Text}))
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", fmt.Sprintf(`{"ms":%d}`, firstToken.Sub(start).Milliseconds()))
			}
			fullReasoning += event.Text
			_ = sse.WriteEvent("reasoning", mustJSON(map[string]any{"text": event.Text}))
		case "usage":
			finalUsage = event.Usage
			_ = sse.WriteEvent("usage", mustJSON(event.Usage))
		case "error":
			streamError = event.Error
			finishReason = "error"
			_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": event.Error}))
		case "done":
			if finishReason == "" {
				finishReason = event.Finish
			}
		}
	}
	if ctx.Err() != nil {
		finishReason = "aborted"
		if streamError == "" {
			streamError = "生成已停止"
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	duration := int(time.Since(start).Milliseconds())
	ttft := 0
	if !firstToken.IsZero() {
		ttft = int(firstToken.Sub(start).Milliseconds())
	}
	usage := convertUsage(finalUsage)
	assistant, appendErr := h.messageStore.Append(sessionID, store.Message{Type: store.MessageTypeAssistant, Content: fullContent, Reasoning: fullReasoning, Model: req.Model, Thinking: req.Thinking, Usage: usage, DurationMs: duration, TTFTMs: ttft, Finish: finishReason, Error: streamError})
	persisted := appendErr == nil
	_ = h.sessionStore.Touch(sessionID)
	if finishReason == "aborted" {
		h.requestStore.WriteAborted(sessionID)
	} else if finishReason == "error" {
		h.requestStore.WriteError(sessionID, streamError)
	} else {
		h.requestStore.WriteDone(sessionID, usageMap(usage), finishReason)
	}
	payload := map[string]any{"finish": finishReason, "duration_ms": duration, "ttft_ms": ttft, "persisted": persisted}
	if assistant != nil {
		payload["message_id"] = assistant.ID
	}
	if appendErr != nil {
		payload["error"] = "回复落盘失败: " + appendErr.Error()
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func (h *ChatHandler) finishWithError(sessionID string, req chatRequest, sse *SSEWriter, message string, duration, ttft int) {
	assistant, err := h.messageStore.Append(sessionID, store.Message{Type: store.MessageTypeAssistant, Model: req.Model, Thinking: req.Thinking, DurationMs: duration, TTFTMs: ttft, Finish: "error", Error: message})
	_ = h.sessionStore.Touch(sessionID)
	_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": message}))
	payload := map[string]any{"finish": "error", "duration_ms": duration, "ttft_ms": ttft, "persisted": err == nil, "error": message}
	if assistant != nil {
		payload["message_id"] = assistant.ID
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func convertUsage(u *openai.Usage) *store.Usage {
	if u == nil {
		return nil
	}
	return &store.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, CachedTokens: u.CachedTokens, ReasoningTokens: u.ReasoningTokens}
}
func usageMap(u *store.Usage) map[string]int {
	if u == nil {
		return map[string]int{}
	}
	return map[string]int{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens, "cached_tokens": u.CachedTokens, "reasoning_tokens": u.ReasoningTokens}
}
func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
