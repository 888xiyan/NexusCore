<script setup lang="ts">
import { ref, computed } from 'vue'
import { useBotsStore } from '../../../stores/bots'
import { botsApi, type BotBlueprint } from '../../../api/bots'
import { UserFilled, MagicStick, Warning, Check, Plus, Refresh, Monitor } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

const botsStore = useBotsStore()
const submitting = ref(false)

const templates: Record<string, Partial<BotBlueprint>> = {
  reviewer: {
    display_name: '代码审查助理',
    persona: '严谨、注重代码质量与边界条件的资深代码架构师',
    system_prompt: '你是一个严格的代码审查助手。在审查代码变更时，重点关注边界安全、并发竞争与资源泄露。',
    initial_memory: `# 代码审查准则\n- 检查 SQL 是否使用了 SKIP LOCKED\n- 检查 信封因果链 是否完整\n- 检查 DDL 的 CHECK 约束`,
    allowed_agents: ['loopback_agent', 'claude'],
  },
  coder: {
    display_name: '全栈开发助理',
    persona: '高效、熟悉 Go 与 TypeScript 的全栈开发工程师',
    system_prompt: '你是一个专注 Go 微内核与 Vue 3 前端研发的协作 Bot，能将模糊需求解析为结构化任务并调度 Agent 执行。',
    initial_memory: `# 编码约定\n- Go 代码强制 gofmt 与 go vet 零警告\n- 前端组件统一采用 Vue 3 Composition API\n- 严格限制单体记忆不超过 200 行`,
    allowed_agents: ['loopback_agent', 'claude', 'agy'],
  },
  ops: {
    display_name: '运维监控助理',
    persona: '敏锐、保障系统高可用与故障自治的 SRE 专家',
    system_prompt: '你负责监控系统健康、检查进程死活、排查队列积压并协助自动化巡检。',
    initial_memory: `# 运维基线\n- 守护进程心跳周期 10s\n- 租约过期阈值 60s\n- 毒丸熔断重试次数上限 N=5`,
    allowed_agents: ['loopback_agent'],
  },
}

const form = ref<BotBlueprint>({
  bot_id: '',
  display_name: '',
  persona: '',
  system_prompt: '',
  initial_memory: '# 默认单体记忆\n- 初始记录',
  allowed_agents: ['loopback_agent'],
})

const selectedTemplate = ref<string>('coder')

const applyTemplate = (key: string) => {
  selectedTemplate.value = key
  const tpl = templates[key]
  if (!tpl) return
  form.value.display_name = tpl.display_name || ''
  form.value.persona = tpl.persona || ''
  form.value.system_prompt = tpl.system_prompt || ''
  form.value.initial_memory = tpl.initial_memory || ''
  form.value.allowed_agents = [...(tpl.allowed_agents || ['loopback_agent'])]
}

// 统计 MEMORY.md 行数（严格 ≤ 200 行约束）
const memoryLineCount = computed(() => {
  if (!form.value.initial_memory) return 0
  return form.value.initial_memory.split('\n').length
})

const isMemoryExceeded = computed(() => memoryLineCount.value > 200)

const availableAgents = [
  { id: 'loopback_agent', name: 'Loopback 内部回环探针' },
  { id: 'claude', name: 'Claude Code CLI 驱动' },
  { id: 'agy', name: 'Google Antigravity CLI 驱动' },
  { id: 'codex', name: 'Codex CLI 驱动' },
]

const handleSubmit = async () => {
  if (!form.value.bot_id || !form.value.display_name) {
    ElMessage.warning('请填写 Bot ID 与显示名称')
    return
  }

  // 保留名检查
  const reserved = ['user_bot', 'create_bot', 'monitor_bot', 'security_bot']
  if (reserved.includes(form.value.bot_id)) {
    ElMessage.error(`Bot ID 不能使用内核保留名: ${form.value.bot_id}`)
    return
  }

  // 200 行单体记忆约束
  if (isMemoryExceeded.value) {
    ElMessage.error(`初始记忆已达 ${memoryLineCount.value} 行，严格超过 200 行上限！`)
    return
  }

  submitting.value = true
  try {
    await botsApi.createBot(form.value)
    ElMessage.success(`Bot [${form.value.display_name}] 派生成功，Supervisor 已重启生效！`)
    await botsStore.fetchBots()
    // 重置表单
    form.value.bot_id = ''
    applyTemplate('coder')
  } catch (err: any) {
    ElMessage.error('创建失败: ' + err.message)
  } finally {
    submitting.value = false
  }
}

// 默认应用 coder 模板
applyTemplate('coder')
</script>

