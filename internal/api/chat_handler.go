package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"wisp/internal/config"
	"wisp/internal/openai"
	"wisp/internal/store"
)

type ChatHandler struct {
	cfg           *config.Config
	sessionStore  *store.SessionStore
	messageStore  *store.MessageStore
	requestStore  *store.RequestStore
	openaiClient  *openai.Client
	streamLocks   map[string]*sync.Mutex
	streamMu      sync.Mutex
}

func NewChatHandler(cfg *config.Config, ss *store.SessionStore, ms *store.MessageStore, rs *store.RequestStore) *ChatHandler {
	return &ChatHandler{
		cfg:          cfg,
		sessionStore: ss,
		messageStore: ms,
		requestStore: rs,
		openaiClient: openai.NewClient(&cfg.OpenAI),
		streamLocks:  make(map[string]*sync.Mutex),
	}
}

func (h *ChatHandler) getStreamLock(sessionID string) *sync.Mutex {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	if h.streamLocks[sessionID] == nil {
		h.streamLocks[sessionID] = &sync.Mutex{}
	}
	return h.streamLocks[sessionID]
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

	// 查找模型配置
	var modelCfg *config.ModelConfig
	for i := range h.cfg.Models {
		if h.cfg.Models[i].ID == req.Model {
			modelCfg = &h.cfg.Models[i]
			break
		}
	}
	if modelCfg == nil {
		http.Error(w, "模型不存在", http.StatusBadRequest)
		return
	}

	// 构造上游请求
	upReq, err := h.openaiClient.BuildRequest(req.Model, modelCfg.ThinkingStyle, req.Thinking, messages)
	if err != nil {
		http.Error(w, "构造请求失败", http.StatusInternalServerError)
		return
	}

	// 留痕 request
	var reqBody map[string]any
	_ = json.Unmarshal([]byte(`{}`), &reqBody) // 占位
	// 重新序列化请求体用于留痕
	reqBodyRaw, _ := json.Marshal(messages)
	_ = reqBodyRaw
	h.requestStore.WriteRequest(sessionID, "POST", upReq.URL.String(), nil)

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
	var fullContent string
	var fullReasoning string
	var finalUsage *openai.Usage
	var finishReason string

	for evt := range ch {
		switch evt.Type {
		case "delta":
			fullContent += evt.Text
			_ = sse.WriteEvent("delta", fmt.Sprintf(`{"text":%q}`, evt.Text))
		case "reasoning":
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
