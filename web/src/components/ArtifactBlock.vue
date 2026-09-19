<template>
  <div class="artifact-block">
    <div class="head">
      <div>
        <span class="kind">Artifact</span>
        <strong>{{ artifact.name }}</strong>
        <span class="type">{{ renderer.label }}</span>
      </div>
      <n-select
        v-if="renderer.versionPicker && artifact.versions.length > 1"
        :value="artifact.active_version"
        :options="versionOptions"
        size="tiny"
        class="version-select"
        @update:value="activate"
      />
      <span v-else class="version">V{{ artifact.active_version }}</span>
    </div>
    <MarkdownView v-if="renderer.markdown && activeVersion?.content" :content="activeVersion.content" />
    <pre v-else-if="activeVersion?.content" class="raw-content">{{ activeVersion.content }}</pre>
    <div v-else class="empty">当前版本没有文本内容</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { artifactRenderer } from '../artifactRenderers'
import { NSelect } from 'naive-ui'
import type { Artifact } from '../stores/chat'
import { useChatStore } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ artifact: Artifact }>()
const chatStore = useChatStore()
const renderer = computed(() => artifactRenderer(props.artifact.type))
const activeVersion = computed(() => props.artifact.versions.find(item => item.version === props.artifact.active_version))
const versionOptions = computed(() => props.artifact.versions.map(item => ({ label: `V${item.version}`, value: item.version })).reverse())
function activate(version: number) { void chatStore.activateArtifact(props.artifact.id, version) }
</script>

<style scoped>
.artifact-block { margin: 0 0 14px; padding: 13px 14px; border: 1px solid var(--border-strong); border-radius: 14px; background: var(--bg); }
.head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.head > div { display: flex; align-items: center; gap: 8px; min-width: 0; }
.kind, .type, .version { font-size: 11px; color: var(--text-3); }
.kind { text-transform: uppercase; letter-spacing: .08em; }
.type { padding: 2px 6px; border-radius: 999px; background: var(--bg-soft); }
.version-select { width: 82px; }
.empty { color: var(--text-3); font-size: 13px; }
.raw-content { margin: 0; white-space: pre-wrap; word-break: break-word; font-size: 12px; }
</style>
