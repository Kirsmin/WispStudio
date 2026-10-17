// 包 compile 实现 Wisp 的核心编译器：从事件日志出发，
// 把某个上下文（context）编译成发给 LLM 的消息列表。
//
// 编译严格按以下顺序执行：
//
//  1. ancestry：沿 contexts.base_ctx 链取 root→leaf 的上下文序列；
//     链成环或上下文缺失直接报错。
//  2. 基线内容：从叶子上下文的 fork_at 事件沿 events.parent_id 回溯到根，
//     收集内容事件（message.created / tool.called / tool.returned），
//     按 (事件类型, subject_id) 去重，同 subject 多版本取 seq 最大者，
//     位置取最新版本在链上的时间位置。整条链与正文 blob 均为批量读取。
//  3. 逐层应用 ctx_ops（root 的操作用完再用 base 的，依此类推到叶子）：
//     append 追加到末尾；insert(anchor+okey) 插入 anchor 组（组紧随
//     anchor 条目，anchor 为空报错；组内按 okey 字典序，空 okey 排最后，
//     相等时后提交者排后）；hide 隐藏条目（幂等）；replace 把目标条目
//     原位替换为 new_id 引用的新内容事件（新旧事件类型必须一致）；
//     pin 置顶条目（幂等，按 pin 的 seq 排序）。subject 指向 tool 调用时，
//     hide/pin 对 call+result 整组生效，replace 以新事件类型匹配组内条目。
//  4. enforceBundles：tool.called 与 tool.returned 原子成组，
//     分组依据 links 表 rel='result_of'（src=tool.returned 事件 ID，
//     dst=tool.called 事件 ID）。组内任一被 hide 则整组隐藏（渲染时整组
//     丢弃、不留占位符）；孤儿 returned（其 called 不在编译列表中）丢弃；
//     孤儿 called 补占位结果 "omitted" 并产生 warning。
//  5. validate 不变量：消息列表非空、首条非 assistant、
//     assistant 正文消息可紧跟工具调用消息（视为同一逻辑 assistant
//     消息，构造 provider 请求时由 runtime 合并为一条）、user 工具结果
//     可相邻、工具调用 ID 与结果一一配对（每个 tool_use 恰有一个
//     tool_result 且出现在其后）。任何一条不满足都返回 error，绝不静默放过。
//  6. render：隐藏的非工具条目渲染为同角色占位消息 "[已隐藏]"；
//     内部 ToolCallID ↔ provider tool_use_id 的映射存于
//     tool.called.attrs.provider_id（不当主键），编译结果在
//     Result.ProviderMap 中给出。
//
// 编译成功后把 RequestManifest（[(id, blob_hash, revision)] 清单）
// 序列化存入 blobs 表，返回哈希供 llm.requested 事件引用（回放用）。
package compile
