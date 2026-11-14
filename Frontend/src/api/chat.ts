// Group Chat REST API
import { apiClient } from './client'

export interface ChatMessage {
  id: string
  room_id: string
  sender_id: string
  role: 'user' | 'bot' | 'system'
  content: string
  mentions?: string[]
  timestamp: string
}

export interface ChatHistoryResponse {
  room_id: string
  messages: ChatMessage[]
}

export interface SendChatMessageRequest {
  room_id?: string
  sender_id?: string
  role?: 'user' | 'bot' | 'system'
  content: string
}

export const chatApi = {
  getMessages: async (roomId: string = 'dev_room'): Promise<ChatMessage[]> => {
    const res = await apiClient.get<ChatHistoryResponse>(`/v1/chat/messages?room_id=${encodeURIComponent(roomId)}`)
    return res.messages || []
  },

  sendMessage: async (req: SendChatMessageRequest): Promise<ChatMessage> => {
    return apiClient.post<ChatMessage>('/v1/chat/messages', req)
  },
}
