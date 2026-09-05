package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

type ChatHandler struct {
	cfg          *config.Config
	catalog      *provider.Catalog
	runs         *RunRegistry
	sessionStore *store.SessionStore
	messageStore *store.MessageStore
	requestStore *store.RequestStore
}

func NewChatHandler(cfg *config.Config, catalog *provider.Catalog, runs *RunRegistry, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore) *ChatHandler {
	return &ChatHandler{
		cfg:          cfg,
		catalog:      catalog,
		runs:         runs,
		sessionStore: ss,
		messageStore: ms,
		requestStore: rs,
	}
}

type chatRequest struct {
	Message  string `json:"message"`
	Provider string `json:"provider"`
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
		writeJSONError(w, http.StatusBadRequest, "缺少会话ID")
		return
	}
	if _, err := h.sessionStore.Get(sessionID); err != nil {
		writeJSONError(w, http.StatusNotFound, "会话不存在")
		return
	}

	var req chatRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Thinking = strings.ToLower(strings.TrimSpace(req.Thinking))
	if req.Message == "" {
		writeJSONError(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if req.Provider == "" || req.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "请选择 Provider 和模型")
		return
	}
	if req.Thinking == "" {
		req.Thinking = "off"
	}

	providerCfg, modelInfo, err := h.catalog.Resolve(r.Context(), req.Provider, req.Model)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := provider.ValidateThinking(*modelInfo, req.Thinking); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	runCtx, ok := h.runs.Begin(sessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "该会话有任务正在执行")
		return
	}
	defer h.runs.End(sessionID)

	history, err := h.messageStore.List(sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "读取历史失败")
		return
	}
	messages := make([]openai.ChatMessage, 0, len(history)+1)
	for _, message := range history {
		role := "user"
		if message.Type == store.MessageTypeAssistant {
			role = "assistant"
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
		}
		messages = append(messages, openai.ChatMessage{Role: role, Content: message.Content})
	}
	messages = append(messages, openai.ChatMessage{Role: "user", Content: req.Message})

	userMessage, err := h.messageStore.Append(sessionID, store.Message{
		Type:     store.MessageTypeUser,
		Content:  req.Message,
		Provider: req.Provider,
		Model:    req.Model,
		Thinking: req.Thinking,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "保存消息失败")
		return
	}
	_ = h.sessionStore.UpdateSelection(sessionID, req.Provider, req.Model)
	if countUserMessages(history) == 0 {
		_ = h.sessionStore.UpdateAutoTitle(sessionID, store.GenerateTitle(req.Message))
	}

	// 从这里开始使用 SSE：user 消息已经可靠落盘，因此刷新页面也不会丢。
	sse, ok := NewSSEWriter(w)
	if !ok {
		return
	}
	_ = sse.WriteEvent("ack", mustJSON(map[string]any{"message": userMessage}))

	client := openai.NewClient(providerCfg.TimeoutSec)
	upReq, err := client.BuildRequest(providerCfg.BaseURL, providerCfg.APIKey, req.Model, modelInfo.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		h.finishTerminal(sessionID, req, sse, "error", "构造上游请求失败: "+err.Error(), 0, 0)
		return
	}
	if upReq.GetBody != nil {
		if body, getErr := upReq.GetBody(); getErr == nil {
			if raw, readErr := io.ReadAll(body); readErr == nil {
				_ = h.requestStore.WriteRequest(sessionID, http.MethodPost, upReq.URL.String(), string(raw))
			}
			_ = body.Close()
		}
	}

	started := time.Now()
	resp, err := client.DoStream(runCtx, upReq)
	if err != nil {
		duration := int(time.Since(started).Milliseconds())
		if runCtx.Err() != nil {
			_ = h.requestStore.WriteAborted(sessionID)
			h.finishTerminal(sessionID, req, sse, "aborted", "生成已停止", duration, 0)
		} else {
			_ = h.requestStore.WriteError(sessionID, err.Error())
			h.finishTerminal(sessionID, req, sse, "error", err.Error(), duration, 0)
		}
		return
	}
	defer resp.Body.Close()

	reader := openai.NewStreamReader(resp.Body)
	events := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(events)

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var usage *openai.Usage
	var finishReason string
	var streamError string
	var firstToken time.Time

	for event := range events {
		switch event.Type {
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(started).Milliseconds()}))
			}
			fullContent.WriteString(event.Text)
			_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": event.Text}))
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(started).Milliseconds()}))
			}
			fullReasoning.WriteString(event.Text)
			_ = sse.WriteEvent("reasoning", mustJSON(map[string]any{"text": event.Text}))
		case "usage":
			usage = event.Usage
			_ = sse.WriteEvent("usage", mustJSON(event.Usage))
		case "error":
			if streamError == "" {
				streamError = event.Error
			}
			finishReason = "error"
			_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": event.Error}))
		case "done":
			if finishReason == "" {
				finishReason = event.Finish
			}
		}
	}

	if runCtx.Err() != nil {
		finishReason = "aborted"
		if streamError == "" {
			streamError = "生成已停止"
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	duration := int(time.Since(started).Milliseconds())
	ttft := 0
	if !firstToken.IsZero() {
		ttft = int(firstToken.Sub(started).Milliseconds())
	}

	storedUsage := convertUsage(usage)
	assistant, appendErr := h.messageStore.Append(sessionID, store.Message{
		Type:       store.MessageTypeAssistant,
		Content:    fullContent.String(),
		Reasoning:  fullReasoning.String(),
		Provider:   req.Provider,
		Model:      req.Model,
		Thinking:   req.Thinking,
		Usage:      storedUsage,
		DurationMs: duration,
		TTFTMs:     ttft,
		Finish:     finishReason,
		Error:      streamError,
	})
	_ = h.sessionStore.Touch(sessionID)

	if finishReason == "aborted" {
		_ = h.requestStore.WriteAborted(sessionID)
	} else if finishReason == "error" {
		_ = h.requestStore.WriteError(sessionID, streamError)
	} else {
		_ = h.requestStore.WriteDone(sessionID, usageMap(storedUsage), finishReason)
	}

	payload := map[string]any{
		"finish":      finishReason,
		"duration_ms": duration,
		"ttft_ms":     ttft,
		"persisted":   appendErr == nil,
	}
	if assistant != nil {
		payload["message_id"] = assistant.ID
	}
	if appendErr != nil {
		payload["error"] = "回复落盘失败: " + appendErr.Error()
	} else if streamError != "" {
		payload["error"] = streamError
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func (h *ChatHandler) finishTerminal(sessionID string, req chatRequest, sse *SSEWriter, finish, message string, duration, ttft int) {
	assistant, err := h.messageStore.Append(sessionID, store.Message{
		Type:       store.MessageTypeAssistant,
		Provider:   req.Provider,
		Model:      req.Model,
		Thinking:   req.Thinking,
		DurationMs: duration,
		TTFTMs:     ttft,
		Finish:     finish,
		Error:      message,
	})
	_ = h.sessionStore.Touch(sessionID)
	if finish == "error" {
		_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": message}))
	}
	payload := map[string]any{
		"finish":      finish,
		"duration_ms": duration,
		"ttft_ms":     ttft,
		"persisted":   err == nil,
		"error":       message,
	}
	if assistant != nil {
		payload["message_id"] = assistant.ID
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func countUserMessages(history []store.Message) int {
	count := 0
	for _, message := range history {
		if message.Type == store.MessageTypeUser {
			count++
		}
	}
	return count
}

func convertUsage(usage *openai.Usage) *store.Usage {
	if usage == nil {
		return nil
	}
	return &store.Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.CachedTokens,
		ReasoningTokens:  usage.ReasoningTokens,
	}
}

func usageMap(usage *store.Usage) map[string]int {
	if usage == nil {
		return map[string]int{}
	}
	return map[string]int{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"cached_tokens":     usage.CachedTokens,
		"reasoning_tokens":  usage.ReasoningTokens,
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}
