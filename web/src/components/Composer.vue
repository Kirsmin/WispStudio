<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NSelect } from 'naive-ui'
import { useChatStore } from '../stores/chat'
import { useConnectionStore } from '../stores/connection'

const chat = useChatStore()
const connection = useConnectionStore()
const composing = ref(false)

const modelOptions = computed(() => connection.models.map((model) => ({ label: model.name || model.id, value: model.id })))
const labels: Record<string, string> = { off: '关闭思考', auto: '自动思考', low: '低', medium: '中', high: '高' }
const thinkingOptions = computed(() => chat.thinkingOptions.map((value) => ({ label: labels[value] || value, value })))
const placeholder = computed(() => {
  if (!connection.isConnected) return '请先连接后端服务'
  if (chat.backgroundGenerating) return '该会话正在后台生成，可先停止后再发送'
  return '输入消息，Enter 发送，Shift+Enter 换行'
})

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' || event.shiftKey || composing.value) return
  event.preventDefault()
  void chat.sendMessage()
}
</script>

<template>
  <div class="composer-wrap">
    <div class="composer" :class="{ busy: chat.isBusy }">
      <textarea
        v-model="chat.input"
        rows="1"
        :placeholder="placeholder"
        :disabled="!connection.isConnected || chat.backgroundGenerating"
        @keydown="onKeydown"
        @compositionstart="composing = true"
        @compositionend="composing = false"
      />
      <div class="composer-toolbar">
        <div class="selectors">
          <NSelect
            v-model:value="chat.selectedModel"
            size="small"
            :options="modelOptions"
            :disabled="chat.isBusy || !connection.isConnected"
            class="model-select"
          />
          <NSelect
            v-if="thinkingOptions.length > 1 || thinkingOptions[0]?.value !== 'off'"
            v-model:value="chat.selectedThinking"
            size="small"
            :options="thinkingOptions"
            :disabled="chat.isBusy"
            class="thinking-select"
          />
        </div>
        <NButton
          circle
          size="small"
          :type="chat.isBusy ? 'error' : 'primary'"
          :disabled="!chat.isBusy && (!chat.input.trim() || !connection.isConnected || !chat.selectedModel)"
          :aria-label="chat.isBusy ? '停止生成' : '发送'"
          @click="chat.isBusy ? chat.stopGeneration() : chat.sendMessage()"
        >
          <span aria-hidden="true">{{ chat.isBusy ? '■' : '↑' }}</span>
        </NButton>
      </div>
    </div>
    <div class="composer-hint">WispStudio 可能会犯错，请核对重要信息。</div>
  </div>
</template>

<style scoped>
.composer-wrap { width: min(880px, calc(100% - 28px)); margin: 0 auto; padding: 10px 0 14px; }
.composer {
  border: 1px solid #e5dce0;
  border-radius: 16px;
  background: #fff;
  padding: 10px 10px 8px;
  box-shadow: 0 8px 28px rgba(78, 48, 59, .07);
  transition: border-color .15s, box-shadow .15s;
}
.composer:focus-within { border-color: #dca4ba; box-shadow: 0 8px 30px rgba(159, 83, 113, .1); }
textarea {
  width: 100%; min-height: 34px; max-height: 180px; resize: none;
  border: 0; outline: 0; padding: 2px 5px 7px; color: #2f292c;
  font: inherit; line-height: 1.55; background: transparent;
  field-sizing: content;
}
textarea::placeholder { color: #aaa1a5; }
.composer-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.selectors { display: flex; gap: 6px; min-width: 0; }
.model-select { width: min(240px, 42vw); }
.thinking-select { width: 112px; }
.composer-hint { text-align: center; font-size: 10px; color: #aaa1a5; margin-top: 7px; }
@media (max-width: 600px) {
  .composer-wrap { width: calc(100% - 16px); }
  .thinking-select { width: 100px; }
  .model-select { width: 46vw; }
}
</style>
