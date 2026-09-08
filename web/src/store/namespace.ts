// 全局命名空间状态：按集群记忆当前选择的命名空间（持久化到 localStorage）
import { defineStore } from 'pinia'

// 值语义与 NamespaceSelect 一致：['__all__'] 表示全部，否则为命名空间数组
export const useNamespaceStore = defineStore('namespace', {
  state: () => ({
    byCluster: JSON.parse(localStorage.getItem('kc-namespaces') || '{}') as Record<string, string[]>,
  }),
  getters: {
    // 当前集群的选择；默认全选。集群切换时自动联动（按集群名分 key）
    selected(): string[] {
      const cluster = localStorage.getItem('kc-cluster') || ''
      const ns = this.byCluster[cluster]
      return ns && ns.length ? ns : ['__all__']
    },
  },
  actions: {
    select(ns: string[]) {
      const cluster = localStorage.getItem('kc-cluster') || ''
      this.byCluster[cluster] = ns && ns.length ? ns : ['__all__']
      localStorage.setItem('kc-namespaces', JSON.stringify(this.byCluster))
    },
  },
})
