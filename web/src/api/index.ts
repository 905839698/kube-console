// 后端 API 定义（与 server/internal/handler 对应）
import { request, http } from './http'


/** 命名空间多选数组转请求参数：含全选 → "*"，否则逗号拼接 */
export function nsParam(ns: string[]): string {
  if (!ns || !ns.length) return ''
  if (ns.includes('__all__')) return '*'
  return ns.join(',')
}

// ---------------- 类型定义 ----------------

export interface Cluster {
  id: number
  name: string
  server: string
  context: string
  status: 'connected' | 'error' | 'unknown'
  errorMessage: string
  lastConnectedAt?: string
  prometheusNamespace?: string
  prometheusService?: string
  prometheusPort?: number
  createdAt: string
  updatedAt: string
}

export interface NodeSummary {
  name: string
  status: string
  roles: string
  internalIP: string
  version: string
  cpuCores: string
  memGi: string
  age: string
}

export interface OverviewStats {
  nodes: number
  nodesReady: number
  namespaces: number
  pods: number
  podsRunning: number
  deployments: number
  statefulSets: number
  daemonSets: number
  services: number
  ingresses: number
  cpuCapacity: string
  memCapacity: string
  nodesList: NodeSummary[]
}

export interface NamespaceItem {
  name: string
  status: string
  labels: Record<string, string>
  age: string
}

export interface PodItem {
  name: string
  namespace: string
  nodeName: string
  status: string
  ready: string
  restarts: number
  ip: string
  age: string
  labels: Record<string, string>
}

export interface ContainerStatus {
  name: string
  ready: boolean
  restartCount: number
  state: string
  reason: string
  message: string
}

export interface EventItem {
  type: string
  reason: string
  message: string
  object: string
  count: number
  firstSeen: string
  lastSeen: string
}

export interface PodDetail extends PodItem {
  phase: string
  containers: ContainerStatus[]
  initContainers: ContainerStatus[]
  conditions: CondItem[]
  events: EventItem[]
  serviceAccount: string
  tolerations: string[]
  startTime: string
}

export interface CondItem {
  type: string
  status: string
  reason: string
  message: string
  updated: string
}

export interface WorkloadItem {
  kind: string
  name: string
  namespace: string
  replicas: number
  ready: number
  updated: number
  available: number
  images: string[]
  age: string
  labels: Record<string, string>
  // Pod 专用扩展（控制器类型不返回）
  status?: string
  readyStr?: string
  restarts?: number
  ip?: string
  nodeName?: string
}

export interface WorkloadDetail {
  kind: string
  name: string
  namespace: string
  replicas: { replicas: number; readyReplicas: number; updated: number; available: number }
  selector: Record<string, string>
  strategy: string
  images: string[]
  labels: Record<string, string>
  age: string
  containers: ContainerInfo[]
  pods: PodItem[]
  events: EventItem[]
  schedule?: string
}

export interface ContainerInfo {
  name: string
  image: string
  command: string
  ports: string
  requests: string
  limits: string
  readyProbe: string
  liveProbe: string
}

export interface RolloutItem {
  revision: number
  name: string
  image: string
  replicas: number
  age: string
  current: boolean
  changeCause: string
}

export interface GenericItem {
  kind: string
  name: string
  namespace: string
  summary: string
  age: string
  labels: Record<string, string>
  /** Service 访问地址（每端口一条：集群内 DNS / LB 外部地址 / ExternalName），供一键复制 */
  addresses?: string[]
}

export interface NodeDetail extends NodeSummary {
  labels: Record<string, string>
  capacity: Record<string, string>
  allocatable: Record<string, string>
  conditions: CondItem[]
  taints: string[]
  taintItems: { key: string; value: string; effect: string }[]
  schedulable: boolean
  pods: PodItem[]
  containerRuntime: string
  osImage: string
  kernelVersion: string
}

export interface ResourceDef {
  group: string
  version: string
  resource: string
  kind: string
  namespaced: boolean
  verbs: string[]
  shortNames?: string[]
}

export interface GroupDef {
  group: string
  preferred: string
  resources: ResourceDef[]
}

