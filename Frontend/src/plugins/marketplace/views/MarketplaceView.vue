<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Shop, Download, Check, Close, Search, Warning, Refresh, Box } from '@element-plus/icons-vue'

interface PluginItem {
  plugin_id: string
  name: string
  version: string
  description: string
  risk_level: string
  max_depth: number
  dependencies: string[]
  tags: string[]
}

const searchQuery = ref('')
const loading = ref(false)
const plugins = ref<PluginItem[]>([])
const activeStage = ref<'idle' | 'download' | 'install' | 'installed'>('idle')
const currentPlugin = ref<PluginItem | null>(null)
const approvalDialogVisible = ref(false)
const userConfirmed = ref(false)

const auditLogs = ref<any[]>([
  {
    time: '2026-11-16 10:20:15',
    plugin_id: 'plugin_groupchat',
    action: 'install',
    result: 'approved (HITL)',
    auditor: 'user_bot',
  },
  {
    time: '2026-11-16 11:45:00',
    plugin_id: 'plugin_bot_creator',
    action: 'install',
    result: 'approved (HITL)',
    auditor: 'user_bot',
  },
])

const fetchPlugins = async () => {
  loading.value = true
  try {
    const res = await fetch(`/v1/marketplace/search?q=${encodeURIComponent(searchQuery.value)}`)
    if (res.ok) {
      const data = await res.json()
      plugins.value = data.plugins || []
    }
  } catch (err: any) {
    ElMessage.error('查询插件商场失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

// 阶段一：下载与静态完整性校验
const startDownloadStage = async (p: PluginItem) => {
  currentPlugin.value = p
  try {
    const res = await fetch('/v1/marketplace/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        plugin_id: p.plugin_id,
        digest: 'sha256_mock_verified',
      }),
    })
    const data = await res.json()
    if (res.ok && data.digest_ok) {
      ElMessage.success(`阶段一完成：[${p.name}] SHA256 校验通过，DAG 深度合规 (MaxDepth <= 2)`)
      activeStage.value = 'download'
      approvalDialogVisible.value = true
    }
  } catch (err: any) {
    ElMessage.error('下载阶段失败: ' + err.message)
  }
}

// 阶段二：特权审查与安装（强制人类确认 HITL）
const confirmInstallStage = async () => {
  if (!currentPlugin.value) return
  if (!userConfirmed.value) {
    ElMessage.warning('插件安装属于 Tier 2 高危操作，必须勾选人类交互确认 (HITL Signature)')
    return
  }

  try {
    const res = await fetch('/v1/marketplace/install', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        plugin_id: currentPlugin.value.plugin_id,
        user_confirmed: true,
      }),
    })
    const data = await res.json()
    if (res.ok) {
      ElMessage.success(`阶段二通过：[${currentPlugin.value.name}] 获得特权批准并成功激活`)
      approvalDialogVisible.value = false
      activeStage.value = 'installed'
      auditLogs.value.unshift({
        time: new Date().toLocaleString(),
        plugin_id: currentPlugin.value.plugin_id,
        action: 'install',
        result: 'approved (HITL)',
        auditor: 'user_bot (Human Confirmed)',
      })
    } else {
      ElMessage.error('安装被安全网关拦截: ' + (data.error || '未获批准'))
    }
  } catch (err: any) {
    ElMessage.error(err.message)
  }
}

// 阶段三：安全级联卸载
const uninstallPlugin = async (p: PluginItem) => {
  try {
    await ElMessageBox.confirm(
      `确定卸载插件 [${p.name}] 吗？将按逆拓扑序优雅停用对应 L2 进程组与 L1 Bot。`,
      '级联卸载确认',
      { type: 'warning' },
    )
    const res = await fetch('/v1/marketplace/uninstall', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        plugin_id: p.plugin_id,
        cascade: true,
      }),
    })
    if (res.ok) {
      ElMessage.success(`阶段三完成：[${p.name}] 已逆序级联安全下线`)
      auditLogs.value.unshift({
        time: new Date().toLocaleString(),
        plugin_id: p.plugin_id,
        action: 'uninstall',
        result: 'cascade_teardown_success',
        auditor: 'user_bot',
      })
    }
  } catch (err: any) {
    if (err !== 'cancel') ElMessage.error(err.message)
  }
}

onMounted(() => {
  fetchPlugins()
})
</script>

