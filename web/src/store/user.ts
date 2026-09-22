// 用户状态：JWT token 与角色持久化到 localStorage
import { defineStore } from 'pinia'

export const useUserStore = defineStore('user', {
  state: () => ({
    token: localStorage.getItem('kc-token') || '',
    username: localStorage.getItem('kc-username') || '',
    // 平台管理员标记：控制「平台管理」菜单与路由（登录响应/Me 接口返回后写入）
    role: localStorage.getItem('kc-role') || '',
    // 平台观察者标记（platform-viewer）：平台管理只读页可见。
    // 由 /rbac/my-permissions 快照刷新（MainLayout 挂载时），本地缓存兜底路由守卫
    viewer: localStorage.getItem('kc-viewer') === '1',
    dark: localStorage.getItem('kc-dark') === '1',
  }),
  getters: {
    isAdmin: (state) => state.role === 'admin',
  },
  actions: {
    setAuth(token: string, username: string, role = '') {
      this.token = token
      this.username = username
      this.role = role
      localStorage.setItem('kc-token', token)
      localStorage.setItem('kc-username', username)
      if (role) localStorage.setItem('kc-role', role)
    },
    setRole(role: string) {
      this.role = role
      if (role) localStorage.setItem('kc-role', role)
    },
    setViewer(v: boolean) {
      this.viewer = v
      if (v) localStorage.setItem('kc-viewer', '1')
      else localStorage.removeItem('kc-viewer')
    },
    toggleDark() {
      this.dark = !this.dark
      localStorage.setItem('kc-dark', this.dark ? '1' : '0')
      document.documentElement.classList.toggle('dark', this.dark)
    },
    logout() {
      this.token = ''
      this.username = ''
      this.role = ''
      this.viewer = false
      localStorage.removeItem('kc-token')
      localStorage.removeItem('kc-username')
      localStorage.removeItem('kc-role')
      localStorage.removeItem('kc-viewer')
    },
  },
})
