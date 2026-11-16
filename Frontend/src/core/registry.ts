import { ref, reactive, shallowRef, markRaw, type Component } from 'vue'
import type { Router } from 'vue-router'
import { ElMessage } from 'element-plus'
import type {
  NexusUIContext,
  NexusPluginUI,
  NexusMenuItem,
  NexusRouteRecord,
} from '../types/nexus-ui'

type EventHandler = (payload: any) => void

class PluginRegistry {
  private router: Router | null = null
  private eventListeners = new Map<string, Set<EventHandler>>()

  // 响应式已注册插件
  public plugins = ref<NexusPluginUI[]>([])

  // 响应式菜单清单（自动按 order 升序）
  public menus = ref<NexusMenuItem[]>([])

  // 扩展插槽组件映射
  public slots = reactive<Record<string, Component[]>>({
    header_status: [],
    header_actions: [],
    sidebar_footer: [],
    workbench_widget: [],
  })

  // 内置/同构微模块注册表（提供开箱即用与静态解耦映射）
  private builtInLoaders: Record<string, () => Promise<{ default: NexusPluginUI }>> = {}

  constructor() {
    this.bus = {
      on: this.on.bind(this),
      off: this.off.bind(this),
      emit: this.emit.bind(this),
    }

    this.notify = {
      success: (msg: string) => ElMessage.success(msg),
      warning: (msg: string) => ElMessage.warning(msg),
      error: (msg: string) => ElMessage.error(msg),
      info: (msg: string) => ElMessage.info(msg),
    }
  }

  public bus: NexusUIContext['bus']
  public notify: NexusUIContext['notify']

  public setRouter(router: Router) {
    this.router = router
  }

  public registerBuiltIn(id: string, loader: () => Promise<{ default: NexusPluginUI }>) {
    this.builtInLoaders[id] = loader
  }

  private on(event: string, handler: EventHandler) {
    if (!this.eventListeners.has(event)) {
      this.eventListeners.set(event, new Set())
    }
    this.eventListeners.get(event)!.add(handler)
  }

  private off(event: string, handler: EventHandler) {
    const handlers = this.eventListeners.get(event)
    if (handlers) {
      handlers.delete(handler)
    }
  }

  public emit(event: string, payload: any) {
    const handlers = this.eventListeners.get(event)
    if (handlers) {
      for (const handler of handlers) {
        try {
          handler(payload)
        } catch (err) {
          console.error(`[PluginRegistry] 事件处理异常 ${event}:`, err)
        }
      }
    }
  }

  public createContext(): NexusUIContext {
    return {
      registerRoute: (route: NexusRouteRecord) => {
        if (!this.router) {
          console.warn('[PluginRegistry] Router 尚未就绪，暂存路由:', route.name)
          return
        }
        // 动态注入到 Vue Router 顶级或工作台视区
        this.router.addRoute({
          path: route.path,
          name: route.name,
          component: route.component,
          meta: route.meta,
        })
      },

      registerMenu: (menu: NexusMenuItem) => {
        const existingIdx = this.menus.value.findIndex((m) => m.id === menu.id)
        if (existingIdx >= 0) {
          this.menus.value[existingIdx] = menu
        } else {
          this.menus.value.push(menu)
        }
        // 保持 order 升序排序
        this.menus.value.sort((a, b) => (a.order || 99) - (b.order || 99))
      },

      registerSlotComponent: (slotName, component: Component) => {
        if (!this.slots[slotName]) {
          this.slots[slotName] = []
        }
        this.slots[slotName].push(markRaw(component))
      },

      bus: this.bus,
      notify: this.notify,
    }
  }

  public async registerPlugin(plugin: NexusPluginUI): Promise<void> {
    const exists = this.plugins.value.some((p) => p.id === plugin.id)
    if (exists) {
      console.warn(`[PluginRegistry] 插件 ${plugin.id} 已注册，忽略重复载入`)
      return
    }

    const ctx = this.createContext()
    await plugin.install(ctx)
    this.plugins.value.push(plugin)
    console.log(`[PluginRegistry] 插件 ${plugin.name} (${plugin.id}@${plugin.version}) 已挂载`)
  }

  /**
   * 从微内核 Hub 接口 GET /v1/plugins 动态获取激活插件清单，并动态挂载
   */
  public async loadFromHub(apiUrl = 'http://127.0.0.1:7200'): Promise<void> {
    try {
      const res = await fetch(`${apiUrl}/v1/plugins`)
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`)
      }
      const pluginItems = await res.json()
      if (!Array.isArray(pluginItems)) {
        return
      }

      for (const item of pluginItems) {
        if (item.status === 'disabled' || item.status === 'uninstalled') {
          continue
        }

        const pluginId = item.plugin_id
        // 1. 如果有内置/同构微模块加载器，优先使用
        if (this.builtInLoaders[pluginId]) {
          const mod = await this.builtInLoaders[pluginId]()
          await this.registerPlugin(mod.default)
          continue
        }

        // 2. 方案 A：原生 ESM 外部动态导入 (支持动态 entry)
        if (item.ui && item.ui.routes && item.ui.routes.length > 0) {
          const entryUrl = item.ui.entry || item.ui.routes[0]?.entry
          if (entryUrl) {
            try {
              const extMod = await import(/* @vite-ignore */ entryUrl)
              if (extMod && extMod.default) {
                await this.registerPlugin(extMod.default)
              }
            } catch (extErr) {
              console.warn(`[PluginRegistry] 动态加载外部插件 ${pluginId} 失败:`, extErr)
            }
          }
        }
      }
    } catch (e) {
      console.warn('[PluginRegistry] 连接 Hub 失败或 Hub 离线，采用本地默认预装载:', e)
      // 离线/开发兜底：装载全部可用同构微模块
      for (const [id, loader] of Object.entries(this.builtInLoaders)) {
        try {
          const mod = await loader()
          await this.registerPlugin(mod.default)
        } catch (loaderErr) {
          console.error(`[PluginRegistry] 兜底装载 ${id} 失败:`, loaderErr)
        }
      }
    }
  }
}

export const pluginRegistry = new PluginRegistry()
