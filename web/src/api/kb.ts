// KubeBlocks 数据库集群 API（权限按集群所在命名空间判定，后端逐请求校验）
import { request } from './http'

export interface KBVolume {
  name: string; accessModes?: string[]; optional?: boolean; snapshotable?: boolean
}
export interface KBCompOption { compDef: string; serviceVersion: string }
export interface KBTemplateComponent {
  name: string; compDef: string; compDefPattern?: string; compDefs?: KBCompOption[]
  replicasMin: number; replicasMax: number
  serviceVersion: string; volumes: KBVolume[]; hasExporter: boolean
  configs?: string[]; highAvailability?: boolean
}
// 分片型拓扑（redis cluster / mongodb sharding）：模板在 ShardingDefinition 里，
// 分片数范围取自其 spec.shardsLimit（apiserver 不校验，后端创建时校验）
export interface KBShardingTemplate {
  name: string; shardingDef: string; shardsMin: number; shardsMax: number
  components: KBTemplateComponent[]
}
export interface KBTopology {
  name: string; default?: boolean; components: KBTemplateComponent[]
  shardings?: KBShardingTemplate[]
}
export interface KBEngine { name: string; topologies: KBTopology[] }

export interface KBComponent {
  name: string; compDef: string; serviceVersion: string
  replicas: number; readyReplicas: number; phase: string; message: string
  headlessService: string; instanceLabel: string
  cpuLimit: string; memLimit: string; storage: string; storageClass: string
}
export interface KBCluster {
  namespace: string; name: string; clusterDef: string; engine: string; topology: string
  phase: string; terminationPolicy: string; message: string; createdAt: string
  components: KBComponent[]
  conditions?: KBCondition[]
  shardings?: KBSharding[]
}
export interface KBOps {
  name: string; type: string; component: string
  phase: string; progress: string; message: string; createdAt: string
}
export interface KBConfigMapRef { name: string; keys: string[] }
export interface KBAccount { name: string; type: string; keys: string[] }

export interface KBContainer { name: string; ready: boolean; restarts: number; image: string }
export interface KBPod {
  name: string; component: string; role: string; node: string; phase: string
  podIP: string; ready: string; restarts: number; startTime: string; containers: KBContainer[]
}
// 镜像自带 CLI 的自动登录入口（命令在容器内展开 $ENV，明文凭证不经过控制台）
export interface KBCLIEntry { component: string; pod: string; container: string; command: string; role?: string }

export interface KBCondition { type: string; status: string; reason?: string; message?: string; lastTransition?: string }
export interface KBSharding {
  name: string; shardingDef: string; shards: number; phase?: string; message?: string; upToDate?: boolean
  components?: KBComponent[] // 分片成员（仅详情接口返回）
}
// KBCluster 扩展字段：conditions 排障时间线；shardings 分片拓扑（成员组件挂在各分片下）
export interface KBClusterEx { conditions?: KBCondition[]; shardings?: KBSharding[] }

export interface KBOpsVolume { name: string; storage: string }
export interface KBOpsDetail {
  name: string; namespace: string; type: string; clusterName: string
  phase: string; progress: string; message: string
  createdAt: string; startAt?: string; completedAt?: string
  cancelable: boolean
  lastConfiguration?: Record<string, any>
  conditions: KBCondition[]
}
// 组件可升级版本链（来自 ComponentVersion.compatibilityRules）
export interface KBComponentVersions { component: string; compDef: string; current: string; candidates: string[] }

// 连接信息（密码永不回显，只指出所在 Secret）
export interface KBConnPort { name: string; port: number; nodePort?: number }
export interface KBConnService { component: string; service: string; dns: string; type: string; clusterIP?: string; ports: KBConnPort[] }
export interface KBConnAccount { component: string; secret: string; username?: string }
export interface KBConnection { services: KBConnService[]; accounts: KBConnAccount[]; note: string }

export interface KBEvent { type: string; reason: string; object: string; message: string; count: number; lastTimestamp: string }

