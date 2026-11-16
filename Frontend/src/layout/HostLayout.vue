<script setup lang="ts">
import { ref, computed, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { pluginRegistry } from '../core/registry'
import { useBotsStore } from '../stores/bots'
import SandboxTrajectoryDrawer from '../components/SandboxTrajectoryDrawer.vue'
import {
  Connection,
  Monitor,
  ChatDotRound,
  Cpu,
  UserFilled,
  Grid,
  Sunny,
  Moon,
  Folder,
  Tickets,
  DataAnalysis,
  Clock,
  Tools,
  Plus,
  Search,
} from '@element-plus/icons-vue'

const route = useRoute()
const router = useRouter()
const botsStore = useBotsStore()

// 控制内置沙盒轨迹抽屉
const isSandboxDrawerVisible = ref(false)

// 居中大输入框的当前 @ 胶囊与文本
const activePill = ref('@xxx')
const showPill = ref(true)
const promptInputText = ref('')
const isMentionPopoverVisible = ref(false)

const openSandboxDrawer = () => {
  isSandboxDrawerVisible.value = true
}

const removePill = () => {
  showPill.value = false
}

const selectMention = (name: string) => {
  activePill.value = name
  showPill.value = true
  isMentionPopoverVisible.value = false
}

const triggerCommand = (target: string, prompt: string) => {
  activePill.value = target
  showPill.value = true
  promptInputText.value = prompt
}

// 主题切换机制 (白昼 / 午夜 / 跟随系统)
const currentTheme = ref<'light' | 'dark' | 'auto'>('dark')
const applyTheme = (theme: 'light' | 'dark' | 'auto') => {
  currentTheme.value = theme
  if (theme === 'dark') {
    document.documentElement.classList.add('dark')
    document.documentElement.setAttribute('data-theme', 'dark')
  } else if (theme === 'light') {
    document.documentElement.classList.remove('dark')
    document.documentElement.setAttribute('data-theme', 'light')
  } else {
    const isDark = window.matchMedia('(prefers-color-scheme: dark)').matches
    if (isDark) {
      document.documentElement.classList.add('dark')
      document.documentElement.setAttribute('data-theme', 'dark')
    } else {
      document.documentElement.classList.remove('dark')
      document.documentElement.setAttribute('data-theme', 'light')
    }
  }
}

// 图标字典映射
const iconMap: Record<string, Component> = {
  ChatDotRound,
  Cpu,
  UserFilled,
  Grid,
  Monitor,
  Connection,
  Tickets,
  DataAnalysis,
  Clock,
  Tools,
}

const resolveIcon = (iconName: string) => {
  return iconMap[iconName] || Grid
}

const activePath = computed(() => route.path)

const handleSelectMenu = (path: string) => {
  router.push(path)
}
</script>

<template>
  <div class="host-shell-layout" :class="[currentTheme]">
    <!-- 顶部导航栏 -->
    <header class="host-header">
      <div class="brand-zone">
        <div class="brand-sparkle">✦</div>
        <div class="brand-info">
          <span class="brand-title">Nexus Atelier Code</span>
          <span class="brand-subtitle">工坊控制台 · 微内核操作系统</span>
        </div>
      </div>

      <div class="header-status-zone">
        <!-- 快速呼出内置沙盒轨迹抽屉 -->
        <button class="sandbox-trigger-btn" @click="openSandboxDrawer" title="查看 Bots 使用工具轨迹">
          <span class="pulse-dot"></span>
          <span>⚡ 沙盒工具轨迹</span>
        </button>

        <div class="status-indicator">
          <el-icon><Connection /></el-icon>
          <span class="status-label">微模块:</span>
          <el-tag size="small" type="success" effect="dark">
            {{ pluginRegistry.plugins.value.length }} 自治插件
          </el-tag>
        </div>

        <div class="status-indicator">
          <el-icon><Monitor /></el-icon>
          <span class="status-label">Bots:</span>
          <el-tag size="small" type="warning" effect="dark">
            {{ botsStore.bots.length }} 位
          </el-tag>
        </div>

        <!-- 主题切换器 -->
        <div class="theme-switch-pills">
          <button
            class="theme-pill-btn"
            :class="{ active: currentTheme === 'light' }"
            @click="applyTheme('light')"
            title="白昼羊皮纸"
          >
            ☀️
          </button>
          <button
            class="theme-pill-btn"
            :class="{ active: currentTheme === 'dark' }"
            @click="applyTheme('dark')"
            title="午夜星空"
          >
            🌙
          </button>
          <button
            class="theme-pill-btn"
            :class="{ active: currentTheme === 'auto' }"
            @click="applyTheme('auto')"
            title="跟随系统"
          >
            💻
          </button>
        </div>
      </div>
    </header>

    <!-- 工作台主体：左侧手绘两段式导航 + 右侧主工作区 -->
    <div class="host-body">
      <!-- 左侧边栏 (严格按原型上下分区) -->
      <aside class="host-sidebar">
        <!-- 上半区：纪事 (Chronicles) -->
        <div class="nav-section">
          <div class="section-label">CHRONICLES · 纪事</div>
          <div class="custom-nav-item" @click="openSandboxDrawer">
            <div class="nav-item-left">
              <el-icon><Tickets /></el-icon>
              <span>日志</span>
            </div>
            <span class="nav-badge">沙盒轨迹</span>
          </div>
          <div class="custom-nav-item" @click="handleSelectMenu('/groupchat')">
            <div class="nav-item-left">
              <el-icon><DataAnalysis /></el-icon>
              <span>数据</span>
            </div>
            <span class="nav-badge">4.2 MB</span>
          </div>
          <div class="custom-nav-item" @click="handleSelectMenu('/groupchat')">
            <div class="nav-item-left">
              <el-icon><Clock /></el-icon>
              <span>定时任务</span>
            </div>
            <span class="nav-badge">3 活跃</span>
          </div>
        </div>

        <!-- 原型中的星芒水平分割线 -->
        <div class="storybook-divider">
          <span>✦</span>
        </div>

        <!-- 下半区：工坊伙伴 (Companions) 动态结合 PluginRegistry 菜单与手绘原型 -->
        <div class="nav-section">
          <div class="section-label">COMPANIONS · 工坊</div>
          
          <!-- 若 PluginRegistry 存在动态注册的业务菜单，优先渲染 -->
          <template v-if="pluginRegistry.menus.value.length > 0">
            <div
              v-for="menu in pluginRegistry.menus.value"
              :key="menu.id"
              class="custom-nav-item"
              :class="{ active: activePath === menu.path }"
              @click="handleSelectMenu(menu.path)"
            >
              <div class="nav-item-left">
                <el-icon><component :is="resolveIcon(menu.icon)" /></el-icon>
                <span>{{ menu.title }}</span>
              </div>
              <span v-if="menu.badge && menu.badge()" class="nav-badge">{{ menu.badge() }}</span>
            </div>
          </template>

          <!-- 原型默认伙伴菜单项 (兜底与快速入口) -->
          <template v-else>
            <div
              class="custom-nav-item"
              :class="{ active: activePath === '/agents' }"
              @click="handleSelectMenu('/agents')"
            >
              <div class="nav-item-left">
                <el-icon><Cpu /></el-icon>
                <span>agents</span>
              </div>
              <span class="nav-badge">探针</span>
            </div>

            <div
              class="custom-nav-item"
              :class="{ active: activePath === '/bot-factory' }"
              @click="handleSelectMenu('/bot-factory')"
            >
              <div class="nav-item-left">
                <el-icon><UserFilled /></el-icon>
                <span>bots</span>
              </div>
              <span class="nav-badge">{{ botsStore.bots.length }}</span>
            </div>

            <div
              class="custom-nav-item"
              :class="{ active: activePath === '/groupchat' }"
              @click="handleSelectMenu('/groupchat')"
            >
              <div class="nav-item-left">
                <el-icon><Tools /></el-icon>
                <span>工具</span>
              </div>
              <span class="nav-badge">16</span>
            </div>
          </template>

          <!-- + 插件按钮 -->
          <button class="add-plugin-btn" @click="handleSelectMenu('/bot-factory')">
            <el-icon><Plus /></el-icon>
            <span>+ 插件</span>
          </button>
        </div>

        <div class="sidebar-footer-ready">
          <span class="status-dot"></span>
          <span>claude-code · ready</span>
        </div>
      </aside>

      <!-- 右侧主舞台视区 -->
      <main class="host-viewport">
        <!-- 居中 @xxx| 输入框核心区域 (对应原型右侧核心红圈) -->
        <div class="global-composer-banner">
          <div class="composer-container">
            <div class="composer-greeting">
              <span class="greeting-sparkle">📖</span>
              <span class="greeting-text">今天想谱写哪一段工坊指令？输入 <strong>@</strong> 召唤伙伴</span>
            </div>

            <!-- @xxx| 输入框卡片 -->
            <div class="prompt-box">
              <div class="prompt-input-row">
                <div v-if="showPill" class="mention-pill" @click="isMentionPopoverVisible = !isMentionPopoverVisible">
                  <span>{{ activePill }}</span>
                  <span class="remove-pill" @click.stop="removePill">&times;</span>
                </div>
                <input
                  v-model="promptInputText"
                  type="text"
                  class="prompt-input-field"
                  placeholder="输入框... 试着键入 @ 召唤其它伙伴，或直接下达指令"
                  @keydown.enter="handleSelectMenu('/groupchat')"
                />
              </div>

              <!-- 快捷候选 Popover -->
              <div v-if="isMentionPopoverVisible" class="mention-dropdown">
                <div class="dropdown-header">召唤工坊伙伴</div>
                <div class="dropdown-item" @click="selectMention('@coder')">
                  <strong>@coder</strong>
                  <span class="desc">架构与代码执行 Agent</span>
                </div>
                <div class="dropdown-item" @click="selectMention('@researcher')">
                  <strong>@researcher</strong>
                  <span class="desc">文献与检索 Agent</span>
                </div>
                <div class="dropdown-item" @click="selectMention('@cron_herald')">
                  <strong>@cron_herald</strong>
                  <span class="desc">定时任务信使 Bot</span>
                </div>
              </div>

              <div class="prompt-toolbar-row">
                <div class="toolbar-chips">
                  <button class="chip-btn" @click="isMentionPopoverVisible = !isMentionPopoverVisible">
                    <span>@ 提及目标</span>
                  </button>
                  <button class="chip-btn highlight" @click="openSandboxDrawer">
                    <span>⚡ 沙盒工具轨迹</span>
                  </button>
                </div>
                <el-button type="primary" size="small" round @click="handleSelectMenu('/groupchat')">
                  执行章节 &rarr;
                </el-button>
              </div>
            </div>

            <!-- 快捷工坊指令气泡 -->
            <div class="quick-tales-row">
              <span
                class="tale-card"
                @click="triggerCommand('@coder', '检查最近一次构建日志并修复异常')"
              >
                <code>@coder</code> 检查最近一次构建日志
              </span>
              <span
                class="tale-card"
                @click="triggerCommand('@researcher', '检索并整理系统架构知识库')"
              >
                <code>@researcher</code> 检索并整理知识库
              </span>
            </div>
          </div>
        </div>

        <!-- 具体的业务页面视区（群聊 / 探针 / Bot工厂） -->
        <div class="view-content-wrapper">
          <router-view v-slot="{ Component }">
            <keep-alive>
              <component :is="Component" />
            </keep-alive>
          </router-view>
        </div>
      </main>
    </div>

    <!-- 内置沙盒工具轨迹抽屉 -->
    <SandboxTrajectoryDrawer v-model="isSandboxDrawerVisible" />
  </div>
</template>

<style scoped>
.host-shell-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  width: 100vw;
  background-color: var(--bg-parchment, #18191d);
  color: var(--text-ink, #eae6df);
  overflow: hidden;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
}

.host-header {
  height: 52px;
  background-color: var(--bg-sidebar, #1f2127);
  border-bottom: 1.5px solid var(--border-warm, #2d3139);
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0 20px;
  z-index: 10;
}

.brand-zone {
  display: flex;
  align-items: center;
  gap: 10px;
}

.brand-sparkle {
  color: #e08264;
  font-size: 18px;
}

.brand-title {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.5px;
}

.brand-subtitle {
  font-size: 11px;
  color: #909399;
  margin-left: 6px;
}

.header-status-zone {
  display: flex;
  align-items: center;
  gap: 14px;
}

.sandbox-trigger-btn {
  background: rgba(224, 130, 100, 0.15);
  border: 1px solid rgba(224, 130, 100, 0.4);
  color: #e08264;
  border-radius: 99px;
  padding: 4px 12px;
  font-size: 12px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  transition: all 0.2s ease;
}

.sandbox-trigger-btn:hover {
  background: rgba(224, 130, 100, 0.25);
  transform: translateY(-1px);
}

.pulse-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background-color: #67c23a;
  box-shadow: 0 0 6px #67c23a;
}

.status-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: #a0a5b1;
}

.theme-switch-pills {
  display: inline-flex;
  background: rgba(0, 0, 0, 0.2);
  border-radius: 99px;
  padding: 2px;
  border: 1px solid var(--border-warm, #2d3139);
}

.theme-pill-btn {
  background: transparent;
  border: none;
  font-size: 11px;
  padding: 3px 8px;
  border-radius: 99px;
  cursor: pointer;
  color: #909399;
}

.theme-pill-btn.active {
  background: rgba(255, 255, 255, 0.15);
  color: #ffffff;
}

.host-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

/* 左侧手绘上下分栏边栏 */
.host-sidebar {
  width: 230px;
  background-color: var(--bg-sidebar, #1f2127);
  border-right: 1.5px solid var(--border-warm, #2d3139);
  display: flex;
  flex-direction: column;
  padding: 16px 12px;
  user-select: none;
}

.nav-section {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.section-label {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.8px;
  color: #707580;
  padding: 4px 8px;
  margin-bottom: 2px;
}

.custom-nav-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-radius: 8px;
  color: #a0a5b1;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s ease;
}

.custom-nav-item:hover, .custom-nav-item.active {
  background-color: rgba(255, 255, 255, 0.08);
  color: #ffffff;
}

.custom-nav-item.active {
  border-left: 3px solid #e08264;
}

.nav-item-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.nav-badge {
  font-size: 10px;
  background: rgba(255, 255, 255, 0.06);
  padding: 2px 6px;
  border-radius: 99px;
  color: #8c919d;
  font-family: monospace;
}

.storybook-divider {
  display: flex;
  align-items: center;
  margin: 14px 4px;
  color: #4a505e;
}

.storybook-divider::before, .storybook-divider::after {
  content: "";
  flex: 1;
  height: 1px;
  background: linear-gradient(90deg, transparent, #3a404e, transparent);
}

.storybook-divider span {
  font-size: 11px;
  color: #e08264;
  margin: 0 6px;
}

.add-plugin-btn {
  margin-top: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 8px;
  border-radius: 8px;
  border: 1px dashed #404554;
  background: transparent;
  color: #e08264;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.2s ease;
}

.add-plugin-btn:hover {
  background: rgba(224, 130, 100, 0.1);
  border-color: #e08264;
}

.sidebar-footer-ready {
  margin-top: auto;
  font-size: 11px;
  color: #606573;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px;
  border-top: 1px solid #2d3139;
  font-family: monospace;
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #67c23a;
}

/* 右侧主舞台 */
.host-viewport {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  background-color: var(--bg-parchment, #18191d);
  overflow: hidden;
}

.global-composer-banner {
  background: linear-gradient(180deg, rgba(31, 33, 39, 0.7) 0%, rgba(24, 25, 29, 0.3) 100%);
  border-bottom: 1px solid var(--border-warm, #2d3139);
  padding: 16px 24px;
  display: flex;
  justify-content: center;
}

.composer-container {
  width: 100%;
  max-width: 680px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.composer-greeting {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #a0a5b1;
}

.prompt-box {
  background: var(--bg-sidebar, #1f2127);
  border: 1.5px solid var(--border-warm, #363b47);
  border-radius: 16px;
  padding: 10px 14px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  gap: 8px;
  position: relative;
}

.prompt-input-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mention-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: rgba(224, 130, 100, 0.2);
  color: #e08264;
  border: 1px solid rgba(224, 130, 100, 0.4);
  padding: 3px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-family: monospace;
  cursor: pointer;
}

.remove-pill {
  font-size: 14px;
  opacity: 0.7;
}

.remove-pill:hover {
  opacity: 1;
}

.prompt-input-field {
  flex: 1;
  border: none;
  outline: none;
  background: transparent;
  color: #ffffff;
  font-size: 13px;
}

.prompt-toolbar-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  border-top: 1px dashed rgba(255, 255, 255, 0.08);
  padding-top: 6px;
}

.toolbar-chips {
  display: flex;
  gap: 6px;
}

.chip-btn {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  color: #a0a5b1;
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.15s;
}

.chip-btn:hover {
  border-color: #e08264;
  color: #ffffff;
}

.chip-btn.highlight {
  color: #e08264;
  border-color: rgba(224, 130, 100, 0.3);
}

.mention-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  left: 10px;
  width: 240px;
  background: #23262f;
  border: 1px solid #363b47;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  padding: 6px;
  z-index: 50;
}

.dropdown-header {
  font-size: 10px;
  color: #707580;
  padding: 4px 6px;
  border-bottom: 1px solid #2d3139;
  font-family: monospace;
}

.dropdown-item {
  padding: 6px 8px;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
}

.dropdown-item:hover {
  background: rgba(224, 130, 100, 0.15);
}

.dropdown-item .desc {
  font-size: 10px;
  color: #909399;
}

.quick-tales-row {
  display: flex;
  justify-content: center;
  gap: 8px;
  flex-wrap: wrap;
}

.tale-card {
  font-size: 11px;
  color: #909399;
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.06);
  padding: 4px 10px;
  border-radius: 99px;
  cursor: pointer;
  transition: all 0.15s;
}

.tale-card:hover {
  border-color: #e08264;
  color: #ffffff;
}

.tale-card code {
  color: #e08264;
}

.view-content-wrapper {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
</style>
