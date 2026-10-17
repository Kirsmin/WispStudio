<template>
  <aside class="sidebar">
    <button class="new-session" type="button" @click="emit('create')">+ 新会话</button>

    <div class="session-list">
      <div
        v-for="session in sessions.sessions"
        :key="session.ID"
        class="session-row"
        :class="{ active: sessions.currentId === session.ID }"
      >
        <button class="session-title" type="button" :title="session.ID" @click="emit('open', session.ID)">
          {{ sessions.title(session) }}
        </button>
        <button
          v-if="confirmId !== session.ID"
          class="delete-icon"
          type="button"
          title="删除会话"
          @click.stop="confirmId = session.ID"
        >×</button>
        <span v-else class="confirm">
          <button class="danger" type="button" :disabled="deleting" @click.stop="remove(session.ID)">删除</button>
          <button type="button" @click.stop="confirmId = ''">取消</button>
        </span>
      </div>

      <div v-if="sessions.sessions.length === 0" class="session-empty">
        {{ sessions.loading ? '加载中…' : '暂无会话' }}
      </div>
    </div>

    <div v-if="errorText" class="session-error">{{ errorText }}</div>
  </aside>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useChatStore } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'

const emit = defineEmits<{
  create: []
  open: [id: string]
}>()

const sessions = useSessionsStore()
const chat = useChatStore()
const confirmId = ref('')
const deleting = ref(false)
const errorText = ref('')

async function remove(id: string): Promise<void> {
  if (deleting.value) return
  deleting.value = true
  errorText.value = ''
  const wasCurrent = sessions.currentId === id
  try {
    await sessions.remove(id)
    confirmId.value = ''
    if (wasCurrent) chat.startNew()
  } catch (error) {
    errorText.value = error instanceof Error ? error.message : String(error)
  } finally {
    deleting.value = false
  }
}
</script>

<style scoped>
.sidebar {
  width: 236px;
  flex: none;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border);
  background: var(--bg-soft);
  min-height: 0;
}

.new-session {
  margin: 12px 12px 8px;
  padding: 7px 10px;
  border: 1px solid var(--border-strong);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}

.new-session:hover {
  border-color: var(--blue);
  color: var(--blue);
}

.session-list {
  flex: 1;
  overflow-y: auto;
  padding: 2px 8px 12px;
}

.session-row {
  display: flex;
  align-items: center;
  gap: 4px;
  border-left: 2px solid transparent;
  margin-bottom: 1px;
}

.session-row:hover {
  background: #eef0f3;
}

.session-row.active {
  background: #e8edf7;
  border-left-color: var(--blue);
}

.session-title {
  flex: 1;
  min-width: 0;
  padding: 8px 6px 8px 8px;
  border: 0;
  background: transparent;
  color: var(--text-2);
  font-size: 13px;
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
}

.session-row.active .session-title {
  color: var(--text);
}

.delete-icon {
  flex: none;
  width: 22px;
  height: 22px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text-3);
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
  opacity: 0;
}

.session-row:hover .delete-icon,
.session-row.active .delete-icon {
  opacity: 1;
}

.delete-icon:hover {
  color: var(--error);
}

.confirm {
  flex: none;
  display: flex;
  gap: 2px;
  padding-right: 4px;
}

.confirm button {
  padding: 3px 5px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-2);
  font-size: 11px;
  cursor: pointer;
}

.confirm button.danger {
  border-color: var(--error);
  color: var(--error);
}

.session-empty {
  padding: 18px 10px;
  color: var(--text-3);
  font-size: 12px;
}

.session-error {
  padding: 8px 12px;
  border-top: 1px solid var(--border);
  color: var(--error);
  font-size: 11px;
}

@media (max-width: 720px) {
  .sidebar {
    width: 180px;
  }
}

@media (max-width: 520px) {
  .sidebar {
    width: 140px;
  }

  .new-session {
    margin-left: 6px;
    margin-right: 6px;
  }

  .session-list {
    padding-left: 4px;
    padding-right: 4px;
  }
}
</style>
