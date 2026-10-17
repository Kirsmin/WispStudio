// 包 runtime 实现 Wisp 的事件驱动运行时：回合（turn）状态不存任何
// 可变字段，全部由「refs 指针 + 事件日志」折叠得出。
//
// 回合状态机：
//
//   - 每轮循环起点：读 refs 中 turn/<id> 的当前指针，叠加 ListByTurn
//     的全部事件 fold 出当前状态。turn.started / turn.completed 等
//     事件只描述事实，refs 指针描述"现在走到哪里"。
//   - 指针取值：running（执行中）、waiting_approval:<审批ID>
//     （挂起等待审批）、completed（正常结束）、failed:<原因>（失败结束）。
//
// 一个循环迭代依次裁决：
//
//  1. 有 tool.called 尚无 tool.returned（按幂等键判断崩溃恢复）→ 写
//     tool.returned 结清；需要审批的工具先写 approval.requested 并把
//     指针移到 waiting_approval 挂起，由 DecideApproval 写
//     approval.decided 后移回 running 再继续（不轮询任何 pending 表）。
//  2. 否则 compile() 编译上下文 → 写 llm.requested → 调 provider
//     （流式）→ 边收流边写 message.delta（增量落盘）→ 完成写
//     message.created + llm.responded；若响应含工具调用则写
//     tool.called（原子批量、含幂等键）进入下一轮结清，否则写
//     turn.completed 并把指针移到 completed。
//
// 内容可见性：每个内容事件（message.created / tool.called /
// tool.returned）都以「当前内容链尾」为 parent_id，
// 并立即以 ctx_op.append 追加到回合上下文，使下一轮 compile 能看见
// 本轮新产生的内容；链尾由事件 fold 得出，追加前查 ctx_ops 去重，
// 因此崩溃后重放不会重复追加。
//
// 工具执行严格 write-ahead：tool.called 先落盘再执行，tool.returned
// 执行后落盘；崩溃恢复按 attrs.idempotency_key 判断调用是否已结清。
// 已知限制：执行与结果落盘之间的崩溃窗口内，有副作用的工具
// （write_file / run_command）可能被重复执行一次。
//
// 文件操作一律 securePath：只允许工作区相对路径，词法限制在
// workspaceRoot 内，并对已存在路径解析符号链接防 ../ 逃逸。
package runtime
