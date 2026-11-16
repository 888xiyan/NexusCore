import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui'
import GroupChatWorkspace from './views/GroupChatWorkspace.vue'

const plugin: NexusPluginUI = {
  id: 'plugin_groupchat',
  name: '多 Bot 群聊协同',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    // 1. 注册工作台路由
    ctx.registerRoute({
      path: '/groupchat',
      name: 'GroupChatWorkspace',
      component: GroupChatWorkspace,
      meta: {
        title: '群聊协同工作台',
      },
    })

    // 2. 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_groupchat',
      title: '群聊协同',
      icon: 'ChatDotRound',
      path: '/groupchat',
      order: 1,
    })
  },
}

export default plugin
