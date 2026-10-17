package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"wisp/internal/compile"
	"wisp/internal/eventlog"
	"wisp/internal/ids"
	"wisp/internal/provider"
)

// stepLLM 执行一次 LLM 步：compile → llm.requested → 流式调用 →
// message.delta（边收流边落盘）→ message.created + llm.responded →
// tool.called（若有工具调用）。
// tail 是当前内容链尾事件 ID，新内容事件以它为 parent_id。
// 返回 done=true 表示响应无工具调用，回合已完成（turn.completed 已写）。
func (r *Runtime) stepLLM(ctx context.Context, p TurnParams, tail ids.EventID) (bool, error) {
	res, err := compile.CompileWithWriter(ctx, r.store, r.writer, p.ContextID)
	if err != nil {
		return false, fmt.Errorf("compile 失败: %w", err)
	}
	// 请求用服务商侧裸模型 id；llm.requested attrs 里记录全称 id
	req := BuildChatRequest(res, r.wireModel, r.tools.Definitions())

	// llm.requested：blob 引用编译产出的 RequestManifest（回放用）
	manifest, err := r.store.GetBlob(ctx, res.ManifestHash)
	if err != nil {
		return false, fmt.Errorf("runtime: 读取 RequestManifest 失败: %w", err)
	}
	reqID := ids.NewRequestID()
	reqEv, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeLLMRequested,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: reqID.String(),
		Blob:      manifest,
		Attrs:     eventlog.LLMRequestedAttrs{ProviderID: r.providerID, Model: r.model},
	})
	if err != nil {
		return false, fmt.Errorf("runtime: 写 llm.requested 失败: %w", err)
	}

	// 流式调用：边收流边写 message.delta（增量落盘）
	msgID := ids.NewMessageID()
	var content, reasoning strings.Builder
	sres, err := r.client.ChatStream(ctx, req, func(d provider.StreamDelta) error {
		return r.appendDelta(ctx, p, msgID, &content, &reasoning, d)
	})
	if err != nil {
		return false, fmt.Errorf("LLM 请求失败: %w", err)
	}

	// message.created：本轮 assistant 消息（正文存 blobs），并追加到上下文
	msgEv, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeMessageCreated,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: msgID.String(),
		ParentID:  tail.String(),
		CauseID:   reqEv.ID.String(),
		Blob:      []byte(content.String()),
		Attrs:     eventlog.MessageCreatedAttrs{Role: string(compile.RoleAssistant)},
	})
	if err != nil {
		return false, fmt.Errorf("runtime: 写 message.created 失败: %w", err)
	}
	if err := r.appendContentToCtx(ctx, p, msgEv.ID); err != nil {
		return false, err
	}
	tail = msgEv.ID

	// llm.responded：响应完成（usage / finish_reason / 耗时）
	if _, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeLLMResponded,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: reqID.String(),
		CauseID:   reqEv.ID.String(),
		Attrs: eventlog.LLMRespondedAttrs{
			PromptTokens:     sres.Usage.PromptTokens,
			CompletionTokens: sres.Usage.CompletionTokens,
			TotalTokens:      sres.Usage.TotalTokens,
			FinishReason:     sres.FinishReason,
			DurationMs:       sres.Duration.Milliseconds(),
		},
	}); err != nil {
		return false, fmt.Errorf("runtime: 写 llm.responded 失败: %w", err)
	}

	if len(sres.ToolCalls) > 0 {
		if err := r.recordToolCalls(ctx, p, reqEv.ID, sres.ToolCalls, tail); err != nil {
			return false, err
		}
		return false, nil
	}

	if _, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
		Type:      eventlog.TypeTurnCompleted,
		SessionID: p.SessionID,
		TurnID:    p.TurnID,
		Actor:     actorRuntime,
		SubjectID: p.TurnID,
		CauseID:   reqEv.ID.String(),
		Attrs:     eventlog.TurnCompletedAttrs{FinishReason: sres.FinishReason},
	}); err != nil {
		return false, fmt.Errorf("runtime: 写 turn.completed 失败: %w", err)
	}
	if _, err := r.writer.MoveRef(ctx, TurnRefName(p.TurnID), RefCompleted); err != nil {
		return false, fmt.Errorf("runtime: 移动指针到 completed 失败: %w", err)
	}
	return true, nil
}

