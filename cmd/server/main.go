package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"wisp/internal/api"
	"wisp/internal/config"
)

const (
	// primaryConfig 优先读取的本地覆盖配置
	primaryConfig = "config.toml.local"
	// legacyConfig 兼容旧的直接编辑 config.toml 的用法
	legacyConfig = "config.toml"
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
	path := resolveConfigPath()
	if path == "" {
		// 首次运行：自动生成默认配置并退出，让用户自行编辑
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

	router := api.NewRouter(cfg)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("已加载配置: %s", path)
	log.Printf("服务器启动: http://%s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("服务器错误: %v", err)
	}
}
