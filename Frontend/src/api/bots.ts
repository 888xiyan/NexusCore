// Bot Management REST API
import { apiClient } from './client'

export interface BotInfo {
  bot_id: string
  role: string
  display_name: string
  activation_mode: string
  capabilities: string[]
  observed_state: string
  enabled: boolean
}

export interface BotListResponse {
  bots: BotInfo[]
}

export interface BotDetail {
  bot_id: string
  role: string
  display_name: string
  activation_mode: string
  capabilities: string[]
  desired_state?: string
  observed_state: string
  enabled: boolean
  memory_lines: number
  memory_content: string
  memory_profile?: string
  prompt_ref?: string
  model?: string
  config_version?: number
}

export interface BotMemory {
  bot_id: string
  max_lines: number
  current_lines: number
  content: string
}

export interface SaveMemoryResponse {
  status: string
  lines: number
}

export interface RestartBotResponse {
  status: string
  bot_id: string
}

export const botsApi = {
  getBots: async (): Promise<BotInfo[]> => {
    const res = await apiClient.get<BotListResponse>('/v1/bots')
    return res.bots || []
  },

  getBot: async (botId: string): Promise<BotDetail> => {
    return apiClient.get<BotDetail>(`/v1/bots/${encodeURIComponent(botId)}`)
  },

  getBotMemory: async (botId: string): Promise<BotMemory> => {
    return apiClient.get<BotMemory>(`/v1/bots/${encodeURIComponent(botId)}/memory`)
  },

  saveBotMemory: async (botId: string, content: string): Promise<SaveMemoryResponse> => {
    return apiClient.put<SaveMemoryResponse>(`/v1/bots/${encodeURIComponent(botId)}/memory`, { content })
  },

  restartBot: async (botId: string): Promise<RestartBotResponse> => {
    return apiClient.post<RestartBotResponse>(`/v1/bots/${encodeURIComponent(botId)}/restart`)
  },
}
