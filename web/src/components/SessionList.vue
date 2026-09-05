<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NDropdown, NInput } from 'naive-ui'
import { useSessionsStore, type Session } from '../stores/sessions'
import { useChatStore } from '../stores/chat'

const sessions = useSessionsStore()
const chat = useChatStore()
const editingId = ref('')
const editingTitle = ref('')

const menuOptions = [
  { label: '重命名', key: 'rename' },
  { label: '删除', key: 'delete' },
]

function startRename(session: Session): void {
  editingId.value = session.id
  editingTitle.value = session.title
}

async function saveRename(session: Session): Promise<void> {
  const title = editingTitle.value.trim()
  editingId.value = ''
  if (!title || title === session.title) return
  try {
    await sessions.renameSession(session.id, title)
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  }
}

async function remove(session: Session): Promise<void> {
  try {
    const wasCurrent = sessions.currentSessionId === session.id
    await sessions.deleteSession(session.id)
    if (wasCurrent) chat.newConversation()
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  }
}

function onMenu(key: string, session: Session): void {
  if (key === 'rename') startRename(session)
  if (key === 'delete' && window.confirm(`确定删除会话「${session.title}」吗？删除后不可恢复。`)) {
    void remove(session)
  }
}
</script>

<template>
  <aside class="sidebar">
    <div class="brand-row">
      <div class="brand-mark">W</div>
      <strong>WispStudio</strong>
    </div>
    <NButton block secondary type="primary" class="new-button" @click="chat.newConversation">
      <span aria-hidden="true">＋</span> 新会话
    </NButton>

    <div class="section-label">会话</div>
    <div class="session-list">
      <div
        v-for="session in sessions.sessions"
        :key="session.id"
        class="session-row"
        :class="{ active: sessions.currentSessionId === session.id }"
        role="button"
        tabindex="0"
        @click="chat.openSession(session.id)"
        @keydown.enter="chat.openSession(session.id)"
      >
        <div class="session-main">
          <NInput
            v-if="editingId === session.id"
            v-model:value="editingTitle"
            size="small"
            autofocus
            @click.stop
            @keyup.enter.stop="saveRename(session)"
            @keyup.esc.stop="editingId = ''"
            @blur="saveRename(session)"
          />
          <template v-else>
            <span class="session-title" :title="session.title">{{ session.title }}</span>
            <span v-if="sessions.currentSessionId === session.id && chat.isBusy" class="live-dot" title="正在生成"></span>
          </template>
        </div>
        <NDropdown trigger="click" :options="menuOptions" @select="key => onMenu(String(key), session)">
          <NButton quaternary circle size="tiny" class="more" @click.stop>
            <span aria-hidden="true">⋯</span>
          </NButton>
          <template #empty></template>
        </NDropdown>
      </div>
      <div v-if="!sessions.sessions.length" class="no-sessions">暂无历史会话</div>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 250px; min-width: 250px; min-height: 0; display: flex; flex-direction: column;
  padding: 14px 10px; border-right: 1px solid #eee7ea; background: #faf7f8;
}
.brand-row { display: flex; align-items: center; gap: 9px; height: 38px; padding: 0 7px; color: #392f33; }
.brand-mark { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 9px; background: #d95f8d; color: white; font-weight: 800; }
.new-button { margin: 12px 0 17px; }
.section-label { padding: 0 8px 7px; color: #a3989d; font-size: 11px; }
.session-list { min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 3px; }
.session-row {
  appearance: none; width: 100%; min-height: 36px; display: flex; align-items: center; gap: 4px;
  border: 0; border-radius: 9px; padding: 4px 5px 4px 9px; background: transparent; color: #5c5055;
  text-align: left; cursor: pointer;
}
.session-row:hover, .session-row.active { background: #f1e6ea; color: #3d3136; }
.session-main { min-width: 0; flex: 1; display: flex; align-items: center; gap: 7px; }
.session-title { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 13px; }
.more { opacity: 0; }
.session-row:hover .more, .session-row.active .more { opacity: 1; }
.live-dot { width: 6px; height: 6px; flex: 0 0 auto; border-radius: 50%; background: #d95f8d; box-shadow: 0 0 0 3px rgba(217,95,141,.12); }
.no-sessions { padding: 14px 8px; color: #aaa0a4; font-size: 12px; }
</style>
