<template>
  <main class="chat">
    <div ref="scroller" class="chat-scroll" @scroll="onScroll">
      <div class="chat-inner">
        <div v-if="visibleItems.length === 0" class="chat-empty">
          <div class="empty-title">开始对话</div>
          <p class="empty-sub">选择模型，输入消息。发送后自动创建会话。</p>
        </div>

        <template v-for="item in visibleItems" :key="item.id">
          <section v-if="item.kind === 'message' && item.role === 'user'" class="msg user">
            <div class="msg-head"><span class="who">你</span></div>
            <div class="user-text">{{ item.content }}</div>
          </section>

          <section v-else-if="item.kind === 'message'" class="msg assistant">
            <div class="msg-head">
              <span class="who">Wisp</span>
              <span v-if="item.streaming" class="msg-live">生成中</span>
            </div>
            <details v-if="item.reasoning" class="reasoning">
              <summary>思考过程 · {{ item.reasoning.length }} 字</summary>
              <pre>{{ item.reasoning }}</pre>
            </details>
            <MarkdownView v-if="item.content" :content="item.content" />
          </section>

          <ToolBlock v-else-if="item.kind === 'tool'" :tool="item" />

          <div v-else class="notice" :class="item.tone">{{ item.text }}</div>
        </template>

        <div v-if="waiting" class="working">
          <span class="working-dot" />
          {{ waitingText }}
        </div>
      </div>
    </div>

    <div v-if="chat.linkState === 'reconnecting' || chat.linkState === 'error'" class="link-banner">
      <span>{{ chat.linkMessage || '事件流连接异常' }}</span>
      <button v-if="chat.linkState === 'error'" type="button" @click="chat.retryNow()">重试</button>
    </div>

    <div class="composer-wrap">
      <div v-if="errorText" class="composer-error">{{ errorText }}</div>
      <div class="composer">
        <textarea
          ref="textarea"
          v-model="draft"
          rows="1"
          :placeholder="placeholder"
          :disabled="sending"
          @keydown="onKeydown"
          @input="resize"
        />
        <div class="composer-bar">
          <label class="model-picker">
            <span class="model-label">模型</span>
            <select v-model="selectedModel" :disabled="modelGroups.length === 0">
              <option v-if="modelGroups.length === 0" value="">无可用模型</option>
              <optgroup
                v-for="group in modelGroups"
                :key="group.provider.ProviderID"
                :label="group.label"
              >
                <option
                  v-for="model in group.models"
                  :key="model.FullID"
                  :value="model.FullID"
                  :title="model.FullID"
                >{{ catalog.label(model) }}</option>
              </optgroup>
            </select>
          </label>

          <span class="composer-hint">Enter 发送 · Shift+Enter 换行</span>

          <button
            v-if="chat.streaming"
            class="send stop"
            type="button"
            :disabled="chat.stopping"
            @click="stop"
          >{{ chat.stopping ? '停止中' : '停止' }}</button>
          <button
            v-else
            class="send"
            type="button"
            :disabled="!canSend"
            @click="send"
          >{{ sending ? '发送中' : '发送' }}</button>
        </div>
      </div>
    </div>
  </main>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { useChatStore } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'
import MarkdownView from './MarkdownView.vue'
import ToolBlock from './ToolBlock.vue'

const MODEL_KEY = 'wisp.model'

const catalog = useCatalogStore()
const sessions = useSessionsStore()
const chat = useChatStore()

const scroller = ref<HTMLDivElement | null>(null)
const textarea = ref<HTMLTextAreaElement | null>(null)
const draft = ref('')
const errorText = ref('')
const sending = ref(false)
const selectedModel = ref(localStorage.getItem(MODEL_KEY) || '')
const stick = ref(true)
let scrollFrame = 0

const visibleItems = computed(() => chat.items.filter((item) => {
  if (item.kind === 'message' && item.role === 'assistant') {
    return Boolean(item.content || item.reasoning)
  }
  return true
}))

const modelGroups = computed(() => catalog.providers
  .filter(provider => provider.Models.length > 0)
  .map(provider => ({
    provider,
    label: catalog.providerLabel(provider),
    models: provider.Models,
  })))

