<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Tools, Key, UserFilled, DocumentAdd, Edit, Delete, Warning, Check, Refresh } from '@element-plus/icons-vue'

interface ToolItem {
  tool_id: string
  name: string
  description: string
  category: string
  is_editable: boolean
  runtime: string
  code?: string
  config_schema?: string
  network_requirement: string
  tier: string
  version: string
}

interface GrantItem {
  grant_id: string
  bot_id: string
  tool_id: string
  permission: string
  granted_by: string
  granted_at: string
  status: string
}

const activeTab = ref('tools')
const loading = ref(false)
const tools = ref<ToolItem[]>([])
const grants = ref<GrantItem[]>([])
const selectedBot = ref('coder_bot')

// 技能创建与编辑对话框
const skillDialogVisible = ref(false)
const isEditMode = ref(false)
const skillForm = ref({
  tool_id: '',
  name: '',
  description: '',
  runtime: 'python',
  code: 'def run(params):\n    # 自定义技能逻辑\n    return "Processed: " + str(params)',
  config_schema: '{"type":"object"}',
  tier: 'tier1',
  network_requirement: 'none',
})

// 中台代批评估模拟表单
const delegationForm = ref({
  proposer_bot_id: 'assistant_bot',
  action_type: 'ui.theme.set',
  target: 'dark',
  user_confirmed: false,
})
const delegationResult = ref<any>(null)

