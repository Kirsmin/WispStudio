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
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : []
  } catch {
    return []
  }
}

function saveCached(list: Session[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(list))
}

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>(loadCached())
  const currentSessionId = ref<string>('')
  const connectionStore = useConnectionStore()

  async function loadSessions() {
    if (!connectionStore.isConnected) return
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions`)
    if (res.ok) {
      sessions.value = await res.json()
      saveCached(sessions.value)
    }
  }

  async function createSession() {
    // 只清当前选中，不立刻建会话
    currentSessionId.value = ''
  }

  async function selectSession(id: string) {
    currentSessionId.value = id
  }

  async function renameSession(id: string, title: string) {
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    if (res.ok) {
      await loadSessions()
    }
  }

  async function deleteSession(id: string) {
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${id}`, {
      method: 'DELETE',
    })
    if (res.ok) {
      if (currentSessionId.value === id) {
        currentSessionId.value = ''
      }
      await loadSessions()
    }
  }

  return {
    sessions,
    currentSessionId,
    loadSessions,
    createSession,
    selectSession,
    renameSession,
    deleteSession,
  }
})