const currentSessionModel = computed(() => sessions.sessions
  .find(session => session.ID === sessions.currentId)?.Model || '')

const waiting = computed(() => chat.streaming && (
  !chat.activeMessage || (!chat.activeMessage.content && !chat.activeMessage.reasoning)
))

const waitingText = computed(() => chat.awaitingApproval ? '等待工具批准…' : '正在处理…')

const placeholder = computed(() => {
  if (modelGroups.value.length === 0) return '请先在配置页配置可用模型'
  if (!sessions.currentId) return '选择模型后输入消息，发送即新建会话'
  return '输入消息…'
})

const canSend = computed(() => Boolean(
  draft.value.trim()
  && selectedModel.value
  && !sending.value
  && !chat.streaming,
))

const scrollSignal = computed(() => {
  const last = chat.items[chat.items.length - 1]
  if (!last) return '0'
  if (last.kind === 'message') return `${chat.items.length}:${last.content.length}:${last.reasoning.length}`
  if (last.kind === 'tool') return `${chat.items.length}:${last.result.length}:${last.approval}:${last.status}`
  return `${chat.items.length}:${last.text.length}`
})

function ensureAvailableModel(): void {
  // 目录尚未成功加载时保留上次选择，避免启动瞬间清空 localStorage 里的模型。
  if (!catalog.loaded) return
  const available = catalog.models
  if (selectedModel.value && available.some(model => model.FullID === selectedModel.value)) return
  const preferred = available.some(model => model.FullID === catalog.defaultModel)
    ? catalog.defaultModel
    : available[0]?.FullID || ''
  selectedModel.value = preferred
  if (preferred) localStorage.setItem(MODEL_KEY, preferred)
}

function syncModelFromSession(): void {
  const model = currentSessionModel.value
  if (model && catalog.models.some(item => item.FullID === model)) {
    selectedModel.value = model
    localStorage.setItem(MODEL_KEY, model)
    return
  }
  ensureAvailableModel()
}

watch(() => sessions.currentId, syncModelFromSession, { immediate: true })
watch(currentSessionModel, syncModelFromSession)
watch(() => catalog.models, syncModelFromSession)
watch(() => catalog.loaded, (loaded) => {
  if (loaded) syncModelFromSession()
})
watch(selectedModel, (value) => {
  if (value) localStorage.setItem(MODEL_KEY, value)
})

function resize(): void {
  const element = textarea.value
  if (!element) return
  element.style.height = 'auto'
  element.style.height = `${Math.min(element.scrollHeight, 200)}px`
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing || event.keyCode === 229) return
  event.preventDefault()
  void send()
}

async function send(): Promise<void> {
  if (!canSend.value) return
  const body = draft.value
  errorText.value = ''
  sending.value = true
  draft.value = ''
  await nextTick()
  resize()
  stick.value = true
  scheduleScroll(true)
  try {
    await chat.send(body, selectedModel.value)
  } catch (error) {
    draft.value = body
    errorText.value = error instanceof Error ? error.message : String(error)
    await nextTick()
    resize()
  } finally {
    sending.value = false
    textarea.value?.focus()
  }
}

async function stop(): Promise<void> {
  errorText.value = ''
  try {
    await chat.stop()
  } catch (error) {
    errorText.value = error instanceof Error ? error.message : String(error)
  }
}

function onScroll(): void {
  const element = scroller.value
  if (!element) return
  stick.value = element.scrollHeight - element.scrollTop - element.clientHeight < 120
}

function scheduleScroll(force = false): void {
  if (!force && !stick.value) return
  if (scrollFrame) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = 0
    const element = scroller.value
    if (element) element.scrollTop = element.scrollHeight
  })
}

watch(scrollSignal, () => scheduleScroll(), { flush: 'post' })
watch(() => sessions.currentId, () => {
  stick.value = true
  void nextTick(() => scheduleScroll(true))
})
watch(draft, () => {
  void nextTick(resize)
})

onMounted(() => {
  resize()
  scheduleScroll(true)
})
</script>

<style scoped>
.chat {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--bg);
}

