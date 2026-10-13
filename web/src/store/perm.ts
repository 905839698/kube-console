// 权限快照（KubeSphere 式三层角色：平台 / 集群 / 项目）：后端 /rbac/my-permissions 给出。
// 只用于菜单与写按钮的动态渲染——真正的判定在后端逐接口执行（前端绕过也没用）。
// 模块级缓存 + 切集群失效，与 ci/nodes.ts 同一套写法。
import { shallowRef } from 'vue'
import { authzApi, type MyPermissions, type RoleBrief } from '../api'
import { activeCluster } from './clusterRef'
import { menuKeyOfPath } from '../utils/menu-key'

const EMPTY: MyPermissions = {
  platformAdmin: false, allNsWrite: false, clusterWrite: false,
  namespaces: {}, namespacesRead: {}, roles: [],
}
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

  // canReadNS 该 ns 是否有任意角色（含只读 view）——与后端 Access.CanReadNS 同口径：
  // CI 的可见性与「能否触发运行」用它（view 可跑，不可改定义）
  function canReadNS(ns?: string) {
    const a = access.value
    if (a.platformAdmin || a.allNsWrite || a.allNsRead) return true
    const key = (ns || 'default').trim()
    return !!a.namespaces?.[key] || !!a.namespacesRead?.[key]
  }

  // canWriteCluster 集群级写（节点运维、命名空间增删、告警静默）
  function canWriteCluster() {
    return access.value.platformAdmin || access.value.clusterWrite
  }

  // canMenu 菜单键是否可见（侧边栏用，精确匹配）。菜单显隐与接口收口
  // （rbac.enforceMenus）共用后端那张能力表，所以这里 false 的项接口也 403。
  function canMenu(key: string) {
    const m = access.value.menus
    if (!m) return true // 快照没带 menus（旧后端）：不藏任何东西
    return m.includes(key)
  }

  // canVisit 路由路径能否进。详情路由先归到它的列表页键，再判可见——
  // 精确匹配会把 /workloads/deployments/foo、/pods/<ns>/<name> 这类页面误判成无权。
  function canVisit(path: string) {
    const m = access.value.menus
    if (!m) return true
    const set = new Set(m)
    if (set.has(path)) return true
    const key = menuKeyOfPath(path)
    if (set.has(key)) return true
    // CRD 浏览器能看任意自定义资源：/crd/<group>/<version>/<resource> 与
    // /resources/<不在菜单里的资源> 都归到它
    if (path.startsWith('/crd/') || key.startsWith('/resources/')) return set.has('/resources')
    return false
  }

  // canGroup 一级分组：后端算的是"组内任一子项可见"
  function canGroup(name: string) {
    const m = access.value.menus
    if (!m) return true
    return m.includes('group:' + name)
  }

  // firstMenu 用户落地页：给一个他真能看的菜单，别再把他丢在 403 页上
  function firstMenu(): string {
    const m = access.value.menus
    if (!m) return '/overview'
    const prefer = ['/overview', '/workloads/matrix', '/ci', '/kb/mysql', '/namespaces']
    for (const p of prefer) if (m.includes(p)) return p
    const item = m.find((k) => k.startsWith('/') && !k.startsWith('/auth/') )
    return item || '/login'
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

  return { access, load, canWriteNS, canReadNS, canWriteCluster, canMenu, canVisit, canGroup, firstMenu, isPlatformAdmin, isPlatformViewer, roles }
}