// appendDelta 把一帧流式增量写入事件日志：content / reasoning 各写一条
// message.delta（正文存 blobs，attrs.kind 标明类别）。聚合内容同步累加。
func (r *Runtime) appendDelta(ctx context.Context, p TurnParams, msgID ids.MessageID, content, reasoning *strings.Builder, d provider.StreamDelta) error {
	if d.Content != "" {
		content.WriteString(d.Content)
		if _, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
			Type:      eventlog.TypeMessageDelta,
			SessionID: p.SessionID,
			TurnID:    p.TurnID,
			Actor:     actorRuntime,
			SubjectID: msgID.String(),
			Blob:      []byte(d.Content),
			Attrs:     eventlog.MessageDeltaAttrs{Kind: "content"},
		}); err != nil {
			return fmt.Errorf("runtime: 写 message.delta 失败: %w", err)
		}
	}
	if d.Reasoning != "" {
		reasoning.WriteString(d.Reasoning)
		if _, err := r.writer.AppendEvent(ctx, eventlog.EventInput{
			Type:      eventlog.TypeMessageDelta,
			SessionID: p.SessionID,
			TurnID:    p.TurnID,
			Actor:     actorRuntime,
			SubjectID: msgID.String(),
			Blob:      []byte(d.Reasoning),
			Attrs:     eventlog.MessageDeltaAttrs{Kind: "reasoning"},
		}); err != nil {
			return fmt.Errorf("runtime: 写 message.delta 失败: %w", err)
		}
	}
	return nil
}

// recordToolCalls 把响应中的工具调用写成 tool.called（原子批量），
// 每个调用带幂等键：同一逻辑调用跨崩溃恢复不变，用于判断是否已结清。
// tail 是本轮 assistant 消息事件 ID，调用事件以它为 parent_id。
func (r *Runtime) recordToolCalls(ctx context.Context, p TurnParams, causeID ids.EventID, calls []provider.ToolCall, tail ids.EventID) error {
	ins := make([]eventlog.EventInput, 0, len(calls))
	for i, tc := range calls {
		tcID := ids.NewToolCallID()
		ins = append(ins, eventlog.EventInput{
			Type:      eventlog.TypeToolCalled,
			SessionID: p.SessionID,
			TurnID:    p.TurnID,
			Actor:     actorRuntime,
			SubjectID: tcID.String(),
			ParentID:  tail.String(),
			CauseID:   causeID.String(),
			Blob:      []byte(tc.Function.Arguments),
			Attrs: eventlog.ToolCalledAttrs{
				Name:           tc.Function.Name,
				ProviderID:     tc.ID,
				IdempotencyKey: idempotencyKey(p.TurnID, tc, i),
			},
		})
	}
	evs, err := r.writer.AppendEvents(ctx, ins)
	if err != nil {
		return fmt.Errorf("runtime: 写 tool.called 失败: %w", err)
	}
	// 追加到上下文，下一轮 compile 才能看见这些调用
	for _, ev := range evs {
		if err := r.appendContentToCtx(ctx, p, ev.ID); err != nil {
			return err
		}
	}
	return nil
}

// idempotencyKey 计算工具调用的幂等键：同一回合内相同
// （provider tool_use_id, 工具名, 参数） 的调用键恒定，
// 崩溃恢复据此识别已结清的调用，绝不重复执行。
func idempotencyKey(turnID string, tc provider.ToolCall, index int) string {
	h := sha256.New()
	h.Write([]byte(turnID))
	h.Write([]byte{0})
	h.Write([]byte(tc.ID))
	h.Write([]byte{0})
	fmt.Fprintf(h, "%d", index)
	h.Write([]byte{0})
	h.Write([]byte(tc.Function.Name))
	h.Write([]byte{0})
	h.Write([]byte(tc.Function.Arguments))
	return hex.EncodeToString(h.Sum(nil))
}

// BuildChatRequest 把编译结果转换为发给 provider 的请求：
//   - assistant 正文消息与紧随其后的工具调用消息合并为一条
//     （content + tool_calls），与 provider 协议一致；
//   - user 工具结果映射为 tool 角色消息（tool_call_id 取 provider 侧 ID）；
//   - 请求固定流式。
func BuildChatRequest(res *compile.Result, model string, tools []provider.ToolDefinition) provider.ChatRequest {
	req := provider.ChatRequest{Model: model, Stream: true, Tools: tools}
	msgs := res.Messages
	for i := 0; i < len(msgs); {
		m := msgs[i]
		switch {
		case m.Role == compile.RoleAssistant:
			content := ""
			j := i
			if m.ToolCall == nil {
				content = m.Content
				j = i + 1
			}
			var tcs []provider.ToolCall
			for j < len(msgs) && msgs[j].Role == compile.RoleAssistant && msgs[j].ToolCall != nil {
				tc := msgs[j].ToolCall
				pid := tc.ProviderID
				if pid == "" {
					pid = res.ProviderMap[tc.ID]
				}
				tcs = append(tcs, provider.ToolCall{
					ID:   pid,
					Type: "function",
					Function: provider.ToolCallFunction{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
				j++
			}
			req.Messages = append(req.Messages, provider.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: tcs,
			})
			i = j
		case m.Role == compile.RoleUser && m.ToolResultFor != "":
			req.Messages = append(req.Messages, provider.Message{
				Role:       "tool",
				ToolCallID: res.ProviderMap[m.ToolResultFor],
				Content:    m.Content,
			})
			i++
		default:
			req.Messages = append(req.Messages, provider.Message{Role: "user", Content: m.Content})
			i++
		}
	}
	return req
}
