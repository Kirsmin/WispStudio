// 包 provider 实现 Wisp 的模型服务商层：模型目录、自动发现与 OpenAI 兼容 HTTP 客户端。
//
// 分层约定：
//   - 本包只返回结构化结果（Result / Model / Catalog），不感知事件；
//     llm.requested / llm.responded 事件由 runtime 在调用前后写入。
//   - reasoning 相关字段（reasoning_effort 请求 / reasoning_content 响应）
//     原样透传，本包不做任何 provider 私有格式转换。
//   - 请求/响应边界一律使用本包的强类型 struct，不出现 map[string]any。
package provider
