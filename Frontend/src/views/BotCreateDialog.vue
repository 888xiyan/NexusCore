<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus, Cpu, Document, User, MagicStick, WarningFilled } from '@element-plus/icons-vue'
import { botsApi, type BotBlueprint, type AgentAsset } from '../api/bots'
import { useBotsStore } from '../stores/bots'

const props = defineProps<{
  visible: boolean
}>()

const emit = defineEmits<{
  (e: 'update:visible', val: boolean): void
  (e: 'created', botId: string): void
}>()

const botsStore = useBotsStore()
const loading = ref(false)
const agentsList = ref<AgentAsset[]>([])

// Form model
const form = ref<BotBlueprint>({
  bot_id: 'user_code_reviewer',
  display_name: '代码审查专家',
  role: 'reviewer',
  system_prompt: '你是一名资深代码审查专家。你的职责是对提交的代码进行全面、细致、建设性的 Review。关注点：代码整洁度、并发与线程安全、错误捕获上下文、架构一致性与边界防守。',
  persona: '客观严谨、建设性、注重质量与规范',
  initial_memory: `# 代码审查专家单体记忆

## 1. 核心定位
- **职责**: 专职 Code Review，协助团队把控质量。
- **风格**: 客观、指出问题同时给出具体改善建议。

## 2. 审查准则
- 检查是否存在吞错、忽略未处理 error 的情况；
- 检查并发 map 读写、goroutine 泄漏与 context 传递；
- 保持回答结构化，输出清晰结论。
`,
  bound_agent_cli: 'claude',
  bound_agent_id: 'loopback_agent',
  activation_mode: 'lazy',
})

const selectedTemplate = ref<string>('reviewer')

const applyTemplate = (tpl: string) => {
  selectedTemplate.value = tpl
  if (tpl === 'reviewer') {
    form.value.bot_id = 'user_code_reviewer'
    form.value.display_name = '代码审查专家'
    form.value.role = 'reviewer'
    form.value.persona = '客观严谨、建设性、注重质量与规范'
    form.value.system_prompt = '你是一名资深代码审查专家。负责全面细致的 Review，关注并发安全与错误处理。'
    form.value.bound_agent_cli = 'claude'
    form.value.initial_memory = `# 代码审查专家单体记忆\n- 重点关注并发安全与错误处理\n- 保持回答结构化\n`
  } else if (tpl === 'coder') {
    form.value.bot_id = 'user_coding_bot'
    form.value.display_name = '编程辅助专家'
    form.value.role = 'coder'
    form.value.persona = '高效极客、务实、崇尚简洁代码'
    form.value.system_prompt = '你是一名资深软件工程师。擅长实现高性能、高可维护的代码并编写表驱动测试。'
    form.value.bound_agent_cli = 'codex'
    form.value.initial_memory = `# 编程辅助助手单体记忆\n- 优先编写表驱动测试\n- 严格遵循脑手分离铁律\n`
  } else if (tpl === 'ops') {
    form.value.bot_id = 'user_ops_bot'
    form.value.display_name = '运维排错专家'
    form.value.role = 'ops'
    form.value.persona = '冷静沉着、以数据说话、警觉度高'
    form.value.system_prompt = '你是一名生产系统运维专家。负责服务巡检、日志分析、连通性探测以及事故根因分析。'
    form.value.bound_agent_cli = 'pi'
    form.value.initial_memory = `# 运维排错专家单体记忆\n- 绝不进行未经授权的高危操作\n- 提炼关键错误输出\n`
  }
}

// Memory line count calculation
const lineCount = computed(() => {
  if (!form.value.initial_memory) return 0
  return form.value.initial_memory.split('\n').length
})

// Enforce Claude Code <= 200 lines
const isOverLimit = computed(() => lineCount.value > 200)

const fetchAgents = async () => {
  try {
    const list = await botsApi.listAgents()
    agentsList.value = list || []
  } catch {
    agentsList.value = []
  }
}

onMounted(() => {
  fetchAgents()
})

watch(() => props.visible, (val) => {
  if (val) {
    fetchAgents()
  }
})

const handleClose = () => {
  emit('update:visible', false)
}

