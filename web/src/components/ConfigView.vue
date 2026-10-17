<template>
  <section class="config">
    <header class="config-head">
      <div class="config-title">
        <h1>配置</h1>
        <p class="config-path">{{ path || 'Config.local.toml' }}</p>
      </div>
      <div class="config-actions">
        <button type="button" :disabled="loading || saving" @click="load">重新加载</button>
        <button class="primary" type="button" :disabled="!dirty || loading || saving" @click="save">
          {{ saving ? '保存中' : '保存' }}
        </button>
      </div>
    </header>

    <div v-if="errorText" class="config-error">{{ errorText }}</div>
    <div v-else-if="message" class="config-message">{{ message }}</div>

    <textarea
      v-model="text"
      class="config-editor"
      spellcheck="false"
      :disabled="loading"
      @keydown="onKeydown"
    />

    <p class="config-foot">
      保存时由后端校验 TOML 语法与 ProviderID 格式；成功后立即热更新模型目录。配置包含明文密钥，仅保存在本机。
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { APIError, postJSON, request } from '../api/client'
import type { ConfigDTO } from '../api/types'
import { useCatalogStore } from '../stores/catalog'

const catalog = useCatalogStore()
const path = ref('')
const text = ref('')
const original = ref('')
const loading = ref(false)
const saving = ref(false)
const errorText = ref('')
const message = ref('')

const dirty = computed(() => text.value !== original.value)

async function load(): Promise<void> {
  loading.value = true
  errorText.value = ''
  message.value = ''
  try {
    const data = await request<ConfigDTO>('/api/config')
    path.value = data.Path || ''
    text.value = data.Text || ''
    original.value = text.value
  } catch (error) {
    errorText.value = errorTextOf(error)
  } finally {
    loading.value = false
  }
}

async function save(): Promise<void> {
  if (!dirty.value || saving.value) return
  saving.value = true
  errorText.value = ''
  message.value = ''
  try {
    await postJSON('/api/config', { Text: text.value })
    original.value = text.value
    message.value = '已保存，模型目录已热更新。'
    try {
      await catalog.load()
    } catch (error) {
      message.value = `配置已保存，但模型目录刷新失败：${errorTextOf(error)}`
    }
  } catch (error) {
    if (error instanceof APIError && error.line > 0) {
      errorText.value = `第 ${error.line} 行：${error.message}`
    } else {
      errorText.value = errorTextOf(error)
    }
  } finally {
    saving.value = false
  }
}

function onKeydown(event: KeyboardEvent): void {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
    event.preventDefault()
    if (dirty.value && !saving.value) void save()
  }
}

function errorTextOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

onMounted(load)
</script>

<style scoped>
.config {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 22px 24px 18px;
  background: var(--bg);
}

.config-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.config-title h1 {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
  color: var(--text);
}

.config-path {
  margin: 5px 0 0;
  color: var(--text-3);
  font-family: var(--mono);
  font-size: 11px;
  word-break: break-all;
}

.config-actions {
  display: flex;
  gap: 6px;
  flex: none;
}

.config-actions button {
  padding: 5px 11px;
  border: 1px solid var(--border-strong);
  background: var(--bg);
  color: var(--text-2);
  font-size: 12px;
  cursor: pointer;
}

.config-actions button.primary {
  border-color: var(--blue);
  background: var(--blue);
  color: #fff;
}

.config-actions button.primary:hover:not(:disabled) {
  border-color: var(--blue-hover);
  background: var(--blue-hover);
}

.config-actions button:disabled {
  opacity: .4;
  cursor: not-allowed;
}

.config-error,
.config-message {
  margin-bottom: 8px;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
}

.config-error {
  color: var(--error);
}

.config-message {
  color: var(--ok);
}

.config-editor {
  flex: 1;
  min-height: 200px;
  width: 100%;
  padding: 12px;
  border: 1px solid var(--border-strong);
  background: var(--bg-soft);
  color: var(--text);
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.65;
  tab-size: 2;
  resize: none;
  outline: none;
}

.config-editor:focus {
  border-color: var(--blue);
}

.config-foot {
  margin: 10px 0 0;
  color: var(--text-3);
  font-size: 11px;
  line-height: 1.5;
}

@media (max-width: 720px) {
  .config {
    padding-left: 14px;
    padding-right: 14px;
  }
}
</style>