// 参数管理（parameters.kubeblocks.io）：schema + 当前值
export interface KBParam {
  name: string; type?: string; default?: string; enum?: string[]; min?: string; max?: string
  description?: string; dynamic: boolean; immutable: boolean; current: string; inSchema: boolean
}
export interface KBConfigFile { name: string; pdName?: string; reload?: string; params: KBParam[] }
export interface KBConfigSchema { cluster: string; component: string; compDef: string; files: KBConfigFile[]; note?: string }

// 备份恢复（dataprotection.kubeblocks.io）
export interface KBBackupMethod { name: string; snapshot: boolean }
export interface KBBackupPolicyView {
  component: string; name: string; repoName: string; phase?: string
  methods: KBBackupMethod[]; credSecret?: string
}
export interface KBScheduleItem { name: string; method: string; cronExpression: string; enabled: boolean; retention?: string }
export interface KBScheduleView { component: string; name: string; policyName: string; phase?: string; items: KBScheduleItem[] }
export interface KBBackupView {
  name: string; namespace: string; method: string; policy: string; phase: string
  failureReason?: string; repo?: string; totalSize?: string; retention?: string
  createdAt: string; startedAt?: string; completedAt?: string; expiration?: string
}
export interface KBBackupAction {
  name: string; phase?: string; targetPod?: string; totalSize?: string
  startAt?: string; completedAt?: string; failureReason?: string
}
export interface KBBackupDetail extends KBBackupView {
  actions?: KBBackupAction[]; timeRange?: Record<string, any>; volumeSnapshots?: number
}
export interface KBRepoView {
  name: string; phase: string; default: boolean; provider?: string
  accessMethod?: string; pathPrefix?: string; storageClass?: string; createdAt: string
}
export interface KBRestoreView {
  name: string; backup?: string; phase: string; message?: string
  duration?: string; createdAt: string; completedAt?: string
}

export interface KBCreateComponent {
  name: string; compDef: string; replicas: number; serviceVersion: string
  cpuRequest?: string; memRequest?: string; cpuLimit?: string; memLimit?: string
  storage?: string; storageClass?: string
}

// 集群归属引擎的判定放在 utils/kb-engines（纯函数，可单测），这里转出保持既有调用点不变
export { kbMatchesEngine } from '../utils/kb-engines'

// Kafka：topic / 消费组（后端进 broker 容器跑 kafka-*.sh）
export interface KBKafkaPartition { partition: number; leader: string; replicas: string[]; isr: string[]; offline: boolean }
export interface KBKafkaTopic {
  name: string; partitionCount: number; replicationFactor: string
  configs: Record<string, string>; partitions: KBKafkaPartition[]
}
export interface KBKafkaGroup {
  group: string; state: string; members: number; topic: string
  partition: number; currentOffset: string; logEndOffset: string; lag: string
}
// Elasticsearch：健康 / 索引 / 查询（后端进 es 容器 curl 127.0.0.1:9200）
export interface KBESHealth {
  status: string; cluster: string; nodes: number; dataNodes: number
  activeShards: number; relocatingShards: number; unassignedShards: number; activeShardsPercent: string
}
export interface KBESIndex {
  index: string; health: string; status: string; uuid: string; primaries: number; replicas: number
  docsCount: number; docsDeleted: number; storeSize: string; segmentsCount: number
}
export interface KBESIndexDetail { settings: Record<string, string>; aliases: string[]; fields: { name: string; type: string }[] }
export interface KBESHits { total: string; tookMs: number; maxSize: number; docs: Record<string, any>[] }

