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

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>([])
  const currentSessionId = ref<string>('')
  const connectionStore = useConnectionStore()

  async function loadSessions() {
    if (!connectionStore.isConnected) return
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions`)
    if (res.ok) {
      sessions.value = await res.json()
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
