// 包 api 提供 Wisp 后端的最小 HTTP API：启动回合、读取事件流与回合状态、
// 提交审批决定。
//
// 设计约定：
//   - 本包不持有会话/回合的可变状态：一切事实都在事件日志里，
//     回合状态由 runtime.Status（refs 指针 + 事件 fold）实时算出。
//   - 回合异步执行：POST /api/turns 立即返回 turn_id，客户端用
//     GET /api/sessions/{session_id}/events?after_seq=N 增量轮询事件流，
//     用 GET /api/turns/{turn_id} 读状态（含待审批列表）。
//   - 模型按 {ProviderID}/{ModelID} 全称 id 解析（provider.Catalog.Endpoint），
//     BaseURL/密钥来自配置，模型 id 原样透传给服务商，不做任何私有映射。
//   - 同一会话的回合串行执行：会话内容链是单条 parent_id 链，
//     并发回合会让链分叉，故按会话加锁。
package api