// Gateway API 资源可用性（discovery 解析的实际版本）
export interface GatewayAvailability {
  kind: string
  version: string
  found: boolean
}

// ---------------- 搜索与矩阵 ----------------

export interface SearchItem {
  type: string
  namespace: string
  name: string
}

export interface QuotaOverviewItem {
  namespace: string
  quotaName: string
  hard: Record<string, string>
  used: Record<string, string>
}

export interface WorkloadMatrix {
  namespaces: string[]
  kinds: string[]
  cells: Record<string, Record<string, { total: number; ready: number }>>
}

// ---------------- 监控类型 ----------------

export type TrendPoint = [number, number]

export interface RankItem {
  name: string
  value: number
}

export interface ControlCard {
  label: string
  value: number
  unit: string
  dec: number
}

export interface ControlPlaneItem {
  name: string
  ready: number
  total: number
  cards: ControlCard[]
}

export interface MonitorOverview {
  cpuCores: number
  memTotalGi: number
  cpuUsagePct: number
  memUsagePct: number
  diskUsagePct: number
  netRxMBs: number
  netTxMBs: number
  cpuUsageTrend: TrendPoint[]
  memUsageTrend: TrendPoint[]
  diskUsageTrend: TrendPoint[]
  netRxTrend: TrendPoint[]
  netTxTrend: TrendPoint[]
  nodeCpuRank: RankItem[]
  nodeMemRank: RankItem[]
  namespaceCpuRank: RankItem[]
  controlPlane: ControlPlaneItem[]
}

export interface NodeMonitor {
  cpuUsagePct: number
  memUsagePct: number
  diskUsagePct: number
  netRxMBs: number
  netTxMBs: number
  cpuUsageTrend: TrendPoint[]
  memUsageTrend: TrendPoint[]
  diskUsageTrend: TrendPoint[]
  netRxTrend: TrendPoint[]
  netTxTrend: TrendPoint[]
}

export interface NamespaceMonitor {
  cpuUsagePct: number
  memUsagePct: number
  podCount: number
  diskWriteMBs: number
  netRxMBs: number
  netTxMBs: number
  cpuUsageTrend: TrendPoint[]
  memUsageTrend: TrendPoint[]
  podCountTrend: TrendPoint[]
  diskWriteTrend: TrendPoint[]
  netRxTrend: TrendPoint[]
  netTxTrend: TrendPoint[]
}

export interface WorkloadMonitor {
  // null = 容器未全覆盖 limit，无有效百分比（前端改用 cpuUsage/memUsageMi 显示绝对值）
  cpuUsagePct: number | null
  memUsagePct: number | null
  cpuUsage: number      // 核数（绝对值）
  memUsageMi: number    // Mi（绝对值）
  podCount: number
  diskWriteMBs: number
  netRxMBs: number
  netTxMBs: number
  cpuUsageTrend: TrendPoint[]
  memUsageTrend: TrendPoint[]
  // 趋势坐标轴单位：true=百分比，false=绝对值（CPU 核数 / 内存 Mi）
  cpuTrendIsPct: boolean
  memTrendIsPct: boolean
  diskWriteTrend: TrendPoint[]
  netRxTrend: TrendPoint[]
  netTxTrend: TrendPoint[]
}

export interface PodMonitor {
  // null = 容器未全覆盖 limit，无有效百分比（前端显示 --）
  cpuUsagePct: number | null
  memUsageMi: number
  memUsagePct: number | null
  netRxMBs: number
  netTxMBs: number
  diskWriteMBs: number
  fsUsageMi?: number
  cpuUsageTrend: TrendPoint[]
  memUsageTrend: TrendPoint[]
  netRxTrend: TrendPoint[]
  netTxTrend: TrendPoint[]
  diskWriteTrend: TrendPoint[]
  fsUsageTrend: TrendPoint[]
}

// ---------------- 告警 ----------------

export interface AlertInstance {
  labels: Record<string, string>
  started: string
  value: string
}

export interface AlertRule {
  alertName: string
  state: 'firing' | 'pending' | 'inactive'
  severity: string // critical / warning / info / none
  namespace: string
  summary: string
  detail: string
  runbook: string
  group: string
  query: string
  duration: number // 触发持续时长（秒）
  lastEval: string
  instances: AlertInstance[]
  // 规则检查（lint）
  hasSeverity: boolean
  hasSummary: boolean
  hasRunbook: boolean
  hasNamespace: boolean
}