<template>
  <div class="bot-creator-container">
    <!-- Header -->
    <div class="view-header">
      <div class="header-left">
        <el-icon :size="20"><UserFilled /></el-icon>
        <div>
          <h2 class="view-title">Bot 蓝图工厂 (Bot Creator)</h2>
          <span class="view-subtitle">M6 专职 Bot 实例蓝图定义、四层单体记忆初始化与 Agent 绑定</span>
        </div>
      </div>
    </div>

    <!-- Main Content -->
    <div class="creator-grid">
      <!-- Left: Create Form -->
      <div class="form-card">
        <div class="card-header">
          <span class="card-title">派生新 Bot 蓝图向导</span>
          <div class="template-selector">
            <span class="tpl-label">快捷模板:</span>
            <el-radio-group v-model="selectedTemplate" size="small" @change="applyTemplate">
              <el-radio-button label="coder">全栈工程师</el-radio-button>
              <el-radio-button label="reviewer">代码评审员</el-radio-button>
              <el-radio-button label="ops">SRE 运维</el-radio-button>
            </el-radio-group>
          </div>
        </div>

        <div class="form-body">
          <el-form label-position="top">
            <div class="form-row-2">
              <el-form-item label="Bot ID (唯一定位标识符)" required>
                <el-input
                  v-model="form.bot_id"
                  placeholder="如: fullstack_bot, ops_assistant_bot"
                />
              </el-form-item>

              <el-form-item label="显示名称" required>
                <el-input
                  v-model="form.display_name"
                  placeholder="如: 架构研发助理"
                />
              </el-form-item>
            </div>

            <el-form-item label="Persona (性格特征与职责设定)">
              <el-input
                v-model="form.persona"
                type="textarea"
                :rows="2"
                placeholder="设定该 Bot 的性格、专业特长与语气风格"
              />
            </el-form-item>

            <el-form-item label="System Prompt (核心执行指令)">
              <el-input
                v-model="form.system_prompt"
                type="textarea"
                :rows="3"
                placeholder="赋予 Bot 的高优先级系统提示词"
              />
            </el-form-item>

            <el-form-item>
              <template #label>
                <div class="label-with-counter">
                  <span>初始单体记忆 (MEMORY.md 注入层)</span>
                  <el-tag
                    size="small"
                    :type="isMemoryExceeded ? 'danger' : 'success'"
                    effect="dark"
                  >
                    当前: {{ memoryLineCount }} / 200 行
                  </el-tag>
                </div>
              </template>
              <el-input
                v-model="form.initial_memory"
                type="textarea"
                :rows="5"
                class="font-mono"
                placeholder="遵循 Claude Code 四层栈规范，行数严格 ≤ 200 行"
              />
              <div v-if="isMemoryExceeded" class="memory-alert">
                <el-icon><Warning /></el-icon>
                <span>警告：单体记忆行数已超出 200 行硬上限，将被系统拒绝！</span>
              </div>
            </el-form-item>

            <el-form-item label="绑定可用本地 Agent (M5 探针能力)">
              <el-checkbox-group v-model="form.allowed_agents">
                <el-checkbox
                  v-for="ag in availableAgents"
                  :key="ag.id"
                  :label="ag.id"
                >
                  {{ ag.name }}
                </el-checkbox>
              </el-checkbox-group>
            </el-form-item>

            <div class="form-submit-row">
              <el-button
                type="primary"
                size="large"
                :icon="Plus"
                :loading="submitting"
                :disabled="isMemoryExceeded || !form.bot_id"
                @click="handleSubmit"
              >
                派生并生效 (Supervisor 自动重启)
              </el-button>
            </div>
          </el-form>
        </div>
      </div>

      <!-- Right: Active Bots Status Overview -->
      <div class="bots-overview-card">
        <div class="card-header">
          <span class="card-title">当前系统活跃 Bot 席位</span>
          <el-button size="small" :icon="Refresh" @click="botsStore.fetchBots">刷新</el-button>
        </div>

        <div class="bots-list">
          <div
            v-for="bot in botsStore.bots"
            :key="bot.bot_id"
            class="active-bot-item"
          >
            <div class="bot-info-main">
              <el-avatar :size="32" class="bot-avatar">{{ bot.display_name.charAt(0) }}</el-avatar>
              <div>
                <div class="bot-name-row">
                  <span class="bot-title">{{ bot.display_name }}</span>
                  <span class="bot-id">({{ bot.bot_id }})</span>
                </div>
                <div class="bot-subinfo">
                  <el-tag size="small" effect="plain" type="info">{{ bot.role || 'bot' }}</el-tag>
                  <el-tag size="small" effect="dark" type="success">Resident 常驻</el-tag>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bot-creator-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  background-color: #1a1c22;
  color: #e0e0e0;
  padding: 16px;
  gap: 16px;
  box-sizing: border-box;
}

.view-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background-color: #21252d;
  border-radius: 8px;
  border: 1px solid #2d323f;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.view-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: #f0f2f5;
}

.view-subtitle {
  font-size: 12px;
  color: #909399;
}

.creator-grid {
  display: grid;
  grid-template-columns: 1fr 380px;
  gap: 16px;
  flex: 1;
  min-height: 0;
}

.form-card,
.bots-overview-card {
  display: flex;
  flex-direction: column;
  background-color: #21252d;
  border-radius: 8px;
  border: 1px solid #2d323f;
  overflow: hidden;
}

.card-header {
  padding: 12px 16px;
  background-color: #272c36;
  border-bottom: 1px solid #2d323f;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: #e4e7ed;
}

.template-selector {
  display: flex;
  align-items: center;
  gap: 8px;
}

.tpl-label {
  font-size: 12px;
  color: #909399;
}

.form-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
}

.form-row-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.label-with-counter {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}

.font-mono :deep(textarea) {
  font-family: monospace;
  font-size: 12px;
}

.memory-alert {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #f56c6c;
  font-size: 12px;
  margin-top: 6px;
}

.form-submit-row {
  margin-top: 24px;
  display: flex;
  justify-content: flex-end;
}

.bots-list {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.active-bot-item {
  padding: 10px 12px;
  background-color: #1a1c22;
  border-radius: 6px;
  border: 1px solid #2d323f;
}

.bot-info-main {
  display: flex;
  align-items: center;
  gap: 10px;
}

.bot-avatar {
  background-color: #409eff;
  font-weight: bold;
}

.bot-name-row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.bot-title {
  font-size: 13px;
  font-weight: 600;
  color: #f0f2f5;
}

.bot-id {
  font-size: 11px;
  color: #909399;
  font-family: monospace;
}

.bot-subinfo {
  margin-top: 4px;
  display: flex;
  gap: 6px;
}
</style>
