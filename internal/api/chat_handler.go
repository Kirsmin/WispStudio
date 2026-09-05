package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/provider"
	"wisp/internal/store"
)

const streamLockShards = 64

type ChatHandler struct {
	cfg          *config.Config
	catalog      *provider.Catalog
	sessionStore *store.SessionStore
	messageStore *store.MessageStore
	requestStore *store.RequestStore
	openaiClient *openai.Client
	streamLocks  [streamLockShards]sync.Mutex
}

func NewChatHandler(cfg *config.Config, catalog *provider.Catalog, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore) *ChatHandler {
	return &ChatHandler{
		cfg:          cfg,
		catalog:      catalog,
		sessionStore: ss,
		messageStore: ms,
		requestStore: rs,
		openaiClient: openai.NewClient(&cfg.OpenAI),
	}
}

func (h *ChatHandler) getStreamLock(sessionID string) *sync.Mutex {
	var h1 uint32 = 2166136261
	for i := 0; i < len(sessionID); i++ {
		h1 ^= uint32(sessionID[i])
		h1 *= 16777619
	}
	return &h.streamLocks[h1%streamLockShards]
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
		http.Error(w, "缺少会话ID", http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}

	lock := h.getStreamLock(sessionID)
	if !lock.TryLock() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "该会话有任务正在执行"})
		return
	}
	defer lock.Unlock()

	sess, err := h.sessionStore.Get(sessionID)
	if err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}

	// Provider 与 Model 必须成对解析。旧版前端只传 model，之前这里却把
	// providerID 固定传成空字符串，而 Catalog.Resolve 又按 provider 精确匹配，
	// 结果任何模型都会被误判成“不存在”。
	//
	// 新前端会显式传 provider；旧前端则优先沿用会话保存的 Provider，仍解析
	// 不到时再按模型 ID 做兼容查找。
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

	// 后续统一使用目录实际解析出的 Provider / Model，避免旧会话中的选择残留。
	req.Provider = provCfg.ID
	req.Model = modelInfo.ID
	_ = h.sessionStore.UpdateSelection(sessionID, req.Provider, req.Model)
	sess.Provider = req.Provider
	sess.Model = req.Model

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

	userMsg := store.Message{
		Type:     store.MessageTypeUser,
		Content:  req.Message,
		Model:    req.Model,
		Thinking: req.Thinking,
	}
	if err := h.messageStore.Append(sessionID, userMsg); err != nil {
		http.Error(w, "保存消息失败", http.StatusInternalServerError)
		return
	}

	if !sess.Renamed && len(history) == 0 {
		_ = h.sessionStore.UpdateTitle(sessionID, store.GenerateTitle(req.Message))
	}

	baseURL, apiKey := h.cfg.OpenAI.BaseURL, h.cfg.OpenAI.APIKey
	if provCfg.BaseURL != "" {
		baseURL = provCfg.BaseURL
	}
	if provCfg.APIKey != "" {
		apiKey = provCfg.APIKey
	}

	upReq, err := h.openaiClient.BuildRequest(baseURL, apiKey, req.Model, modelInfo.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		http.Error(w, "构造请求失败", http.StatusInternalServerError)
		return
	}

	raw := ""
	if upReq.GetBody != nil {
		if rc, rerr := upReq.GetBody(); rerr == nil {
			if b, rerr2 := io.ReadAll(rc); rerr2 == nil {
				raw = string(b)
			}
			_ = rc.Close()
		}
	}
	h.requestStore.WriteRequest(sessionID, http.MethodPost, upReq.URL.String(), raw)

	// 先建立并 flush 浏览器 SSE，再等待上游响应。
	// 这样前端可以立即创建 assistant 占位，而不是等模型首包到了才看到“等待响应”。
	sse, ok := NewSSEWriter(w)
	if !ok {
		http.Error(w, "SSE 不支持", http.StatusInternalServerError)
		return
	}
	_ = sse.WriteEvent("start", `{"phase":"waiting"}`)

	startTime := time.Now()
	ctx := r.Context()
	resp, err := h.openaiClient.DoStream(ctx, upReq)
	if err != nil {
		duration := int(time.Since(startTime).Milliseconds())
		message := err.Error()
		h.requestStore.WriteError(sessionID, message)
		_ = h.messageStore.Append(sessionID, store.Message{
			Type:       store.MessageTypeAssistant,
			Model:      req.Model,
			Thinking:   req.Thinking,
			DurationMs: duration,
			Finish:     "error",
			Error:      message,
		})
		_ = h.sessionStore.Touch(sessionID)
		_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": message}))
		_ = sse.WriteEvent("done", mustJSON(map[string]any{"finish": "error", "duration_ms": duration}))
		return
	}
	defer resp.Body.Close()

	reader := openai.NewStreamReader(resp.Body)
	ch := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(ch)

	var fullContent string
	var fullReasoning string
	var finalUsage *openai.Usage
	var finishReason string
	var streamError string
	var firstToken time.Time

	for evt := range ch {
		switch evt.Type {
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()}))
			}
			fullContent += evt.Text
			_ = sse.WriteEvent("delta", mustJSON(map[string]any{"text": evt.Text}))
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				_ = sse.WriteEvent("ttft", mustJSON(map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()}))
			}
			fullReasoning += evt.Text
			_ = sse.WriteEvent("reasoning", mustJSON(map[string]any{"text": evt.Text}))
		case "usage":
			finalUsage = evt.Usage
			_ = sse.WriteEvent("usage", mustJSON(evt.Usage))
		case "done":
			if finishReason == "" {
				finishReason = evt.Finish
			}
		case "error":
			streamError = evt.Error
			finishReason = "error"
			_ = sse.WriteEvent("error", mustJSON(map[string]any{"message": evt.Error}))
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

	duration := int(time.Since(startTime).Milliseconds())
	ttft := 0
	if !firstToken.IsZero() {
		ttft = int(firstToken.Sub(startTime).Milliseconds())
	}

	var usage *store.Usage
	if finalUsage != nil {
		usage = &store.Usage{
			PromptTokens:     finalUsage.PromptTokens,
			CompletionTokens: finalUsage.CompletionTokens,
			CachedTokens:     finalUsage.CachedTokens,
			ReasoningTokens:  finalUsage.ReasoningTokens,
		}
	}

	assistantMsg := store.Message{
		Type:       store.MessageTypeAssistant,
		Content:    fullContent,
		Reasoning:  fullReasoning,
		Model:      req.Model,
		Thinking:   req.Thinking,
		Usage:      usage,
		DurationMs: duration,
		TTFTMs:     ttft,
		Finish:     finishReason,
		Error:      streamError,
	}
	if err := h.messageStore.Append(sessionID, assistantMsg); err != nil && streamError == "" {
		streamError = "回复保存失败: " + err.Error()
	}
	_ = h.sessionStore.Touch(sessionID)

	if finishReason == "aborted" {
		h.requestStore.WriteAborted(sessionID)
	} else if finishReason == "error" {
		h.requestStore.WriteError(sessionID, streamError)
	} else {
		u := map[string]int{}
		if usage != nil {
			u["prompt_tokens"] = usage.PromptTokens
			u["completion_tokens"] = usage.CompletionTokens
			u["cached_tokens"] = usage.CachedTokens
			u["reasoning_tokens"] = usage.ReasoningTokens
		}
		h.requestStore.WriteDone(sessionID, u, finishReason)
	}

	payload := map[string]any{
		"finish":      finishReason,
		"duration_ms": duration,
		"ttft_ms":     ttft,
	}
	if streamError != "" {
		payload["error"] = streamError
	}
	_ = sse.WriteEvent("done", mustJSON(payload))
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}