const handleSubmit = async () => {
  if (isOverLimit.value) {
    ElMessage.error('单体记忆 MEMORY.md 超过 200 行硬约束限制！')
    return
  }
  if (!form.value.bot_id || !form.value.display_name) {
    ElMessage.error('请填写完整的 Bot ID 和显示名称')
    return
  }

  loading.value = true
  try {
    await botsApi.createBot(form.value)
    ElMessage.success(`专职 Bot [${form.value.display_name}] 创建成功并已毫秒级激活上线！`)
    await botsStore.fetchBots()
    emit('created', form.value.bot_id)
    emit('update:visible', false)
  } catch (err: any) {
    ElMessage.error(err?.message || '创建 Bot 失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="props.visible"
    title="Bot 工厂 - 派生专职智能体"
    width="680px"
    destroy-on-close
    :before-close="handleClose"
  >
    <div v-loading="loading" class="dialog-body">
      <!-- 快捷模板选择 -->
      <div class="template-bar">
        <span class="bar-title"><el-icon><MagicStick /></el-icon> 快捷角色模板:</span>
        <el-radio-group v-model="selectedTemplate" size="small" @change="applyTemplate">
          <el-radio-button label="reviewer">代码审查专家</el-radio-button>
          <el-radio-button label="coder">编程辅助助手</el-radio-button>
          <el-radio-button label="ops">运维排错专家</el-radio-button>
        </el-radio-group>
      </div>

      <el-form label-position="top" class="form-container">
        <div class="form-grid">
          <el-form-item label="Bot 身份标识 (bot_id)" required>
            <el-input v-model="form.bot_id" placeholder="如 user_code_reviewer (小写、数字、下划线)" />
          </el-form-item>
          <el-form-item label="显示名称" required>
            <el-input v-model="form.display_name" placeholder="如 代码审查专家" />
          </el-form-item>
        </div>

        <div class="form-grid">
          <el-form-item label="角色分类">
            <el-input v-model="form.role" placeholder="如 reviewer, coder, ops" />
          </el-form-item>
          <el-form-item label="绑定本地 Agent CLI (M5 资产)">
            <el-select v-model="form.bound_agent_cli" placeholder="选择或填写绑定的本地 Agent CLI" allow-create filterable style="width: 100%">
              <el-option label="Claude Code (claude)" value="claude" />
              <el-option label="Codex CLI (codex)" value="codex" />
              <el-option label="Pi-Agent (pi)" value="pi" />
              <el-option label="Mock Agent (测试探针)" value="nexus-mock-agent" />
              <el-option
                v-for="ag in agentsList"
                :key="ag.agent_id"
                :label="`${ag.display_name || ag.agent_id} (${ag.status})`"
                :value="ag.agent_id"
              />
            </el-select>
          </el-form-item>
        </div>

        <el-form-item label="Persona (性格与语气特征)">
          <el-input v-model="form.persona" placeholder="如 客观严谨、建设性、注重质量与规范" />
        </el-form-item>

        <el-form-item label="System Prompt (核心系统指令)" required>
          <el-input v-model="form.system_prompt" type="textarea" :rows="3" placeholder="定义 Bot 的核心职责与遵循规则" />
        </el-form-item>

        <el-form-item>
          <template #label>
            <div class="memory-label-bar">
              <span><el-icon><Document /></el-icon> 初始单体记忆 (Claude Code MEMORY.md ≤ 200 行)</span>
              <el-tag
                size="small"
                :type="isOverLimit ? 'danger' : 'success'"
                class="line-tag"
              >
                {{ lineCount }} / 200 行 {{ isOverLimit ? '(超过限制)' : '' }}
              </el-tag>
            </div>
          </template>
          <el-input
            v-model="form.initial_memory"
            type="textarea"
            :rows="6"
            placeholder="输入初始化 MEMORY.md 内容（严格限制在 200 行以内）"
            class="code-input"
          />
        </el-form-item>
      </el-form>
    </div>

    <template #footer>
      <div class="dialog-footer">
        <el-button @click="handleClose">取消</el-button>
        <el-button
          type="primary"
          :disabled="isOverLimit || !form.bot_id || !form.display_name"
          @click="handleSubmit"
        >
          <el-icon><Plus /></el-icon> 创建并激活上线
        </el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.dialog-body {
  padding: 0 10px;
}
.template-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--el-fill-color-light);
  padding: 10px 14px;
  border-radius: 6px;
  margin-bottom: 18px;
}
.bar-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  display: flex;
  align-items: center;
  gap: 6px;
}
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.memory-label-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}
.line-tag {
  font-weight: bold;
}
.code-input :deep(textarea) {
  font-family: 'JetBrains Mono', 'Fira Code', Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
}
</style>
