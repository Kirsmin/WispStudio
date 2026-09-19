<template>
  <div v-if="turn || canRestore || completedTurns.length" class="runtime-controls">
    <div v-if="turn" class="turn-state">
      <span class="dot" :class="turn.status" />
      <span>{{ turn.active_agent || 'agent' }}</span>
      <span class="muted">{{ turn.status }}</span>
      <span class="muted">epoch {{ turn.context_epoch }}</span>
    </div>
    <div class="buttons">
      <n-button v-if="caps.can_start_build" size="tiny" type="primary" @click="run(chatStore.startBuild)">开始执行</n-button>
      <n-button v-if="caps.can_pause" size="tiny" @click="run(chatStore.pauseTurn)">Pause</n-button>
      <n-button v-if="caps.can_resume" size="tiny" @click="run(chatStore.resumeTurn)">Resume</n-button>
      <n-button v-if="caps.can_stop" size="tiny" @click="run(chatStore.stopTurn)">Stop</n-button>
      <n-button v-if="caps.can_cancel" size="tiny" type="error" ghost @click="run(chatStore.hardCancel)">Hard Cancel</n-button>
      <n-dropdown v-if="completedTurns.length" :options="foldOptions" trigger="click" @select="fold">
        <n-button size="tiny">Fold Turn</n-button>
      </n-dropdown>
      <n-button v-if="canRestore" size="tiny" @click="run(chatStore.restoreFold)">Restore Fold</n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NDropdown } from 'naive-ui'
import { useChatStore } from '../stores/chat'

const chatStore = useChatStore()
const turn = computed(() => chatStore.runtimeState.turn)
const caps = computed(() => chatStore.capabilities)
const canRestore = computed(() => chatStore.runtimeState.can_restore_fold)
const completedTurns = computed(() => chatStore.runtimeState.turns.filter(item => item.status === 'completed'))
const foldOptions = computed(() => completedTurns.value.map(item => ({ label: `Turn ${item.turn_index}: ${item.objective || item.id}`, key: item.id })))
async function run(action: () => Promise<unknown>) {
  try { await action() } catch (error) { window.$message?.error(error instanceof Error ? error.message : String(error)) }
}
function fold(turnId: string | number) { void run(() => chatStore.foldTurn(String(turnId))) }
</script>

<style scoped>
.runtime-controls { max-width: 760px; margin: 0 auto 8px; min-height: 34px; display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 6px 9px; border: 1px solid var(--border); border-radius: 12px; background: var(--bg-soft); }
.turn-state { display: flex; align-items: center; gap: 7px; min-width: 0; font-size: 12px; color: var(--text); text-transform: capitalize; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--text-3); }
.dot.running { background: var(--accent); }
.dot.waiting_user, .dot.paused { background: #b78320; }
.dot.failed, .dot.cancelled, .dot.stopped { background: #b33f52; }
.muted { color: var(--text-3); text-transform: none; }
.buttons { display: flex; align-items: center; justify-content: flex-end; gap: 6px; flex-wrap: wrap; }
@media (max-width: 720px) { .runtime-controls { align-items: flex-start; flex-direction: column; } .buttons { justify-content: flex-start; } }
</style>
