import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { useConnectionStore } from './connection'

export interface Session {
  id: string
  title: string
  renamed: boolean
  provider?: string
  model: string
  inject_agents: boolean
  created_at: string
  updated_at: string
}

const STORAGE_KEY = 'wisp_sessions_cache'
const CURRENT_KEY = 'wisp_current_session'
const AGENTS_DEFAULT_KEY = 'wisp_inject_agents_default'

function loadCached(): Session[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? (JSON.parse(raw) as Session[]).map(item => ({ ...item, inject_agents: item.inject_agents !== false })) : []
  } catch { return [] }
}
function saveCached(list: Session[]): void { localStorage.setItem(STORAGE_KEY, JSON.stringify(list)) }
function loadAgentsDefault(): boolean { return localStorage.getItem(AGENTS_DEFAULT_KEY) !== 'false' }

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>(loadCached())
  const currentSessionId = ref(localStorage.getItem(CURRENT_KEY) || '')
  const defaultInjectAgents = ref(loadAgentsDefault())
  const connection = useConnectionStore()
  const currentSession = computed(() => sessions.value.find(item => item.id === currentSessionId.value) || null)
  const injectAgents = computed(() => currentSession.value ? currentSession.value.inject_agents : defaultInjectAgents.value)

  function setCurrent(id: string): void {
    currentSessionId.value = id
    if (id) localStorage.setItem(CURRENT_KEY, id)
    else localStorage.removeItem(CURRENT_KEY)
  }
  function setDefaultInjectAgents(enabled: boolean): void {
    defaultInjectAgents.value = enabled
    localStorage.setItem(AGENTS_DEFAULT_KEY, String(enabled))
  }

  async function loadSessions(): Promise<void> {
    if (!connection.isConnected) return
    const response = await fetch(connection.api('/api/sessions'), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, `读取会话失败 (${response.status})`))
    const list = await response.json() as Session[]
    sessions.value = (Array.isArray(list) ? list : []).map(item => ({ ...item, inject_agents: item.inject_agents !== false }))
    saveCached(sessions.value)
    if (currentSessionId.value && !sessions.value.some(session => session.id === currentSessionId.value)) setCurrent('')
  }

  async function createPersistedSession(title: string): Promise<Session> {
    const response = await fetch(connection.api('/api/sessions'), {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title, inject_agents: defaultInjectAgents.value }),
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `创建会话失败 (${response.status})`))
    const session = await response.json() as Session
    sessions.value = [session, ...sessions.value.filter(item => item.id !== session.id)]
    saveCached(sessions.value); setCurrent(session.id)
    return session
  }

  function beginNewSession(): void { setCurrent('') }
  function selectSession(id: string): void { setCurrent(id) }

  async function patchSession(id: string, body: Record<string, unknown>): Promise<Session> {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}`), {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `更新会话失败 (${response.status})`))
    const updated = await response.json() as Session
    sessions.value = sessions.value.map(item => item.id === id ? updated : item)
    saveCached(sessions.value)
    return updated
  }

  async function renameSession(id: string, title: string): Promise<void> { await patchSession(id, { title }) }
  async function setAgentsInjection(enabled: boolean): Promise<void> {
    const previousDefault = defaultInjectAgents.value
    setDefaultInjectAgents(enabled)
    const id = currentSessionId.value
    if (!id) return
    try {
      await patchSession(id, { inject_agents: enabled })
    } catch (error) {
      setDefaultInjectAgents(previousDefault)
      throw error
    }
  }
  async function deleteSession(id: string): Promise<void> {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}`), { method: 'DELETE' })
    if (!response.ok) throw new Error(await readHTTPError(response, `删除失败 (${response.status})`))
    if (currentSessionId.value === id) setCurrent('')
    await loadSessions()
  }

  return {
    sessions, currentSessionId, currentSession, defaultInjectAgents, injectAgents,
    loadSessions, createPersistedSession, beginNewSession, selectSession, renameSession,
    setAgentsInjection, deleteSession,
  }
})

async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try { const payload = await response.json() as { error?: string }; return payload.error || fallback } catch { return fallback }
}
