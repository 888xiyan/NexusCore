<script setup lang="ts">
import { useAgentStore } from '../stores/agent'
import AgentActivityCard from '../components/AgentActivityCard.vue'
import { Delete, VideoPlay, Aim } from '@element-plus/icons-vue'

const agentStore = useAgentStore()
</script>

<template>
  <div class="agent-monitor-view">
    <!-- Top Bar -->
    <div class="monitor-header">
      <div class="header-title-box">
        <el-icon class="title-icon"><Aim /></el-icon>
        <span class="title-text">Agent 探针执行实时监控</span>
      </div>
      <div class="header-actions">
        <el-button
          size="small"
          :icon="Delete"
          @click="agentStore.clearLogs"
          :disabled="agentStore.frames.length === 0"
        >
          清空日志
        </el-button>
      </div>
    </div>

    <!-- Active Execution Card -->
    <div class="activity-card-container">
      <AgentActivityCard
        :active-stream-id="agentStore.activeStreamId"
        :active-job-id="agentStore.activeJobId"
        :active-bot-id="agentStore.activeBotId"
        :active-agent-id="agentStore.activeAgentId"
        :active-protocol-id="agentStore.activeProtocolId"
        :status="agentStore.status"
        :elapsed-ms="agentStore.elapsedMs"
        :frames="agentStore.frames"
        :terminal-logs="agentStore.terminalLogs"
      />
    </div>

    <!-- Frame Stream History -->
    <div class="frame-stream-section" v-if="agentStore.frames.length > 0">
      <div class="frame-stream-header">
        <el-icon><VideoPlay /></el-icon>
        <span>实时帧序列流 (Recent Frames: {{ agentStore.frames.length }})</span>
      </div>
      <div class="frame-stream-list">
        <div
          v-for="(f, idx) in agentStore.frames.slice().reverse()"
          :key="idx"
          class="stream-item"
        >
          <div class="stream-item-top">
            <el-tag size="small" :type="f.frame_type === 'close' ? 'success' : f.frame_type === 'open' ? 'primary' : 'info'">
              {{ f.frame_type }}
            </el-tag>
            <span class="stream-time">{{ f.timestamp ? new Date(f.timestamp).toLocaleTimeString() : '' }}</span>
            <span class="stream-elapsed" v-if="f.elapsed_ms > 0">{{ f.elapsed_ms }}ms</span>
          </div>
          <div class="stream-payload" v-if="f.payload">
            <code>{{ f.payload }}</code>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-monitor-view {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: #fdfdfd;
  padding: 16px;
  box-sizing: border-box;
  overflow-y: auto;
}

.monitor-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.header-title-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.title-icon {
  font-size: 18px;
  color: #409eff;
}

.title-text {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
}

.activity-card-container {
  margin-bottom: 16px;
}

.frame-stream-section {
  background: #ffffff;
  border: 1px solid #e4e7ed;
  border-radius: 8px;
  padding: 12px 14px;
}

.frame-stream-header {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: #606266;
  margin-bottom: 10px;
}

.frame-stream-list {
  max-height: 240px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.stream-item {
  background: #f8f9fa;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 12px;
}

.stream-item-top {
  display: flex;
  align-items: center;
  gap: 8px;
}

.stream-time {
  color: #909399;
  font-size: 11px;
}

.stream-elapsed {
  margin-left: auto;
  color: #67c23a;
  font-family: monospace;
}

.stream-payload {
  margin-top: 6px;
  background: #282c34;
  color: #98c379;
  padding: 4px 8px;
  border-radius: 4px;
  font-family: monospace;
  word-break: break-all;
  white-space: pre-wrap;
}
</style>
