// 轻量 HTTP 客户端：同源请求，统一解析后端 {"error","line"} 错误。
// 不引入任何 UI / 请求库。

export class APIError extends Error {
  status: number
  line: number

  constructor(message: string, status = 0, line = 0) {
    super(message)
    this.name = 'APIError'
    this.status = status
    this.line = line
  }
}

// responseError 把非 2xx 响应转换为带行号的 APIError。
export async function responseError(response: Response): Promise<APIError> {
  const contentType = response.headers.get('content-type') || ''
  if (contentType.includes('application/json')) {
    try {
      const body = await response.json() as { error?: unknown; line?: unknown }
      const message = typeof body.error === 'string' && body.error.trim()
        ? body.error
        : `请求失败 (${response.status})`
      const line = Number(body.line)
      return new APIError(message, response.status, Number.isFinite(line) && line > 0 ? line : 0)
    } catch {
      // 落到纯文本分支
    }
  }
  const text = (await response.text().catch(() => '')).trim()
  return new APIError(text || `请求失败 (${response.status})`, response.status)
}

// sessionToken 从 index.html 注入的 meta 读取本机访问令牌；未注入时返回空串。
export function sessionToken(): string {
  const meta = document.querySelector('meta[name="wisp-token"]')
  return meta?.getAttribute('content')?.trim() ?? ''
}

// request 执行 JSON 请求；204 或空响应返回 undefined。
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  const token = sessionToken()
  if (token && !headers.has('X-Token')) headers.set('X-Token', token)
  const response = await fetch(path, { cache: 'no-store', ...init, headers })
  if (!response.ok) throw await responseError(response)
  if (response.status === 204) return undefined as T
  const text = await response.text()
  if (!text.trim()) return undefined as T
  return JSON.parse(text) as T
}

// postJSON 是常用的 JSON POST 简写。
export function postJSON<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}