export const kbApi = {
  engines: () => request<{ installed: boolean; engines: KBEngine[] }>({ url: '/kb/engines' }),
  clusters: (namespace?: string) =>
    request<KBCluster[]>({ url: '/kb/clusters', params: namespace ? { namespace } : {} }),
  cluster: (namespace: string, name: string) =>
    request<KBCluster>({ url: `/kb/clusters/${namespace}/${name}` }),
  create: (namespace: string, body: Record<string, any>) =>
    request({ url: '/kb/clusters', method: 'post', params: { namespace }, data: body }),
  remove: (namespace: string, name: string) =>
    request({ url: `/kb/clusters/${namespace}/${name}`, method: 'delete' }),
  ops: (namespace: string, name: string) =>
    request<KBOps[]>({ url: `/kb/clusters/${namespace}/${name}/ops` }),
  submitOps: (namespace: string, name: string, body: Record<string, any>) =>
    request<{ name: string; phase?: string; message?: string }>({ url: `/kb/clusters/${namespace}/${name}/ops`, method: 'post', data: body }),
  opsDetail: (namespace: string, name: string, ops: string) =>
    request<KBOpsDetail>({ url: `/kb/clusters/${namespace}/${name}/ops/${encodeURIComponent(ops)}` }),
  cancelOps: (namespace: string, name: string, ops: string) =>
    request({ url: `/kb/clusters/${namespace}/${name}/ops/${encodeURIComponent(ops)}/cancel`, method: 'post' }),
  versions: (namespace: string, name: string) =>
    request<KBComponentVersions[]>({ url: `/kb/clusters/${namespace}/${name}/versions` }),
  connection: (namespace: string, name: string) =>
    request<KBConnection>({ url: `/kb/clusters/${namespace}/${name}/connection` }),
  events: (namespace: string, name: string) =>
    request<KBEvent[]>({ url: `/kb/clusters/${namespace}/${name}/events` }),
  configSchema: (namespace: string, name: string, component: string) =>
    request<KBConfigSchema>({ url: `/kb/clusters/${namespace}/${name}/config-schema/${encodeURIComponent(component)}` }),
  submitReconfigure: (namespace: string, name: string, component: string, parameters: Record<string, string>) =>
    request<{ name: string }>({ url: `/kb/clusters/${namespace}/${name}/config-schema/${encodeURIComponent(component)}`, method: 'post', data: { parameters } }),

  // ---- 备份恢复（dataprotection.kubeblocks.io）----
  backupRepos: () => request<KBRepoView[]>({ url: '/kb/repos' }),
  backupPolicies: (namespace: string, name: string) =>
    request<KBBackupPolicyView[]>({ url: `/kb/clusters/${namespace}/${name}/backup/policies` }),
  backupSchedule: (namespace: string, name: string) =>
    request<KBScheduleView[]>({ url: `/kb/clusters/${namespace}/${name}/backup/schedule` }),
  updateBackupSchedule: (namespace: string, name: string, body: { component: string; items: KBScheduleItem[] }) =>
    request({ url: `/kb/clusters/${namespace}/${name}/backup/schedule`, method: 'put', data: body }),
  backups: (namespace: string, name: string) =>
    request<KBBackupView[]>({ url: `/kb/clusters/${namespace}/${name}/backups` }),
  backupDetail: (namespace: string, name: string, backup: string) =>
    request<KBBackupDetail>({ url: `/kb/clusters/${namespace}/${name}/backups/${encodeURIComponent(backup)}` }),
  createBackup: (namespace: string, name: string, body: { component: string; method?: string; retention?: string }) =>
    request<{ name: string }>({ url: `/kb/clusters/${namespace}/${name}/backups`, method: 'post', data: body }),
  deleteBackup: (namespace: string, name: string, backup: string) =>
    request({ url: `/kb/clusters/${namespace}/${name}/backups/${encodeURIComponent(backup)}`, method: 'delete' }),
  restoreCreate: (namespace: string, name: string, body: { name: string; component: string; backupName: string; backupNamespace?: string; volumeRestorePolicy?: string; terminationPolicy?: string }) =>
    request<{ cluster: string }>({ url: `/kb/clusters/${namespace}/${name}/backup/restore`, method: 'post', data: body }),
  restores: (namespace: string, name: string) =>
    request<KBRestoreView[]>({ url: `/kb/clusters/${namespace}/${name}/backup/restores` }),
  rebuild: (namespace: string, name: string, body: { component: string; backupName: string; inPlace: boolean }) =>
    request<{ name: string }>({ url: `/kb/clusters/${namespace}/${name}/backup/rebuild`, method: 'post', data: body }),
  monitorAccess: (namespace: string, name: string) =>
    request<{ created: string[] }>({ url: `/kb/clusters/${namespace}/${name}/monitor-access`, method: 'post' }),
  configMaps: (namespace: string, name: string) =>
    request<KBConfigMapRef[]>({ url: `/kb/clusters/${namespace}/${name}/configmaps` }),
  getConfigMap: (namespace: string, name: string, cm: string) =>
    request<{ name: string; data: Record<string, string> }>({ url: `/kb/clusters/${namespace}/${name}/configmaps/${cm}` }),
  putConfigMap: (namespace: string, name: string, cm: string, data: Record<string, string>) =>
    request<{ ok: boolean; note: string }>({ url: `/kb/clusters/${namespace}/${name}/configmaps/${cm}`, method: 'put', data: { data } }),
  accounts: (namespace: string, name: string) =>
    request<KBAccount[]>({ url: `/kb/clusters/${namespace}/${name}/accounts` }),
  pods: (namespace: string, name: string) =>
    request<KBPod[]>({ url: `/kb/clusters/${namespace}/${name}/pods` }),
  cli: (namespace: string, name: string) =>
    request<{ engine: string; entries: KBCLIEntry[] }>({ url: `/kb/clusters/${namespace}/${name}/cli` }),

  // ---- Kafka 管理（进 broker 容器执行官方脚本）----
  kafkaTopics: (namespace: string, name: string) =>
    request<KBKafkaTopic[]>({ url: `/kb/clusters/${namespace}/${name}/kafka/topics` }),
  kafkaCreateTopic: (namespace: string, name: string, body: Record<string, any>) =>
    request<{ result: string }>({ url: `/kb/clusters/${namespace}/${name}/kafka/topics`, method: 'post', data: body }),
  kafkaAlterTopic: (namespace: string, name: string, topic: string, body: { set?: Record<string, string>; unset?: string[] }) =>
    request<{ result: string }>({ url: `/kb/clusters/${namespace}/${name}/kafka/topics/${encodeURIComponent(topic)}`, method: 'put', data: body }),
  kafkaDeleteTopic: (namespace: string, name: string, topic: string) =>
    request<{ result: string }>({ url: `/kb/clusters/${namespace}/${name}/kafka/topics/${encodeURIComponent(topic)}`, method: 'delete' }),
  kafkaGroups: (namespace: string, name: string) =>
    request<KBKafkaGroup[]>({ url: `/kb/clusters/${namespace}/${name}/kafka/groups` }),
  kafkaConsume: (namespace: string, name: string, body: Record<string, any>) =>
    request<{ output: string }>({ url: `/kb/clusters/${namespace}/${name}/kafka/consume`, method: 'post', data: body }),

  // ---- Elasticsearch 索引与查询（进 es 容器 curl）----
  esHealth: (namespace: string, name: string) =>
    request<KBESHealth>({ url: `/kb/clusters/${namespace}/${name}/es/health` }),
  esIndices: (namespace: string, name: string) =>
    request<KBESIndex[]>({ url: `/kb/clusters/${namespace}/${name}/es/indices` }),
  esIndex: (namespace: string, name: string, index: string) =>
    request<KBESIndexDetail>({ url: `/kb/clusters/${namespace}/${name}/es/indices/${encodeURIComponent(index)}` }),
  esDeleteIndex: (namespace: string, name: string, index: string) =>
    request<{ result: string }>({ url: `/kb/clusters/${namespace}/${name}/es/indices/${encodeURIComponent(index)}`, method: 'delete' }),
  esSearch: (namespace: string, name: string, body: { index: string; size: number; body: string }) =>
    request<{ hits: KBESHits; raw: string }>({ url: `/kb/clusters/${namespace}/${name}/es/search`, method: 'post', data: body }),
}

// 指标面板定义较多，单独放 kbmetrics.ts
export * from './kbmetrics'

