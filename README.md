# Wisp

一个能对话的流式 demo。Go 后端 + Vue 前端，前端可连接任意地址的后端，会话持久化，原始请求全量留痕。

## 快速开始

### 后端

```bash
# 安装依赖
go mod tidy

# 编辑配置文件
cp config.toml config.toml.local
# 修改 config.toml.local 中的 api_key

# 启动服务器
go run ./cmd/server/main.go
```

服务器默认监听 `http://127.0.0.1:7860`。

### 前端

```bash
cd web
npm install
npm run dev
```

前端开发服务器默认在 `http://127.0.0.1:5173`，会自动代理 `/api` 到后端。

## 配置

`config.toml`：

```toml
[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"            # 会话与请求日志根目录

[openai]
base_url = "https://api.deepseek.com/v1"
api_key  = "sk-xxx"          # 替换为你的 API Key
timeout_sec = 120

[[models]]
id = "deepseek-chat"
name = "DeepSeek-V3"
default = true
thinking_levels = ["off"]
thinking_style = "none"
```

## 数据目录

运行时自动创建 `Data/` 目录：

- `Data/sessions.json` — 会话元数据
- `Data/Sessions/<id>.jsonl` — 每会话消息记录
- `Data/Requests/<id>.jsonl` — 原始请求留痕

## 技术栈

- 后端：Go 1.22+，标准库路由，SSE 流式
- 前端：Vue 3 + TypeScript + NaiveUI + Pinia + Vite
