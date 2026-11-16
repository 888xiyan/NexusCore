<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useBotsStore } from './stores/bots'
import { useChatStore } from './stores/chat'
import { useAgentStore } from './stores/agent'
import { connectEventStream } from './api/events'
import { pluginRegistry } from './core/registry'

const botsStore = useBotsStore()
const chatStore = useChatStore()
const agentStore = useAgentStore()

let disconnectSSE: (() => void) | null = null

onMounted(async () => {
  // 1. 初始化基础数据
  await Promise.all([
    botsStore.fetchBots(),
    chatStore.fetchMessages(),
  ])

  // 2. 建立 SSE 事件流并与 PluginRegistry 总线桥接
  disconnectSSE = connectEventStream({
    onOpen: () => {
      pluginRegistry.bus.emit('nexus.sse.connected', true)
    },
    onBotStatus: (evt) => {
      botsStore.updateBotStatus(evt.bot_id, evt.status)
      pluginRegistry.bus.emit('kernel.bot.status_changed', evt)
    },
    onChatMessage: (msg) => {
      chatStore.appendMessage(msg)
      pluginRegistry.bus.emit('chat.message.received', msg)
    },
    onAgentEvent: (evt) => {
      agentStore.handleAgentEvent(evt)
      pluginRegistry.bus.emit('agent.event.received', evt)
    },
    onError: () => {
      pluginRegistry.bus.emit('nexus.sse.connected', false)
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
  <router-view />
</template>

<style>
/* Global Resets */
html,
body,
#app {
  margin: 0;
  padding: 0;
  height: 100%;
  width: 100%;
  box-sizing: border-box;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
  background-color: #1a1c22;
  color: #e0e0e0;
}

*,
*::before,
*::after {
  box-sizing: border-box;
}

/* Scrollbar styles */
::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

::-webkit-scrollbar-track {
  background: transparent;
}

::-webkit-scrollbar-thumb {
  background: #3c4252;
  border-radius: 3px;
}

::-webkit-scrollbar-thumb:hover {
  background: #50586c;
}
</style>
