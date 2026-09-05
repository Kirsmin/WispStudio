import { ref } from 'vue'
import { defineStore } from 'pinia'

export interface Session {
  id: string
  title: string
  renamed: boolean
  model: string
  created_at: string
  updated_at: string
}

const sessions = ref<Session[]>([])
const currentSessionId = ref(localStorage.getItem('wisp_current_session_id') || '')

export const useSessionsStore = defineStore('sessions', () => {
  function baseURL(): string {
    return (localStorage.getItem('wisp_server_url') || '').replace(/\/$/, '')
  }

  async function loadSessions(): Promise<void> {
    const base = baseURL()
    if (!base) {
      sessions.value = []
      return
    }
    const response = await fetch(`${base}/api/sessions`)
    if (!response.ok) throw new Error(`读取会话失败 (${response.status})`)
    sessions.value = (await response.json()) as Session[]
    if (currentSessionId.value && !sessions.value.some((item) => item.id === currentSessionId.value)) {
      currentSessionId.value = ''
      localStorage.removeItem('wisp_current_session_id')
    }
  }

  async function createSession(title: string): Promise<Session> {
    const response = await fetch(`${baseURL()}/api/sessions`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(await readError(response, '创建会话失败'))
    const session = (await response.json()) as Session
    sessions.value = [session, ...sessions.value.filter((item) => item.id !== session.id)]
    selectSession(session.id)
    return session
  }

  function selectSession(id: string): void {
    currentSessionId.value = id
    if (id) localStorage.setItem('wisp_current_session_id', id)
    else localStorage.removeItem('wisp_current_session_id')
  }

  function beginNewSession(): void {
    selectSession('')
  }

  async function renameSession(id: string, title: string): Promise<void> {
    const response = await fetch(`${baseURL()}/api/sessions/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(await readError(response, '重命名失败'))
    const target = sessions.value.find((item) => item.id === id)
    if (target) {
      target.title = title
      target.renamed = true
      target.updated_at = new Date().toISOString()
    }
  }

  async function deleteSession(id: string): Promise<void> {
    const response = await fetch(`${baseURL()}/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
    if (!response.ok) throw new Error(await readError(response, '删除会话失败'))
    sessions.value = sessions.value.filter((item) => item.id !== id)
    if (currentSessionId.value === id) selectSession('')
  }

  function clear(): void {
    sessions.value = []
    selectSession('')
  }

  return {
    sessions,
    currentSessionId,
    loadSessions,
    createSession,
    selectSession,
    beginNewSession,
    renameSession,
    deleteSession,
    clear,
  }
})

async function readError(response: Response, fallback: string): Promise<string> {
  try {
    const data = (await response.json()) as { error?: string }
    return data.error || fallback
  } catch {
    return fallback
  }
}
