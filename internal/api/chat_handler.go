package api

import (
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

const streamLockShards = 64

type ChatHandler struct {
	cfg            *config.Config
	sessionStore   *store.SessionStore
	messageStore   *store.MessageStore
	requestStore   *store.RequestStore
	openaiClient   *openai.Client
	streamLocks    [streamLockShards]sync.Mutex
	cachedModels   []provider.ModelInfo
}

func NewChatHandler(cfg *config.Config, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore, cachedModels []provider.ModelInfo) *ChatHandler {
	return &ChatHandler{
		cfg:          cfg,
		sessionStore: ss,
		messageStore: ms,
		requestStore: rs,
		openaiClient: openai.NewClient(&cfg.OpenAI),
		cachedModels: cachedModels,
	}
}

// getStreamLock 按会话 ID 哈希取分片锁：同一会话始终命中同一把锁，锁数量恒定不泄漏
func (h *ChatHandler) getStreamLock(sessionID string) *sync.Mutex {
	var h1 uint32 = 2166136261 // FNV-1a
	for i := 0; i < len(sessionID); i++ {
		h1 ^= uint32(sessionID[i])
		h1 *= 16777619
	}
	return &h.streamLocks[h1%streamLockShards]
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}

	// 获取流锁
	lock := h.getStreamLock(sessionID)
	if !lock.TryLock() {
		// 冲突 409
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "该会话有任务正在执行"})
		return
	}
	defer lock.Unlock()

	// 获取会话
	sess, err := h.sessionStore.Get(sessionID)
	if err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}

	// 更新会话模型
	if req.Model != "" {
		h.sessionStore.UpdateModel(sessionID, req.Model)
		sess.Model = req.Model
	}

	// 读取历史消息
	history, err := h.messageStore.List(sessionID)
	if err != nil {
		http.Error(w, "读取历史失败", http.StatusInternalServerError)
		return
	}

	// 构造 messages
	var messages []openai.ChatMessage
	for _, msg := range history {
		role := "user"
		if msg.Type == store.MessageTypeAssistant {
			role = "assistant"
		}
		messages = append(messages, openai.ChatMessage{
			Role:    role,
			Content: msg.Content,
		})
	}
	// 追加本次 user 消息
	messages = append(messages, openai.ChatMessage{
		Role:    "user",
		Content: req.Message,
	})

	// 保存 user 消息
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

	// 生成标题（如果未重命名且是第一条 user 消息）
	if !sess.Renamed && len(history) == 0 {
		title := store.GenerateTitle(req.Message)
		h.sessionStore.UpdateTitle(sessionID, title)
	}

	// 查找模型配置（从 Provider 缓存的模型列表中查找）
	modelInfo := provider.FindModel(h.cachedModels, req.Model)
	if modelInfo == nil {
		// 向后兼容：从 cfg.Models 中查找
		for i := range h.cfg.Models {
			if h.cfg.Models[i].ID == req.Model {
				m := h.cfg.Models[i]
				modelInfo = &provider.ModelInfo{
					ID:             m.ID,
					Name:           m.Name,
					ThinkingLevels: m.ThinkingLevels,
					ThinkingStyle:  m.ThinkingStyle,
					BaseURL:        m.BaseURL,
					APIKey:         m.APIKey,
				}
				break
			}
		}
	}
	if modelInfo == nil {
		http.Error(w, "模型不存在", http.StatusBadRequest)
		return
	}

	// 解析模型生效的网关与密钥：模型可单独覆盖 base_url / api_key，否则沿用全局
	baseURL, apiKey := h.cfg.OpenAI.BaseURL, h.cfg.OpenAI.APIKey
	if modelInfo.BaseURL != "" {
		baseURL = modelInfo.BaseURL
	}
	if modelInfo.APIKey != "" {
		apiKey = modelInfo.APIKey
	}

	// 构造上游请求
	upReq, err := h.openaiClient.BuildRequest(baseURL, apiKey, req.Model, modelInfo.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		http.Error(w, "构造请求失败", http.StatusInternalServerError)
		return
	}

	// 留痕：读取将要发送的原始请求体（字节级原文），随方法/URL 一起完整落盘
	raw := ""
	if upReq.GetBody != nil {
		if rc, rerr := upReq.GetBody(); rerr == nil {
			defer rc.Close()
			if b, rerr2 := io.ReadAll(rc); rerr2 == nil {
				raw = string(b)
			}
		}
	}
	h.requestStore.WriteRequest(sessionID, "POST", upReq.URL.String(), raw)

	// 发送上游请求
	ctx := r.Context()
	resp, err := h.openaiClient.DoStream(ctx, upReq)
	if err != nil {
		h.requestStore.WriteError(sessionID, err.Error())
		http.Error(w, "上游请求失败", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 设置 SSE
	sse, ok := NewSSEWriter(w)
	if !ok {
		http.Error(w, "SSE 不支持", http.StatusInternalServerError)
		return
	}

	// 解析流
	reader := openai.NewStreamReader(resp.Body)
	ch := make(chan openai.StreamEvent, 16)
	go reader.ReadEvents(ch)

	startTime := time.Now()
	var firstTokenTime time.Time
	var fullContent string
	var fullReasoning string
	var finalUsage *openai.Usage
	var finishReason string

	for evt := range ch {
		switch evt.Type {
		case "delta":
			if firstTokenTime.IsZero() {
				firstTokenTime = time.Now()
				_ = sse.WriteEvent("ttft", fmt.Sprintf(`{"ms":%d}`, int(firstTokenTime.Sub(startTime).Milliseconds())))
			}
			fullContent += evt.Text
			_ = sse.WriteEvent("delta", fmt.Sprintf(`{"text":%q}`, evt.Text))
		case "reasoning":
			if firstTokenTime.IsZero() {
				firstTokenTime = time.Now()
				_ = sse.WriteEvent("ttft", fmt.Sprintf(`{"ms":%d}`, int(firstTokenTime.Sub(startTime).Milliseconds())))
			}
			fullReasoning += evt.Text
			_ = sse.WriteEvent("reasoning", fmt.Sprintf(`{"text":%q}`, evt.Text))
		case "usage":
			finalUsage = evt.Usage
			data, _ := json.Marshal(evt.Usage)
			_ = sse.WriteEvent("usage", string(data))
		case "done":
			finishReason = evt.Finish
			_ = sse.WriteEvent("done", fmt.Sprintf(`{"finish":%q}`, evt.Finish))
		case "error":
			_ = sse.WriteEvent("error", fmt.Sprintf(`{"message":%q}`, evt.Error))
			finishReason = "error"
		}
	}

	duration := int(time.Since(startTime).Milliseconds())

	// 保存 assistant 消息
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
		Usage:      usage,
		DurationMs: duration,
		Finish:     finishReason,
	}
	_ = h.messageStore.Append(sessionID, assistantMsg)

	// 更新会话时间
	_ = h.sessionStore.Touch(sessionID)

	// 留痕 done / error / aborted
	if finishReason == "aborted" || ctx.Err() != nil {
		h.requestStore.WriteAborted(sessionID)
	} else if finishReason == "error" {
		h.requestStore.WriteError(sessionID, "流式解析错误")
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
}
