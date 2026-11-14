<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import type { AgentEvent, AgentLifecycleStatus } from '../api/events'
import { Monitor, CircleCheck, Loading, Warning } from '@element-plus/icons-vue'

const props = defineProps<{
  activeStreamId: number | null
  activeJobId: string | null
  activeBotId: string | null
  activeAgentId: string | null
  activeProtocolId: string | null
  status: AgentLifecycleStatus
  elapsedMs: number
  frames: AgentEvent[]
  terminalLogs: string[]
}>()

const terminalRef = ref<HTMLDivElement | null>(null)

// Auto-scroll terminal on new logs
watch(
  () => props.terminalLogs.length,
  () => {
    nextTick(() => {
      if (terminalRef.value) {
        terminalRef.value.scrollTop = terminalRef.value.scrollHeight
      }
    })
  }
)

const statusTagType = computed(() => {
  switch (props.status) {
    case 'running':
      return 'primary'
    case 'completed':
      return 'success'
    case 'error':
      return 'danger'
    default:
      return 'info'
  }
})

const statusLabel = computed(() => {
  switch (props.status) {
    case 'running':
      return '执行中 (Running)'
    case 'completed':
      return '已完成 (Completed)'
    case 'error':
      return '异常 (Error)'
    default:
      return '待命 (Idle)'
  }
})

const frameSteps = ['open', 'data_in', 'data_out', 'close']

const hasFrame = (frameType: string) => {
  return props.frames.some((f) => f.frame_type === frameType)
}
</script>

<template>
  <el-card class="agent-activity-card" shadow="hover">
    <template #header>
      <div class="card-header">
        <div class="header-left">
          <el-icon :size="18" class="icon-monitor"><Monitor /></el-icon>
          <span class="header-title">跨设备 Agent 探针执行追踪</span>
        </div>
        <div class="header-right">
          <el-tag :type="statusTagType" effect="dark" size="small">
            <template #icon>
              <el-icon v-if="status === 'running'" class="is-loading"><Loading /></el-icon>
              <el-icon v-else-if="status === 'completed'"><CircleCheck /></el-icon>
              <el-icon v-else-if="status === 'error'"><Warning /></el-icon>
            </template>
            {{ statusLabel }}
          </el-tag>
          <span class="elapsed-badge" v-if="elapsedMs > 0">{{ elapsedMs }} ms</span>
        </div>
      </div>
    </template>

    <!-- Metadata Grid -->
    <div class="meta-grid">
      <div class="meta-item">
        <span class="meta-label">调度 Bot:</span>
        <span class="meta-value">{{ activeBotId || '暂无' }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">目标探针:</span>
        <span class="meta-value">{{ activeAgentId || 'loopback_agent' }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">协议:</span>
        <span class="meta-value">{{ activeProtocolId || 'nexus.echo.v1' }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">Stream ID:</span>
        <span class="meta-value">{{ activeStreamId !== null ? activeStreamId : '-' }}</span>
      </div>
    </div>

    <!-- Transport Frame Sequence Steps -->
    <div class="frame-steps-section">
      <div class="section-title">传输协议帧状态 (Transport Frames)</div>
      <div class="frame-chips">
        <div
          v-for="ft in frameSteps"
          :key="ft"
          :class="['frame-chip', { active: hasFrame(ft) }]"
        >
          <span class="chip-dot"></span>
          <span class="chip-name">{{ ft }}</span>
        </div>
      </div>
    </div>

    <!-- Terminal Stdout/Stderr Output Echo Window -->
    <div class="terminal-section">
      <div class="terminal-header">
        <span class="term-dot red"></span>
        <span class="term-dot yellow"></span>
        <span class="term-dot green"></span>
        <span class="term-title">受控探针输出流 (Terminal Echo)</span>
      </div>
      <div ref="terminalRef" class="terminal-body">
        <div v-if="terminalLogs.length === 0" class="term-empty">
          [等待 Agent 任务调度与终端流输出...]
        </div>
        <div v-for="(line, idx) in terminalLogs" :key="idx" class="term-line">
          <span class="term-prefix">&gt;</span>
          <span class="term-text">{{ line }}</span>
        </div>
      </div>
    </div>
  </el-card>
</template>

<style scoped>
.agent-activity-card {
  border-radius: 8px;
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.icon-monitor {
  color: #409eff;
}

.header-title {
  font-weight: 600;
  font-size: 15px;
  color: #303133;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.elapsed-badge {
  font-family: monospace;
  font-size: 12px;
  background: #f4f4f5;
  color: #606266;
  padding: 2px 6px;
  border-radius: 4px;
}

.meta-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px 16px;
  font-size: 13px;
  margin-bottom: 16px;
  background: #f8f9fa;
  padding: 10px 14px;
  border-radius: 6px;
}

.meta-item {
  display: flex;
  gap: 6px;
}

.meta-label {
  color: #909399;
}

.meta-value {
  color: #303133;
  font-weight: 500;
  font-family: monospace;
}

.frame-steps-section {
  margin-bottom: 16px;
}

.section-title {
  font-size: 12px;
  color: #909399;
  margin-bottom: 8px;
  font-weight: 500;
}

.frame-chips {
  display: flex;
  gap: 8px;
}

.frame-chip {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 12px;
  background: #f4f4f5;
  color: #909399;
  font-size: 12px;
  font-family: monospace;
  transition: all 0.2s;
}

.frame-chip.active {
  background: #e1f3d8;
  color: #529b2e;
  font-weight: bold;
}

.chip-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #c0c4cc;
}

.frame-chip.active .chip-dot {
  background: #67c23a;
}

.terminal-section {
  border-radius: 6px;
  overflow: hidden;
  background: #1e1e1e;
  border: 1px solid #333;
}

.terminal-header {
  display: flex;
  align-items: center;
  padding: 6px 12px;
  background: #2d2d2d;
  gap: 6px;
}

.term-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
}

.term-dot.red { background: #ff5f56; }
.term-dot.yellow { background: #ffbd2e; }
.term-dot.green { background: #27c93f; }

.term-title {
  font-size: 11px;
  color: #888;
  margin-left: 8px;
}

.terminal-body {
  height: 200px;
  overflow-y: auto;
  padding: 10px 14px;
  font-family: 'Fira Code', Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.5;
  color: #4af626;
}

.term-empty {
  color: #666;
  font-style: italic;
}

.term-line {
  display: flex;
  gap: 8px;
  word-break: break-all;
}

.term-prefix {
  color: #00bcd4;
  user-select: none;
}

.term-text {
  color: #e6e6e6;
}
</style>
