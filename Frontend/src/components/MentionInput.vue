<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import type { BotInfo } from '../api/bots'
import { Promotion } from '@element-plus/icons-vue'

const props = defineProps<{
  bots: BotInfo[]
  disabled?: boolean
  loading?: boolean
  placeholder?: string
}>()

const emit = defineEmits<{
  (e: 'send', content: string): void
}>()

const text = ref<string>('')
const textareaRef = ref<HTMLTextAreaElement | null>(null)
const showSuggestions = ref<boolean>(false)
const suggestionQuery = ref<string>('')
const selectedIndex = ref<number>(0)
const cursorPosition = ref<number>(0)

// Filter online / available bots matching query
const filteredBots = computed(() => {
  const q = suggestionQuery.value.toLowerCase()
  return props.bots.filter(
    (b) =>
      b.bot_id.toLowerCase().includes(q) ||
      (b.display_name && b.display_name.toLowerCase().includes(q))
  )
})

watch(filteredBots, () => {
  selectedIndex.value = 0
})

const handleInput = (e: Event) => {
  const target = e.target as HTMLTextAreaElement
  cursorPosition.value = target.selectionStart || 0
  const val = text.value.slice(0, cursorPosition.value)

  // Check if cursor is right after an '@' pattern: @[a-zA-Z0-9_]*$
  const match = val.match(/@([a-zA-Z0-9_]*)$/)
  if (match) {
    showSuggestions.value = true
    suggestionQuery.value = match[1]
  } else {
    showSuggestions.value = false
  }
}

const selectBot = (bot: BotInfo) => {
  const val = text.value
  const beforeCursor = val.slice(0, cursorPosition.value)
  const afterCursor = val.slice(cursorPosition.value)

  // Replace the trailing @query with @bot_id
  const updatedBefore = beforeCursor.replace(/@([a-zA-Z0-9_]*)$/, `@${bot.bot_id} `)
  text.value = updatedBefore + afterCursor
  showSuggestions.value = false

  nextTick(() => {
    if (textareaRef.value) {
      textareaRef.value.focus()
      const newPos = updatedBefore.length
      textareaRef.value.setSelectionRange(newPos, newPos)
    }
  })
}

const handleKeyDown = (e: KeyboardEvent) => {
  if (showSuggestions.value && filteredBots.value.length > 0) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      selectedIndex.value = (selectedIndex.value + 1) % filteredBots.value.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      selectedIndex.value =
        (selectedIndex.value - 1 + filteredBots.value.length) % filteredBots.value.length
      return
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      const selected = filteredBots.value[selectedIndex.value]
      if (selected) {
        selectBot(selected)
      }
      return
    }
    if (e.key === 'Escape') {
      showSuggestions.value = false
      return
    }
  }

  // Handle send on Enter (without Shift)
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    doSend()
  }
}

const doSend = () => {
  const content = text.value.trim()
  if (!content || props.disabled || props.loading) return
  emit('send', content)
  text.value = ''
  showSuggestions.value = false
}
</script>

<template>
  <div class="mention-input-wrapper">
    <!-- Suggestions Dropdown -->
    <div v-if="showSuggestions && filteredBots.length > 0" class="suggestions-popover">
      <div class="popover-header">选择要 @ 的 Bot</div>
      <div
        v-for="(bot, idx) in filteredBots"
        :key="bot.bot_id"
        :class="['suggestion-item', { active: idx === selectedIndex }]"
        @mousedown.prevent="selectBot(bot)"
      >
        <el-avatar :size="24" class="bot-tiny-avatar">
          {{ (bot.display_name || bot.bot_id).slice(0, 1) }}
        </el-avatar>
        <span class="bot-mention-id">@{{ bot.bot_id }}</span>
        <span class="bot-mention-name">{{ bot.display_name }}</span>
      </div>
    </div>

    <!-- Textarea & Action Bar -->
    <div class="input-box-container">
      <textarea
        ref="textareaRef"
        v-model="text"
        :placeholder="placeholder || '输入消息，输入 @ 唤醒指定 Bot（回车发送，Shift+回车换行）...'"
        :disabled="disabled || loading"
        class="custom-textarea"
        rows="3"
        @input="handleInput"
        @keydown="handleKeyDown"
      ></textarea>

      <div class="input-footer">
        <div class="shortcut-tip">
          提示：输入 <code>@ops_bot</code> 唤醒运维助手，仅用户 @ 触发路由
        </div>
        <el-button
          type="primary"
          :icon="Promotion"
          :loading="loading"
          :disabled="disabled || !text.trim()"
          class="send-btn"
          @click="doSend"
        >
          发送
        </el-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mention-input-wrapper {
  position: relative;
  width: 100%;
}

.suggestions-popover {
  position: absolute;
  bottom: calc(100% + 8px);
  left: 12px;
  width: 280px;
  max-height: 220px;
  overflow-y: auto;
  background: #ffffff;
  border: 1px solid #e4e7ed;
  border-radius: 8px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  z-index: 1000;
  padding: 4px 0;
}

.popover-header {
  font-size: 11px;
  color: #909399;
  padding: 6px 12px;
  border-bottom: 1px solid #f2f6fc;
}

.suggestion-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  cursor: pointer;
  transition: background 0.2s;
}

.suggestion-item:hover,
.suggestion-item.active {
  background: #f0f7ff;
}

.bot-tiny-avatar {
  background: #409eff;
  font-size: 11px;
}

.bot-mention-id {
  font-weight: 600;
  color: #409eff;
  font-size: 13px;
}

.bot-mention-name {
  font-size: 12px;
  color: #606266;
  margin-left: auto;
}

.input-box-container {
  background: #ffffff;
  border: 1px solid #dcdfe6;
  border-radius: 8px;
  padding: 8px 12px;
  transition: border-color 0.2s;
}

.input-box-container:focus-within {
  border-color: #409eff;
}

.custom-textarea {
  width: 100%;
  border: none;
  outline: none;
  resize: none;
  font-size: 14px;
  font-family: inherit;
  color: #303133;
  line-height: 1.5;
  box-sizing: border-box;
}

.input-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 6px;
  border-top: 1px solid #f2f6fc;
  padding-top: 6px;
}

.shortcut-tip {
  font-size: 12px;
  color: #909399;
}

.shortcut-tip code {
  background: #f4f4f5;
  color: #409eff;
  padding: 1px 4px;
  border-radius: 3px;
}

.send-btn {
  padding: 8px 18px;
}
</style>
