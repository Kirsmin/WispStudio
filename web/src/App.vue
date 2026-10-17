<template>
  <div class="app">
    <header class="topbar">
      <button class="brand" type="button" @click="view = 'chat'">
        <img src="/logo.svg" alt="" width="18" height="18" />
        <span>Wisp</span>
      </button>

      <div class="topbar-right">
        <span class="link-state" :class="chat.linkState">
          <span class="dot" />{{ linkLabel }}
        </span>
        <nav class="view-nav">
          <button type="button" :class="{ active: view === 'chat' }" @click="view = 'chat'">对话</button>
          <button type="button" :class="{ active: view === 'config' }" @click="view = 'config'">配置</button>
        </nav>
      </div>
    </header>

    <div v-if="initError" class="init-error">
      <span>{{ initError }}</span>
      <button type="button" @click="bootstrap">重试</button>
    </div>

    <div class="shell">
      <Sidebar @create="createSession" @open="openSession" />
      <ChatView v-show="view === 'chat'" />
      <ConfigView v-if="view === 'config'" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import ChatView from './components/ChatView.vue'
import ConfigView from './components/ConfigView.vue'
import Sidebar from './components/Sidebar.vue'
import { useCatalogStore } from './stores/catalog'
import { useChatStore } from './stores/chat'
import { useSessionsStore } from './stores/sessions'

const catalog = useCatalogStore()
const sessions = useSessionsStore()
const chat = useChatStore()

const view = ref<'chat' | 'config'>('chat')
const initError = ref('')

const linkLabel = computed(() => {
  switch (chat.linkState) {
    case 'open': return '已连接'
    case 'connecting': return '连接中'
    case 'reconnecting': return '重连中'
    case 'error': return '连接失败'
    default: return '就绪'
  }
})

async function bootstrap(): Promise<void> {
  initError.value = ''
  const results = await Promise.allSettled([catalog.load(), sessions.load()])
  const failures = results
    .filter((result): result is PromiseRejectedResult => result.status === 'rejected')
    .map(result => result.reason instanceof Error ? result.reason.message : String(result.reason))
  if (failures.length > 0) initError.value = failures.join('；')
  if (sessions.currentId) chat.openSession(sessions.currentId)
}

function createSession(): void {
  view.value = 'chat'
  chat.startNew()
}

function openSession(id: string): void {
  view.value = 'chat'
  chat.openSession(id)
}

onMounted(bootstrap)
</script>

<style scoped>
.app {
  height: 100vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--bg);
}

.topbar {
  flex: none;
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 14px;
  border-bottom: 1px solid var(--border);
  background: var(--bg);
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border: 0;
  background: transparent;
  color: var(--text);
  font-size: 15px;
  font-weight: 650;
  letter-spacing: .01em;
  cursor: pointer;
}

.brand img {
  display: block;
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 18px;
}

.link-state {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--text-3);
  font-size: 11px;
  white-space: nowrap;
}

.link-state .dot {
  width: 6px;
  height: 6px;
  background: var(--text-3);
}

.link-state.open .dot { background: var(--ok); }
.link-state.connecting .dot { background: var(--blue); }
.link-state.reconnecting .dot { background: var(--warn); }
.link-state.error .dot { background: var(--error); }

.view-nav {
  display: flex;
  gap: 2px;
}

.view-nav button {
  padding: 5px 11px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--text-2);
  font-size: 12px;
  cursor: pointer;
}

.view-nav button:hover {
  color: var(--text);
}

.view-nav button.active {
  border-color: var(--border-strong);
  color: var(--text);
}

.init-error {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 6px 12px;
  border-bottom: 1px solid #e5c4c4;
  background: #fdf3f3;
  color: var(--error);
  font-size: 12px;
}

.init-error button {
  border: 0;
  background: transparent;
  color: var(--error);
  text-decoration: underline;
  cursor: pointer;
  font-size: 12px;
}

.shell {
  flex: 1;
  min-height: 0;
  display: flex;
}
</style>
