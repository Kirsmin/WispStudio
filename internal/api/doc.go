// 包 api 提供 Wisp 后端的 HTTP/SSE 接口。
//
// 设计约定：
//   - 事实只存在 eventlog：会话、消息、回合、审批全部由事件日志投影。
//     API 不持有业务可变状态，也不直接执行 SQL。
//   - 所有写入经 eventlog.Writer；写入成功后在 eventlog 内 fan-out 通知，
//     通知只提醒“有新事件”，SSE 始终回读日志，因此断线重连可按 after_seq
//     完整补发且不重复。
//   - 协议消息是事件投影，JSON tag 使用 PascalCase；大块正文经 BlobText 提供。
//   - 同一会话的回合串行执行：会话内容链是单条 parent_id 链，并发回合会
//     让链分叉，故按会话加锁。
//   - 优雅关闭时先关闭通知中心唤醒 SSE，再停止在途回合，最后关闭 Store
//     （Store.Close 会排空 writer 队列再关库）。
package api
