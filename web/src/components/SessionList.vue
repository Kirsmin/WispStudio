<template>
  <div class="session-list" :class="{ disabled: !isConnected }">
    <div class="session-header">
      <n-button class="new-btn" type="primary" :disabled="!isConnected" @click="createSession">
        <template #icon>
          <span class="plus">+</span>
        </template>
        新会话
      </n-button>
    </div>
    <div class="session-items">
      <div
        v-for="session in sessions"
        :key="session.id"
        class="session-item"
        :class="{ active: currentSessionId === session.id }"
        @click="selectSession(session.id)"
      >
        <span class="session-title">{{ session.title }}</span>
        <n-dropdown
          :options="menuOptions"
          trigger="click"
          @select="(key: string) => handleMenuSelect(key, session)"
        >
          <span class="menu-btn" @click.stop>···</span>
        </n-dropdown>
      </div>
    </div>

    <!-- 重命名对话框 -->
    <n-modal v-model:show="showRenameModal" title="重命名会话" preset="card" style="width: 380px">
      <n-input v-model:value="renameValue" placeholder="新标题" @keydown.enter="confirmRename" />
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showRenameModal = false">取消</n-button>
          <n-button type="primary" @click="confirmRename">保存</n-button>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { NButton, NDropdown, NModal, NInput, useDialog, useMessage } from 'naive-ui'
import { ref } from 'vue'
import { useSessionsStore } from '../stores/sessions'
import { useConnectionStore } from '../stores/connection'
import { storeToRefs } from 'pinia'

const sessionsStore = useSessionsStore()
const connectionStore = useConnectionStore()
const { sessions, currentSessionId } = storeToRefs(sessionsStore)
const { isConnected } = storeToRefs(connectionStore)
const { createSession, selectSession, renameSession, deleteSession } = sessionsStore

const dialog = useDialog()
const message = useMessage()

const menuOptions = [
  { label: '重命名', key: 'rename' },
  { label: '删除', key: 'delete' },
]

const showRenameModal = ref(false)
const renameValue = ref('')
const renameTargetId = ref('')

function handleMenuSelect(key: string, session: any) {
  if (key === 'rename') {
    renameTargetId.value = session.id
    renameValue.value = session.title
    showRenameModal.value = true
  } else if (key === 'delete') {
    dialog.warning({
      title: '确认删除',
      content: `确定要删除会话「${session.title}」吗？`,
      positiveText: '删除',
      negativeText: '取消',
      onPositiveClick: async () => {
        await deleteSession(session.id)
        message.success('已删除')
      },
    })
  }
}

async function confirmRename() {
  if (renameValue.value.trim()) {
    await renameSession(renameTargetId.value, renameValue.value.trim())
    showRenameModal.value = false
    message.success('已重命名')
  }
}
</script>

<style scoped>
.session-list {
  width: 248px;
  border-right: 1px solid var(--border);
  background: var(--bg);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
}

.session-list.disabled {
  opacity: 0.5;
  pointer-events: none;
}

.session-header {
  padding: 14px 14px 10px;
}

.new-btn {
  width: 100%;
  font-weight: 500;
}

.plus {
  font-size: 16px;
  line-height: 1;
}

.session-items {
  flex: 1;
  overflow-y: auto;
  padding: 4px 10px 10px;
}

.session-item {
  padding: 9px 12px;
  border-radius: 10px;
  cursor: pointer;
  margin-bottom: 2px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  overflow: hidden;
  color: var(--text);
}

.session-item:hover {
  background: var(--accent-tint);
}

.session-item.active {
  background: var(--accent-soft);
  color: var(--accent-text);
  font-weight: 500;
}

.session-title {
  font-size: 14px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
}

.menu-btn {
  opacity: 0;
  font-size: 12px;
  padding: 2px 6px;
  cursor: pointer;
  border-radius: 6px;
  color: var(--text-2);
}

.menu-btn:hover {
  color: var(--accent-text);
}

.session-item:hover .menu-btn {
  opacity: 1;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
