<script setup lang="ts">
import { ref, watch, nextTick, computed } from 'vue'
import { useChatStore } from '../stores/chat'
import { useBotsStore } from '../stores/bots'
import ChatBubble from '../components/ChatBubble.vue'
import MentionInput from '../components/MentionInput.vue'
import { ChatDotRound, InfoFilled } from '@element-plus/icons-vue'
import { identityLabels } from '../api/bots'

const chatStore = useChatStore()
const botsStore = useBotsStore()

const messagesContainerRef = ref<HTMLDivElement | null>(null)

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
      return '思考中...'
    case 'executing':
      return '执行探针'
    default:
      return '离线'
  }
}
</script>

<template>
  <div class="chat-view-container">
    <!-- Top Online Bots Indicator Bar -->
    <div class="online-bots-bar">
      <div class="bar-title">
        <el-icon><ChatDotRound /></el-icon>
        <span>群聊席位 (点击卡片查看单体记忆)</span>
      </div>
      <div class="bot-chips-list">
        <div
          v-for="b in botsStore.bots"
          :key="b.bot_id"
          class="bot-presence-chip"
          @click="botsStore.openDrawer(b.bot_id)"
        >
          <el-badge is-dot :type="getStatusBadgeType(getBotStatus(b.bot_id))" class="bot-badge">
            <el-avatar :size="28" class="chip-avatar">
              {{ (b.display_name || b.bot_id).slice(0, 1) }}
            </el-avatar>
          </el-badge>
          <div class="chip-meta">
            <span class="chip-name">{{ b.display_name || b.bot_id }}</span>
            <span class="chip-status" :class="getBotStatus(b.bot_id)">
              {{ identityLabels(b.identity, b.identities) }}{{ identityLabels(b.identity, b.identities) ? ' · ' : '' }}{{ getStatusText(getBotStatus(b.bot_id)) }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <!-- Rule Banner -->
    <div class="rule-banner">
      <el-icon><InfoFilled /></el-icon>
      <span>路由安全铁律：仅用户 @ 触发 Bot 唤醒调度；Bot 回复中的 @ 仅作展示，杜绝级联死循环。</span>
    </div>

    <!-- Messages Container -->
    <div ref="messagesContainerRef" class="messages-scroll-area">
      <div v-if="chatStore.loading && chatStore.messages.length === 0" class="chat-empty-state">
        正在加载群聊消息...
      </div>
      <div v-else-if="chatStore.messages.length === 0" class="chat-empty-state">
        暂无群聊消息，在下方输入 <code>@ops_bot</code> 开启协作对话吧！
      </div>
      <div v-else class="messages-list">
        <ChatBubble
          v-for="msg in chatStore.messages"
          :key="msg.id"
          :message="msg"
          :bots="botsStore.bots"
        />
      </div>
    </div>

    <!-- Mention Input Area -->
    <div class="input-area">
      <MentionInput
        :bots="botsStore.bots"
        :loading="chatStore.sending"
        @send="handleSendMessage"
      />
    </div>
  </div>
</template>

<style scoped>
.chat-view-container {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: #f7f9fa;
  border-right: 1px solid #e4e7ed;
  box-sizing: border-box;
}

.online-bots-bar {
  padding: 10px 16px;
  background: #ffffff;
  border-bottom: 1px solid #ebeef5;
  display: flex;
  align-items: center;
  gap: 16px;
}

.bar-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: #606266;
  white-space: nowrap;
}

.bot-chips-list {
  display: flex;
  gap: 10px;
  overflow-x: auto;
  padding: 2px 0;
}

.bot-presence-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  background: #f4f4f5;
  border: 1px solid #e9e9eb;
  border-radius: 20px;
  padding: 4px 10px;
  cursor: pointer;
  transition: all 0.2s;
  white-space: nowrap;
}

.bot-presence-chip:hover {
  background: #ecf5ff;
  border-color: #c6e2ff;
}

.chip-avatar {
  background: #409eff;
  color: white;
  font-size: 12px;
}

.chip-meta {
  display: flex;
  flex-direction: column;
}

.chip-name {
  font-size: 12px;
  font-weight: 600;
  color: #303133;
}

.chip-status {
  font-size: 10px;
}

.chip-status.idle { color: #67c23a; }
.chip-status.thinking { color: #e6a23c; }
.chip-status.executing { color: #409eff; }
.chip-status.offline { color: #909399; }

.rule-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  background: #fdf6ec;
  color: #e6a23c;
  font-size: 12px;
  border-bottom: 1px solid #faecd8;
}

.messages-scroll-area {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
}

.chat-empty-state {
  text-align: center;
  color: #909399;
  font-size: 14px;
  margin-top: 80px;
}

.chat-empty-state code {
  background: #e9ecef;
  color: #409eff;
  padding: 2px 6px;
  border-radius: 4px;
}

.messages-list {
  display: flex;
  flex-direction: column;
}

.input-area {
  padding: 12px 16px;
  background: #ffffff;
  border-top: 1px solid #ebeef5;
}
</style>
