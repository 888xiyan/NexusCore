import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui'
import AgentDiscoveryView from './views/AgentDiscoveryView.vue'

const plugin: NexusPluginUI = {
  id: 'plugin_agent_discovery',
  name: '本地 Agent 探索与探针',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    // 1. 注册工作台路由
    ctx.registerRoute({
      path: '/agents',
      name: 'AgentDiscovery',
      component: AgentDiscoveryView,
      meta: {
        title: 'Agent 资产与探针监控',
      },
    })

    // 2. 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_agent_discovery',
      title: 'Agent 资产与探针',
      icon: 'Cpu',
      path: '/agents',
      order: 2,
    })
  },
}

export default plugin