const fetchTools = async () => {
  loading.value = true
  try {
    const res = await fetch('/v1/tools')
    if (res.ok) {
      const data = await res.json()
      tools.value = data.tools || []
    }
  } catch (err: any) {
    ElMessage.error('加载工具列表失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const fetchGrants = async () => {
  try {
    const res = await fetch(`/v1/tools/grants?bot_id=${selectedBot.value}`)
    if (res.ok) {
      const data = await res.json()
      grants.value = data.grants || []
    }
  } catch (err: any) {
    ElMessage.error('加载授权记录失败: ' + err.message)
  }
}

const openCreateSkillDialog = () => {
  isEditMode.value = false
  skillForm.value = {
    tool_id: 'skill.custom.' + Math.floor(Math.random() * 1000),
    name: '新建自定义技能',
    description: '通过 Python 或 Shell 实现的轻量可编辑技能',
    runtime: 'python',
    code: 'def run(params):\n    return "Result: OK"',
    config_schema: '{"type":"object"}',
    tier: 'tier1',
    network_requirement: 'none',
  }
  skillDialogVisible.value = true
}

const openEditSkillDialog = (tool: ToolItem) => {
  if (!tool.is_editable) {
    ElMessage.warning('系统原生工具属于不可编辑工具，禁止在线篡改源码')
    return
  }
  isEditMode.value = true
  skillForm.value = {
    tool_id: tool.tool_id,
    name: tool.name,
    description: tool.description,
    runtime: tool.runtime,
    code: tool.code || '',
    config_schema: tool.config_schema || '{"type":"object"}',
    tier: tool.tier,
    network_requirement: tool.network_requirement,
  }
  skillDialogVisible.value = true
}

const saveSkill = async () => {
  try {
    if (isEditMode.value) {
      const res = await fetch(`/v1/tools/skills/${skillForm.value.tool_id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          code: skillForm.value.code,
          config_schema: skillForm.value.config_schema,
        }),
      })
      if (!res.ok) throw new Error((await res.json()).error || '更新失败')
      ElMessage.success('技能源码已更新并实时生效')
    } else {
      const res = await fetch('/v1/tools/skills', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...skillForm.value,
          is_editable: true,
          category: 'editable_skill',
          version: '1.0.0',
        }),
      })
      if (!res.ok) throw new Error((await res.json()).error || '创建失败')
      ElMessage.success('自定义技能创建成功')
    }
    skillDialogVisible.value = false
    await fetchTools()
  } catch (err: any) {
    ElMessage.error(err.message)
  }
}

const deleteSkill = async (toolID: string) => {
  try {
    await ElMessageBox.confirm(`确定要永久删除技能 [${toolID}] 吗？`, '删除确认', {
      type: 'warning',
    })
    const res = await fetch(`/v1/tools/skills/${toolID}`, { method: 'DELETE' })
    if (!res.ok) throw new Error((await res.json()).error || '删除失败')
    ElMessage.success('技能已成功删除')
    await fetchTools()
  } catch (err: any) {
    if (err !== 'cancel') ElMessage.error(err.message)
  }
}

const grantToolToBot = async (toolID: string) => {
  try {
    const res = await fetch('/v1/tools/grants', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        bot_id: selectedBot.value,
        tool_id: toolID,
        permission: 'execute',
        granted_by: 'user_bot',
      }),
    })
    if (!res.ok) throw new Error('授权失败')
    ElMessage.success(`已将 [${toolID}] 执行权限授予 ${selectedBot.value}`)
    await fetchGrants()
  } catch (err: any) {
    ElMessage.error(err.message)
  }
}

const revokeGrant = async (toolID: string) => {
  try {
    const res = await fetch('/v1/tools/grants/revoke', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        bot_id: selectedBot.value,
        tool_id: toolID,
      }),
    })
    if (!res.ok) throw new Error('吊销失败')
    ElMessage.success('已吊销该工具执行授权')
    await fetchGrants()
  } catch (err: any) {
    ElMessage.error(err.message)
  }
}

const evaluateDelegation = async () => {
  try {
    const res = await fetch('/v1/delegation/evaluate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        proposer_bot_id: delegationForm.value.proposer_bot_id,
        action_type: delegationForm.value.action_type,
        params: { target: delegationForm.value.target },
      }),
    })
    const data = await res.json()
    delegationResult.value = data
  } catch (err: any) {
    ElMessage.error('评估请求失败: ' + err.message)
  }
}

const executeDelegation = async () => {
  try {
    const res = await fetch('/v1/delegation/execute', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        proposer_bot_id: delegationForm.value.proposer_bot_id,
        action_type: delegationForm.value.action_type,
        params: { target: delegationForm.value.target },
        user_confirmed: delegationForm.value.user_confirmed,
      }),
    })
    const data = await res.json()
    if (res.ok) {
      ElMessage.success('操作执行成功 (已批准通过)')
    } else {
      ElMessage.error('执行被拦截: ' + (data.error || '需要人工确认'))
    }
    delegationResult.value = data
  } catch (err: any) {
    ElMessage.error(err.message)
  }
}

onMounted(() => {
  fetchTools()
  fetchGrants()
})
</script>

<template>
  <div class="tool-center-view">
    <div class="view-header">
      <div class="title-zone">
        <el-icon class="header-icon"><Tools /></el-icon>
        <div>
          <h2>工具与技能中心 (Tool Center)</h2>
          <p class="subtitle">管理系统原生工具、在线编写可编辑 Skill、治理 Bot-Tool 授权矩阵与中台代批安全防线</p>
        </div>
      </div>
      <div class="action-zone">
        <el-button type="primary" :icon="DocumentAdd" @click="openCreateSkillDialog">
          新建可编辑 Skill
        </el-button>
        <el-button :icon="Refresh" @click="fetchTools">刷新</el-button>
      </div>
    </div>

    <el-tabs v-model="activeTab" class="tool-tabs">
      <!-- Tab 1: 工具与技能库 -->
      <el-tab-pane label="工具与技能生态" name="tools">
        <el-table :data="tools" v-loading="loading" stripe style="width: 100%">
          <el-table-column prop="tool_id" label="工具 ID" width="200" />
          <el-table-column prop="name" label="名称" width="180" />
          <el-table-column label="分类" width="130">
            <template #default="{ row }">
              <el-tag v-if="row.category === 'system_native'" type="info" effect="dark">系统不可编辑</el-tag>
              <el-tag v-else-if="row.category === 'editable_skill'" type="success" effect="dark">可编辑 Skill</el-tag>
              <el-tag v-else type="warning" effect="dark">外部 MCP</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="description" label="功能说明" min-width="220" />
          <el-table-column label="网络隔离" width="140">
            <template #default="{ row }">
              <el-tag v-if="row.network_requirement === 'none'" type="info" size="small">完全离线</el-tag>
              <el-tag v-else-if="row.network_requirement === 'egress_whitelist'" type="warning" size="small">443受控外联</el-tag>
              <el-tag v-else type="danger" size="small">任意网络</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="风险等级" width="100">
            <template #default="{ row }">
              <el-tag v-if="row.tier === 'tier1'" type="success" size="small">Tier 1 低危</el-tag>
              <el-tag v-else type="danger" size="small">Tier 2 高危</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="160" fixed="right">
            <template #default="{ row }">
              <el-button
                v-if="row.is_editable"
                size="small"
                type="primary"
                link
                :icon="Edit"
                @click="openEditSkillDialog(row)"
              >
                编辑源码
              </el-button>
              <span v-else class="text-muted">固化内核</span>
              <el-button
                v-if="row.is_editable"
                size="small"
                type="danger"
                link
                :icon="Delete"
                @click="deleteSkill(row.tool_id)"
              >
                删除
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- Tab 2: Bot-Tool 授权矩阵 -->
      <el-tab-pane label="Bot 授权矩阵 (bot_tool_grants)" name="grants">
        <div class="matrix-toolbar">
          <span>选择受控 Bot：</span>
          <el-select v-model="selectedBot" style="width: 220px" @change="fetchGrants">
            <el-option label="coder_bot (业务开发)" value="coder_bot" />
            <el-option label="reviewer_bot (代码审查)" value="reviewer_bot" />
            <el-option label="ops_bot (运维诊断)" value="ops_bot" />
            <el-option label="search_bot (网络搜索)" value="search_bot" />
          </el-select>
        </div>

        <div class="matrix-grid">
          <div class="grants-list-card">
            <h3>已授予权限列表</h3>
            <el-table :data="grants" empty-text="当前 Bot 尚未授予任何工具权限">
              <el-table-column prop="tool_id" label="工具 ID" />
              <el-table-column prop="permission" label="权限级别" width="100" />
              <el-table-column prop="status" label="状态" width="100">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.status === 'active' ? 'success' : 'danger'">
                    {{ row.status }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="100">
                <template #default="{ row }">
                  <el-button size="small" type="danger" link @click="revokeGrant(row.tool_id)">
                    吊销
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>

          <div class="available-tools-card">
            <h3>可授权工具候选池</h3>
            <div class="tool-chips">
              <div v-for="t in tools" :key="t.tool_id" class="tool-chip-item">
                <div class="chip-info">
                  <strong>{{ t.name }}</strong>
                  <small>{{ t.tool_id }}</small>
                </div>
                <el-button size="small" type="primary" @click="grantToolToBot(t.tool_id)">
                  授予权限
                </el-button>
              </div>
            </div>
          </div>
        </div>
      </el-tab-pane>

      <!-- Tab 3: 中台 Bot 代批 RBAC 审计与测试 -->
      <el-tab-pane label="中台代批 RBAC 网关" name="delegation">
        <div class="delegation-sandbox">
          <div class="sandbox-intro">
            <h3>中台管家 Bot 分级代批 RBAC 验证</h3>
            <p>
              中台 Bot（HubBot）以 <code>user_bot</code> 身份入总线代理调度。系统构建刚性安全防线：
              <strong>Tier 1 允许受控代批</strong>（主题切换、只读查询）；
              <strong>Tier 2 严禁代批</strong>（插件安装、进程 Exec、沙箱网络放行），必须人类用户显式确认 (Mandatory HITL)。
            </p>
          </div>

          <el-form :model="delegationForm" label-width="140px" style="max-width: 600px">
            <el-form-item label="代理发起 Bot">
              <el-input v-model="delegationForm.proposer_bot_id" disabled />
            </el-form-item>
            <el-form-item label="申请操作类型">
              <el-select v-model="delegationForm.action_type" style="width: 100%">
                <el-option label="[Tier 1 低危] 切换 UI 样式 (ui.theme.set)" value="ui.theme.set" />
                <el-option label="[Tier 1 低危] 系统健康度查询 (system.health.get)" value="system.health.get" />
                <el-option label="[Tier 2 高危] 远程插件安装 (plugin.install)" value="plugin.install" />
                <el-option label="[Tier 2 高危] 执行物理 Shell (process.exec)" value="process.exec" />
                <el-option label="[Tier 2 高危] 沙箱网络穿透放行 (network.bypass)" value="network.bypass" />
              </el-select>
            </el-form-item>
            <el-form-item label="操作目标/参数">
              <el-input v-model="delegationForm.target" />
            </el-form-item>
            <el-form-item label="人类用户确认">
              <el-checkbox v-model="delegationForm.user_confirmed">
                已在终端/界面人工确认 (HITL Signature)
              </el-checkbox>
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="evaluateDelegation">评估代批分级</el-button>
              <el-button type="success" @click="executeDelegation">模拟执行</el-button>
            </el-form-item>
          </el-form>

          <div v-if="delegationResult" class="decision-result-panel">
            <h4>代批判定决策结论：</h4>
            <pre>{{ JSON.stringify(delegationResult, null, 2) }}</pre>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 技能编辑模态框 -->
    <el-dialog v-model="skillDialogVisible" :title="isEditMode ? '编辑 Skill 源码' : '新建可编辑 Skill'" width="650px">
      <el-form :model="skillForm" label-width="120px">
        <el-form-item label="工具 ID">
          <el-input v-model="skillForm.tool_id" :disabled="isEditMode" />
        </el-form-item>
        <el-form-item label="显示名称">
          <el-input v-model="skillForm.name" :disabled="isEditMode" />
        </el-form-item>
        <el-form-item label="描述说明">
          <el-input v-model="skillForm.description" :disabled="isEditMode" />
        </el-form-item>
        <el-form-item label="运行时环境">
          <el-select v-model="skillForm.runtime" :disabled="isEditMode" style="width: 100%">
            <el-option label="Python 3" value="python" />
            <el-option label="Shell Script" value="shell" />
          </el-select>
        </el-form-item>
        <el-form-item label="技能源码">
          <el-input v-model="skillForm.code" type="textarea" :rows="8" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="skillDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveSkill">保存并应用</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.tool-center-view {
  padding: 24px;
  background: var(--bg-primary, #fff);
  min-height: calc(100vh - 80px);
}
.view-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
  border-bottom: 1px solid var(--border-color, #e2e8f0);
  padding-bottom: 16px;
}
.title-zone {
  display: flex;
  align-items: center;
  gap: 16px;
}
.header-icon {
  font-size: 32px;
  color: var(--el-color-primary, #409eff);
}
.subtitle {
  margin: 4px 0 0 0;
  color: #64748b;
  font-size: 13px;
}
.matrix-toolbar {
  margin-bottom: 16px;
  display: flex;
  align-items: center;
  gap: 12px;
}
.matrix-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}
.grants-list-card, .available-tools-card {
  border: 1px solid var(--border-color, #e2e8f0);
  border-radius: 8px;
  padding: 16px;
  background: var(--bg-secondary, #f8fafc);
}
.tool-chips {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 12px;
}
.tool-chip-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 14px;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
}
.chip-info strong {
  display: block;
  font-size: 14px;
}
.chip-info small {
  color: #94a3b8;
  font-size: 11px;
}
.delegation-sandbox {
  padding: 16px;
  background: var(--bg-secondary, #f8fafc);
  border-radius: 8px;
}
.sandbox-intro {
  margin-bottom: 20px;
}
.decision-result-panel {
  margin-top: 24px;
  padding: 16px;
  background: #1e293b;
  color: #38bdf8;
  border-radius: 8px;
}
.text-muted {
  color: #94a3b8;
  font-size: 12px;
}
</style>
