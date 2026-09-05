// API 客户端封装
const BASE_URL = '' // 使用相对路径，vite proxy 处理

export async function fetchHealth(baseUrl: string) {
  const res = await fetch(`${baseUrl}/api/health`)
  return res.ok
}

export async function fetchModels(baseUrl: string) {
  const res = await fetch(`${baseUrl}/api/models`)
  if (!res.ok) return []
  return res.json()
}

export async function fetchSessions(baseUrl: string) {
  const res = await fetch(`${baseUrl}/api/sessions`)
  if (!res.ok) return []
  return res.json()
}

export async function createSession(baseUrl: string, title: string) {
  const res = await fetch(`${baseUrl}/api/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
  if (!res.ok) return null
  return res.json()
}

export async function renameSession(baseUrl: string, id: string, title: string) {
  const res = await fetch(`${baseUrl}/api/sessions/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
  return res.ok
}

export async function deleteSession(baseUrl: string, id: string) {
  const res = await fetch(`${baseUrl}/api/sessions/${id}`, {
    method: 'DELETE',
  })
  return res.ok
}

export async function fetchMessages(baseUrl: string, sessionId: string) {
  const res = await fetch(`${baseUrl}/api/sessions/${sessionId}/messages`)
  if (!res.ok) return []
  return res.json()
}
