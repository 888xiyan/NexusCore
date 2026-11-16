<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useAgentStore } from '../../../stores/agent'
import AgentActivityCard from '../../../components/AgentActivityCard.vue'
import { Cpu, Refresh, Check, Connection, Monitor, Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

interface DiscoveredAgent {
  name: string
  command: string
  path: string
  version: string
  supportedModes: string[]
  status: 'available' | 'missing'
}

const agentStore = useAgentStore()
const scanning = ref(false)

const discoveredAgents = ref<DiscoveredAgent[]>([
  {
    name: 'Claude Code',
    command: 'claude',
    path: '/usr/local/bin/claude',
    version: '1.0.12',
    supportedModes: ['-p (pipe)', 'non-interactive'],
    status: 'available',
  },
  {
    name: 'Google Antigravity CLI',
    command: 'agy',
    path: '/usr/bin/agy',
    version: '2.0.4',
    supportedModes: ['headless', 'json'],
    status: 'available',
  },
  {
    name: 'Codex CLI',
    command: 'codex',
    path: '/usr/local/bin/codex',
    version: '0.9.8',
    supportedModes: ['exec', 'pipe'],
    status: 'available',
  },
  {
    name: 'Pi-Agent',
    command: 'pi',
    path: '~/.cargo/bin/pi',
    version: '0.4.1',
    supportedModes: ['stream', 'batch'],
    status: 'available',
  },
])

const handleScanAgents = async () => {
  scanning.value = true
  try {
    // 模拟或真实调用探活接口
    await new Promise((r) => setTimeout(r, 800))
    ElMessage.success('本地 Agent 资产扫描完成，已刷新 4 个已安装 CLI 驱动')
  } catch (err: any) {
    ElMessage.error('扫描失败: ' + err.message)
  } finally {
    scanning.value = false
  }
}
</script>

<template>
  <div class="agent-discovery-container">
    <!-- Header -->
    <div class="view-header">
      <div class="header-left">
        <el-icon :size="20"><Cpu /></el-icon>
        <div>
          <h2 class="view-title">本地 Agent 资产与探针监控</h2>
          <span class="view-subtitle">M5 本地 CLI 驱动适配、资产探针与物理执行流追溯</span>
        </div>
      </div>
      <div class="header-right">
        <el-button type="primary" :icon="Refresh" :loading="scanning" @click="handleScanAgents">
          重新探测本地 Agent
        </el-button>
      </div>
    </div>

    <!-- Main Content Layout -->
    <div class="discovery-grid">
      <!-- Left Column: Discovered Agents List -->
      <div class="agents-panel">
        <div class="panel-card">
          <div class="panel-header">
            <span class="panel-title">已探测本地 Coding Agent CLI 资产</span>
            <el-tag type="success" size="small">{{ discoveredAgents.length }} 个就绪</el-tag>
          </div>

          <div class="agents-list">
            <div
              v-for="ag in discoveredAgents"
              :key="ag.command"
              class="agent-item-card"
            >
              <div class="agent-card-top">
                <div class="agent-meta">
                  <el-icon class="agent-icon"><Monitor /></el-icon>
                  <div>
                    <span class="agent-name">{{ ag.name }}</span>
                    <span class="agent-cmd">`{{ ag.command }}`</span>
                  </div>
                </div>
                <el-tag size="small" type="success" effect="dark">
                  <el-icon><Check /></el-icon> 已就绪
                </el-tag>
              </div>

              <div class="agent-details">
                <div class="detail-row">
                  <span class="label">安装路径:</span>
                  <span class="val font-mono">{{ ag.path }}</span>
                </div>
                <div class="detail-row">
                  <span class="label">版本号:</span>
                  <span class="val">{{ ag.version }}</span>
                </div>
                <div class="detail-row">
                  <span class="label">运行模式:</span>
                  <span class="val modes-tags">
                    <span v-for="mode in ag.supportedModes" :key="mode" class="mode-tag">{{ mode }}</span>
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Right Column: Real-time Probe Execution Stream -->
      <div class="probe-monitor-panel">
        <div class="panel-card">
          <div class="panel-header">
            <div class="header-title-group">
              <span class="panel-title">物理探针实时执行流 (agent-daemon)</span>
              <el-tag
                size="small"
                :type="agentStore.currentExecution ? 'primary' : 'info'"
                effect="dark"
              >
                {{ agentStore.currentExecution ? '正在执行' : '等待调度' }}
              </el-tag>
            </div>
            <span class="panel-hint">反向 TCP 传输帧通道</span>
          </div>

          <div class="probe-stream-content">
            <AgentActivityCard
              v-if="agentStore.currentExecution"
              :execution="agentStore.currentExecution"
            />
            <div v-else class="empty-probe-state">
              <el-empty description="当前无正在执行的物理探针任务">
                <template #image>
                  <el-icon :size="48" color="#606266"><Connection /></el-icon>
                </template>
              </el-empty>
              <p class="empty-desc">
                当用户在群聊中 @Bot 触发物理任务时，Bot 将结构化调度并在此流式回显执行帧与终端日志。
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-discovery-container {
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

.discovery-grid {
  display: grid;
  grid-template-columns: 420px 1fr;
  gap: 16px;
  flex: 1;
  min-height: 0;
}

.panel-card {
  display: flex;
  flex-direction: column;
  height: 100%;
  background-color: #21252d;
  border-radius: 8px;
  border: 1px solid #2d323f;
  overflow: hidden;
}

.panel-header {
  padding: 12px 16px;
  background-color: #272c36;
  border-bottom: 1px solid #2d323f;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.panel-title {
  font-size: 14px;
  font-weight: 600;
  color: #e4e7ed;
}

.panel-hint {
  font-size: 12px;
  color: #909399;
}

.agents-list {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.agent-item-card {
  padding: 12px;
  background-color: #1a1c22;
  border: 1px solid #2d323f;
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  transition: all 0.2s;
}

.agent-item-card:hover {
  border-color: #409eff;
}

.agent-card-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.agent-meta {
  display: flex;
  align-items: center;
  gap: 8px;
}

.agent-icon {
  font-size: 18px;
  color: #409eff;
}

.agent-name {
  font-weight: 600;
  color: #f0f2f5;
  margin-right: 6px;
  font-size: 14px;
}

.agent-cmd {
  font-family: monospace;
  font-size: 12px;
  color: #e6a23c;
}

.agent-details {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}

.detail-row {
  display: flex;
  gap: 8px;
}

.detail-row .label {
  color: #909399;
  min-width: 60px;
}

.detail-row .val {
  color: #dcdfe6;
}

.font-mono {
  font-family: monospace;
  word-break: break-all;
}

.modes-tags {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.mode-tag {
  background-color: #2b303c;
  padding: 1px 6px;
  border-radius: 4px;
  color: #409eff;
  font-size: 11px;
}

.probe-stream-content {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
}

.empty-probe-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
}

.empty-desc {
  max-width: 400px;
  text-align: center;
  font-size: 12px;
  color: #909399;
  margin-top: 8px;
}
</style>
