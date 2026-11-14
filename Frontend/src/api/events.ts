// SSE Stream Listener
import type { ChatMessage } from './chat'

export type BotLifecycleStatus = 'idle' | 'thinking' | 'executing' | 'offline'

export interface BotStatusEvent {
  bot_id: string
  status: BotLifecycleStatus
  timestamp: string
}

export type AgentLifecycleStatus = 'idle' | 'running' | 'completed' | 'error'
export type AgentFrameType = 'open' | 'data_in' | 'data_out' | 'close' | 'auth' | 'ack' | 'heartbeat'

export interface AgentEvent {
  stream_id: number
  job_id: string
  bot_id: string
  agent_id: string
  protocol_id: string
  frame_type: AgentFrameType
  status: AgentLifecycleStatus
  payload: string
  elapsed_ms: number
  timestamp: string
}

export interface StreamEventHandlers {
  onBotStatus?: (event: BotStatusEvent) => void
  onChatMessage?: (event: ChatMessage) => void
  onAgentEvent?: (event: AgentEvent) => void
  onOpen?: () => void
  onError?: (err: Event) => void
}

export function connectEventStream(handlers: StreamEventHandlers): () => void {
  const url = '/v1/events/stream'
  const eventSource = new EventSource(url)

  eventSource.onopen = () => {
    handlers.onOpen?.()
  }

  eventSource.addEventListener('bot_status', (e: MessageEvent) => {
    try {
      const data = JSON.parse(e.data) as BotStatusEvent
      handlers.onBotStatus?.(data)
    } catch (err) {
      console.error('Failed to parse bot_status event:', err, e.data)
    }
  })

  eventSource.addEventListener('chat_message', (e: MessageEvent) => {
    try {
      const data = JSON.parse(e.data) as ChatMessage
      handlers.onChatMessage?.(data)
    } catch (err) {
      console.error('Failed to parse chat_message event:', err, e.data)
    }
  })

  eventSource.addEventListener('agent_event', (e: MessageEvent) => {
    try {
      const data = JSON.parse(e.data) as AgentEvent
      handlers.onAgentEvent?.(data)
    } catch (err) {
      console.error('Failed to parse agent_event:', err, e.data)
    }
  })

  eventSource.onerror = (err) => {
    handlers.onError?.(err)
  }

  return () => {
    eventSource.close()
  }
}