export interface GroupSummary {
  name: string
  interval: string
  lastEval: string
  alertRules: number
  recRules: number
  firing: number
  severities: string[]
}

export interface AlertSummary {
  firing: number
  pending: number
  inactive: number
  total: number
  bySeverity: Record<string, number>
}

export interface AlertRulesResponse {
  summary: AlertSummary
  rules: AlertRule[]
  groups: number
  groupList: GroupSummary[]
  lastEval: string
}

// ---------------- 认证 ----------------

export const authApi = {
  login: (username: string, password: string) =>
    request<{ token: string; username: string }>({ url: '/auth/login', method: 'post', data: { username, password } }),
  me: () => request<{ username: string }>({ url: '/auth/me' }),
}

// ---------------- 集群管理 ----------------

export const clusterApi = {
  list: () => request<Cluster[]>({ url: '/clusters' }),
  create: (name: string, kubeconfig: string) =>
    request<Cluster>({ url: '/clusters', method: 'post', data: { name, kubeconfig } }),
  update: (name: string, kubeconfig: string) =>
    request<Cluster>({ url: `/clusters/${name}`, method: 'put', data: { kubeconfig } }),
  updatePrometheus: (name: string, prom: { prometheusNamespace: string; prometheusService: string; prometheusPort: number }) =>
    request<Cluster>({ url: `/clusters/${name}/prometheus`, method: 'put', data: prom }),
  remove: (name: string) => request({ url: `/clusters/${name}`, method: 'delete' }),
  connectivity: (name: string) => request<Cluster>({ url: `/clusters/${name}/connectivity` }),
}

// ---------------- 集群资源 ----------------

