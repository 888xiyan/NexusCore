import { defineStore } from 'pinia'
import { ref } from 'vue'
import { chatApi, type ChatMessage } from '../api/chat'

export const useChatStore = defineStore('chat', () => {
  const currentRoomId = ref<string>('dev_room')
  const messages = ref<ChatMessage[]>([])
  const loading = ref<boolean>(false)
  const sending = ref<boolean>(false)
  const error = ref<string | null>(null)

  const fetchMessages = async (roomId: string = currentRoomId.value) => {
    loading.value = true
    error.value = null
    try {
      currentRoomId.value = roomId
      const res = await chatApi.getMessages(roomId)
      messages.value = res
    } catch (err: any) {
      error.value = err?.message || '加载聊天记录失败'
    } finally {
      loading.value = false
    }
  }

  const sendMessage = async (content: string, senderId = 'user', role: 'user' | 'bot' | 'system' = 'user') => {
    if (!content.trim()) return
    sending.value = true
    error.value = null
    try {
      const msg = await chatApi.sendMessage({
        room_id: currentRoomId.value,
        sender_id: senderId,
        role,
        content,
      })
      appendMessage(msg)
      return msg
    } catch (err: any) {
      error.value = err?.message || '发送消息失败'
      throw err
    } finally {
      sending.value = false
    }
  }

  const appendMessage = (msg: ChatMessage) => {
    if (msg.room_id && msg.room_id !== currentRoomId.value) {
      return
    }
    const exists = messages.value.some((m) => m.id === msg.id)
    if (!exists) {
      messages.value.push(msg)
    }
  }

  const clearMessages = () => {
    messages.value = []
  }

  return {
    currentRoomId,
    messages,
    loading,
    sending,
    error,
    fetchMessages,
    sendMessage,
    appendMessage,
    clearMessages,
  }
})
