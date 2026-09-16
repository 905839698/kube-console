// CI（内置 Tekton）API 层——直连 kube-console /api/ci/*（集群隔离经 http 层自动带 X-Cluster）。
// 替代原 ci-platform HTTP 透传；SSE/WS 走 60s 短期流 token（?token= 查询鉴权）。
import { request } from './http'
import { activeCluster } from '../store/clusterRef'

// ============ 类型 ============

export interface CIPropOpt { value: string; label: string }

/** 节点属性 schema（后端 node/schema.json 的唯一事实来源） */
export interface CIPropSchema {
  name: string
  type: 'string' | 'number' | 'boolean' | 'credential' | 'select' | 'text'
  required?: boolean
  default?: unknown
  options?: CIPropOpt[]
  description?: string
  optionsSource?: 'git-branch' | 'git-tag' | 'k8s-ns' | 'k8s-deployments' | 'k8s-container' | 'upstream-task' | 'task-result'
}

export interface CINodeType {
  type: string
  name: string
  category: string
  icon?: string
  version?: string
  description?: string
  properties: CIPropSchema[]
  results?: CIPropOpt[]
}

export interface CIPosition { x: number; y: number }
export interface CIPNode { id: string; type: string; params: Record<string, unknown>; position?: CIPosition }
export interface CIPEdge { source: string; target: string; branch?: string }
/** 画布 DSL（与后端 model.Graph 逐字段一致） */
export interface CIGraph { name: string; version: number; nodes: CIPNode[]; edges: CIPEdge[] }

export interface CIProject {
  id: number; name: string; displayName: string; description: string
  namespace: string; createdAt?: string
}
export interface CIPipeline {
  id: number; projectId: number; name: string; description: string
  status: string; latestVersion?: number; createdAt?: string; updatedAt?: string
}
export interface CIPipelineVersion {
  id: number; pipelineId: number; version: number; graphJson: string; compiledYaml: string
  createdAt?: string
}
export interface CIPipelineDetail {
  pipeline: CIPipeline
  latestVersion?: CIPipelineVersion | null
  graph?: CIGraph | null
}
export type CIRunStatus = 'pending' | 'running' | 'success' | 'failed' | 'cancelled'
export interface CIRun {
  id: number; pipelineId: number; versionId: number; runNo: number
  status: CIRunStatus; triggerType: string
  gitCommit?: string; gitBranch?: string; gitRepo?: string
  startedBy?: string; tektonRunName?: string
  startedAt?: string | null; finishedAt?: string | null
}
export interface CITaskRun {
  id: number; runId: number; nodeId: string; nodeType: string; name: string
  status: CIRunStatus
  startedAt?: string | null; finishedAt?: string | null
  tektonTaskRun?: string; results?: Record<string, string> | null
}
export interface CIRunDetail { run: CIRun; tasks: CITaskRun[]; pipeline: { id: number; name: string; projectId: number } }
export interface CICredential {
  id: number; name: string; form: string; type?: string
  secretName: string; secretNS: string
  extra?: Record<string, string>
  projectId?: number | null; references?: string[]
  createdAt?: string; updatedAt?: string
}
export interface CIApproval { nodeId: string; name: string; status: string; decision: string; tektonRun: string }
export interface CICIRReady { tektonInstalled: boolean; platformNS: string; serviceAccount: string; cacheEnabled: boolean }

// ============ 定时 / Webhook / 全局变量 ============

export interface CISchedule {
  id: number; pipelineId: number; projectId: number
  cron: string; enabled: boolean
  lastRunAt?: string | null; nextRunAt?: string | null; lastError?: string
  createdAt?: string
}
export interface CIWebhook {
  id: number; pipelineId: number; projectId: number
  token: string; branch: string; enabled: boolean
  url?: string; createdAt?: string
}
export interface CIArtifact {
  id: number; projectId: number; pipelineId: number; pipelineRunId: number
  name: string; type: string; version?: string
  storageType: string; storagePath?: string; size?: number; sha256?: string; digest?: string
  createdAt?: string
}
export interface CIArtifactDetail extends CIArtifact {
  pipelineName?: string; gitCommit?: string; gitBranch?: string
  runNo?: number; startedBy?: string; runStartedAt?: string; pullCommand?: string
}
export interface CIDeploymentTarget { kind: string; name: string; images: Record<string, string> }
export interface CIDeployment {
  id: number; projectId: number; pipelineId: number; runId: number
  nodeName: string; kind: string; namespace: string; release?: string
  targets: CIDeploymentTarget[]; status: string
  gitCommit?: string; gitBranch?: string; rolledBackFrom?: number
  createdBy?: string; createdAt?: string
}
export interface CIGlobalVar {
  id: number; key: string; value: string; description: string
  createdAt?: string; updatedAt?: string
}

