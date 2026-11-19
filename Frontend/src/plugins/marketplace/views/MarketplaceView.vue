<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Shop, Download, Search, Refresh, Box } from '@element-plus/icons-vue'

interface PluginItem {
  plugin_id: string
  name: string
  version: string
  description: string
  risk_level: string
  depth?: number
  max_depth: number
  has_cycle?: boolean
  digest?: string
  digest_match?: boolean
  installed?: boolean
  dependencies: string[]
  tags: string[]
}

const searchQuery = ref('')
const loading = ref(false)
const plugins = ref<PluginItem[]>([])
const activeStage = ref<'idle' | 'proposal' | 'demo'>('idle')
const currentPlugin = ref<PluginItem | null>(null)
const approvalDialogVisible = ref(false)
const proposalId = ref('')
const proposalChecksum = ref('')

const auditLogs = ref<any[]>([])

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

const startDownloadStage = async (p: PluginItem) => {
  currentPlugin.value = p
  proposalId.value = ''
  proposalChecksum.value = ''
  try {
    const res = await fetch('/v1/marketplace/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        plugin_id: p.plugin_id,
        digest: p.digest || '',
      }),
    })
    const data = await res.json()
    if (res.ok && data.proposal_id) {
      proposalId.value = data.proposal_id
      proposalChecksum.value = data.manifest_checksum || ''
      activeStage.value = 'proposal'
      approvalDialogVisible.value = true
      auditLogs.value.unshift({
        time: new Date().toLocaleString(),
        plugin_id: p.plugin_id,
        action: 'download',
        result: '提案 ' + data.proposal_id,
        auditor: '尚未安装',
      })
      ElMessage.success(`已生成安装提案，插件未安装`)
      return
    }
    if (res.ok && data.digest_ok && !data.proposal_id) {
      activeStage.value = 'demo'
      approvalDialogVisible.value = true
      ElMessage.warning('这是演示目录，没有生成安装提案')
      return
    }
    ElMessage.error(data.error || '下载未生成提案')
  } catch (err: any) {
    ElMessage.error('下载阶段失败: ' + err.message)
  }
}

// 阶段三：安全级联卸载
const uninstallPlugin = async (p: PluginItem) => {
  try {
    await ElMessageBox.confirm(
      `卸载 [${p.name}] 时，仍被依赖会拒绝单点卸载。级联只给出逆拓扑顺序，停机由内核执行。`,
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
    const data = await res.json()
    if (!res.ok) {
      ElMessage.error(data.error || '卸载被拒绝')
      return
    }
    if (data.order) {
      ElMessage.success(`卸载提案顺序：${data.order.join(' → ')}。停机仍由内核执行`)
      return
    }
    ElMessage.warning('演示接口返回了卸载。正式卸载走内核逆拓扑')
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
          <p class="subtitle">用一句话检索目录。下载只生成提案；安装要由 user_bot 在终端按 checksum 批准</p>
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
            <span>依赖:</span>
            <span>{{ p.dependencies?.length ? p.dependencies.join(', ') : '无' }}</span>
          </div>
          <div class="meta-row">
            <span>依赖深度:</span>
            <span class="depth-badge">Depth {{ p.depth ?? p.max_depth }} / Max {{ p.max_depth || 2 }}</span>
          </div>
          <div class="meta-row">
            <span>环路:</span>
            <span>{{ p.has_cycle ? '有环' : '无环' }}</span>
          </div>
          <div class="meta-row" v-if="p.digest_match !== undefined">
            <span>摘要:</span>
            <span>{{ p.digest_match ? '匹配' : '不匹配' }}</span>
          </div>
          <div class="meta-tags">
            <el-tag v-for="t in p.tags" :key="t" size="small" type="info">{{ t }}</el-tag>
          </div>
        </div>

        <div class="card-actions">
          <el-button type="primary" size="small" :icon="Download" @click="startDownloadStage(p)">
            下载并生成提案
          </el-button>
          <el-button type="danger" link size="small" @click="uninstallPlugin(p)">
            安全卸载
          </el-button>
        </div>
      </el-card>
    </div>

    <!-- 审计日志表格 -->
    <div class="audit-section">
      <h3>本次页面上的下载提案</h3>
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
    <el-dialog v-model="approvalDialogVisible" title="下载结果" width="600px">
      <div v-if="currentPlugin" class="approval-content">
        <el-alert
          v-if="activeStage === 'proposal'"
          title="只生成了安装提案"
          type="warning"
          description="插件还没有安装，也没有拉起进程。请由 user_bot 在交互终端按清单 checksum 批准。商场不能代替这次批准。"
          show-icon
          :closable="false"
          style="margin-bottom: 16px"
        />
        <el-alert
          v-else
          title="演示目录"
          type="info"
          description="未设置商场目录时，这里不会生成提案，也不会把插件标成已安装。"
          show-icon
          :closable="false"
          style="margin-bottom: 16px"
        />
        <div class="info-list">
          <div><strong>目标插件:</strong> {{ currentPlugin.name }} ({{ currentPlugin.plugin_id }})</div>
          <div v-if="proposalId"><strong>提案:</strong> {{ proposalId }}</div>
          <div v-if="proposalChecksum"><strong>checksum:</strong> {{ proposalChecksum.slice(0, 12) }}</div>
          <div><strong>依赖:</strong> {{ currentPlugin.dependencies?.join(', ') || '无' }}</div>
          <div><strong>深度 / 环路:</strong> {{ currentPlugin.depth ?? currentPlugin.max_depth }} / {{ currentPlugin.has_cycle ? '有环' : '无环' }}</div>
        </div>
      </div>
      <template #footer>
        <el-button type="primary" @click="approvalDialogVisible = false">知道了</el-button>
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
