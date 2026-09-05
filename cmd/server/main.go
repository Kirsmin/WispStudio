package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"wisp/internal/api"
	"wisp/internal/config"
)

const (
	primaryConfig = "config.toml.local"
	legacyConfig  = "config.toml"
)

func main() {
	path := resolveConfigPath()
	if path == "" {
		if err := config.WriteDefault(primaryConfig); err != nil {
			log.Fatalf("生成默认配置失败: %v", err)
		}
		log.Printf("已生成 %s。请填写 API Key / Provider 后重新启动。", primaryConfig)
		return
	}

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	router := api.NewRouter(cfg)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Printf("WispStudio 已启动: http://%s", addr)
	log.Printf("配置文件: %s", path)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务器错误: %v", err)
	}
}

func resolveConfigPath() string {
	for _, path := range []string{primaryConfig, legacyConfig} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
