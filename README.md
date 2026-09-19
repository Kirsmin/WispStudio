# WispStudio · Wisp

Go + Vue 3 + TypeScript 的本地会话式 coding agent。Plan 和 Build 由 Runtime 状态机协调；SQLite 保存完整事件、请求快照与版本化产物。

## 启动

需要 Go 1.22+、Node.js 和 npm。项目根目录执行：

```bash
cd web && npm ci && npm run build
cd .. && go run ./cmd/server
```

首次启动生成 `config.toml.local`，填写 `[[providers]]` 的 `base_url`、`api_key` 等设置后再次启动。默认地址为 `http://127.0.0.1:7860`，数据保存在 `Data/`。开发前端可在 `web/` 运行 `npm run dev`。

## 工作方式

- **项目指令按会话选择：** 输入框的 `AGENTS.md` 开关默认关闭，开启才注入工作区根目录的指令（最多 128 KiB），并将选择持久化到 Session；不会因此自动展示整个工作区。项目指令不覆盖 Runtime 的权限检查。
- **有效目标不失忆：** 原始请求只用于记录；最新有效 Plan 更新 `current_objective`，后续明确修改另存为 UserOverrides，Build 按当前要求执行。
- **更少的额外流程：** 独立单文件任务跳过无意义的仓库调查；简单任务不创建 Phase，复杂任务的进度与 Checkpoint 由 Runtime 管理，不占用工具调用。
- **工具与审批：** 每轮最多接受四个独立只读工具；写入和 Shell 命令需授权，拒绝或暂停后仍按原批次配对回放。审批可仅允许本次，或允许本 Turn 的文件写入 / 同一条命令；只读 `verify_file` 无需 Shell 授权。
- **双层界面：** 主聊天折叠连续执行步骤；右侧「调试」增量读取模型摘要，展开单次调用才加载实际 Prompt 分层、消息、工具结果与完整请求。
- **完整导出：** 会话顶部的导出按钮生成本地保存数据的 Zip，其中包含完整模型请求和 Session 文件。

## 数据与升级

SQLite schema 为 v8。当前源码仅支持新建数据库和 **v7 → v8** 的保留数据升级；更早格式不再提供自动兼容。升级前备份 `Data/`，需要时可先用旧版本导出 Session。`go run ./cmd/server sync` 可将 SQLite 内容导出到 `Data/Sync/`。

配置和数据库不应提交到 Git；参见 `.gitignore`。
