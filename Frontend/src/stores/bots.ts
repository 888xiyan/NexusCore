import { defineStore } from 'pinia'
import { ref } from 'vue'
import { botsApi, type BotInfo, type BotDetail, type BotMemory } from '../api/bots'
import type { BotLifecycleStatus } from '../api/events'

export const useBotsStore = defineStore('bots', () => {
  const bots = ref<BotInfo[]>([])
  const statuses = ref<Record<string, BotLifecycleStatus>>({})
  const loading = ref<boolean>(false)
  const error = ref<string | null>(null)

  // Drawer state
  const drawerVisible = ref<boolean>(false)
  const selectedBotId = ref<string | null>(null)
  const selectedBotDetail = ref<BotDetail | null>(null)
  const selectedBotMemory = ref<BotMemory | null>(null)
  const drawerLoading = ref<boolean>(false)
  const savingOrRestarting = ref<boolean>(false)

  const fetchBots = async () => {
    loading.value = true
    error.value = null
    try {
      const list = await botsApi.getBots()
      bots.value = list
      for (const b of list) {
        if (!statuses.value[b.bot_id]) {
          statuses.value[b.bot_id] = b.observed_state === 'ready' ? 'idle' : 'offline'
        }
      }
    } catch (err: any) {
      error.value = err?.message || '获取 Bot 列表失败'
    } finally {
      loading.value = false
    }
  }

  const updateBotStatus = (botId: string, status: BotLifecycleStatus) => {
    statuses.value[botId] = status
  }

  const openDrawer = async (botId: string) => {
    selectedBotId.value = botId
    drawerVisible.value = true
    drawerLoading.value = true
    try {
      const [detail, memory] = await Promise.all([
        botsApi.getBot(botId),
        botsApi.getBotMemory(botId).catch(() => ({
          bot_id: botId,
          max_lines: 200,
          current_lines: 0,
          content: '',
        })),
      ])
      selectedBotDetail.value = detail
      selectedBotMemory.value = memory
    } catch (err: any) {
      error.value = err?.message || `获取 Bot ${botId} 详情失败`
    } finally {
      drawerLoading.value = false
    }
  }

  const closeDrawer = () => {
    drawerVisible.value = false
    selectedBotId.value = null
    selectedBotDetail.value = null
    selectedBotMemory.value = null
  }

  const saveMemory = async (botId: string, content: string) => {
    const res = await botsApi.saveBotMemory(botId, content)
    if (selectedBotMemory.value && selectedBotMemory.value.bot_id === botId) {
      selectedBotMemory.value.content = content
      selectedBotMemory.value.current_lines = res.lines
    }
    return res
  }

  const restartBot = async (botId: string) => {
    const res = await botsApi.restartBot(botId)
    updateBotStatus(botId, 'idle')
    return res
  }

  const saveAndRestart = async (botId: string, content: string) => {
    savingOrRestarting.value = true
    try {
      await saveMemory(botId, content)
      await restartBot(botId)
      // Refresh details
      const detail = await botsApi.getBot(botId)
      selectedBotDetail.value = detail
    } finally {
      savingOrRestarting.value = false
    }
  }

  return {
    bots,
    statuses,
    loading,
    error,
    drawerVisible,
    selectedBotId,
    selectedBotDetail,
    selectedBotMemory,
    drawerLoading,
    savingOrRestarting,
    fetchBots,
    updateBotStatus,
    openDrawer,
    closeDrawer,
    saveMemory,
    restartBot,
    saveAndRestart,
  }
})
