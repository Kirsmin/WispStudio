package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"wisp/internal/api"
	"wisp/internal/config"
)

func exists(p string) bool { i, e := os.Stat(p); return e == nil && !i.IsDir() }
func main() {
	path := "config.toml.local"
	if !exists(path) {
		if exists("config.toml") {
			path = "config.toml"
		} else {
			if e := config.WriteDefault(path); e != nil {
				log.Fatal(e)
			}
			log.Printf("已生成 %s，请填写 Provider 配置后重启", path)
			return
		}
	}
	cfg, e := config.Load(path)
	if e != nil {
		log.Fatal(e)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Wisp: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, api.NewRouter(cfg)))
}
