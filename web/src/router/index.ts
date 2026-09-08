// 路由配置 + 登录守卫
import { createRouter, createWebHistory } from 'vue-router'
import { useUserStore } from '../store/user'
import { askCiDesignLeave } from '../views/ci/dirtyGuard'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('../views/Login.vue'), meta: { title: '登录' } },
    {
      path: '/',
      component: () => import('../layouts/MainLayout.vue'),
      redirect: '/overview',
      children: [
        { path: 'overview', name: 'overview', component: () => import('../views/Overview.vue'), meta: { title: '集群总览' } },
        { path: 'monitor', name: 'monitor', component: () => import('../views/Monitor.vue'), meta: { title: '监控' } },
        { path: 'monitor/alerts', name: 'alerts', component: () => import('../views/AlertList.vue'), meta: { title: '告警' } },
        { path: 'monitor/namespace/:name', name: 'namespaceMonitor', component: () => import('../views/NamespaceMonitor.vue'), meta: { title: '命名空间监控' } },
        { path: 'clusters', name: 'clusters', component: () => import('../views/ClusterList.vue'), meta: { title: '集群管理' } },
        { path: 'namespaces', name: 'namespaces', component: () => import('../views/NamespaceList.vue'), meta: { title: '命名空间' } },
        { path: 'workloads/matrix', name: 'workloadMatrix', component: () => import('../views/WorkloadMatrix.vue'), meta: { title: '工作负载矩阵' } },
        { path: 'workloads/:kind', name: 'workloads', component: () => import('../views/WorkloadList.vue'), meta: { title: '工作负载' } },
        { path: 'workloads/:kind/:name', name: 'workloadDetail', component: () => import('../views/WorkloadDetail.vue'), meta: { title: '工作负载详情' } },
        // Pod 已并入工作负载列表（/workloads/pods）；旧地址重定向
        { path: 'pods', redirect: (to) => ({ path: '/workloads/pods', query: to.query }) },
        { path: 'pods/:namespace/:name', name: 'podDetail', component: () => import('../views/PodDetail.vue'), meta: { title: 'Pod 详情' } },
        // 通用资源（kind 模式）：/resources/services 等
        { path: 'resources/:kind', name: 'resources', component: () => import('../views/GenericResourceList.vue') },
        // 自定义资源（GVR 模式）：/crd/:group/:version/:resource
        { path: 'crd/:group/:version/:resource', name: 'crd', component: () => import('../views/GenericResourceList.vue'), meta: { title: '自定义资源' } },
        { path: 'quotas', name: 'quotaOverview', component: () => import('../views/QuotaOverview.vue'), meta: { title: '配额总览' } },
        { path: 'resources', name: 'resourceBrowser', component: () => import('../views/ResourceBrowser.vue'), meta: { title: '自定义资源' } },
        { path: 'nodes', name: 'nodes', component: () => import('../views/NodeList.vue'), meta: { title: '节点' } },
        { path: 'nodes/:name', name: 'nodeDetail', component: () => import('../views/NodeDetail.vue'), meta: { title: '节点详情' } },
        // Helm 应用管理
        { path: 'helm/releases', name: 'helmReleases', component: () => import('../views/HelmList.vue'), meta: { title: 'Helm 应用' } },
        { path: 'helm/releases/:name', name: 'helmReleaseDetail', component: () => import('../views/HelmDetail.vue'), meta: { title: 'Helm 应用详情' } },
        { path: 'helm/repos', name: 'helmRepos', component: () => import('../views/HelmRepoList.vue'), meta: { title: 'Chart 仓库' } },
        // 事件中心
        { path: 'events', name: 'events', component: () => import('../views/EventList.vue'), meta: { title: '事件中心' } },
        { path: 'logsearch', name: 'logsearch', component: () => import('../views/LogSearch.vue'), meta: { title: '日志检索' } },
        { path: 'ci/pipelines/:id/design', name: 'ciDesigner', component: () => import('../views/ci/designer/Designer.vue'), meta: { title: '流水线设计' } },
{ path: 'ci', name: 'ci', component: () => import('../views/CIPipelines.vue'), meta: { title: 'CI 流水线' } },
        { path: 'argocd', name: 'argocd', component: () => import('../views/ArgoCDApps.vue'), meta: { title: 'ArgoCD 应用' } },
        { path: 'argocd/repos', name: 'argocdRepos', component: () => import('../views/ArgoCDRepos.vue'), meta: { title: 'ArgoCD 仓库' } },
        // 平台管理（仅管理员）
        { path: 'users', name: 'users', component: () => import('../views/UserList.vue'), meta: { title: '用户管理', admin: true } },
        { path: 'authz', name: 'authz', component: () => import('../views/AuthzList.vue'), meta: { title: '授权管理', admin: true } },
        { path: 'audit', name: 'audit', component: () => import('../views/AuditList.vue'), meta: { title: '审计日志', admin: true } },
        { path: 'notify', name: 'notify', component: () => import('../views/NotifyAdmin.vue'), meta: { title: '通知管理', admin: true } },
        { path: 'registry', name: 'registry', component: () => import('../views/RegistryList.vue'), meta: { title: '镜像仓库', admin: true } },
        { path: 'usage', name: 'usage', component: () => import('../views/UsageReport.vue'), meta: { title: '用量报表', admin: true } },
        { path: 'backups', name: 'backups', component: () => import('../views/BackupList.vue'), meta: { title: '备份概览', admin: true } },
      ],
    },
  ],
})

router.beforeEach(async (to, from) => {
  const user = useUserStore()
  document.title = `${to.meta.title || ''} - Kube Console`
  if (to.path !== '/login' && !user.token) {
    return { path: '/login' }
  }
  if (to.path === '/login' && user.token) {
    return { path: '/' }
  }
  // 平台管理页面仅管理员可见
  if (to.meta.admin && !user.isAdmin) {
    return { path: '/overview' }
  }
  // 设计器未保存修改：离开设计器路由前确认
  if (from.path !== to.path && /\/ci\/pipelines\/\d+\/design$/.test(from.path)) {
    const ok = await askCiDesignLeave()
    if (!ok) return false
  }
})

export default router
