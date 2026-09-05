package main

import (
	"fmt"
	"log"
	"net/http"

	"wisp/internal/api"
	"wisp/internal/config"
)

func main() {
	cfg, err := config.Load("config.toml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	router := api.NewRouter(cfg)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("服务器启动: http://%s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("服务器错误: %v", err)
	}
}
