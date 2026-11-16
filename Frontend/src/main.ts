import * as Vue from 'vue'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import * as VueRouter from 'vue-router'
import ElementPlus from 'element-plus'
import * as ElementPlusIcons from '@element-plus/icons-vue'
import 'element-plus/dist/index.css'
import 'highlight.js/styles/github-dark.css'

import App from './App.vue'
import { router } from './router'
import { pluginRegistry } from './core/registry'

// 1. 暴露共享运行时底座至全局（零冗余运行时共享规范）
;(window as any).__NEXUS_CORE__ = {
  Vue,
  VueRouter,
  Pinia: { createPinia },
  ElementPlus,
  ElementPlusIcons,
  registry: pluginRegistry,
}

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(ElementPlus)

// 2. 注入 Router 到注册中心
pluginRegistry.setRouter(router)

// 3. 注册内置微模块加载器（支持开箱即用与同构解耦）
pluginRegistry.registerBuiltIn('plugin_groupchat', () => import('./plugins/groupchat'))
pluginRegistry.registerBuiltIn('plugin_agent_discovery', () => import('./plugins/agent-discovery'))
pluginRegistry.registerBuiltIn('plugin_bot_creator', () => import('./plugins/bot-creator'))
pluginRegistry.registerBuiltIn('plugin_tool_center', () => import('./plugins/tool-center'))
pluginRegistry.registerBuiltIn('plugin_marketplace', () => import('./plugins/marketplace'))

// 4. 异步初始化微插件中心并挂载应用
async function bootstrap() {
  await pluginRegistry.loadFromHub()
  app.use(router)
  await router.isReady()
  app.mount('#app')
}

bootstrap()
