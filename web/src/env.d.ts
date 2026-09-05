import type { MessageProviderInst } from 'naive-ui'

declare global {
  interface Window { $message?: MessageProviderInst }
}
export {}