.chat-scroll {
  flex: 1;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.chat-inner {
  max-width: 820px;
  margin: 0 auto;
  padding: 28px 24px 24px;
}

.chat-empty {
  padding: 96px 0 48px;
}

.empty-title {
  color: var(--text);
  font-size: 18px;
  font-weight: 600;
  margin-bottom: 8px;
}

.empty-sub {
  margin: 0;
  color: var(--text-2);
  font-size: 13px;
}

.msg {
  margin-bottom: 26px;
}

.msg-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.who {
  color: var(--text-3);
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: .04em;
  text-transform: uppercase;
}

.msg-live {
  color: var(--blue);
  font-size: 11px;
}

.msg-live::after {
  content: '';
  display: inline-block;
  width: 5px;
  height: 5px;
  margin-left: 5px;
  vertical-align: 1px;
  background: currentColor;
  animation: blink 1s step-end infinite;
}

.user-text {
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text);
  font-size: 14px;
  line-height: 1.7;
  padding-left: 12px;
  border-left: 2px solid var(--border-strong);
}

.reasoning {
  margin: 0 0 10px;
  border: 1px solid var(--border);
  background: var(--bg-soft);
}

.reasoning summary {
  padding: 6px 9px;
  color: var(--text-3);
  font-size: 12px;
  cursor: pointer;
  user-select: none;
}

.reasoning pre {
  margin: 0;
  padding: 9px;
  border-top: 1px solid var(--border);
  color: var(--text-2);
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 260px;
  overflow: auto;
}

.notice {
  margin: 0 0 18px;
  color: var(--text-3);
  font-size: 12px;
  padding-left: 12px;
  border-left: 2px solid var(--border);
}

.notice.error {
  color: var(--error);
  border-left-color: var(--error);
}

.working {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-3);
  font-size: 12px;
  padding: 4px 0 18px;
}

.working-dot {
  width: 6px;
  height: 6px;
  background: var(--blue);
  animation: blink 1s step-end infinite;
}

.link-banner {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 6px 12px;
  border-top: 1px solid var(--border);
  background: #fbf7ed;
  color: var(--warn);
  font-size: 12px;
}

.link-banner button {
  border: 0;
  background: transparent;
  color: var(--blue);
  cursor: pointer;
  text-decoration: underline;
  font-size: 12px;
}

.composer-wrap {
  padding: 10px 24px 20px;
  background: var(--bg);
}

.composer {
  max-width: 820px;
  margin: 0 auto;
  border: 1px solid var(--border-strong);
  background: var(--bg);
  padding: 10px 12px 8px;
}

.composer:focus-within {
  border-color: var(--blue);
}

.composer textarea {
  display: block;
  width: 100%;
  min-height: 24px;
  max-height: 200px;
  margin: 0;
  padding: 0;
  border: 0;
  outline: 0;
  resize: none;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 14px;
  line-height: 1.6;
}

.composer textarea::placeholder {
  color: var(--text-3);
}

.composer-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}

.model-picker {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.model-label {
  color: var(--text-3);
  font-size: 11px;
}

.model-picker select {
  max-width: 260px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-2);
  font-size: 12px;
  padding: 3px 5px;
  outline: 0;
}

.model-picker select:focus {
  border-color: var(--blue);
}

.composer-hint {
  margin-left: auto;
  color: var(--text-3);
  font-size: 11px;
  white-space: nowrap;
}

.send {
  flex: none;
  min-width: 62px;
  padding: 5px 13px;
  border: 1px solid var(--blue);
  background: var(--blue);
  color: #fff;
  font-size: 13px;
  cursor: pointer;
}

.send:hover:not(:disabled) {
  border-color: var(--blue-hover);
  background: var(--blue-hover);
}

.send:disabled {
  opacity: .38;
  cursor: not-allowed;
}

.send.stop {
  border-color: var(--border-strong);
  background: var(--bg);
  color: var(--text-2);
}

.send.stop:hover:not(:disabled) {
  border-color: var(--error);
  color: var(--error);
  background: var(--bg);
}

.composer-error {
  max-width: 820px;
  margin: 0 auto 8px;
  color: var(--error);
  font-size: 12px;
}

@keyframes blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0; }
}

@media (max-width: 720px) {
  .chat-inner,
  .composer-wrap {
    padding-left: 14px;
    padding-right: 14px;
  }

  .composer-hint {
    display: none;
  }
}
</style>
