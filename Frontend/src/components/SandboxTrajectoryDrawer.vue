<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import {
  Document,
  Lock,
  Search,
  Check,
  Close,
  Timer,
  Cpu,
} from '@element-plus/icons-vue'

const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', val: boolean): void
}>()

export interface TrajectoryItem {
  trajectory_id: string
  traceparent: string
  bot_id: string
  tool_id: string
  tool_type: string
  sandbox_id: string
  network_policy: string
  input_payload: any
  exit_code: number
  raw_output_ref?: string
  distilled_output: string
  duration_ms: number
  executed_at: string
}

const trajectories = ref<TrajectoryItem[]>([])
const loading = ref(false)

const fetchTrajectories = async () => {
  loading.value = true
  try {
    const res = await fetch('http://127.0.0.1:7200/v1/trajectories')
    if (res.ok) {
      const data = await res.json()
      trajectories.value = data.trajectories || []
    }
  } catch (err) {
    console.warn('获取沙盒工具轨迹失败:', err)
  } finally {
    loading.value = false
  }
}

let evtSource: EventSource | null = null

onMounted(() => {
  fetchTrajectories()
  try {
    evtSource = new EventSource('http://127.0.0.1:7200/v1/events/stream')
    evtSource.addEventListener('tool_trajectory', (e: MessageEvent) => {
      try {
        const item: TrajectoryItem = JSON.parse(e.data)
        trajectories.value.unshift(item)
        if (trajectories.value.length > 200) {
          trajectories.value.pop()
        }
      } catch (err) {
        console.error('解析 SSE 轨迹事件失败:', err)
      }
    })
  } catch (err) {
    console.warn('SSE 连接失败:', err)
  }
})

onUnmounted(() => {
  if (evtSource) {
    evtSource.close()
    evtSource = null
  }
})

const formatPayload = (payload: any) => {
  if (typeof payload === 'string') return payload
  try {
    return JSON.stringify(payload, null, 2)
  } catch {
    return String(payload)
  }
}
</script>

<template>
  <el-drawer
    :model-value="modelValue"
    title="内置沙盒 · 工具调用轨迹审计"
    size="560px"
    destroy-on-close
    @update:model-value="(val) => emit('update:modelValue', val)"
  >
    <div class="sandbox-drawer-content" v-loading="loading">
      <!-- 概览状态卡片 -->
      <div class="sandbox-overview-card">
        <div class="overview-item">
          <span class="label">执行隔离层级</span>
          <span class="value"><el-icon><Lock /></el-icon> Linux Namespace / cgroups</span>
        </div>
        <div class="overview-item">
          <span class="label">网络沙盒状态</span>
          <span class="value text-success">普通离线 / 搜索放行 443</span>
        </div>
        <div class="overview-item">
          <span class="label">防死循环安全锁</span>
          <span class="value">代码级 3 层锁 (仅用户@)</span>
        </div>
        <div class="overview-item">
          <span class="label">单体记忆配额</span>
          <span class="value">MEMORY.md &le; 200 行</span>
        </div>
      </div>

      <div class="section-title">
        <span>TOOL EXECUTION TRAJECTORIES (BOTS 真实调用留痕)</span>
        <el-button link size="small" type="primary" @click="fetchTrajectories">刷新</el-button>
      </div>

      <!-- 轨迹时间线 -->
      <div v-if="trajectories.length === 0" class="empty-state">
        暂无沙盒工具执行轨迹
      </div>

      <div v-else class="trajectories-list">
        <div
          v-for="item in trajectories"
          :key="item.trajectory_id"
          class="trajectory-card"
        >
          <div class="trajectory-card-header">
            <div class="header-left">
              <span class="bot-badge">@{{ item.bot_id }}</span>
              <span class="arrow">&rarr;</span>
              <span class="tool-name">{{ item.tool_id }}</span>
            </div>
            <div class="header-right">
              <el-tag
                size="small"
                :type="item.network_policy === 'search_allowlist' ? 'success' : 'info'"
                effect="plain"
              >
                <el-icon v-if="item.network_policy === 'search_allowlist'"><Search /></el-icon>
                <el-icon v-else><Lock /></el-icon>
                {{ item.network_policy === 'search_allowlist' ? '放行443搜索' : '完全离线' }}
              </el-tag>
              <span class="duration"><el-icon><Timer /></el-icon> {{ item.duration_ms }}ms</span>
            </div>
          </div>

          <!-- 入参 Payload -->
          <div class="payload-box">
            <div class="box-title">输入参数 (Input Payload)</div>
            <pre class="code-block">{{ formatPayload(item.input_payload) }}</pre>
          </div>

          <!-- 蒸馏产物 -->
          <div class="distilled-box">
            <div class="box-title">蒸馏提炼产物 (专供 Bot 决策)</div>
            <div class="distilled-text">{{ item.distilled_output }}</div>
          </div>

          <!-- 底部元数据 -->
          <div class="card-footer">
            <span class="trace-id">Trace: {{ item.traceparent.slice(0, 18) }}...</span>
            <span class="status-tag" :class="item.exit_code === 0 ? 'success' : 'error'">
              退出码: {{ item.exit_code }}
            </span>
          </div>
        </div>
      </div>
    </div>
  </el-drawer>
