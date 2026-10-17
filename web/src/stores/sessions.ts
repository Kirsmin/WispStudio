import { ref } from 'vue'
import { defineStore } from 'pinia'
import { postJSON, request } from '../api/client'
import type { SessionDTO } from '../api/types'

const CURRENT_KEY = 'wisp.currentSession'

// 会话列表：只保留新建、选择和删除三件事。
export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<SessionDTO[]>([])
  const currentId = ref(localStorage.getItem(CURRENT_KEY) || '')
  const loading = ref(false)
  const error = ref('')

  function select(id: string): void {
    currentId.value = id
    if (id) localStorage.setItem(CURRENT_KEY, id)
    else localStorage.removeItem(CURRENT_KEY)
  }

  function clearCurrent(): void {
    select('')
  }

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const list = await request<SessionDTO[]>('/api/sessions')
      sessions.value = Array.isArray(list) ? list : []
      if (currentId.value && !sessions.value.some(session => session.ID === currentId.value)) {
        clearCurrent()
      }
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause)
      throw cause
    } finally {
      loading.value = false
    }
  }

  async function create(model: string): Promise<SessionDTO> {
    const created = await postJSON<SessionDTO>('/api/sessions', {
      Title: '新会话',
      Model: model || undefined,
    })
    sessions.value = [created, ...sessions.value.filter(session => session.ID !== created.ID)]
    return created
  }

  async function remove(id: string): Promise<void> {
    await request<void>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
    sessions.value = sessions.value.filter(session => session.ID !== id)
    if (currentId.value === id) clearCurrent()
  }

  function title(session: SessionDTO): string {
    return (session.Title || '').trim() || '新会话'
  }

  return {
    sessions,
    currentId,
    loading,
    error,
    select,
    clearCurrent,
    load,
    create,
    remove,
    title,
  }
})
