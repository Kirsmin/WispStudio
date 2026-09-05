package api

import (
	"context"
	"encoding/json"
	"errors"
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

type activeRun struct {
	cancel    context.CancelFunc
	startedAt time.Time
}

type ChatHandler struct {
	cfg          *config.Config
	sessionStore *store.SessionStore
	messageStore *store.MessageStore
	requestStore *store.RequestStore
	openaiClient *openai.Client

	modelsMu sync.RWMutex
	models   []provider.ModelInfo
	runsMu   sync.Mutex
	runs     map[string]activeRun
}

func NewChatHandler(cfg *config.Config, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore, models []provider.ModelInfo) *ChatHandler {
	return &ChatHandler{
		cfg: cfg, sessionStore: ss, messageStore: ms, requestStore: rs,
		openaiClient: openai.NewClient(&cfg.OpenAI), models: append([]provider.ModelInfo(nil), models...),
		runs: make(map[string]activeRun),
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

func (h *ChatHandler) beginRun(sessionID string) (context.Context, bool) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if _, exists := h.runs[sessionID]; exists {
		return nil, false
	}
	base := context.Background()
	var ctx context.Context
	var cancel context.CancelFunc
	if h.cfg.OpenAI.MaxGenerationSec > 0 {
		ctx, cancel = context.WithTimeout(base, time.Duration(h.cfg.OpenAI.MaxGenerationSec)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(base)
	}
	h.runs[sessionID] = activeRun{cancel: cancel, startedAt: time.Now().UTC()}
	return ctx, true
}

func (h *ChatHandler) endRun(sessionID string) {
	h.runsMu.Lock()
	if run, ok := h.runs[sessionID]; ok {
		run.cancel()
		delete(h.runs, sessionID)
	}
	h.runsMu.Unlock()
}

func (h *ChatHandler) CancelRun(sessionID string) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	run, ok := h.runs[sessionID]
	if !ok {
		return false
	}
	run.cancel()
	return true
}

func (h *ChatHandler) RunStatus(sessionID string) (bool, time.Time) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	run, ok := h.runs[sessionID]
	if !ok {
		return false, time.Time{}
	}
	return true, run.startedAt
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
	if _, err := h.sessionStore.Get(sessionID); err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	req.Model = strings.TrimSpace(req.Model)
	if req.Message == "" {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "未选择模型", http.StatusBadRequest)
		return
	}
	modelInfo := h.findModel(req.Model)
	if modelInfo == nil {
		http.Error(w, "模型不存在或模型列表已变化，请刷新模型后重试", http.StatusBadRequest)
		return
	}

	history, err := h.messageStore.List(sessionID)
	if err != nil {
		http.Error(w, "读取历史失败", http.StatusInternalServerError)
		return
	}
	messages := make([]openai.ChatMessage, 0, len(history)+1)
	for _, msg := range history {
		role := "user"
		if msg.Type == store.MessageTypeAssistant {
			// 没有最终正文的半截 reasoning 不作为 assistant 正文回灌，避免污染下一轮上下文。
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			role = "assistant"
		}
		messages = append(messages, openai.ChatMessage{Role: role, Content: msg.Content})
	}
	messages = append(messages, openai.ChatMessage{Role: "user", Content: req.Message})

	baseURL, apiKey := h.cfg.OpenAI.BaseURL, h.cfg.OpenAI.APIKey
	if modelInfo.BaseURL != "" {
		baseURL = modelInfo.BaseURL
	}
	if modelInfo.APIKey != "" {
		apiKey = modelInfo.APIKey
	}
	upReq, err := h.openaiClient.BuildRequest(baseURL, apiKey, req.Model, modelInfo.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		http.Error(w, "构造上游请求失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	runCtx, ok := h.beginRun(sessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "该会话有任务正在执行")
		return
	}
	defer h.endRun(sessionID)

	sse, ok := NewSSEWriter(w)
	if !ok {
		http.Error(w, "SSE 不支持", http.StatusInternalServerError)
		return
	}
	clientWritable := true
	write := func(event string, value any) {
		if !clientWritable {
			return
		}
		if err := sse.WriteJSON(event, value); err != nil {
			// 客户端刷新/切会话后仅停止推送，不取消上游生成。
			clientWritable = false
		}
	}
	write("start", map[string]any{"session_id": sessionID, "model": req.Model})

	raw := ""
	if upReq.GetBody != nil {
		if rc, getErr := upReq.GetBody(); getErr == nil {
			if b, readErr := io.ReadAll(rc); readErr == nil {
				raw = string(b)
			}
			_ = rc.Close()
		}
	}
	_ = h.requestStore.WriteRequest(sessionID, http.MethodPost, upReq.URL.String(), raw)

	// 上游先成功建立流，再落用户消息；这样 401/429/网关错误不会留下“已保存但实际没发出”的重复消息。
	resp, err := h.openaiClient.DoStream(runCtx, upReq)
	if err != nil {
		_ = h.requestStore.WriteError(sessionID, err.Error())
		write("error", map[string]any{"message": err.Error(), "retryable": true, "persisted": false})
		write("done", map[string]any{"finish": "error", "persisted": false})
		return
	}
	defer resp.Body.Close()

	userMsg, err := h.messageStore.Append(sessionID, store.Message{
		Type: store.MessageTypeUser, Content: req.Message, Model: req.Model, Thinking: req.Thinking,
	})
	if err != nil {
		_ = resp.Body.Close()
		write("error", map[string]any{"message": "保存用户消息失败", "retryable": true, "persisted": false})
		write("done", map[string]any{"finish": "error", "persisted": false})
		return
	}
	write("ack", map[string]any{"message": userMsg})
	_ = h.sessionStore.UpdateModel(sessionID, req.Model)
	if len(history) == 0 {
		if sess, getErr := h.sessionStore.Get(sessionID); getErr == nil && !sess.Renamed {
			_ = h.sessionStore.SetAutoTitle(sessionID, store.GenerateTitle(req.Message))
		}
	}

	reader := openai.NewStreamReader(resp.Body)
	ch := make(chan openai.StreamEvent, 32)
	go reader.ReadEvents(ch)
	startTime := time.Now()
	var firstToken time.Time
	var fullContent, fullReasoning string
	var finalUsage *openai.Usage
	finishReason := ""
	streamError := ""
	for evt := range ch {
		switch evt.Type {
		case "delta":
			if firstToken.IsZero() {
				firstToken = time.Now()
				write("ttft", map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()})
			}
			fullContent += evt.Text
			write("delta", map[string]any{"text": evt.Text})
		case "reasoning":
			if firstToken.IsZero() {
				firstToken = time.Now()
				write("ttft", map[string]any{"ms": firstToken.Sub(startTime).Milliseconds()})
			}
			fullReasoning += evt.Text
			write("reasoning", map[string]any{"text": evt.Text})
		case "usage":
			finalUsage = evt.Usage
			write("usage", evt.Usage)
		case "error":
			streamError = evt.Error
			write("error", map[string]any{"message": evt.Error, "retryable": false, "persisted": true})
		case "done":
			finishReason = evt.Finish
		}
	}

	if errors.Is(runCtx.Err(), context.Canceled) {
		finishReason = "aborted"
		streamError = "生成已停止"
	} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		finishReason = "timeout"
		streamError = "生成超过服务端最长时限"
	} else if finishReason == "" {
		finishReason = "stop"
	}
	if finishReason == "error" && streamError == "" {
		streamError = "上游流式响应解析失败"
	}
	if strings.TrimSpace(fullContent) == "" && strings.TrimSpace(fullReasoning) == "" && streamError == "" {
		finishReason = "error"
		streamError = "上游结束了请求，但没有返回可展示的正文或推理内容"
	}

	duration := int(time.Since(startTime).Milliseconds())
	ttft := 0
	if !firstToken.IsZero() {
		ttft = int(firstToken.Sub(startTime).Milliseconds())
	}
	var usage *store.Usage
	if finalUsage != nil {
		usage = &store.Usage{
			PromptTokens: finalUsage.PromptTokens, CompletionTokens: finalUsage.CompletionTokens,
			CachedTokens: finalUsage.CachedTokens, ReasoningTokens: finalUsage.ReasoningTokens,
		}
	}
	assistantMsg, saveErr := h.messageStore.Append(sessionID, store.Message{
		Type: store.MessageTypeAssistant, Content: fullContent, Reasoning: fullReasoning,
		Model: req.Model, Thinking: req.Thinking, Usage: usage, DurationMs: duration,
		TTFTMs: ttft, Finish: finishReason, Error: streamError,
	})
	if saveErr != nil {
		streamError = "保存助手消息失败: " + saveErr.Error()
		finishReason = "error"
		_ = h.requestStore.WriteError(sessionID, streamError)
	} else {
		_ = h.sessionStore.Touch(sessionID)
	}

	usageLog := map[string]int{}
	if usage != nil {
		usageLog["prompt_tokens"] = usage.PromptTokens
		usageLog["completion_tokens"] = usage.CompletionTokens
		usageLog["cached_tokens"] = usage.CachedTokens
		usageLog["reasoning_tokens"] = usage.ReasoningTokens
	}
	switch finishReason {
	case "aborted":
		_ = h.requestStore.WriteAborted(sessionID)
	case "error", "timeout":
		_ = h.requestStore.WriteError(sessionID, streamError)
	default:
		_ = h.requestStore.WriteDone(sessionID, usageLog, finishReason)
	}
	write("done", map[string]any{
		"finish": finishReason, "duration_ms": duration, "ttft_ms": ttft,
		"message_id": assistantMsg.ID, "error": streamError, "persisted": saveErr == nil,
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func upstreamMessage(err error) string {
	var up *openai.UpstreamError
	if errors.As(err, &up) {
		return up.Message
	}
	return fmt.Sprint(err)
}
