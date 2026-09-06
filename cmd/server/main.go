package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"wisp/internal/api"
	"wisp/internal/config"
	"wisp/internal/store"
)

const (
	primaryConfig = "config.toml.local"
	legacyConfig  = "config.toml"
)

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func resolveConfigPath() string {
	if fileExists(primaryConfig) {
		return primaryConfig
	}
	if fileExists(legacyConfig) {
		return legacyConfig
	}
	return ""
}

func main() {
	if len(os.Args) > 1 {
		if os.Args[1] != "sync" || len(os.Args) != 2 {
			log.Fatalf("用法: wisp [sync]")
		}
		runSync()
		return
	}

	path := resolveConfigPath()
	if path == "" {
		if err := config.WriteDefault(primaryConfig); err != nil {
			log.Fatalf("生成默认配置失败: %v", err)
		}
		log.Printf("未检测到配置文件，已自动生成 %s", primaryConfig)
		log.Printf("请编辑该文件填写 API Key 等配置后重新启动服务器")
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	router, err := api.NewRouter(cfg)
	if err != nil {
		log.Fatalf("初始化服务器失败: %v", err)
	}
	defer router.Close()

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("已加载配置: %s", path)
	log.Printf("服务器启动: http://%s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("服务器错误: %v", err)
	}
}

func runSync() {
	dataDir := "Data"
	if path := resolveConfigPath(); path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			log.Fatalf("加载配置失败: %v", err)
		}
		dataDir = cfg.Storage.DataDir
	}
	st, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer st.Close()
	if err := st.SyncJSONL(); err != nil {
		log.Fatalf("导出失败: %v", err)
	}
	log.Printf("已导出 SQLite -> %s/Sync", dataDir)
}
