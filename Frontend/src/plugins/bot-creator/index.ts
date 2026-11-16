import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui'
import BotCreatorView from './views/BotCreatorView.vue'

const plugin: NexusPluginUI = {
  id: 'plugin_bot_creator',
  name: 'Bot 蓝图工厂',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    // 1. 注册工作台路由
    ctx.registerRoute({
      path: '/bot-factory',
      name: 'BotCreator',
      component: BotCreatorView,
      meta: {
        title: 'Bot 蓝图工厂',
      },
    })

    // 2. 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_bot_creator',
      title: 'Bot 蓝图工厂',
      icon: 'UserFilled',
      path: '/bot-factory',
      order: 3,
    })
  },
}

export default plugin