// ============ 接口 ============

export const ciApi = {
  ready: () => request<CICIRReady>({ url: '/ci/ready' }),
  nodeTypes: () => request<CINodeType[]>({ url: '/ci/node-types' }),
  streamToken: () => request<{ token: string }>({ url: '/ci/stream-token' }),

  // ---- 定时任务 ----
  schedules: (pipelineId?: number) =>
    request<CISchedule[]>({ url: '/ci/schedules', params: pipelineId ? { pipelineId } : undefined }),
  createSchedule: (body: { pipelineId: number; cron: string }) =>
    request<CISchedule>({ url: '/ci/schedules', method: 'post', data: body }),
  updateSchedule: (id: number, body: { enabled?: boolean; cron?: string }) =>
    request<CISchedule>({ url: `/ci/schedules/${id}`, method: 'put', data: body }),
  deleteSchedule: (id: number) => request<void>({ url: `/ci/schedules/${id}`, method: 'delete' }),

  // ---- Webhook ----
  webhooks: (pipelineId?: number) =>
    request<CIWebhook[]>({ url: '/ci/webhooks', params: pipelineId ? { pipelineId } : undefined }),
  createWebhook: (body: { pipelineId: number; branch?: string }) =>
    request<CIWebhook>({ url: '/ci/webhooks', method: 'post', data: body }),
  updateWebhook: (id: number, body: { enabled?: boolean; branch?: string }) =>
    request<CIWebhook>({ url: `/ci/webhooks/${id}`, method: 'put', data: body }),
  deleteWebhook: (id: number) => request<void>({ url: `/ci/webhooks/${id}`, method: 'delete' }),

  // ---- 制品 ----
  artifacts: (params: { projectId?: number; runId?: number; type?: string; page?: number; size?: number }) =>
    request<{ total: number; items: CIArtifact[] }>({ url: '/ci/artifacts', params }),
  artifact: (id: number) => request<CIArtifactDetail>({ url: `/ci/artifacts/${id}` }),
  artifactDownloadUrl: (id: number) => `/api/ci/artifacts/${id}/download`,

  // ---- 发布记录 ----
  deployments: (params: { projectId?: number; page?: number; size?: number }) =>
    request<{ total: number; items: CIDeployment[] }>({ url: '/ci/deployments', params }),
  rollbackDeployment: (id: number) =>
    request<CIDeployment>({ url: `/ci/deployments/${id}/rollback`, method: 'post', data: {} }),

  // ---- 全局变量 ----
  globals: () => request<CIGlobalVar[]>({ url: '/ci/globals' }),
  createGlobal: (body: { key: string; value?: string; description?: string }) =>
    request<CIGlobalVar>({ url: '/ci/globals', method: 'post', data: body }),
  updateGlobal: (id: number, body: { key: string; value?: string; description?: string }) =>
    request<CIGlobalVar>({ url: `/ci/globals/${id}`, method: 'put', data: body }),
  deleteGlobal: (id: number) => request<void>({ url: `/ci/globals/${id}`, method: 'delete' }),

  // ---- 项目（= 命名空间隔离单元） ----
  projects: () => request<CIProject[]>({ url: '/ci/projects' }),
  createProject: (body: { name: string; displayName?: string; description?: string; namespace: string }) =>
    request<CIProject>({ url: '/ci/projects', method: 'post', data: body }),
  updateProject: (id: number, body: { displayName?: string; description?: string }) =>
    request<CIProject>({ url: `/ci/projects/${id}`, method: 'put', data: body }),
  deleteProject: (id: number) => request<void>({ url: `/ci/projects/${id}`, method: 'delete' }),

  // ---- 流水线 / 版本 ----
  pipelines: (projectId?: number) =>
    request<CIPipeline[]>({ url: '/ci/pipelines', params: projectId ? { projectId } : undefined }),
  pipeline: (id: number) => request<CIPipelineDetail>({ url: `/ci/pipelines/${id}` }),
  createPipeline: (body: { projectId: number; name: string; description?: string }) =>
    request<CIPipeline>({ url: '/ci/pipelines', method: 'post', data: body }),
  updatePipeline: (id: number, body: { name?: string; description?: string }) =>
    request<CIPipeline>({ url: `/ci/pipelines/${id}`, method: 'put', data: body }),
  deletePipeline: (id: number) => request<void>({ url: `/ci/pipelines/${id}`, method: 'delete' }),
  duplicatePipeline: (id: number, body: { targetProjectId: number; name?: string; description?: string }) =>
    request<CIPipeline>({ url: `/ci/pipelines/${id}/duplicate`, method: 'post', data: body }),
  pipelineVersions: (id: number) => request<CIPipelineVersion[]>({ url: `/ci/pipelines/${id}/versions` }),
  saveVersion: (id: number, graph: CIGraph) =>
    request<CIPipelineVersion>({ url: `/ci/pipelines/${id}/versions`, method: 'post', data: { graphJson: graph } }),
  validate: (id: number, graph: CIGraph) =>
    request<{ valid: boolean; errors: string[] }>({ url: `/ci/pipelines/${id}/validate`, method: 'post', data: { graphJson: graph } }),
  compile: (id: number, graph: CIGraph) =>
    request<string>({ url: `/ci/pipelines/${id}/compile`, method: 'post', data: { graphJson: graph } }),

  // ---- 运行 ----
  run: (pipelineId: number, opts?: { versionId?: number; branch?: string; tag?: string }) => {
    const body: Record<string, unknown> = {}
    if (opts?.versionId) body.versionId = opts.versionId
    if (opts?.branch) body.branch = opts.branch
    if (opts?.tag) body.tag = opts.tag
    return request<CIRun>({ url: `/ci/pipelines/${pipelineId}/run`, method: 'post', data: body })
  },
  runs: (params: { pipelineId?: number; status?: string; page?: number; size?: number }) =>
    request<{ total: number; items: CIRun[] }>({ url: '/ci/runs', params }),
  runDetail: (id: number) => request<CIRunDetail>({ url: `/ci/runs/${id}` }),
  runTasks: (id: number) => request<CITaskRun[]>({ url: `/ci/runs/${id}/tasks` }),
  rerun: (id: number) => request<CIRun>({ url: `/ci/runs/${id}/rerun`, method: 'post', data: {} }),
  cancel: (id: number) => request<CIRun>({ url: `/ci/runs/${id}/cancel`, method: 'post', data: {} }),
  approvals: (id: number) => request<CIApproval[]>({ url: `/ci/runs/${id}/approvals` }),
  decideApproval: (id: number, nodeId: string, decision: 'approved' | 'rejected') =>
    request<void>({ url: `/ci/runs/${id}/approvals`, method: 'post', data: { nodeId, decision } }),

  // ---- 凭据 ----
  credentials: (projectId?: number) =>
    request<CICredential[]>({ url: '/ci/credentials', params: projectId ? { projectId } : undefined }),
  createCredential: (body: Record<string, any>) =>
    request<{ id: number; name: string; form: string }>({ url: '/ci/credentials', method: 'post', data: body }),
  updateCredential: (id: number, body: Record<string, any>) =>
    request<CICredential>({ url: `/ci/credentials/${id}`, method: 'put', data: body }),
  deleteCredential: (id: number) => request<void>({ url: `/ci/credentials/${id}`, method: 'delete' }),

  // ---- 辅助数据源 ----
  repoRefs: (url: string, credential?: string) =>
    request<{ branches: string[]; tags: string[] }>({ url: '/ci/repo/refs', params: { url, credential } }),
  k8sTargets: (namespace?: string) =>
    request<{ namespace: string; workloads: { kind: string; name: string; containers: string[] }[] }>(
      { url: '/ci/k8s/targets', params: namespace ? { namespace } : undefined }),
}

// ============ SSE / WS 地址（?token= 查询鉴权） ============

export async function taskLogsUrl(runId: number, taskId: number): Promise<string> {
  const { token } = await ciApi.streamToken()
  return `/api/ci/runs/${runId}/tasks/${taskId}/logs?token=${encodeURIComponent(token)}`
}

export async function runWsUrl(runId: number): Promise<string> {
  const { token } = await ciApi.streamToken()
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const cluster = encodeURIComponent(activeCluster.value || localStorage.getItem('kc-cluster') || '')
  return `${proto}://${window.location.host}/api/ci/ws?runId=${runId}&cluster=${cluster}&token=${encodeURIComponent(token)}`
}
