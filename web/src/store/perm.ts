// 权限快照（KubeSphere 式三层角色：平台 / 集群 / 项目）：后端 /rbac/my-permissions 给出。
// 只用于菜单与写按钮的动态渲染——真正的判定在后端逐接口执行（前端绕过也没用）。
// 模块级缓存 + 切集群失效，与 ci/nodes.ts 同一套写法。
import { shallowRef } from 'vue'
import { authzApi, type MyPermissions, type RoleBrief } from '../api'
import { activeCluster } from './clusterRef'

const EMPTY: MyPermissions = { platformAdmin: false, allNsWrite: false, clusterWrite: false, namespaces: {}, roles: [] }
const access = shallowRef<MyPermissions>({ ...EMPTY })
let loadedCluster = ''
let inflight: Promise<void> | null = null

function currentCluster() {
  return activeCluster.value || localStorage.getItem('kc-cluster') || ''
}

export function usePerm() {
  async function load(force = false) {
    const cluster = currentCluster()
    if (!cluster) return
    if (!force && loadedCluster === cluster) return
    if (inflight && loadedCluster === cluster) return inflight
    // 切集群：先清空，避免用上一个集群的权限渲染按钮
    if (loadedCluster !== cluster) access.value = { ...EMPTY }
    loadedCluster = cluster
    inflight = (async () => {
      try {
        const a = await authzApi.myPermissions()
        if (loadedCluster === cluster) access.value = a // 切集群期间的迟到响应不覆盖
      } catch {
        access.value = { ...EMPTY } // 取不到按无权限（与后端 fail-closed 一致）
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  // canWriteNS 判断某命名空间能否写（未传按 default，与后端口径一致）
  function canWriteNS(ns?: string) {
    const a = access.value
    if (a.platformAdmin || a.allNsWrite) return true
    return !!a.namespaces?.[(ns || 'default').trim()]
  }

  // canWriteCluster 集群级写（节点运维、命名空间增删、告警静默）
  function canWriteCluster() {
    return access.value.platformAdmin || access.value.clusterWrite
  }

  // 平台层角色（菜单动态渲染依据）
  function isPlatformAdmin() {
    return !!access.value.platformAdmin
  }
  function isPlatformViewer() {
    return !access.value.platformAdmin && !!access.value.platformViewer
  }
  function roles(): RoleBrief[] {
    return access.value.roles || []
  }

  return { access, load, canWriteNS, canWriteCluster, isPlatformAdmin, isPlatformViewer, roles }
}
