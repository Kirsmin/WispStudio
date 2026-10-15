// Wisp 后端入口：加载配置、打开事件日志、构建模型目录并触发自动发现。
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"wisp/internal/config"
	"wisp/internal/eventlog"
	"wisp/internal/provider"
)

func main() {
	// 加载配置（不存在时在与可执行文件同目录生成默认配置）
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置已加载: Port=%d Providers=%d", cfg.Server.Port, len(cfg.Providers))

	// 打开事件日志（数据文件放在可执行文件同目录的 Data/ 下）
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("定位可执行文件失败: %v", err)
	}
	dataDir := filepath.Join(filepath.Dir(exe), "Data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	store, err := eventlog.Open(filepath.Join(dataDir, "eventlog.db"), nil)
	if err != nil {
		log.Fatalf("打开事件日志失败: %v", err)
	}
	defer store.Close()
	log.Println("事件日志已就绪")

	// 构建模型目录并触发自动发现；单个 provider 发现失败只记日志，不阻塞启动
	catalog := provider.NewCatalog(cfg, filepath.Join(filepath.Dir(exe), config.FileName))
	discoverCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	catalog.DiscoverAll(discoverCtx, func(msg string, args ...any) { slog.Warn(msg, args...) })
	log.Println("模型目录已就绪")
}
