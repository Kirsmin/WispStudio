package api

import (
	"context"
	"fmt"
	"strings"

	"wisp/internal/provider"
	"wisp/internal/runtime"
)

// Recover 在启动时重建进程丢失的回合：遍历 turn/* 指针，跳过终态与等待审批，
// 其余回合用事件里最近一次 llm.requested 的模型重建运行时并重新执行。
// 回合在后台运行，单个回合失败只记日志；仅读取 refs 失败时返回错误。
func (s *Server) Recover(ctx context.Context) error {
	refs, err := s.cfg.Store.ListRefs(ctx)
	if err != nil {
		return fmt.Errorf("api: 读取 refs 失败: %w", err)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, "turn/") {
			continue
		}
		if runtime.IsTerminalRef(ref.Target) || runtime.IsWaitingRef(ref.Target) {
			continue
		}
		turnID := strings.TrimPrefix(ref.Name, "turn/")
		if err := s.recoverTurn(ctx, turnID); err != nil {
			s.cfg.Logf("api: 恢复回合 %s 失败: %v", turnID, err)
		}
	}
	return nil
}

// recoverTurn 重建单个回合的编译入参与运行时，并交给 runTurn 后台执行。
// RunTurn 幂等：指针已是终态或等待审批时不会重复执行。
func (s *Server) recoverTurn(ctx context.Context, turnID string) error {
	status, err := runtime.Status(ctx, s.cfg.Store, turnID)
	if err != nil {
		return err
	}
	if status.SessionID == "" || status.ContextID == "" {
		return fmt.Errorf("回合缺少会话或上下文信息")
	}
	if s.isRunning(status.SessionID) {
		return nil // 已在本进程执行
	}
	fullID, err := s.recoverModel(ctx, status)
	if err != nil {
		return err
	}
	ep, err := s.cfg.Catalog.Endpoint(fullID)
	if err != nil {
		return err
	}
	rt, err := s.newRuntime(ep)
	if err != nil {
		return err
	}

	lock := s.sessionState(status.SessionID)
	if !lock.lock.TryLock() {
		return fmt.Errorf("会话 %s 正在处理另一个请求", status.SessionID)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a := &activeTurn{turnID: turnID, rt: rt, cancel: cancel, done: make(chan struct{})}
	if !s.addActive(status.SessionID, a) {
		cancel()
		lock.lock.Unlock()
		return fmt.Errorf("服务正在关闭")
	}
	p := runtime.TurnParams{SessionID: status.SessionID, TurnID: turnID, ContextID: status.ContextID}
	go s.runTurn(runCtx, &lock.lock, a, p)
	return nil
}

// recoverModel 确定恢复回合要用的模型：优先 llm.requested 记录的模型，
// 其次会话默认模型，最后目录默认模型。
func (s *Server) recoverModel(ctx context.Context, status runtime.TurnStatus) (provider.ModelFullID, error) {
	if status.Model != "" {
		return provider.Parse(status.Model)
	}
	facts, err := s.sessionFacts(ctx, status.SessionID)
	if err != nil {
		return "", err
	}
	return s.resolveModel(facts.model)
}
