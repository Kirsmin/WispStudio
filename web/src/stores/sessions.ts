import { defineStore } from 'pinia'
import { ref } from 'vue'
import { useConnectionStore } from './connection'

export interface Session {
  id: string
  title: string
  renamed: boolean
  model: string
  created_at: string
  updated_at: string
}

const STORAGE_KEY = 'wisp_sessions_cache'
function loadCached(): Session[] {
  try { return JSON.parse(localStorage.getItem(STORAGE_KEY) || '[]') as Session[] } catch { return [] }
}
function saveCached(list: Session[]): void { localStorage.setItem(STORAGE_KEY, JSON.stringify(list)) }

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>(loadCached())
  const currentSessionId = ref('')
  const connection = useConnectionStore()

  async function loadSessions(): Promise<void> {
    if (!connection.isConnected) return
    const response = await fetch(`${connection.serverUrl}/api/sessions`, { cache: 'no-store' })
    if (!response.ok) throw new Error(`读取会话失败 (${response.status})`)
    sessions.value = await response.json() as Session[]
    saveCached(sessions.value)
  }
  async function createPersistedSession(title: string): Promise<Session> {
    const response = await fetch(`${connection.serverUrl}/api/sessions`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(`创建会话失败 (${response.status})`)
    const session = await response.json() as Session
    currentSessionId.value = session.id
    sessions.value = [session, ...sessions.value.filter(item => item.id !== session.id)]
    saveCached(sessions.value)
    return session
  }
  function beginNewSession(): void { currentSessionId.value = '' }
  function selectSession(id: string): void { currentSessionId.value = id }
  async function renameSession(id: string, title: string): Promise<void> {
    const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(id)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title }),
    })
    if (!response.ok) throw new Error(`重命名失败 (${response.status})`)
    await loadSessions()
  }
  async function deleteSession(id: string): Promise<void> {
    const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
    if (!response.ok) throw new Error(`删除失败 (${response.status})`)
    if (currentSessionId.value === id) currentSessionId.value = ''
    await loadSessions()
  }
  return { sessions, currentSessionId, loadSessions, createPersistedSession, beginNewSession, selectSession, renameSession, deleteSession }
})
