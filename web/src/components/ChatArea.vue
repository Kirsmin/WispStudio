<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { NAlert, NButton } from 'naive-ui'
import Composer from './Composer.vue'
import MessageItem from './MessageItem.vue'
import { useChatStore } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'

const chat = useChatStore()
const sessions = useSessionsStore()
const scroller = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)

function onScroll(): void {
  const el = scroller.value
  if (!el) return
  stickToBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 110
}

async function scrollToBottom(force = false): Promise<void> {
  if (!force && !stickToBottom.value) return
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

watch(
  () => chat.messages.map((item) => `${item.id}:${item.content.length}:${item.reasoning?.length || 0}:${item.status}`).join('|'),
  () => void scrollToBottom(false),
)

watch(
  () => sessions.currentSessionId,
  () => {
    stickToBottom.value = true
    void scrollToBottom(true)
  },
)

onMounted(() => void scrollToBottom(true))
</script>

<template>
  <main class="chat-area">
    <div ref="scroller" class="messages-scroller" @scroll.passive="onScroll">
      <div v-if="chat.notice" class="notice-wrap">
        <NAlert type="info" :bordered="false" closable @close="chat.notice = ''">{{ chat.notice }}</NAlert>
      </div>

      <div v-if="chat.backgroundGenerating" class="notice-wrap">
        <NAlert type="info" :bordered="false">
          这个会话仍在服务端生成。你可以离开或刷新页面，生成不会因此被取消。
          <template #action><NButton size="tiny" @click="chat.stopGeneration">停止</NButton></template>
        </NAlert>
      </div>

      <div v-if="chat.messages.length" class="messages">
        <MessageItem v-for="message in chat.messages" :key="message.id" :message="message" />
      </div>
      <div v-else-if="!chat.backgroundGenerating" class="empty-state">
        <div class="empty-mark">W</div>
        <h2>{{ sessions.currentSessionId ? '这个会话还没有消息' : '开始一段对话' }}</h2>
        <p>{{ sessions.currentSessionId ? '发送第一条消息，内容会实时显示在这里。' : '消息将发送给当前选中的模型' }}</p>
      </div>
    </div>
    <Composer />
  </main>
</template>

<style scoped>
.chat-area { min-width: 0; min-height: 0; display: flex; flex-direction: column; background: #fdfcfc; }
.messages-scroller { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.messages { padding: 16px 0 48px; }
.notice-wrap { width: min(860px, calc(100% - 32px)); margin: 12px auto 0; }
.empty-state {
  min-height: 100%; display: grid; place-content: center; justify-items: center;
  padding: 48px 24px; color: #6d6267; text-align: center;
}
.empty-mark {
  width: 52px; height: 52px; display: grid; place-items: center;
  border-radius: 16px; background: #d95f8d; color: #fff; font-weight: 800; font-size: 20px;
  box-shadow: 0 9px 28px rgba(184, 74, 117, .2);
}
.empty-state h2 { margin: 18px 0 6px; color: #332c2f; font-size: 20px; }
.empty-state p { margin: 0; color: #9b9095; font-size: 13px; }
</style>
