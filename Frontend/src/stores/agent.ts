import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { AgentEvent, AgentLifecycleStatus } from '../api/events'

export const useAgentStore = defineStore('agent', () => {
  const activeStreamId = ref<number | null>(null)
  const activeJobId = ref<string | null>(null)
  const activeBotId = ref<string | null>(null)
  const activeAgentId = ref<string | null>(null)
  const activeProtocolId = ref<string | null>(null)
  const status = ref<AgentLifecycleStatus>('idle')
  const elapsedMs = ref<number>(0)
  const frames = ref<AgentEvent[]>([])
  const terminalLogs = ref<string[]>([])

  const handleAgentEvent = (evt: AgentEvent) => {
    frames.value.push(evt)
    activeStreamId.value = evt.stream_id
    activeJobId.value = evt.job_id
    activeBotId.value = evt.bot_id
    activeAgentId.value = evt.agent_id
    activeProtocolId.value = evt.protocol_id
    elapsedMs.value = evt.elapsed_ms

    if (evt.status) {
      status.value = evt.status
    } else if (evt.frame_type === 'close') {
      status.value = 'completed'
    } else if (evt.frame_type === 'open' || evt.frame_type === 'data_in' || evt.frame_type === 'data_out') {
      status.value = 'running'
    }

    if (evt.payload) {
      // If payload contains multiple lines, split them
      const lines = evt.payload.split('\n')
      for (const line of lines) {
        if (line.trim().length > 0) {
          terminalLogs.value.push(line)
        }
      }
    }
  }

  const clearLogs = () => {
    activeStreamId.value = null
    activeJobId.value = null
    activeBotId.value = null
    activeAgentId.value = null
    activeProtocolId.value = null
    status.value = 'idle'
    elapsedMs.value = 0
    frames.value = []
    terminalLogs.value = []
  }

  return {
    activeStreamId,
    activeJobId,
    activeBotId,
    activeAgentId,
    activeProtocolId,
    status,
    elapsedMs,
    frames,
    terminalLogs,
    handleAgentEvent,
    clearLogs,
  }
})
