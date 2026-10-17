# WispStudio · Wisp

Go + Vue 3 + TypeScript 的本地会话式 coding agent。事件驱动运行时通过 SSE 流式输出；SQLite 保存完整事件与模型请求快照。

## 启动

需要 Go 1.22+、Node.js 和 npm。项目根目录执行：

```bash
cd web && npm ci && npm run build
cd .. && go build -o server ./cmd/server && ./server
```

首次启动会在**可执行文件同目录**生成 `Config.local.toml`（请勿提交版本库）：按 `[[Providers]]` 段填写 `BaseURL`、`Secret`、`Models`（模型 id 原样透传给服务商）后重启。默认监听 `http://127.0.0.1:8787`，事件日志在可执行文件同目录的 `Data/eventlog.db`，工具的工作区根也是可执行文件同目录。`web/dist` 存在时，同一端口会直接托管前端页面；开发前端可在 `web/` 运行 `npm run dev`。

## HTTP API

所有写操作经 `eventlog.Writer`，API 层不直接执行 SQL；事实全部来自事件日志。
协议消息是事件投影，JSON tag 使用 PascalCase（如 `ID`/`Seq`/`Type`/`SessionID`/`TurnID`/`Attrs`），
大块正文经 `BlobText` 返回。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 存活检查 |
| GET | `/api/sessions` | 会话列表（由 `session.created` 与内容事件折叠） |
| POST | `/api/sessions` | 新建会话并写 `session.created`，body `{"Title","Model?"}` |
| DELETE | `/api/sessions/{id}` | 软删除会话（追加 `session.deleted`），回合执行中返回 409 |
| GET | `/api/sessions/{id}/events?after_seq=N` | SSE：先回放日志中 `seq>N` 的事件，再推送实时事件；断线带 `after_seq` 重连即可补发 |
| POST | `/api/sessions/{id}/messages` | body `{"Text","Model?"}`，写 `message.created` + `turn.started` 并异步执行回合 |
| POST | `/api/sessions/{id}/stop` | 停止当前回合，写 `turn.completed` 终态 |
| GET | `/api/models` | catalog 全量：手动模型、自动发现结果、别名与 `ProviderName` |
| POST | `/api/models` | body `{"ProviderID","ModelID","Alias?"}`，写回 `Config.local.toml` |
| GET | `/api/config` | 返回 `Config.local.toml` 路径与原文（仅本机来源） |
| POST | `/api/config` | body `{"Text"}`，校验 TOML 与 ProviderID 后原子写回并热更新 catalog；失败返回 400 与 `line` |
| GET | `/api/events/{id}/trace` | 沿 `cause_id` 回溯因果链，调试用 |

SSE 每个事件带 `id: <seq>`、`event: <type>` 与 `data: <事件投影 JSON>`；客户端用
`after_seq`（服务端也支持 `Last-Event-ID`）补齐。事件写入后只在 `eventlog.Hub` 发“有新事件”提示，
SSE 收到提示后回读日志，因此通知可合并、数据不会丢。

兼容接口仍保留：`GET /api/catalog`、`POST /api/turns`、`GET /api/turns/{id}`、
`POST /api/turns/{id}/approvals/{tool_call_id}`。

真实端点冒烟测试（默认跳过，设置密钥才执行）：

```bash
WISP_LIVE_API_KEY=sk-xxx go test ./internal/provider/ -run TestLiveToolCallWireFormat -v
```


## 工作方式

- **界面只保留三件事：** 会话列表、流式聊天与 Markdown 渲染。工具调用以可折叠块内联展示，写入和 Shell 命令在工具块内批准或拒绝。
- **断线不丢消息：** SSE 客户端按事件 `Seq` 记录位置，断线后指数退避重连并携带 `after_seq` 补发；刷新页面从 0 回放完整历史。
- **配置页：** 直接编辑 `Config.local.toml` 原文，保存时后端校验语法与 `ProviderID` 并返回错误行号，成功后热更新模型目录，密钥对新请求立即生效。
- **模型选择：** 新会话只需在下拉框选择模型；下拉显示 `ModelAlias`，没有别名时显示全称 id。

## 数据与升级

SQLite 是仅追加的事件日志（schema v2，只允许纯增量加表，不做猜测性迁移）；更早的 coordinator 版本数据库不再兼容，请更换 `Data/` 下的数据库文件。回合状态不存在任何可变字段表，全部由 refs 指针 + 事件折叠得出。

配置和数据库不应提交到 Git；参见 `.gitignore`。
