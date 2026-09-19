<template>
  <div class="subagent">
    <button class="head" type="button" @click="toggle">
      <span class="agent-icon">↳</span>
      <span class="name">{{ run.profile_id }}</span>
      <span class="status">{{ run.status }}</span>
      <span class="chevron">{{ expanded ? '▼' : '▶' }}</span>
    </button>
    <div v-if="resultText" class="summary">{{ resultText }}</div>
    <div v-if="expanded" class="details">
      <div v-if="loading" class="muted">读取 Child Timeline…</div>
      <div v-else-if="records.length === 0" class="muted">没有可显示的 Child Timeline。</div>
      <template v-else>
        <div v-for="record in records" :key="record.id" class="child-item">
          <span>{{ record.kind }}</span>
          <div v-if="record.content" class="child-content">{{ record.content }}</div>
          <pre v-else-if="Object.keys(record.data || {}).length">{{ JSON.stringify(record.data, null, 2) }}</pre>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { AgentRun, TimelineRecord } from '../stores/chat'
import { useChatStore } from '../stores/chat'

const props = defineProps<{ run: AgentRun }>()
const chatStore = useChatStore()
const expanded = ref(false)
const loading = ref(false)
const records = ref<TimelineRecord[]>([])
const loaded = ref(false)
const resultText = computed(() => {
  const result = props.run.result || {}
  return String(result.summary || result.message || result.error || '').trim()
})
async function toggle() {
  expanded.value = !expanded.value
  if (!expanded.value || loaded.value) return
  loading.value = true
  try { records.value = await chatStore.loadAgentTimeline(props.run.id); loaded.value = true }
  catch (error) { window.$message?.error(error instanceof Error ? error.message : String(error)) }
  finally { loading.value = false }
}
</script>

<style scoped>
.subagent { margin: 0 0 14px; border: 1px solid var(--border); border-radius: 12px; background: var(--bg-soft); overflow: hidden; }
.head { width: 100%; border: 0; background: transparent; color: var(--text); display: flex; align-items: center; gap: 8px; padding: 10px 12px; cursor: pointer; text-align: left; }
.agent-icon { color: var(--accent-text); }
.name { font-weight: 600; text-transform: capitalize; }
.status { margin-left: auto; font-size: 11px; color: var(--text-3); }
.chevron { font-size: 10px; color: var(--text-3); }
.summary { padding: 0 12px 10px 30px; color: var(--text-2); font-size: 13px; white-space: pre-wrap; }
.details { border-top: 1px solid var(--border); padding: 8px 12px 10px; }
.child-item { padding: 7px 0; font-size: 11px; color: var(--text-3); border-bottom: 1px dashed var(--border); }
.child-item:last-child { border-bottom: 0; }
.child-content { margin-top: 4px; color: var(--text-2); font-size: 12px; white-space: pre-wrap; }
.child-item pre { margin: 4px 0 0; max-height: 180px; overflow: auto; white-space: pre-wrap; word-break: break-word; color: var(--text-2); font-size: 11px; }
.muted { color: var(--text-3); font-size: 12px; padding: 5px 0; }
</style>
