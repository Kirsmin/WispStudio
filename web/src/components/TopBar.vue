<template>
  <div class="top-bar">
    <div class="logo">Wisp</div>
    <div class="connection-status">
      <ConnectDialog />
    </div>
  </div>
</template>

<script setup lang="ts">
import ConnectDialog from './ConnectDialog.vue'
</script>

<style scoped>
.top-bar {
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
  border-bottom: 1px solid var(--border);
  background: var(--bg);
  flex-shrink: 0;
}

.logo {
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
}

.connection-status {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
