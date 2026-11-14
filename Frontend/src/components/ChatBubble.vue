<script setup lang="ts">
import { computed } from 'vue'
import MarkdownIt from 'markdown-it'
import hljs from 'highlight.js'
import type { ChatMessage } from '../api/chat'
import type { BotInfo } from '../api/bots'

const props = defineProps<{
  message: ChatMessage
  bots?: BotInfo[]
}>()

const md: MarkdownIt = new MarkdownIt({
  html: false,
  breaks: true,
  linkify: true,
  highlight: function (str: string, lang: string): string {
    if (lang && hljs.getLanguage(lang)) {
      try {
        return `<pre class="hljs"><code class="language-${lang}">${
          hljs.highlight(str, { language: lang, ignoreIllegals: true }).value
        }</code></pre>`
      } catch (__) {}
    }
    return `<pre class="hljs"><code>${md.utils.escapeHtml(str)}</code></pre>`
  },
})

const isUser = computed(() => props.message.role === 'user')
const isSystem = computed(() => props.message.role === 'system')

const botInfo = computed(() => {
  if (isUser.value || isSystem.value) return null
  return props.bots?.find((b) => b.bot_id === props.message.sender_id)
})

const senderDisplayName = computed(() => {
  if (isUser.value) return '用户 (user_bot)'
  if (isSystem.value) return '系统通知'
  return botInfo.value?.display_name || props.message.sender_id
})

const formattedTime = computed(() => {
  if (!props.message.timestamp) return ''
  try {
    const d = new Date(props.message.timestamp)
    return d.toLocaleTimeString('zh-CN', { hour12: false })
  } catch {
    return props.message.timestamp
  }
})

// Highlight @mentions in user messages, but keep them as inert spans in bot messages
const renderedContent = computed(() => {
  const rawHtml = md.render(props.message.content || '')
  // Replace @bot_id with a styled inert tag to prevent cascade triggers
  return rawHtml.replace(/@([a-z0-9_]{5,64})\b/g, (_match, botId) => {
    return `<span class="mention-tag inert-mention">@${botId}</span>`
  })
})
</script>

<template>
  <div :class="['chat-bubble-container', isUser ? 'is-user' : isSystem ? 'is-system' : 'is-bot']">
    <!-- Avatar -->
    <div class="avatar-col" v-if="!isSystem">
      <el-avatar :size="40" :class="['bubble-avatar', isUser ? 'user-avatar' : 'bot-avatar']">
        <template v-if="isUser">👤</template>
        <template v-else>{{ senderDisplayName.slice(0, 2) }}</template>
      </el-avatar>
    </div>

    <!-- Message Body -->
    <div class="bubble-content-col">
      <div class="sender-info" v-if="!isSystem">
        <span class="sender-name">{{ senderDisplayName }}</span>
        <el-tag v-if="!isUser" size="small" type="info" effect="plain" class="role-tag">
          {{ botInfo?.role || 'Bot' }}
        </el-tag>
        <span class="message-time">{{ formattedTime }}</span>
      </div>

      <!-- Bubble Text Box -->
      <div :class="['message-box', isUser ? 'user-box' : isSystem ? 'system-box' : 'bot-box']">
        <div class="markdown-body" v-html="renderedContent"></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-bubble-container {
  display: flex;
  margin-bottom: 20px;
  gap: 12px;
}

.chat-bubble-container.is-user {
  flex-direction: row-reverse;
}

.chat-bubble-container.is-system {
  justify-content: center;
}

.avatar-col {
  flex-shrink: 0;
}

.bubble-avatar {
  font-weight: bold;
}

.user-avatar {
  background: #409eff;
  color: white;
}

.bot-avatar {
  background: #67c23a;
  color: white;
}

.bubble-content-col {
  max-width: 75%;
  display: flex;
  flex-direction: column;
}

.is-user .bubble-content-col {
  align-items: flex-end;
}

.sender-info {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 13px;
  color: #606266;
}

.sender-name {
  font-weight: 600;
}

.message-time {
  font-size: 11px;
  color: #909399;
}

.message-box {
  padding: 12px 16px;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.06);
  word-break: break-word;
  line-height: 1.6;
}

.user-box {
  background: #ecf5ff;
  border: 1px solid #d9ecff;
  color: #1f2f3d;
  border-top-right-radius: 2px;
}

.bot-box {
  background: #ffffff;
  border: 1px solid #e4e7ed;
  color: #303133;
  border-top-left-radius: 2px;
}

.system-box {
  background: #f4f4f5;
  color: #909399;
  font-size: 12px;
  padding: 6px 14px;
  border-radius: 16px;
}

:deep(.markdown-body) {
  font-size: 14px;
}

:deep(.markdown-body p) {
  margin: 0 0 8px 0;
}

:deep(.markdown-body p:last-child) {
  margin-bottom: 0;
}

:deep(.markdown-body pre) {
  background: #282c34;
  color: #abb2bf;
  padding: 10px 14px;
  border-radius: 6px;
  overflow-x: auto;
  margin: 8px 0;
}

:deep(.markdown-body code) {
  font-family: 'Fira Code', Consolas, Monaco, monospace;
  font-size: 13px;
}

:deep(.markdown-body :not(pre) > code) {
  background: #e9ecef;
  color: #e83e8c;
  padding: 2px 6px;
  border-radius: 4px;
}

:deep(.mention-tag) {
  background: #e6f7ff;
  color: #1890ff;
  border: 1px solid #91d5ff;
  border-radius: 4px;
  padding: 1px 6px;
  font-weight: 500;
  display: inline-block;
  user-select: text;
}

:deep(.inert-mention) {
  cursor: default;
}
</style>
