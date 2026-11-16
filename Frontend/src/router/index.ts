import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router'
import HostLayout from '../layout/HostLayout.vue'
import { pluginRegistry } from '../core/registry'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'Host',
    component: HostLayout,
    children: [],
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: () => {
      const firstMenu = pluginRegistry.menus.value[0]
      return firstMenu ? firstMenu.path : '/'
    },
  },
]

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// 增强 registry 中的 registerRoute，使其默认作为 HostLayout 的子视区挂载
const originalCreateContext = pluginRegistry.createContext.bind(pluginRegistry)
pluginRegistry.createContext = function () {
  const ctx = originalCreateContext()
  const origRegisterRoute = ctx.registerRoute
  ctx.registerRoute = (route) => {
    // 注入为 HostLayout 的子路由，渲染在宿主工作台视区内
    router.addRoute('Host', {
      path: route.path.startsWith('/') ? route.path.slice(1) : route.path,
      name: route.name,
      component: route.component,
      meta: route.meta,
    })
  }
  return ctx
}
