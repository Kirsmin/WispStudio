package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type ChatHandler struct {
	cfg          *config.Config
	sessions     *store.SessionStore
	messages     *store.MessageStore
	requests     *store.RequestStore
	upstream     *openai.Client
	modelsMu     sync.RWMutex
	models       []provider.ModelInfo
	runsMu       sync.Mutex
	activeCancel map[string]context.CancelFunc
}

type chatRequest struct {
	Message  string `json:"message"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

func NewChatHandler(cfg *config.Config, sessions *store.SessionStore, messages *store.MessageStore, requests *store.RequestStore, models []provider.ModelInfo) *ChatHandler {
	return &ChatHandler{
		cfg: cfg, sessions: sessions, messages: messages, requests: requests,
		upstream: openai.NewClient(&cfg.OpenAI), models: append([]provider.ModelInfo(nil), models...),
		activeCancel: map[string]context.CancelFunc{},
	}
}

func (h *ChatHandler) SetModels(models []provider.ModelInfo) {
	h.modelsMu.Lock()
	h.models = append([]provider.ModelInfo(nil), models...)
	h.modelsMu.Unlock()
}

func (h *ChatHandler) findModel(id string) *provider.ModelInfo {
	h.modelsMu.RLock()
	defer h.modelsMu.RUnlock()
	return provider.FindModel(h.models, id)
}

func (h *ChatHandler) IsRunning(sessionID string) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	_, ok := h.activeCancel[sessionID]
	return ok
}

func (h *ChatHandler) Cancel(sessionID string) bool {
	h.runsMu.Lock()
	cancel, ok := h.activeCancel[sessionID]
	h.runsMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (h *ChatHandler) beginRun(sessionID string) (context.Context, bool) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if _, exists := h.activeCancel[sessionID]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.activeCancel[sessionID] = cancel
	return ctx, true
}

func (h *ChatHandler) endRun(sessionID string) {
	h.runsMu.Lock()
	delete(h.activeCancel, sessionID)
	h.runsMu.Unlock()
}

func (h *ChatHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSONError(w, 400, "缺少会话ID")
		return
	}
	if _, err := h.sessions.Get(sessionID); err != nil {
		writeJSONError(w, 404, "会话不存在")
		return
	}

	var input chatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&input); err != nil {
		writeJSONError(w, 400, "请求体解析失败")
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		writeJSONError(w, 400, "消息不能为空")
		return
	}
	model := h.findModel(input.Model)
	if model == nil {
		writeJSONError(w, 400, "模型不存在或模型列表已变化，请刷新后重试")
		return
	}
	if input.Thinking == "" {
		input.Thinking = "off"
		if len(model.ThinkingLevels) > 0 && model.ThinkingLevels[0] != "off" {
			input.Thinking = model.ThinkingLevels[0]
		}
	}
	if err := provider.ValidateThinking(*model, input.Thinking); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}

	ctx, ok := h.beginRun(sessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "该会话有任务正在执行")
		return
	}
	defer h.endRun(sessionID)

	history, err := h.messages.List(sessionID)
	if err != nil {
		writeJSONError(w, 500, "读取历史失败: "+err.Error())
		return
	}
	upstreamMessages := make([]openai.ChatMessage, 0, len(history)+1)
	for _, message := range history {
		role := "user"
		if message.Type == store.MessageTypeAssistant {
			role = "assistant"
		}
		// 出错且正文为空的 assistant 不注入上下文，避免把错误文本污染下一轮。
		if role == "assistant" && message.Content == "" {
			continue
		}
		upstreamMessages = append(upstreamMessages, openai.ChatMessage{Role: role, Content: message.Content})
	}
	upstreamMessages = append(upstreamMessages, openai.ChatMessage{Role: "user", Content: input.Message})

	userMessage, err := h.messages.Append(sessionID, store.Message{
		Type: store.MessageTypeUser, Content: input.Message, Model: input.Model, Thinking: input.Thinking,
	})
	if err != nil {
		writeJSONError(w, 500, "保存用户消息失败: "+err.Error())
		return
	}
	_ = h.sessions.UpdateModel(sessionID, input.Model)
	if len(history) == 0 {
		_ = h.sessions.UpdateAutoTitle(sessionID, store.GenerateTitle(input.Message))
	}

	sse, ok := NewSSEWriter(w)
	if !ok {
		return
	}
	_ = sse.WriteEvent("ack", mustJSON(map[string]any{"message": userMessage}))

	upReq, rawBody, err := h.upstream.BuildRequest(model.BaseURL, model.APIKey, input.Model, model.ThinkingStyle, input.Thinking, upstreamMessages)
	if err != nil {
		h.persistError(sessionID, input, sse, "构造上游请求失败: "+err.Error(), 0, 0)
		return
	}
	h.requests.WriteRequest(sessionID, http.MethodPost, upReq.URL.String(), string(rawBody))

	started := time.Now()
	resp, err := h.upstream.DoStream(ctx, upReq)
	if err != nil {
		duration := int(time.Since(started).Milliseconds())
		h.requests.WriteError(sessionID, err.Error())
		h.persistError(sessionID, input, sse, err.Error(), duration, 0)
		return
	}
	defer resp.Body.Close()

	stream := openai.NewStreamReader(resp.Body)
	events := make(chan openai.StreamEvent, 64)
	go stream.ReadEvents(events)

	var content strings.Builder
	var reasoning strings.Builder
	var usage *openai.Usage
	finish := ""
	streamError := ""
	var firstToken time.Time

	for event := range events {
		switch event.Type {
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(started).Milliseconds()}))
			}
			content.WriteString(event.Text)
			_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": event.Text}))
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(started).Milliseconds()}))
			}
			reasoning.WriteString(event.Text)
			_ = sse.WriteEvent("reasoning", mustJSON(map[string]any{"text": event.Text}))
		case "usage":
			usage = event.Usage
			_ = sse.WriteEvent("usage", mustJSON(event.Usage))
		case "error":
			streamError = event.Error
			finish = "error"
			_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": event.Error}))
		case "done":
			if finish == "" {
				finish = event.Finish
			}
		}
	}

	if ctx.Err() != nil {
		// 用户主动停止时，底层 HTTP/Scanner 往往先返回 context canceled。
		// 对外与持久化统一为可读状态，不泄露实现层错误。
		finish = "aborted"
		streamError = "生成已停止"
	}
	if finish == "" {
		finish = "stop"
	}
	durationMs := int(time.Since(started).Milliseconds())
	ttftMs := 0
	if !firstToken.IsZero() {
		ttftMs = int(firstToken.Sub(started).Milliseconds())
	}
	storedUsage := convertUsage(usage)

	assistant, appendErr := h.messages.Append(sessionID, store.Message{
		Type: store.MessageTypeAssistant, Content: content.String(), Reasoning: reasoning.String(),
		Model: input.Model, Thinking: input.Thinking, Usage: storedUsage, DurationMs: durationMs,
		TTFTMs: ttftMs, Finish: finish, Error: streamError,
	})
	_ = h.sessions.Touch(sessionID)
	if finish == "aborted" {
		h.requests.WriteAborted(sessionID)
	} else if finish == "error" {
		h.requests.WriteError(sessionID, streamError)
	} else {
		h.requests.WriteDone(sessionID, finish, usageMap(storedUsage))
	}

	payload := map[string]any{
		"finish": finish, "duration_ms": durationMs, "ttft_ms": ttftMs, "persisted": appendErr == nil,
	}
	if assistant != nil {
		payload["message_id"] = assistant.ID
	}
	if appendErr != nil {
		payload["error"] = "回复落盘失败: " + appendErr.Error()
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func (h *ChatHandler) persistError(sessionID string, input chatRequest, sse *SSEWriter, message string, durationMs, ttftMs int) {
	assistant, err := h.messages.Append(sessionID, store.Message{
		Type: store.MessageTypeAssistant, Model: input.Model, Thinking: input.Thinking,
		Finish: "error", Error: message, DurationMs: durationMs, TTFTMs: ttftMs,
	})
	_ = h.sessions.Touch(sessionID)
	_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": message}))
	payload := map[string]any{"finish": "error", "duration_ms": durationMs, "ttft_ms": ttftMs, "persisted": err == nil, "error": message}
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
func mustJSON(v any) string { data, _ := json.Marshal(v); return string(data) }
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
