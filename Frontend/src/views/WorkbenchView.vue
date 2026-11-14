<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useBotsStore } from '../stores/bots'
import { useChatStore } from '../stores/chat'
import { useAgentStore } from '../stores/agent'
import { connectEventStream } from '../api/events'
import ChatView from './ChatView.vue'
import AgentMonitorView from './AgentMonitorView.vue'
import BotDrawer from './BotDrawer.vue'
import { Connection, Sunny, Monitor } from '@element-plus/icons-vue'

const botsStore = useBotsStore()
const chatStore = useChatStore()
const agentStore = useAgentStore()

const sseConnected = ref<boolean>(false)
let disconnectSSE: (() => void) | null = null

onMounted(async () => {
  // 1. Fetch initial bots and chat messages
  await Promise.all([
    botsStore.fetchBots(),
    chatStore.fetchMessages(),
  ])

  // 2. Establish native Server-Sent Events stream
  disconnectSSE = connectEventStream({
    onOpen: () => {
      sseConnected.value = true
    },
    onBotStatus: (evt) => {
      botsStore.updateBotStatus(evt.bot_id, evt.status)
    },
    onChatMessage: (msg) => {
      chatStore.appendMessage(msg)
    },
    onAgentEvent: (evt) => {
      agentStore.handleAgentEvent(evt)
    },
    onError: () => {
      sseConnected.value = false
    },
  })
})

onUnmounted(() => {
  if (disconnectSSE) {
    disconnectSSE()
    disconnectSSE = null
  }
})
</script>

<template>
  <div class="workbench-layout">
    <!-- Workbench Top Navigation Bar -->
    <header class="workbench-navbar">
      <div class="nav-brand">
        <div class="brand-logo">N</div>
        <div class="brand-titles">
          <span class="brand-name">NexusCore</span>
          <span class="brand-subtitle">个人智能工作台 (M4)</span>
        </div>
      </div>

      <div class="nav-status-indicators">
        <div class="indicator-item">
          <el-icon><Connection /></el-icon>
          <span class="ind-label">SSE 事件总线:</span>
          <el-tag size="small" :type="sseConnected ? 'success' : 'danger'" effect="dark">
            {{ sseConnected ? '已连接' : '连接中断' }}
          </el-tag>
        </div>
        <div class="indicator-item">
          <el-icon><Monitor /></el-icon>
          <span class="ind-label">在线 Bot 席位:</span>
          <el-tag size="small" type="primary" effect="plain">
            {{ botsStore.bots.length }} 位
          </el-tag>
        </div>
        <div class="indicator-item">
          <el-icon><Sunny /></el-icon>
          <span class="ind-label">微内核架构:</span>
          <span class="ind-val">Go + Vue 3 (M4)</span>
        </div>
      </div>
    </header>

    <!-- Main Workspace Content (Chat + Agent Monitor Split) -->
    <main class="workbench-main">
      <div class="split-left">
        <ChatView />
      </div>
      <div class="split-right">
        <AgentMonitorView />
      </div>
    </main>

    <!-- Bot Detail & Memory Management Drawer -->
    <BotDrawer />
  </div>
</template>

<style scoped>
.workbench-layout {
  height: 100vh;
  width: 100vw;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: #f0f2f5;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
}

.workbench-navbar {
  height: 56px;
  background: #1e293b;
  color: #ffffff;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.15);
  z-index: 10;
  flex-shrink: 0;
}

.nav-brand {
  display: flex;
  align-items: center;
  gap: 12px;
}

.brand-logo {
  width: 32px;
  height: 32px;
  background: linear-gradient(135deg, #3b82f6, #8b5cf6);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 900;
  font-size: 18px;
  color: white;
}

.brand-titles {
  display: flex;
  flex-direction: column;
}

.brand-name {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.5px;
}

.brand-subtitle {
  font-size: 11px;
  color: #94a3b8;
}

.nav-status-indicators {
  display: flex;
  align-items: center;
  gap: 20px;
}

.indicator-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #cbd5e1;
}

.ind-label {
  color: #94a3b8;
}

.ind-val {
  color: #e2e8f0;
  font-weight: 500;
}

.workbench-main {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.split-left {
  flex: 6;
  height: 100%;
  min-width: 500px;
}

.split-right {
  flex: 4;
  height: 100%;
  min-width: 400px;
}
</style>