export const k8sApi = {
  overview: () => request<OverviewStats>({ url: '/overview' }),

  namespaces: (search = '') => request<NamespaceItem[]>({ url: '/namespaces', params: { search } }),
  createNamespace: (name: string) => request({ url: '/namespaces', method: 'post', data: { name } }),
  deleteNamespace: (name: string) => request({ url: `/namespaces/${name}`, method: 'delete' }),

  nodes: () => request<NodeDetail[]>({ url: '/nodes' }),
  nodeDetail: (name: string) => request<NodeDetail>({ url: `/nodes/${name}` }),

  pods: (namespace: string, search = '') =>
    request<PodItem[]>({ url: '/pods', params: { namespace, search } }),
  podDetail: (namespace: string, name: string) =>
    request<PodDetail>({ url: `/pods/${name}`, params: { namespace } }),
  deletePod: (namespace: string, name: string) =>
    request({ url: `/pods/${name}`, method: 'delete', params: { namespace } }),
  evictPod: (namespace: string, name: string) =>
    request({ url: `/pods/${name}/evict`, method: 'post', params: { namespace } }),
  podLogsUrl: (namespace: string, name: string, container: string, tailLines = 500, follow = true) =>
    `/api/pods/${name}/logs?namespace=${namespace}&container=${container}&tailLines=${tailLines}&follow=${follow ? 1 : 0}`,

  workloads: (kind: string, namespace: string, search = '') =>
    request<WorkloadItem[]>({ url: `/workloads/${kind}`, params: { namespace, search } }),
  workloadDetail: (kind: string, namespace: string, name: string) =>
    request<WorkloadDetail>({ url: `/workloads/${kind}/${name}`, params: { namespace } }),
  scaleWorkload: (kind: string, namespace: string, name: string, replicas: number) =>
    request({ url: `/workloads/${kind}/${name}/scale`, method: 'put', params: { namespace }, data: { replicas } }),
  rollouts: (kind: string, namespace: string, name: string) =>
    request<RolloutItem[]>({ url: `/workloads/${kind}/${name}/rollouts`, params: { namespace } }),
  rollback: (namespace: string, name: string, revision: number) =>
    request({ url: `/workloads/deployments/${name}/rollback`, method: 'post', data: { namespace, revision } }),
  restartWorkload: (kind: string, namespace: string, name: string) =>
    request({ url: `/workloads/${kind}/${name}/restart`, method: 'put', params: { namespace } }),
  deleteWorkload: (kind: string, namespace: string, name: string) =>
    request({ url: `/workloads/${kind}/${name}`, method: 'delete', params: { namespace } }),

  resources: (kind: string, namespace: string, search = '') =>
    request<GenericItem[]>({ url: `/resources/${kind}`, params: { namespace, search } }),
  resourceYaml: (kind: string, namespace: string, name: string) =>
    request<{ yaml: string }>({ url: `/resources/${kind}/${name}/yaml`, params: { namespace } }),
  deleteResource: (kind: string, namespace: string, name: string) =>
    request({ url: `/resources/${kind}/${name}`, method: 'delete', params: { namespace } }),

  applyYaml: (yaml: string) => request<{ created: boolean }>({ url: '/yaml/apply', method: 'post', data: { yaml } }),
  getYaml: (resource: string, namespace: string, name: string) =>
    request<{ yaml: string }>({ url: '/yaml', params: { resource, namespace, name } }),

  resourceDefs: (force = false) => request<GroupDef[]>({ url: '/resource-defs', params: { force: force ? 1 : undefined } }),

  gatewayAvailability: () => request<GatewayAvailability[]>({ url: '/gateway-availability' }),

  // 全局搜索与工作负载矩阵
  search: (q: string, limit = 10) => request<SearchItem[]>({ url: '/search', params: { q, limit } }),
  workloadMatrix: () => request<WorkloadMatrix>({ url: '/workloads/matrix' }),
  quotaOverview: () => request<QuotaOverviewItem[]>({ url: '/quotas/overview' }),

  // Prometheus 监控
  monitorOverview: (range = '6h') => request<MonitorOverview>({ url: '/monitor/overview', params: { range } }),
  monitorNode: (name: string, range = '6h') => request<NodeMonitor>({ url: `/monitor/node/${name}`, params: { range } }),
  monitorNamespace: (name: string, range = '6h') => request<NamespaceMonitor>({ url: `/monitor/namespace/${name}`, params: { range } }),
  monitorWorkload: (kind: string, namespace: string, name: string, range = '6h') =>
    request<WorkloadMonitor>({ url: '/monitor/workload', params: { kind, namespace, name, range } }),
  monitorPod: (namespace: string, name: string, range = '6h') =>
    request<PodMonitor>({ url: '/monitor/pod', params: { namespace, name, range } }),
  monitorCheck: () => request<{ status: string; prometheus: string }>({ url: '/monitor/prometheus-check' }),
  monitorAlerts: () => request<AlertRulesResponse>({ url: '/monitor/alerts' }),

  // 任意 GVR（CRD 浏览）
  genericList: (group: string, version: string, resource: string, namespace = '', search = '', labelSelector = '') =>
    request<GenericItem[]>({ url: `/generic/${group || 'core'}/${version}/${resource}`, params: { namespace, search, labelSelector } }),
  genericYaml: (group: string, version: string, resource: string, namespace: string, name: string) =>
    request<{ yaml: string }>({ url: `/generic/${group || 'core'}/${version}/${resource}/${name}/yaml`, params: { namespace } }),
  genericDelete: (group: string, version: string, resource: string, namespace: string, name: string) =>
    request({ url: `/generic/${group || 'core'}/${version}/${resource}/${name}`, method: 'delete', params: { namespace } }),
}

// ---------------- Helm 应用管理 ----------------

export interface HelmReleaseItem {
  name: string
  namespace: string
  chart: string
  chartVersion: string
  appVersion: string
  version: number
  status: string
  updated: string
  age: string
  description: string
}

export interface HelmReleaseInfo {
  name: string
  namespace: string
  chart: string
  chartVersion: string
  appVersion: string
  version: number
  status: string
  manifest: string
  values: string
  notes: string
  updated: string
}

export interface HelmReleaseHistory {
  version: number
  status: string
  updated: string
  age: string
  chart: string
}

export interface HelmRepoItem {
  id: number
  name: string
  url: string
  username: string
  hasAuth: boolean
  updatedAt: string
  chartCount: number
}

