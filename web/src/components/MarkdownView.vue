<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { MdPreview } from 'md-editor-v3'
import DOMPurify from 'dompurify'
import 'md-editor-v3/lib/preview.css'

const props = defineProps<{ content: string }>()
const previewId = `mdp-${Math.random().toString(36).slice(2, 9)}`
const rendered = ref(props.content)
let timer: ReturnType<typeof setTimeout> | null = null
let lastRender = 0
const FRAME_MS = 48
const sanitize = (html: string) => DOMPurify.sanitize(html)

function renderNow(value: string): void {
  rendered.value = value
  lastRender = performance.now()
}

// 真节流而不是 debounce：高频 token 也会持续刷新 Markdown。
watch(
  () => props.content,
  (value) => {
    const elapsed = performance.now() - lastRender
    if (elapsed >= FRAME_MS) {
      if (timer) clearTimeout(timer)
      timer = null
      renderNow(value)
      return
    }
    if (timer) return
    timer = setTimeout(() => {
      timer = null
      renderNow(props.content)
    }, Math.max(0, FRAME_MS - elapsed))
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <MdPreview
    :id="previewId"
    :model-value="rendered"
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

<style scoped>
.md {
  --md-color: #332c2f;
  --md-bk-color: transparent;
  --md-theme: #d95f8d;
  --md-border-color: #eadfe4;
  --md-bk-color-outstand: #faf7f8;
  font-size: 15px;
  line-height: 1.72;
  background: transparent;
}
.md :deep(.md-editor-preview-wrapper) { padding: 0; }
.md :deep(p) { margin: 0 0 9px; }
.md :deep(p:last-child) { margin-bottom: 0; }
.md :deep(pre) { border: 1px solid #eadfe4; border-radius: 10px; }
.md :deep(code) { font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace; }
</style>