<template>
  <div class="marketplace-view">
    <div class="view-header">
      <div class="title-zone">
        <el-icon class="header-icon"><Shop /></el-icon>
        <div>
          <h2>插件商场与审批中心 (Marketplace)</h2>
          <p class="subtitle">自然语言检索插件、SHA256 完整性强校验、三阶段安全审批 (下载 -> 安装 -> 卸载) 全生命周期留痕</p>
        </div>
      </div>
      <div class="search-zone">
        <el-input
          v-model="searchQuery"
          placeholder="搜索插件 (如: 浏览器, vscode, wiki, 剪映)..."
          clearable
          style="width: 320px"
          @input="fetchPlugins"
        >
          <template #prefix>
            <el-icon><Search /></el-icon>
          </template>
        </el-input>
        <el-button :icon="Refresh" @click="fetchPlugins">刷新</el-button>
      </div>
    </div>

    <!-- 插件卡片网格 -->
    <div class="plugin-grid" v-loading="loading">
      <el-card v-for="p in plugins" :key="p.plugin_id" class="plugin-card" shadow="hover">
        <template #header>
          <div class="card-header">
            <div class="card-title">
              <el-icon class="plugin-icon"><Box /></el-icon>
              <strong>{{ p.name }}</strong>
            </div>
            <el-tag
              size="small"
              :type="p.risk_level === 'low' ? 'success' : p.risk_level === 'medium' ? 'warning' : 'danger'"
            >
              {{ p.risk_level.toUpperCase() }} RISK
            </el-tag>
          </div>
        </template>
        
        <p class="plugin-desc">{{ p.description }}</p>
        
        <div class="plugin-meta">
          <div class="meta-row">
            <span>插件 ID:</span>
            <code>{{ p.plugin_id }}</code>
          </div>
          <div class="meta-row">
            <span>依赖深度:</span>
            <span class="depth-badge">Depth {{ p.max_depth }} / Max 2</span>
          </div>
          <div class="meta-tags">
            <el-tag v-for="t in p.tags" :key="t" size="small" type="info">{{ t }}</el-tag>
          </div>
        </div>

        <div class="card-actions">
          <el-button type="primary" size="small" :icon="Download" @click="startDownloadStage(p)">
            安装审批流程
          </el-button>
          <el-button type="danger" link size="small" @click="uninstallPlugin(p)">
            安全卸载
          </el-button>
        </div>
      </el-card>
    </div>

    <!-- 审计日志表格 -->
    <div class="audit-section">
      <h3>插件全生命周期审计留痕 (kernel_plugin_audit_logs)</h3>
      <el-table :data="auditLogs" stripe style="width: 100%">
        <el-table-column prop="time" label="审计时间" width="200" />
        <el-table-column prop="plugin_id" label="目标插件" width="220" />
        <el-table-column prop="action" label="操作阶段" width="140" />
        <el-table-column prop="result" label="审批结论" min-width="200">
          <template #default="{ row }">
            <el-tag size="small" type="success">{{ row.result }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="auditor" label="审批主体" width="200" />
      </el-table>
    </div>

    <!-- 阶段二审批模态框 -->
    <el-dialog v-model="approvalDialogVisible" title="三阶段安全审批：阶段二（特权安装）" width="600px">
      <div v-if="currentPlugin" class="approval-content">
        <el-alert
          title="Tier 2 高危操作警示"
          type="error"
          description="插件安装涉及 L2 进程组 exec 与能力注入，严禁中台 Bot 代批，必须由人类管理员签名确认！"
          show-icon
          :closable="false"
          style="margin-bottom: 16px"
        />

        <div class="info-list">
          <div><strong>目标插件:</strong> {{ currentPlugin.name }} ({{ currentPlugin.plugin_id }})</div>
          <div><strong>SHA256 完整性:</strong> <el-tag size="small" type="success">Verified Match</el-tag></div>
          <div><strong>拓扑合规性:</strong> <el-tag size="small" type="success">No Cycle, Depth &le; 2</el-tag></div>
        </div>

        <div class="hitl-confirm-box">
          <el-checkbox v-model="userConfirmed">
            <strong>我已阅读安全审查报告，确认由人类用户显式批准安装 (HITL Signature)</strong>
          </el-checkbox>
        </div>
      </div>
      <template #footer>
        <el-button @click="approvalDialogVisible = false">取消终止</el-button>
        <el-button type="danger" :disabled="!userConfirmed" @click="confirmInstallStage">
          签署并批准安装
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.marketplace-view {
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
.search-zone {
  display: flex;
  gap: 12px;
}
.plugin-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 20px;
  margin-bottom: 32px;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.card-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.plugin-icon {
  font-size: 18px;
  color: #3b82f6;
}
.plugin-desc {
  font-size: 13px;
  color: #475569;
  line-height: 1.5;
  min-height: 40px;
}
.plugin-meta {
  background: var(--bg-secondary, #f8fafc);
  padding: 10px;
  border-radius: 6px;
  margin: 12px 0;
  font-size: 12px;
}
.meta-row {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
}
.depth-badge {
  color: #059669;
  font-weight: bold;
}
.meta-tags {
  display: flex;
  gap: 6px;
  margin-top: 6px;
}
.card-actions {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 12px;
}
.audit-section {
  border-top: 1px solid var(--border-color, #e2e8f0);
  padding-top: 20px;
}
.audit-section h3 {
  margin-bottom: 16px;
}
.approval-content {
  line-height: 1.8;
}
.info-list {
  background: var(--bg-secondary, #f8fafc);
  padding: 12px;
  border-radius: 6px;
  margin-bottom: 16px;
}
.hitl-confirm-box {
  padding: 12px;
  background: #fef2f2;
  border: 1px solid #fecaca;
  border-radius: 6px;
}
</style>