export interface RepoChartItem {
  name: string
  latestVersion: string
  versions: string[]
  description: string
  icon: string
}

export interface HelmInstallParams {
  repoName: string
  chart: string
  version: string
  releaseName: string
  namespace: string
  createNamespace: boolean
  values: string
  wait: boolean
  timeoutSeconds: number
  atomic: boolean
}

export interface HelmUpgradeParams {
  repoName: string
  chart: string
  version: string
  values: string
  wait: boolean
  timeoutSeconds: number
}

export const helmApi = {
  // Release
  releases: (namespace: string, search = '') =>
    request<HelmReleaseItem[]>({ url: '/helm/releases', params: { namespace, search } }),
  releaseInfo: (namespace: string, name: string) =>
    request<HelmReleaseInfo>({ url: `/helm/releases/${name}/info`, params: { namespace } }),
  releaseHistory: (namespace: string, name: string) =>
    request<HelmReleaseHistory[]>({ url: `/helm/releases/${name}/history`, params: { namespace } }),
  install: (params: HelmInstallParams) =>
    request<HelmReleaseInfo>({ url: '/helm/releases', method: 'post', data: params }),
  upgrade: (namespace: string, name: string, params: HelmUpgradeParams) =>
    request<HelmReleaseInfo>({ url: `/helm/releases/${name}/upgrade`, method: 'put', params: { namespace }, data: params }),
  rollback: (namespace: string, name: string, revision: number, wait: boolean) =>
    request({ url: `/helm/releases/${name}/rollback`, method: 'put', params: { namespace, revision, wait: wait ? 'true' : 'false' } }),
  uninstall: (namespace: string, name: string, keepHistory: boolean) =>
    request({ url: `/helm/releases/${name}`, method: 'delete', params: { namespace, keepHistory: keepHistory ? 'true' : 'false' } }),

  // Chart 仓库
  repos: () => request<HelmRepoItem[]>({ url: '/helm/repos' }),
  // chart 默认 values（安装/升级弹窗预填）
  chartValues: (repoName: string, chart: string, version: string) =>
    request<{ values: string }>({ url: '/helm/charts/values', params: { repo: repoName, chart, version } }),
  // release 当前 values（升级预填：用户 values 优先，空则 release 内 chart 默认值；不依赖源仓库注册）
  releaseUpgradeValues: (namespace: string, name: string) =>
    request<{ config: string; chartValues: string }>({ url: `/helm/releases/${name}/upgrade-values`, params: { namespace } }),
  addRepo: (data: { name: string; url: string; username: string; password: string }) =>
    request<HelmRepoItem>({ url: '/helm/repos', method: 'post', data }),
  updateRepo: (id: number, data: { url: string; username: string; password: string }) =>
    request<HelmRepoItem>({ url: `/helm/repos/${id}`, method: 'put', data }),
  removeRepo: (id: number) => request({ url: `/helm/repos/${id}`, method: 'delete' }),
  refreshRepo: (id: number) => request<{ chartCount: number }>({ url: `/helm/repos/${id}/refresh`, method: 'put' }),
  repoCharts: (id: number, search = '') =>
    request<RepoChartItem[]>({ url: `/helm/repos/${id}/charts`, params: { search } }),
}

// ------------------- 平台管理：用户 / 授权 / 审计 -------------------

export interface PlatformUser {
  id: number
  username: string
  role: 'admin' | 'user'
  groupId: number
  createdAt: string
}

export interface AuditItem {
  id: number
  username: string
  method: string
  path: string
  query: string
  cluster: string
  status: number
  latencyMs: number
  ip: string
  body: string
  createdAt: string
}

export interface GrantItem {
  kind: 'RoleBinding' | 'ClusterRoleBinding'
  name: string
  namespace: string
  role: string
  grantee: string
  subjects: string[]
  age: string
}

