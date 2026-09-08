// 集群状态：当前选中的集群（持久化到 localStorage）
import { defineStore } from 'pinia'
import { clusterApi, type Cluster } from '../api'
import { activeCluster } from './clusterRef'

export const useClusterStore = defineStore('cluster', {
  state: () => ({
    clusters: [] as Cluster[],
    current: localStorage.getItem('kc-cluster') || '',
    // 集群列表加载完成且已确定当前集群；内容区/集群相关组件在此之前不渲染，
    // 避免页面在集群未就绪时发起请求导致「缺少 X-Cluster 请求头」
    ready: false,
  }),
  getters: {
    currentCluster(state): Cluster | undefined {
      return state.clusters.find((c) => c.name === state.current)
    },
  },
  actions: {
    async load() {
      try {
        this.clusters = await clusterApi.list()
        // 当前集群不存在时回退到第一个；并始终持久化，确保 localStorage 与 current 一致
        if (this.clusters.length > 0) {
          const valid = this.clusters.some((c) => c.name === this.current)
          this.select(valid ? this.current : this.clusters[0].name)
        }
      } finally {
        // 无论成功失败都置就绪，避免集群接口异常时内容区一直卡在「加载中」
        this.ready = true
      }
    },
    // 重新检测当前（或指定）集群连通性，成功后用返回的最新状态刷新本地列表。
    // 用于「集群不可达」面板的“重新检测”：网络恢复后 status 变 connected，面板自动消失。
    async recheck(name?: string) {
      const target = name || this.current
      if (!target) return
      const updated = await clusterApi.connectivity(target)
      const idx = this.clusters.findIndex((c) => c.name === updated.name)
      if (idx >= 0) this.clusters[idx] = { ...this.clusters[idx], ...updated }
      else this.clusters.push(updated)
      if (this.clusters.length && !this.clusters.some((c) => c.name === this.current)) {
        this.select(this.clusters[0].name)
      }
    },
    select(name: string) {
      this.current = name
      activeCluster.value = name
      localStorage.setItem('kc-cluster', name)
    },
  },
})
