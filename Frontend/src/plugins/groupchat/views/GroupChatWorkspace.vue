<script setup lang="ts">
import { ref, watch, nextTick, computed } from 'vue'
import { useChatStore } from '../../../stores/chat'
import { useBotsStore } from '../../../stores/bots'
import ChatBubble from '../../../components/ChatBubble.vue'
import MentionInput from '../../../components/MentionInput.vue'
import BotDrawer from '../../../views/BotDrawer.vue'
import { ChatDotRound, InfoFilled, User } from '@element-plus/icons-vue'

const chatStore = useChatStore()
const botsStore = useBotsStore()

const messagesContainerRef = ref<HTMLDivElement | null>(null)
const selectedBotId = ref<string>('')
const drawerVisible = ref<boolean>(false)

// Auto-scroll to bottom on new messages
watch(
  () => chatStore.messages.length,
  () => {
    nextTick(() => {
      if (messagesContainerRef.value) {
        messagesContainerRef.value.scrollTop = messagesContainerRef.value.scrollHeight
      }
    })
  }
)

const handleSendMessage = async (content: string) => {
  await chatStore.sendMessage(content, 'user', 'user')
}

const openBotDrawer = (botId: string) => {
  selectedBotId.value = botId
  drawerVisible.value = true
}

const getBotStatus = (botId: string) => {
  return botsStore.statuses[botId] || 'idle'
}

const getStatusBadgeType = (status: string) => {
  switch (status) {
    case 'idle':
      return 'success'
    case 'thinking':
      return 'warning'
    case 'executing':
      return 'primary'
    default:
      return 'info'
  }
}

const getStatusText = (status: string) => {
  switch (status) {
    case 'idle':
      return '空闲'
    case 'thinking':
      return '思考中'
    case 'executing':
      return '调用Agent中'
    default:
      return '未知'
  }
}
</script>

<template>
  <div class="groupchat-container">
    <!-- Chat Header: Bot Participants List & Statuses -->
    <div class="chat-header">
      <div class="chat-header-title">
        <el-icon><ChatDotRound /></el-icon>
        <span>群聊协同视区（仅用户 @ 触发）</span>
      </div>

      <div class="bot-participants-bar">
        <span class="participants-label">同台在线 Bot:</span>
        <div class="participants-chips">
          <div
            v-for="bot in botsStore.bots"
            :key="bot.bot_id"
            class="participant-chip"
            @click="openBotDrawer(bot.bot_id)"
            title="点击查看/编辑画像与单体记忆"
          >
            <el-avatar :size="24" class="bot-chip-avatar">{{ bot.display_name.charAt(0) }}</el-avatar>
            <span class="bot-chip-name">{{ bot.display_name }}</span>
            <el-tag
              size="small"
              :type="getStatusBadgeType(getBotStatus(bot.bot_id))"
              effect="dark"
              class="bot-chip-status"
            >
              {{ getStatusText(getBotStatus(bot.bot_id)) }}
            </el-tag>
          </div>
        </div>
      </div>
    </div>

    <!-- Messages List Container -->
    <div ref="messagesContainerRef" class="chat-messages-scroll">
      <div v-if="chatStore.messages.length === 0" class="chat-empty-state">
        <el-empty description="暂无群聊消息，在下方输入 @ 即可唤醒指定 Bot 协作">
          <template #image>
            <el-icon :size="48" color="#909399"><InfoFilled /></el-icon>
          </template>
        </el-empty>
      </div>

      <div v-else class="messages-flow">
        <ChatBubble
          v-for="msg in chatStore.messages"
          :key="msg.id"
          :message="msg"
          @open-bot-drawer="openBotDrawer"
        />
      </div>
    </div>

    <!-- Input Footer Bar with Mentions -->
    <div class="chat-input-bar">
      <MentionInput
        :bots="botsStore.bots"
        @send="handleSendMessage"
      />
    </div>

    <!-- Bot Persona and Memory Management Drawer -->
    <BotDrawer
      v-model:visible="drawerVisible"
      :bot-id="selectedBotId"
    />
  </div>
</template>

<style scoped>
.groupchat-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  background-color: #1a1c22;
  color: #e0e0e0;
}

.chat-header {
  padding: 12px 20px;
  background-color: #21252d;
  border-bottom: 1px solid #2d323f;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.chat-header-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  color: #f0f2f5;
}

.bot-participants-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.participants-label {
  font-size: 12px;
  color: #909399;
}

.participants-chips {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.participant-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 8px;
  background-color: #2b303c;
  border-radius: 14px;
  cursor: pointer;
  border: 1px solid #3c4252;
  transition: all 0.2s ease;
}

.participant-chip:hover {
  background-color: #363d4d;
  border-color: #409eff;
}

.bot-chip-avatar {
  background-color: #409eff;
  color: #ffffff;
  font-size: 11px;
}

.bot-chip-name {
  font-size: 12px;
  font-weight: 500;
  color: #e4e7ed;
}

.bot-chip-status {
  height: 18px;
  padding: 0 4px;
  font-size: 10px;
}

.chat-messages-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
}

.chat-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
}

.messages-flow {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.chat-input-bar {
  padding: 12px 16px;
  background-color: #21252d;
  border-top: 1px solid #2d323f;
}
</style>
