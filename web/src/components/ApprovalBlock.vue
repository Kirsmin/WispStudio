<template>
  <div class="approval" :class="approval.status">
    <div class="head">
      <div>
        <span class="eyebrow">需要确认</span>
        <strong>{{ approval.tool_name }}</strong>
      </div>
      <n-tag size="small" :type="statusType" :bordered="false">{{ statusLabel }}</n-tag>
    </div>
    <div class="risk">{{ approval.risk || '该操作需要用户确认。' }}</div>
    <details class="args-wrap">
      <summary>查看参数</summary>
      <pre class="args">{{ prettyArgs }}</pre>
    </details>
    <div v-if="approval.status === 'pending'" class="actions">
      <n-button v-if="capabilities.can_reject" size="small" @click="decide('reject')">拒绝</n-button>
      <n-button v-if="capabilities.can_challenge" size="small" @click="showChallenge = !showChallenge">询问原因</n-button>
      <n-dropdown v-if="capabilities.can_approve" trigger="click" :options="approvalOptions" @select="approveScope">
        <n-button size="small" type="primary">批准…</n-button>
      </n-dropdown>
    </div>
    <div v-if="showChallenge && approval.status === 'pending'" class="challenge">
      <n-input v-model:value="question" size="small" placeholder="为什么需要执行这个操作？" @keyup.enter="challenge" />
      <n-button size="small" :disabled="!question.trim()" @click="challenge">发送</n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NDropdown, NInput, NTag } from 'naive-ui'
import type { Approval } from '../stores/chat'
import { useChatStore } from '../stores/chat'

const props = defineProps<{ approval: Approval }>()
const chatStore = useChatStore()
const capabilities = computed(() => chatStore.capabilities)
const showChallenge = ref(false)
const question = ref('为什么需要执行这个操作？')
const prettyArgs = computed(() => JSON.stringify(props.approval.args || {}, null, 2))
const statusLabels: Record<string, string> = { pending: '待确认', approved: '已批准', rejected: '已拒绝' }
const statusLabel = computed(() => statusLabels[props.approval.status] || props.approval.status)
const statusType = computed<'warning' | 'success' | 'error' | 'default'>(() => props.approval.status === 'pending' ? 'warning' : props.approval.status === 'approved' ? 'success' : props.approval.status === 'rejected' ? 'error' : 'default')
const approvalOptions = [
  { label: '仅批准这一次', key: 'once' },
  { label: '本阶段允许同类操作', key: 'phase' },
  { label: '本轮允许同类操作', key: 'turn' },
]
async function decide(decision: 'approve' | 'reject', scope: 'once' | 'phase' | 'turn' = 'once') {
  try { await chatStore.decideApproval(props.approval.id, decision, scope) } catch (error) { window.$message?.error(error instanceof Error ? error.message : String(error)) }
}
function approveScope(value: string | number) { const scope = String(value) as 'once' | 'phase' | 'turn'; void decide('approve', scope) }
async function challenge() {
  const value = question.value.trim(); if (!value) return
  try { await chatStore.challengeApproval(props.approval.id, value); showChallenge.value = false } catch (error) { window.$message?.error(error instanceof Error ? error.message : String(error)) }
}
</script>

<style scoped>
.approval { margin: 0 0 14px; padding: 13px 14px; border: 1px solid var(--border-strong); border-radius: 14px; background: var(--bg-soft); }.approval.pending { border-color: color-mix(in srgb, var(--accent) 42%, var(--border-strong)); }
.head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }.head > div { display: flex; align-items: baseline; gap: 8px; }.eyebrow { font-size: 11px; color: var(--text-3); letter-spacing: .04em; }.risk { margin-top: 8px; font-size: 13px; color: var(--text-2); }
.args-wrap { margin-top: 8px; color: var(--text-3); font-size: 11px; }.args-wrap summary { cursor: pointer; user-select: none; }.args { margin: 7px 0 0; padding: 9px 10px; max-height: 220px; overflow: auto; border-radius: 9px; background: var(--bg); border: 1px solid var(--border); font-size: 12px; white-space: pre-wrap; word-break: break-word; }
.actions { display: flex; justify-content: flex-end; gap: 7px; margin-top: 10px; }.challenge { display: flex; gap: 7px; margin-top: 10px; }
</style>
