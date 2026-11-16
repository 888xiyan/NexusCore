import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui'
import ToolCenterView from './views/ToolCenterView.vue'

const plugin: NexusPluginUI = {
  id: 'plugin_tool_center',
  name: '工具与技能中心',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    // 1. 注册工作台路由
    ctx.registerRoute({
      path: '/tools',
      name: 'ToolCenter',
      component: ToolCenterView,
      meta: {
        title: '工具与技能中心',
      },
    })

    // 2. 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_tool_center',
      title: '工具与技能',
      icon: 'Tools',
      path: '/tools',
      order: 4,
    })
  },
}

export default plugin
