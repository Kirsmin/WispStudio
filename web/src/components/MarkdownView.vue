<template>
  <MdPreview
    :id="previewId"
    :model-value="displayContent"
    class="md"
    preview-theme="github"
    code-theme="github"
    :sanitize="sanitize"
    :show-code-row-number="false"
    :code-foldable="false"
    :no-mermaid="true"
    :no-echarts="true"
    :no-img-zoom-in="true"
  />
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { MdPreview } from 'md-editor-v3'
import DOMPurify from 'dompurify'
import 'md-editor-v3/lib/preview.css'

const props = defineProps<{
  content: string
}>()

const previewId = 'mdp-' + Math.random().toString(36).slice(2, 9)
const sanitize = (html: string) => DOMPurify.sanitize(html)

const displayContent = ref(props.content || '')
let pendingContent = props.content || ''
let renderTimer: ReturnType<typeof setTimeout> | null = null
let lastRenderAt = 0

// 关键修复：这里必须是 throttle，而不是 debounce。
// 旧代码每个 token 都 clearTimeout + 重新 80ms，模型持续以 <80ms 间隔吐 token 时，
// Markdown 永远不会更新，直到模型停下来才一次性显示，正是“思考中/等待响应一直不动”的根因。
// 32ms ≈ 30fps：足够像打字机，同时不会每个 token 都做完整 Markdown parse。
const RENDER_INTERVAL_MS = 32

function renderNow() {
  renderTimer = null
  displayContent.value = pendingContent
  lastRenderAt = Date.now()
}

watch(() => props.content, (newContent) => {
  pendingContent = newContent || ''
  const elapsed = Date.now() - lastRenderAt

  if (renderTimer === null && elapsed >= RENDER_INTERVAL_MS) {
    renderNow()
    return
  }

  if (renderTimer === null) {
    renderTimer = setTimeout(renderNow, Math.max(0, RENDER_INTERVAL_MS - elapsed))
  }
}, { immediate: true })

onBeforeUnmount(() => {
  if (renderTimer !== null) clearTimeout(renderTimer)
})
</script>

<style scoped>
.md {
  --md-color: var(--text);
  --md-bk-color: transparent;
  --md-bk-color-outstand: var(--bg-soft);
  --md-border-color: var(--border);
  --md-border-hover-color: var(--border-strong);
  --md-border-active-color: var(--blue);
  --md-bk-hover-color: var(--bg-soft);
  --md-hover-color: var(--text-2);
  font-size: 14px;
  line-height: 1.72;
  background: transparent;
}

.md :deep(.md-editor-preview-wrapper) {
  padding: 0;
}

.md :deep(p) {
  margin: 0 0 9px 0;
}

.md :deep(p:last-child) {
  margin-bottom: 0;
}

.md :deep(h1),
.md :deep(h2),
.md :deep(h3),
.md :deep(h4) {
  color: var(--text);
  line-height: 1.35;
  margin: 18px 0 8px;
}

.md :deep(table) {
  display: table;
  width: max-content;
  max-width: 100%;
  overflow: auto;
}

.md :deep(pre) {
  border: 1px solid var(--border);
  background: var(--bg-soft);
}

.md :deep(code) {
  font-family: var(--mono);
  font-size: .92em;
}

.md :deep(a) {
  color: var(--blue);
}

.md :deep(blockquote) {
  border-left: 3px solid var(--border-strong);
  color: var(--text-2);
  padding-left: 12px;
  margin-left: 0;
}
</style>