export const userApi = {
  list: () => request<PlatformUser[]>({ url: '/users' }),
  create: (data: { username: string; password: string; role: string }) =>
    request<PlatformUser>({ url: '/users', method: 'post', data }),
  resetPassword: (id: number, password: string) =>
    request({ url: `/users/${id}/password`, method: 'put', data: { password } }),
  updateRole: (id: number, role: string) =>
    request({ url: `/users/${id}/role`, method: 'put', data: { role } }),
  remove: (id: number) => request({ url: `/users/${id}`, method: 'delete' }),
  changeMyPassword: (oldPassword: string, newPassword: string) =>
    request({ url: '/auth/password', method: 'put', data: { oldPassword, newPassword } }),
}

export const auditApi = {
  list: (params: { username?: string; q?: string; page?: number; size?: number }) =>
    request<{ total: number; items: AuditItem[]; page: number; size: number }>({ url: '/audit', params }),
}

export const authzApi = {
  userPermissions: (username: string) =>
    request<{ username: string; groups: string[]; permissions: UserPermItem[] }>({ url: '/authz/user-permissions', params: { username } }),
  grant: (data: { username: string; role: string; namespaces: string[] }) =>
    request<{ created: number; binding: string }>({ url: '/authz/grant', method: 'post', data }),
  bindings: () => request<GrantItem[]>({ url: '/authz/bindings' }),
  revoke: (kind: string, name: string, namespace = '') =>
    request({ url: `/authz/bindings/${kind}/${name}`, method: 'delete', params: { namespace } }),
}

// ------------------- 事件中心 -------------------

export interface EventItemEx {
  name: string
  namespace: string
  type: string
  reason: string
  message: string
  object: string
  count: number
  lastAt: string
}

export const eventsApi = {
  list: (namespace: string, type = '', search = '', limit = 300) =>
    request<EventItemEx[]>({ url: '/events', params: { namespace, type, search, limit } }),
}

// ------------------- 节点运维 -------------------

export const nodeApi = {
  cordon: (name: string, cordon: boolean) =>
    request({ url: `/nodes/${name}/cordon`, method: 'post', data: { cordon } }),
  drain: (name: string, opts: { force: boolean; ignoreDaemonsets: boolean; deleteEmptydirData: boolean; gracePeriodSeconds: number; timeoutSeconds: number }) =>
    request<{ evicted: string[]; skipped: string[]; failed: { Name: string; Error: string }[] }>({ url: `/nodes/${name}/drain`, method: 'post', data: opts }),
  updateTaints: (name: string, taints: { key: string; value?: string; effect: string }[]) =>
    request({ url: `/nodes/${name}/taints`, method: 'put', data: { taints } }),
  updateLabels: (name: string, labels: Record<string, string | null>) =>
    request({ url: `/nodes/${name}/labels`, method: 'put', data: { labels } }),
}

// ------------------- 端口转发 -------------------

export interface PortForwardItem {
  id: number
  cluster: string
  namespace: string
  pod: string
  localPort: number
  remotePort: number
  createdAt: string
}

export const pfApi = {
  list: () => request<PortForwardItem[]>({ url: '/portforwards' }),
  start: (namespace: string, pod: string, port: number, localPort = 0) =>
    request<PortForwardItem>({ url: '/portforwards', method: 'post', data: { namespace, pod, port, localPort } }),
  stop: (id: number) => request({ url: `/portforwards/${id}`, method: 'delete' }),
}

// ------------------- 容器文件浏览器 -------------------

export interface FileEntryItem {
  name: string
  type: 'dir' | 'file' | 'link' | 'other'
  size: string
  mode: string
  mtime: string
  linkTarget: string
}

