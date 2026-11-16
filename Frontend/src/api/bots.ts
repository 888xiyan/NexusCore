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

export interface BotBlueprint {
  bot_id: string
  display_name: string
  role: string
  system_prompt: string
  persona: string
  initial_memory: string
  bound_agent_cli?: string
  bound_agent_id?: string
  activation_mode?: string
}

export interface AgentAsset {
  agent_id: string
  display_name: string
  capabilities: any
  status: string
  last_seen_at?: string
}

export interface TemplateItem {
  id: string
  name: string
  description: string
  blueprint: BotBlueprint
}

export interface ValidateResult {
  valid: boolean
  memory_lines: number
  violations?: string[]
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

  listAgents: async (): Promise<AgentAsset[]> => {
    return apiClient.get<AgentAsset[]>('/v1/agents')
  },

  createBot: async (bp: BotBlueprint): Promise<any> => {
    const caps = ['chat']
    if (bp.bound_agent_cli) {
      caps.push('agent-cli:' + bp.bound_agent_cli)
    }
    const agentId = bp.bound_agent_id || 'loopback_agent'
    caps.push('agent-id:' + agentId)

    // 1. 注册 Bot
    const created = await apiClient.post<any>('/v1/bots', {
      bot_id: bp.bot_id,
      display_name: bp.display_name,
      role: 'plugin',
      activation_mode: bp.activation_mode || 'lazy',
      capabilities: caps,
      memory_profile: 'personal_stack_v1',
      prompt_ref: bp.system_prompt,
      model: bp.persona,
    })

    // 2. 初始化单体记忆 MEMORY.md
    if (bp.initial_memory) {
      await apiClient.put(`/v1/bots/${encodeURIComponent(bp.bot_id)}/memory`, {
        content: bp.initial_memory,
      })
    }

    // 3. 触发重启生效
    await apiClient.post(`/v1/bots/${encodeURIComponent(bp.bot_id)}/restart`)

    return created
  },
}
