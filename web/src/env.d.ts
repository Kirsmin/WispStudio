// 全局类型声明
import type { MessageProviderInst } from 'naive-ui'

declare global {
  interface Window {
    // 全局消息实例（由 NMessageProvider 挂载）
    $message?: MessageProviderInst
  }
}

export {}
