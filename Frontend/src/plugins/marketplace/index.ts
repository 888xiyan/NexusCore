import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui'
import MarketplaceView from './views/MarketplaceView.vue'

const plugin: NexusPluginUI = {
  id: 'plugin_marketplace',
  name: '插件商场与审批中心',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    // 1. 注册工作台路由
    ctx.registerRoute({
      path: '/marketplace',
      name: 'Marketplace',
      component: MarketplaceView,
      meta: {
        title: '插件商场与审批中心',
      },
    })

    // 2. 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_marketplace',
      title: '插件商场',
      icon: 'Shop',
      path: '/marketplace',
      order: 5,
    })
  },
}

export default plugin