</template>

<style scoped>
.sandbox-drawer-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 0 4px;
}

.sandbox-overview-card {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
  background-color: var(--el-fill-color-light, #21252d);
  border: 1px solid var(--el-border-color, #2d323f);
  border-radius: 8px;
  padding: 12px 16px;
}

.overview-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.overview-item .label {
  font-size: 11px;
  color: var(--el-text-color-secondary, #909399);
}

.overview-item .value {
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary, #f0f2f5);
  display: flex;
  align-items: center;
  gap: 4px;
}

.text-success {
  color: #67c23a !important;
}

.section-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
  font-family: 'JetBrains Mono', monospace;
  color: var(--el-text-color-secondary, #909399);
  letter-spacing: 0.5px;
}

.trajectories-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.trajectory-card {
  background-color: var(--el-fill-color-blank, #1e222b);
  border: 1px solid var(--el-border-color-lighter, #2d323f);
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  transition: all 0.2s ease;
}

.trajectory-card:hover {
  border-color: var(--el-color-primary, #409eff);
}

.trajectory-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 6px;
  font-family: 'JetBrains Mono', monospace;
}

.bot-badge {
  font-weight: 600;
  color: #e6a23c;
  font-size: 13px;
}

.arrow {
  color: #909399;
}

.tool-name {
  font-weight: 600;
  color: #409eff;
  font-size: 13px;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.duration {
  font-size: 11px;
  color: #909399;
  display: flex;
  align-items: center;
  gap: 3px;
  font-family: 'JetBrains Mono', monospace;
}

.payload-box, .distilled-box {
  background-color: var(--el-fill-color-dark, #15181f);
  border: 1px solid var(--el-border-color-extra-light, #282c37);
  border-radius: 6px;
  padding: 8px 10px;
}

.box-title {
  font-size: 10px;
  color: #909399;
  font-family: 'JetBrains Mono', monospace;
  margin-bottom: 4px;
}

.code-block {
  margin: 0;
  font-family: 'JetBrains Mono', monospace;
  font-size: 11px;
  color: #cfd3dc;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 120px;
  overflow-y: auto;
}

.distilled-text {
  font-size: 12px;
  color: #67c23a;
  line-height: 1.4;
}

.card-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 10px;
  color: #909399;
  font-family: 'JetBrains Mono', monospace;
  border-top: 1px dashed var(--el-border-color-extra-light, #282c37);
  padding-top: 6px;
  margin-top: 2px;
}

.status-tag.success {
  color: #67c23a;
}
.status-tag.error {
  color: #f56c6c;
}

.empty-state {
  text-align: center;
  padding: 40px 0;
  color: #909399;
  font-size: 13px;
}
</style>
