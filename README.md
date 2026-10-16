# WispStudio · Wisp

Go + Vue 3 + TypeScript 的本地会话式 coding agent。Plan 和 Build 由 Runtime 状态机协调；SQLite 保存完整事件、请求快照与版本化产物。

## 启动

需要 Go 1.22+、Node.js 和 npm。项目根目录执行：

```bash
cd web && npm ci && npm run build
cd .. && go build -o server ./cmd/server && ./server
```

首次启动会在**可执行文件同目录**生成 `Config.local.toml`（请勿提交版本库）：按 `[[Providers]]` 段填写 `BaseURL`、`Secret`、`Models`（模型 id 原样透传给服务商）后重启。默认监听 `http://127.0.0.1:8787`，事件日志在可执行文件同目录的 `Data/eventlog.db`，工具的工作区根也是可执行文件同目录。开发前端可在 `web/` 运行 `npm run dev`。

## HTTP API

最小 JSON 接口（仅本机监听）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 存活检查 |
| GET | `/api/catalog` | 已配置 provider 与模型（含自动发现结果） |
| POST | `/api/turns` | 启动回合，body `{"text","session_id?","model?"}`，返回 `session_id`/`context_id`/`turn_id`/`model` |
| GET | `/api/sessions/{session_id}/events?after_seq=N&limit=M` | 增量读取事件流，返回 `events` 与 `next_seq` |
| GET | `/api/turns/{turn_id}` | 回合状态（refs 指针 + 事件折叠），含 `pending_approvals` |
| POST | `/api/turns/{turn_id}/approvals/{tool_call_id}` | 提交审批，body `{"decision":"approved"\|"rejected","reason?"}` |

回合异步执行：启动后用返回的 `next_seq` 轮询事件流；`GET /api/turns/{turn_id}` 的 `ref` 取值 `running` / `waiting_approval:<审批ID>` / `completed` / `failed:<原因>`。需要审批的工具会先挂起，提交决定后自动继续。

真实端点冒烟测试（默认跳过，设置密钥才执行）：

```bash
WISP_LIVE_API_KEY=sk-xxx go test ./internal/provider/ -run TestLiveToolCallWireFormat -v
WISP_LIVE_API_KEY=sk-xxx go test ./internal/api/ -run TestLiveAPITurnWithRealModel -v
```


## 工作方式

- **项目指令按会话选择：** 输入框的 `AGENTS.md` 开关默认关闭，开启才注入工作区根目录的指令（最多 128 KiB），并将选择持久化到 Session；不会因此自动展示整个工作区。项目指令不覆盖 Runtime 的权限检查。
- **有效目标不失忆：** 原始请求只用于记录；最新有效 Plan 更新 `current_objective`，后续明确修改另存为 UserOverrides，Build 按当前要求执行。
- **更少的额外流程：** 独立单文件任务跳过无意义的仓库调查；简单任务不创建 Phase，复杂任务的进度与 Checkpoint 由 Runtime 管理，不占用工具调用。
- **工具与审批：** 每轮最多接受四个独立只读工具；写入和 Shell 命令需授权，拒绝或暂停后仍按原批次配对回放。审批可仅允许本次，或允许本 Turn 的文件写入 / 同一条命令；只读 `verify_file` 无需 Shell 授权。
- **双层界面：** 主聊天折叠连续执行步骤；右侧「调试」增量读取模型摘要，展开单次调用才加载实际 Prompt 分层、消息、工具结果与完整请求。
- **完整导出：** 会话顶部的导出按钮生成本地保存数据的 Zip，其中包含完整模型请求和 Session 文件。

## 数据与升级

SQLite 是仅追加的事件日志（schema v2，只允许纯增量加表，不做猜测性迁移）；更早的 coordinator 版本数据库不再兼容，请更换 `Data/` 下的数据库文件。回合状态不存在任何可变字段表，全部由 refs 指针 + 事件折叠得出。

配置和数据库不应提交到 Git；参见 `.gitignore`。
