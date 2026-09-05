import { defineStore } from 'pinia'
import { ref } from 'vue'
import { useConnectionStore } from './connection'

export interface Session {
  id: string
  title: string
  renamed: boolean
  provider?: string
  model: string
  created_at: string
  updated_at: string
}

const STORAGE_KEY = 'wisp_sessions_cache'
const CURRENT_KEY = 'wisp_current_session'

function loadCached(): Session[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) as Session[] : []
  } catch {
    return []
  }
}

function saveCached(list: Session[]): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(list))
}

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>(loadCached())
  const currentSessionId = ref(localStorage.getItem(CURRENT_KEY) || '')
  const connection = useConnectionStore()

  function setCurrent(id: string): void {
    currentSessionId.value = id
    if (id) localStorage.setItem(CURRENT_KEY, id)
    else localStorage.removeItem(CURRENT_KEY)
  }

  async function loadSessions(): Promise<void> {
    if (!connection.isConnected) return
    const response = await fetch(connection.api('/api/sessions'), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, `读取会话失败 (${response.status})`))
    const list = await response.json() as Session[]
    sessions.value = Array.isArray(list) ? list : []
    saveCached(sessions.value)
    if (currentSessionId.value && !sessions.value.some(session => session.id === currentSessionId.value)) {
      setCurrent('')
    }
  }

  async function createPersistedSession(title: string): Promise<Session> {
    const response = await fetch(connection.api('/api/sessions'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `创建会话失败 (${response.status})`))
    const session = await response.json() as Session
    sessions.value = [session, ...sessions.value.filter(item => item.id !== session.id)]
    saveCached(sessions.value)
    setCurrent(session.id)
    return session
  }

  function beginNewSession(): void {
    setCurrent('')
  }

  function selectSession(id: string): void {
    setCurrent(id)
  }

  async function renameSession(id: string, title: string): Promise<void> {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}`), {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `重命名失败 (${response.status})`))
    await loadSessions()
  }

  async function deleteSession(id: string): Promise<void> {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}`), {
      method: 'DELETE',
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `删除失败 (${response.status})`))
    if (currentSessionId.value === id) setCurrent('')
    await loadSessions()
  }

  return {
    sessions,
    currentSessionId,
    loadSessions,
    createPersistedSession,
    beginNewSession,
    selectSession,
    renameSession,
    deleteSession,
  }
})

async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try {
    const payload = await response.json() as { error?: string }
    return payload.error || fallback
  } catch {
    return fallback
  }
}
