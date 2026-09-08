// 节点类型元数据组合式（替代原 ci-platform 的 zustand store）：
// 模块级缓存 + in-flight 去重——节点面板/画布/属性面板共用一份 GET /ci/node-types 结果。
// 集群切换（X-Cluster 变化）时失效重拉。
import { shallowRef } from 'vue'
import { ciApi, type CINodeType } from '../../api/ci'
import { activeCluster } from '../../store/clusterRef'

const types = shallowRef<CINodeType[]>([])
const loading = shallowRef(false)
const error = shallowRef('')
let inflight: Promise<void> | null = null
let loadedCluster = ''

function currentCluster() {
  return activeCluster.value || localStorage.getItem('kc-cluster') || ''
}

export function useNodeTypes() {
  async function load(force = false) {
    const cluster = currentCluster()
    if (!force && loadedCluster === cluster && types.value.length > 0 && !error.value) return
    if (inflight && loadedCluster === cluster) return inflight
    loadedCluster = cluster
    inflight = (async () => {
      loading.value = true
      error.value = ''
      try {
        types.value = await ciApi.nodeTypes()
      } catch (e: any) {
        error.value = e?.message || '节点列表加载失败'
      } finally {
        loading.value = false
        inflight = null
      }
    })()
    return inflight
  }
  return { types, loading, error, load }
}

// 集群切换钩子：主布局切集群时调用
export function invalidateNodeTypes() {
  loadedCluster = ''
}
