<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useBotsStore } from '../stores/bots'
import { ElMessage } from 'element-plus'
import { Cpu, Refresh, WarningFilled, Document, User } from '@element-plus/icons-vue'

const botsStore = useBotsStore()

const editableMemory = ref<string>('')

// Initialize editable memory when selected memory changes
watch(
  () => botsStore.selectedBotMemory?.content,
  (newContent) => {
    editableMemory.value = newContent || ''
  },
  { immediate: true }
)

// Calculate line count accurately
const lineCount = computed(() => {
  if (!editableMemory.value) return 0
  return editableMemory.value.split('\n').length
})

// Enforce Claude Code <= 200 lines constraint
const isMemoryOverLimit = computed(() => lineCount.value > 200)

const handleSaveAndRestart = async () => {
  if (!botsStore.selectedBotId) return

  if (isMemoryOverLimit.value) {
    ElMessage.error('单体记忆 MEMORY.md 超过 200 行硬约束限制，无法保存！')
    return
  }

  try {
    await botsStore.saveAndRestart(botsStore.selectedBotId, editableMemory.value)
    ElMessage.success(`Bot ${botsStore.selectedBotId} 单体记忆已更新并完成进程重启生效！`)
  } catch (err: any) {
    ElMessage.error(err?.message || '保存或重启 Bot 失败')
  }
}
</script>

<template>
  <el-drawer
    v-model="botsStore.drawerVisible"
    :title="`Bot 画像与单体记忆管理 - ${botsStore.selectedBotDetail?.display_name || botsStore.selectedBotId}`"
    size="550px"
    direction="rtl"
    :before-close="() => botsStore.closeDrawer()"
  >
    <div v-loading="botsStore.drawerLoading" class="drawer-container">
      <div v-if="botsStore.selectedBotDetail" class="drawer-content">
        <!-- Basic Info Section -->
        <div class="section-card">
          <div class="section-header">
            <el-icon><User /></el-icon>
            <span>基本画像与运行时状态</span>
          </div>
          <div class="info-grid">
            <div class="info-row">
              <span class="info-label">Bot ID:</span>
              <el-tag size="small" type="primary" effect="plain">{{ botsStore.selectedBotDetail.bot_id }}</el-tag>
            </div>
            <div class="info-row">
              <span class="info-label">显示名称:</span>
              <span class="info-val">{{ botsStore.selectedBotDetail.display_name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">角色定位:</span>
              <el-tag size="small" type="info">{{ botsStore.selectedBotDetail.role }}</el-tag>
            </div>
            <div class="info-row">
              <span class="info-label">运行状态:</span>
              <el-tag
                size="small"
                :type="botsStore.selectedBotDetail.observed_state === 'ready' ? 'success' : 'warning'"
              >
                {{ botsStore.selectedBotDetail.observed_state }}
              </el-tag>
            </div>
            <div class="info-row">
              <span class="info-label">激活模式:</span>
              <span class="info-val">{{ botsStore.selectedBotDetail.activation_mode }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">模型:</span>
              <span class="info-val">{{ botsStore.selectedBotDetail.model || '标准' }}</span>
            </div>
          </div>

          <!-- Capabilities -->
          <div class="capabilities-box" v-if="botsStore.selectedBotDetail.capabilities?.length">
            <span class="info-label">声明能力:</span>
            <div class="caps-tags">
              <el-tag
                v-for="cap in botsStore.selectedBotDetail.capabilities"
                :key="cap"
                size="small"
                effect="light"
              >
                {{ cap }}
              </el-tag>
            </div>
          </div>
        </div>

        <!-- System Prompt Reference -->
        <div class="section-card">
          <div class="section-header">
            <el-icon><Cpu /></el-icon>
            <span>系统提示词 (System Prompt Ref)</span>
          </div>
          <div class="prompt-box">
            <code>{{ botsStore.selectedBotDetail.prompt_ref || '内置原生提示词 (data/prompts)' }}</code>
          </div>
        </div>

        <!-- Single-Agent Memory (MEMORY.md) Section -->
        <div class="section-card memory-card">
          <div class="section-header memory-header">
            <div class="header-left">
              <el-icon><Document /></el-icon>
              <span>单体记忆 (MEMORY.md)</span>
            </div>
            <div class="header-right">
              <el-tag
                :type="isMemoryOverLimit ? 'danger' : 'success'"
                size="small"
                effect="dark"
                class="line-counter-tag"
              >
                {{ lineCount }} / 200 行
              </el-tag>
            </div>
          </div>

          <div class="constraint-alert" v-if="isMemoryOverLimit">
            <el-alert
              title="行数硬约束告警"
              type="error"
              description="Claude Code 原生单体记忆严格限制 MEMORY.md ≤ 200 行！当前内容超过限制，无法保存。"
              show-icon
              :closable="false"
            />
          </div>
          <div class="constraint-note" v-else>
            Claude Code 四层记忆栈约束：严格限制 MEMORY.md ≤ 200 行，削峰填谷、零二次方膨胀。
          </div>

          <!-- Memory Textarea Editor -->
          <div class="memory-editor-wrapper">
            <textarea
              v-model="editableMemory"
              class="memory-textarea"
              placeholder="# 写入 Bot 个人单体记忆（<= 200 行）..."
              rows="12"
            ></textarea>
          </div>
        </div>
      </div>
    </div>

    <!-- Drawer Footer Actions -->
    <template #footer>
      <div class="drawer-footer">
        <el-button @click="botsStore.closeDrawer()">取消</el-button>
        <el-button
          type="primary"
          :icon="Refresh"
          :loading="botsStore.savingOrRestarting"
          :disabled="isMemoryOverLimit"
          @click="handleSaveAndRestart"
        >
          保存并重启 Bot (Save &amp; Restart)
        </el-button>
      </div>
    </template>
  </el-drawer>
</template>

<style scoped>
.drawer-container {
  padding: 0 4px;
}

.section-card {
  background: #ffffff;
  border: 1px solid #e4e7ed;
  border-radius: 8px;
  padding: 14px 16px;
  margin-bottom: 16px;
}

.section-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 14px;
  color: #303133;
  margin-bottom: 12px;
  border-bottom: 1px solid #f2f6fc;
  padding-bottom: 8px;
}

.info-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px 16px;
  font-size: 13px;
}

.info-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.info-label {
  color: #909399;
  font-size: 13px;
}

.info-val {
  color: #303133;
  font-weight: 500;
}

.capabilities-box {
  margin-top: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.caps-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.prompt-box {
  background: #f8f9fa;
  padding: 10px 12px;
  border-radius: 6px;
  font-family: monospace;
  font-size: 12px;
  color: #606266;
  border: 1px dashed #dcdfe6;
}

.memory-header {
  justify-content: space-between;
}

.line-counter-tag {
  font-family: monospace;
  font-weight: bold;
}

.constraint-alert {
  margin-bottom: 12px;
}

.constraint-note {
  font-size: 12px;
  color: #909399;
  margin-bottom: 10px;
}

.memory-editor-wrapper {
  border: 1px solid #dcdfe6;
  border-radius: 6px;
  overflow: hidden;
}

.memory-textarea {
  width: 100%;
  border: none;
  outline: none;
  font-family: 'Fira Code', Consolas, Monaco, monospace;
  font-size: 13px;
  line-height: 1.5;
  padding: 12px;
  box-sizing: border-box;
  resize: vertical;
  color: #2c3e50;
  background: #fafafa;
}

.memory-textarea:focus {
  background: #ffffff;
}

.drawer-footer {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}
</style>
