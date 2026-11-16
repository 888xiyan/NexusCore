import type { Component } from 'vue'

export interface NexusRouteRecord {
  path: string
  name: string
  component: Component | (() => Promise<Component | { default: Component }>)
  meta?: {
    title?: string
    requiresAuth?: boolean
    [key: string]: any
  }
}

export interface NexusMenuItem {
  id: string
  title: string
  icon: string // Element Plus icon 名称，如 ChatDotRound, Cpu, UserFilled
  path: string
  order?: number
  badge?: () => number | string | undefined
}

export interface NexusUIContext {
  // 1. 动态路由注入
  registerRoute(route: NexusRouteRecord): void

  // 2. 侧边栏菜单注册
  registerMenu(menu: NexusMenuItem): void

  // 3. 全局插槽组件注册
  registerSlotComponent(
    slotName: 'header_status' | 'header_actions' | 'sidebar_footer' | 'workbench_widget',
    component: Component
  ): void

  // 4. 全局总线与事件广播
  bus: {
    on(event: string, handler: (payload: any) => void): void
    off(event: string, handler: (payload: any) => void): void
    emit(event: string, payload: any): void
  }

  // 5. 统一消息轻提示
  notify: {
    success(msg: string): void
    warning(msg: string): void
    error(msg: string): void
    info(msg: string): void
  }
}

export interface NexusPluginUI {
  id: string
  name: string
  version: string
  install(ctx: NexusUIContext): void | Promise<void>
  uninstall?(ctx: NexusUIContext): void
}

export interface NexusPluginManifestUI {
  menu?: {
    title: string
    icon: string
    path: string
    order?: number
  }
  routes?: Array<{
    path: string
    name: string
    entry?: string
  }>
}
