# Wisp

一个能对话的流式 demo。Go 后端 + Vue 前端，前端可连接任意地址的后端，会话持久化，原始请求全量留痕。

## 快速开始

### 后端

```bash
# 安装依赖
go mod tidy

# 首次运行：自动生成默认配置 config.toml.local 后退出
go run ./cmd/server/main.go

# 编辑 config.toml.local，填入 API Key 等配置，再次启动即可
go run ./cmd/server/main.go
```

> 配置加载优先级：`config.toml.local` > `config.toml`（旧用法兼容）。两个文件都已加入 `.gitignore`，不会提交到仓库。

服务器默认监听 `http://127.0.0.1:7860`。

### 前端

```bash
cd web
npm install
npm run dev
```

前端开发服务器默认在 `http://127.0.0.1:5173`，会自动代理 `/api` 到后端。

## 配置

`config.toml.local`（首次运行自动生成）：

```toml
[server]
host = "127.0.0.1"
port = 7860

[storage]
data_dir = "Data"            # 会话与请求日志根目录

[openai]
base_url = "https://api.deepseek.com/v1"   # 默认上游网关
api_key  = "sk-xxx"                        # 替换为你的 API Key
timeout_sec = 120            # 建连与等待首个响应包的超时（不限制流式时长）

# 每个模型可单独覆盖 base_url / api_key，未覆盖的沿用 [openai] 的默认值
[[models]]
id = "deepseek-chat"
name = "DeepSeek-V3"
default = true
thinking_levels = ["off"]
thinking_style = "none"

# 示例：走其他服务商的模型，单独指定网关
[[models]]
id = "qwen3-max"
name = "Qwen3-Max"
base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key  = "sk-xxx"
thinking_levels = ["off"]
thinking_style = "none"
```

## 数据目录

运行时自动创建 `Data/` 目录：

- `Data/sessions.json` — 会话元数据
- `Data/Sessions/<id>.jsonl` — 每会话消息记录
- `Data/Requests/<id>.jsonl` — 原始请求全量留痕（方法、URL、完整请求体原文）

## 技术栈

- 后端：Go 1.22+，标准库路由，SSE 流式
- 前端：Vue 3 + TypeScript + NaiveUI + Pinia + Vite
