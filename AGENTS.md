# Wisp 架构约定

项目名 WispStudio，显示为 Wisp。Go + Vue 3 + TypeScript。

## 架构

- `internal/eventlog`：SQLite 仅追加事件日志。单 writer goroutine 独占 `seq` 分配；
  事件与 blob 同事务写入；`refs` 指针 + 事件折叠表达回合状态，不存可变状态表。
- `internal/compile`：从 `contexts.base_ctx`、`ctx_ops` 与 `events.parent_id` 内容链
  编译消息列表；工具调用按 `links.rel='result_of'` 成组，正文 blob 批量读取。
- `internal/provider`：OpenAI 兼容 `/chat/completions` 客户端与模型目录；
  只返回结构化结果，不感知事件。
- `internal/runtime`：回合状态机与内置工具；流式增量、工具调用、审批全部落事件。
- `internal/api`：HTTP/SSE、会话、审批、模型目录与配置热更新。
- `internal/config`：单一 `Config.local.toml` 的解析、校验与原子写回。
- `web`：Vue 3 + Pinia 前端，SSE 按 `seq` 回放。
- `tools/stress` + `scripts/stats.sh`：压测与事件/blob 体积统计。

## 不变量

- 事件日志是唯一事实来源；API 不直接执行 SQL，写入统一经 `eventlog.Writer`。
- `seq` 从 1 开始连续单调；任何批量事件在同一事务提交。
- 同一会话的回合串行执行；回合状态只由 `refs` + 事件折叠得出。
- 配置解析、校验、写回、catalog 热更新全部成功才生效；失败保持旧配置。
- 不向前兼容：不保留旧接口、旧 schema 迁移、兼容字段、注释掉的旧实现或空实现。
- 注释用中文；提交信息用中文一句话。
- 开发在 Dev 分支；没有指示不动 main。
- 不写测试实例；验收用 `go vet`、`staticcheck`、前端构建与压测。

## 命令

```bash
go test ./...
go vet ./...
staticcheck ./...
cd web && npm run build

go run ./tools/stress -turns 200 -dir Data/stress
scripts/stats.sh Data/stress/eventlog.db
```