export const filesApi = {
  list: (namespace: string, pod: string, container: string, path: string) =>
    request<{ path: string; entries: FileEntryItem[] }>({ url: `/pods/${pod}/files`, params: { namespace, container, path } }),
  downloadUrl: (namespace: string, pod: string, container: string, path: string) =>
    `/api/pods/${pod}/files/download?namespace=${encodeURIComponent(namespace)}&container=${encodeURIComponent(container)}&path=${encodeURIComponent(path)}`,
  upload: (namespace: string, pod: string, container: string, path: string, file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return http.post(`/pods/${pod}/files/upload?namespace=${encodeURIComponent(namespace)}&container=${encodeURIComponent(container)}&path=${encodeURIComponent(path)}`, fd, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
  },
  action: (namespace: string, pod: string, container: string, data: { action: string; path: string; to?: string }) =>
    request({ url: `/pods/${pod}/files/action`, method: 'post', params: { namespace, container }, data }),
}

// ------------------- 平台扩展：日志/通知/镜像仓库/报表/证书/备份/用户组/Token -------------------

export interface LogHit {
  timestamp: string
  namespace: string
  pod: string
  container: string
  level: string
  message: string
}
export interface PodFacet { pod: string; count: number }
export interface LogSearchResult { total: number; items: LogHit[]; took: number; podFacet: PodFacet[] }
export interface LogSourceItem {
  id: number; clusterName: string; namespace: string; service: string; port: number
  directURL: string; indexPrefix: string; username: string; enabled: boolean
}
export interface NotifyChannelItem {
  id: number; name: string; type: string; webhook: string; minSeverity: string; enabled: boolean
}
export interface NotifyLogItem {
  id: number; channelName: string; cluster: string; alertName: string; severity: string
  message: string; ok: boolean; error: string; sentAt: string
}
export interface RegProject { name: string; repo_count: number }
export interface RegRepository { name: string; artifact_count: number; pull_count: number; update_time: string }
export interface RegArtifact { digest: string; size: number; push_time: string; tags: { name: string }[] }
export interface NSUsageRow {
  namespace: string; cpuAvgCores: number; cpuMaxCores: number
  memAvgGi: number; memMaxGi: number; podAvgCount: number
}
export interface ClusterCertItem {
  cluster: string; status: string; error?: string
  cert?: { host: string; subject: string; issuer: string; notAfter: string; daysLeft: number; expired: boolean }
}
export interface UserGroupItem { id: number; name: string; description: string; users: number }
export interface ApiTokenItem { id: number; name: string; expiresAt: string | null; lastUsedAt: string | null; createdAt: string }
export interface UserPermItem { kind: string; name: string; namespace: string; role: string; roleKind: string; verbs: string[]; resources: string[] }

export const logsApi = {
  search: (q: Partial<LogSearchResult> & Record<string, any>) =>
    request<LogSearchResult>({ url: '/logs/search', method: 'post', data: q }),
  sources: () => request<LogSourceItem[]>({ url: '/logsources' }),
  saveSource: (s: Partial<LogSourceItem> & { password?: string }) =>
    request({ url: '/logsources', method: 'post', data: s }),
  deleteSource: (cluster: string) => request({ url: `/logsources/${encodeURIComponent(cluster)}`, method: 'delete' }),
  testSource: (s: Partial<LogSourceItem> & { password?: string }) =>
    request({ url: '/logsources/test', method: 'post', data: s }),
}

export const notifyApi = {
  channels: () => request<NotifyChannelItem[]>({ url: '/notify/channels' }),
  saveChannel: (c: Partial<NotifyChannelItem> & { secret?: string }) =>
    request({ url: '/notify/channels', method: 'post', data: c }),
  deleteChannel: (id: number) => request({ url: `/notify/channels/${id}`, method: 'delete' }),
  testChannel: (id: number) => request({ url: `/notify/channels/${id}/test`, method: 'post' }),
  logs: (page = 1, size = 50) =>
    request<{ total: number; items: NotifyLogItem[] }>({ url: '/notify/logs', params: { page, size } }),
}

export const registryApi = {
  getConfig: () => request<{ url?: string; username?: string; insecure?: boolean }>({ url: '/registry/config' }),
  saveConfig: (c: { url: string; username?: string; password?: string; insecure?: boolean }) =>
    request({ url: '/registry/config', method: 'post', data: c }),
  test: () => request<{ ok: boolean; projects: number }>({ url: '/registry/test', method: 'post' }),
  projects: (search = '') => request<RegProject[]>({ url: '/registry/projects', params: { search } }),
  repos: (project: string, search = '') => request<RegRepository[]>({ url: `/registry/projects/${project}/repos`, params: { search } }),
  tags: (project: string, repo: string) => request<RegArtifact[]>({ url: '/registry/tags', params: { project, repo } }),
  /** action=get 查扫描状态 / post 触发扫描 */
  scan: (action: 'get' | 'post', project: string, repo: string, reference: string) =>
    request<any>({ url: '/registry/scan', method: action, params: { project, repo, reference } }),
  vulns: (project: string, repo: string, reference: string) =>
    request<any>({ url: '/registry/vulns', params: { project, repo, reference } }),
  imageTags: (image: string) => request<{ tags: string[] }>({ url: '/registry/image-tags', params: { image } }),
  imageVulns: (image: string) =>
    request<{ overview: any; report: any }>({ url: '/registry/image-vulns', params: { image } }),
  // 静默版：失败不弹全局 toast（VulnChip / Tag 下拉等辅助探测，由调用方降级展示）
  imageTagsQuiet: (image: string) => request<{ tags: string[] }>({ url: '/registry/image-tags', params: { image } }, { silent: true }),
  imageVulnsQuiet: (image: string) =>
    request<{ overview: any; report: any }>({ url: '/registry/image-vulns', params: { image } }, { silent: true }),
}

export const usageApi = { report: (days = 7) => request<NSUsageRow[]>({ url: '/usage', params: { days } }) }
export const backupApi = { list: () => request<{ installed: boolean; items?: GenericItem[]; schedules?: GenericItem[] }>({ url: '/backups' }) }
export const certsApi = { list: () => request<ClusterCertItem[]>({ url: '/clusters/certs' }) }

export const groupApi = {
  list: () => request<UserGroupItem[]>({ url: '/groups' }),
  save: (g: Partial<UserGroupItem>) => request({ url: '/groups', method: 'post', data: g }),
  remove: (id: number) => request({ url: `/groups/${id}`, method: 'delete' }),
  setUserGroup: (userId: number, groupId: number) =>
    request({ url: `/users/${userId}/group`, method: 'put', data: { groupId } }),
}

export const tokenApi = {
  list: () => request<ApiTokenItem[]>({ url: '/tokens' }),
  create: (name: string, expiresInDays: number) =>
    request<{ id: number; name: string; token: string; expiresAt: string | null }>({ url: '/tokens', method: 'post', data: { name, expiresInDays } }),
  revoke: (id: number) => request({ url: `/tokens/${id}`, method: 'delete' }),
}

// ------------------- CD（ArgoCD） -------------------
export interface ArgoCDApp {
  namespace: string
  name: string
  sync: string
  health: string
  repoURL: string
  path: string
  target: string
  destNamespace: string
  age: string
  autoSync: boolean
}

// ArgoCD 仓库（v3：以 Secret 存储，标签 argocd.argoproj.io/secret-type=repository）。凭据只返回有无标志，不返回明文。
export interface ArgoCDRepo {
  namespace: string
  name: string
  url: string
  type: 'git' | 'helm'
  username?: string
  hasPassword: boolean
  hasSSHKey: boolean
  insecure: boolean
  enableLfs: boolean
  // 主机级 URL（如 http://gitlab.xxx.com）= 凭据模板，该域下所有仓库自动继承账号密码
  credentialOnly?: boolean
}

export const argocdApi = {
  apps: () => request<{ installed: boolean; items: ArgoCDApp[] }>({ url: '/argocd/apps' }),
  refresh: (namespace: string, name: string) =>
    request({ url: `/argocd/apps/${namespace}/${name}/refresh`, method: 'post' }),
  // 完整 Application 对象 YAML（可视化编辑器加载）
  appDetail: (namespace: string, name: string) =>
    request<{ yaml: string }>({ url: `/argocd/apps/${namespace}/${name}` }),
  // 仓库管理（含账号密码；更新时密码留空 = 保留原凭据）
  repos: () => request<{ installed: boolean; namespace: string; items: ArgoCDRepo[] }>({ url: '/argocd/repos' }),
  repoCreate: (data: Partial<ArgoCDRepo> & { url: string; name: string }) =>
    request({ url: '/argocd/repos', method: 'post', data }),
  repoUpdate: (name: string, data: Partial<ArgoCDRepo> & { url: string }) =>
    request({ url: `/argocd/repos/${name}`, method: 'put', data }),
  repoDelete: (name: string, namespace: string) =>
    request({ url: `/argocd/repos/${name}`, method: 'delete', params: { namespace } }),
}
