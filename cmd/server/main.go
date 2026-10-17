// Wisp 后端入口：加载配置、打开事件日志、构建模型目录、触发自动发现，
// 并在 127.0.0.1 上启动 HTTP/SSE API。
//
// 退出顺序：先停 API（唤醒 SSE、停止在途回合），再停 HTTP 服务，
// 最后关闭 Store（排空 writer 队列并关库）。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"wisp/internal/api"
	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
)

// shutdownTimeout 是收到退出信号后排空在途请求的等待时间。
const shutdownTimeout = 5 * time.Second

func main() {
	// 加载配置（不存在时在与可执行文件同目录生成默认配置）
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置已加载: Port=%d Providers=%d", cfg.Server.Port, len(cfg.Providers))

	// 可执行文件目录既是数据目录的父目录，也是工具的工作区根
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("定位可执行文件失败: %v", err)
	}
	baseDir := filepath.Dir(exe)
	dataDir := filepath.Join(baseDir, "Data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	store, err := eventlog.Open(filepath.Join(dataDir, "eventlog.db"), nil)
	if err != nil {
		log.Fatalf("打开事件日志失败: %v", err)
	}
	defer func() {
		// 兜底关闭；正常路径在 ListenAndServe 返回后显式关闭。
		_ = store.Close()
	}()
	log.Println("事件日志已就绪")

	// 构建模型目录并触发自动发现；单个 provider 发现失败只记日志，不阻塞启动
	catalog := provider.NewCatalog(cfg, filepath.Join(baseDir, config.FileName))
	discoverCtx, cancelDiscover := context.WithTimeout(context.Background(), 15*time.Second)
	catalog.DiscoverAll(discoverCtx, func(msg string, args ...any) { slog.Warn(fmt.Sprintf(msg, args...)) })
	cancelDiscover()
	log.Println("模型目录已就绪")

	// 前端构建产物与可执行文件同目录时，由同一端口托管页面。
	webDist := filepath.Join(baseDir, "web", "dist")
	if info, statErr := os.Stat(webDist); statErr != nil || !info.IsDir() {
		webDist = ""
		log.Println("未找到 web/dist，仅提供 HTTP API")
	}

	apiSrv, err := api.NewServer(api.Config{
		Store:         store,
		Catalog:       catalog,
		WorkspaceRoot: baseDir,
		WebDist:       webDist,
		Logf:          func(msg string, args ...any) { slog.Info(fmt.Sprintf(msg, args...)) },
	})
	if err != nil {
		log.Fatalf("构造 HTTP API 失败: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 收到 Ctrl-C / SIGTERM 后优雅关闭：先停 API（关通知中心、停回合），
	// 再停 HTTP，最后排空 writer 并关库。
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := apiSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("API 关闭超时", "err", err)
		}
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP 服务关闭超时", "err", err)
		}
	}()

	log.Printf("HTTP API 已就绪: http://%s", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
	// 先排空 writer 队列，再关闭数据库与通知中心。
	if err := store.Close(); err != nil {
		slog.Warn("关闭事件日志失败", "err", err)
	}
	log.Println("已退出")
}
