// 工作负载表单模块：Deployment/StatefulSet/DaemonSet/CronJob/Job/ReplicaSet/ReplicationController
import type { FormModule } from './types'
import { clone, textToList } from './utils'

// ---------- 探针 / 生命周期 辅助 ----------

function probe(p: any, defPath: string, defPort: number) {
  if (!p) return { enabled: false, type: 'httpGet', path: '/', port: defPort, command: '', initialDelaySeconds: 5, periodSeconds: 10 }
  let type = 'httpGet'
  let path = '/'
  let port = defPort
  let command = ''
  if (p.httpGet) {
    path = p.httpGet.path || '/'
    port = p.httpGet.port ? Number(p.httpGet.port) : defPort
  } else if (p.tcpSocket) {
    type = 'tcpSocket'
    port = p.tcpSocket.port ? Number(p.tcpSocket.port) : defPort
  } else if (p.exec) {
    type = 'exec'
    command = (p.exec.command || []).join(' ')
  }
  return {
    enabled: true, type, path, port, command,
    initialDelaySeconds: p.initialDelaySeconds ?? 5,
    periodSeconds: p.periodSeconds ?? 10,
  }
}

function buildProbe(p: any): any {
  if (!p?.enabled) return undefined
  const base: any = {}
  if (p.initialDelaySeconds !== undefined) base.initialDelaySeconds = p.initialDelaySeconds
  if (p.periodSeconds !== undefined) base.periodSeconds = p.periodSeconds
  if (p.type === 'httpGet') return { ...base, httpGet: { path: p.path || '/', port: p.port || 80 } }
  if (p.type === 'tcpSocket') return { ...base, tcpSocket: { port: p.port || 80 } }
  return { ...base, exec: { command: (p.command || '').split(/\s+/).filter(Boolean) } }
}

// 生命周期钩子 <-> 探针同构表单（enabled/type/path/port/command）
function lifecycleToProbe(h: any) {
  return { ...probe(h, '/', 80), initialDelaySeconds: 5, periodSeconds: 10 }
}

function probeToLifecycle(p: any): any {
  if (!p?.enabled) return undefined
  if (p.type === 'httpGet') return { httpGet: { path: p.path || '/', port: p.port || 80 } }
  if (p.type === 'tcpSocket') return { tcpSocket: { port: p.port || 80 } }
  return { exec: { command: (p.command || '').split(/\s+/).filter(Boolean) } }
}

// 环境变量：对象 -> envList 行
function parseEnv(e: any): any {
  const vf = e.valueFrom || {}
  if (vf.configMapKeyRef) return { name: e.name || '', sourceType: 'configMapKeyRef', refName: vf.configMapKeyRef.name || '', key: vf.configMapKeyRef.key || '', value: '', fieldPath: '' }
  if (vf.secretKeyRef) return { name: e.name || '', sourceType: 'secretKeyRef', refName: vf.secretKeyRef.name || '', key: vf.secretKeyRef.key || '', value: '', fieldPath: '' }
  if (vf.fieldRef) return { name: e.name || '', sourceType: 'fieldRef', refName: '', key: '', value: '', fieldPath: vf.fieldRef.fieldPath || '' }
  return { name: e.name || '', sourceType: 'value', refName: '', key: '', value: e.value ?? '', fieldPath: '' }
}

// envList 行 -> 对象
function buildEnv(e: any): any {
  if (e.sourceType === 'configMapKeyRef') return { name: e.name, valueFrom: { configMapKeyRef: { name: e.refName, key: e.key } } }
  if (e.sourceType === 'secretKeyRef') return { name: e.name, valueFrom: { secretKeyRef: { name: e.refName, key: e.key } } }
  if (e.sourceType === 'fieldRef') return { name: e.name, valueFrom: { fieldRef: { fieldPath: e.fieldPath } } }
  return { name: e.name, value: e.value }
}

function parseContainer(c: any): any {
  const res = c.resources || {}
  return {
    name: c.name || '',
    image: c.image || '',
    imagePullPolicy: c.imagePullPolicy || '',
    workingDir: c.workingDir || '',
    command: (c.command || []).join(' '),
    args: (c.args || []).join(' '),
    envList: (c.env || []).map(parseEnv),
    envFrom: (c.envFrom || []).map((e: any) => ({
      type: e.configMapRef ? 'configMap' : 'secret',
      name: e.configMapRef?.name || e.secretRef?.name || '',
      prefix: e.prefix || '',
    })),
    ports: (c.ports || []).map((p: any) => ({ name: p.name || '', containerPort: p.containerPort || 80, hostPort: p.hostPort || 0, protocol: p.protocol || 'TCP' })),
    cpuRequest: res.requests?.cpu || '',
    memoryRequest: res.requests?.memory || '',
    storageRequest: res.requests?.['ephemeral-storage'] || '',
    cpuLimit: res.limits?.cpu || '',
    memoryLimit: res.limits?.memory || '',
    storageLimit: res.limits?.['ephemeral-storage'] || '',
    readinessProbe: probe(c.readinessProbe, 'readiness', 80),
    livenessProbe: probe(c.livenessProbe, 'liveness', 80),
    startupProbe: probe(c.startupProbe, 'startup', 8080),
    postStart: lifecycleToProbe(c.lifecycle?.postStart),
    preStop: lifecycleToProbe(c.lifecycle?.preStop),
    volumeMounts: (c.volumeMounts || []).map((m: any) => ({ name: m.name || '', mountPath: m.mountPath || '', subPath: m.subPath || '', readOnly: !!m.readOnly })),
    sc: {
      runAsUser: c.securityContext?.runAsUser ?? '',
      runAsGroup: c.securityContext?.runAsGroup ?? '',
      privileged: !!c.securityContext?.privileged,
      runAsNonRoot: !!c.securityContext?.runAsNonRoot,
      readOnlyRootFilesystem: !!c.securityContext?.readOnlyRootFilesystem,
      capAdd: (c.securityContext?.capabilities?.add || []).join(', '),
      capDrop: (c.securityContext?.capabilities?.drop || []).join(', '),
    },
  }
}

function buildContainer(c: any): any {
  const container: any = { name: c.name, image: c.image }
  if (c.imagePullPolicy) container.imagePullPolicy = c.imagePullPolicy
  if (c.workingDir) container.workingDir = c.workingDir
  if (c.command) container.command = textToList(c.command)
  if (c.args) container.args = textToList(c.args)
  if ((c.envList || []).some((e: any) => e.name)) {
    container.env = c.envList.filter((e: any) => e.name).map(buildEnv)
  }
  if ((c.envFrom || []).some((e: any) => e.name)) {
    container.envFrom = c.envFrom.filter((e: any) => e.name).map((e: any) => {
      const item: any = {}
      if (e.prefix) item.prefix = e.prefix
      if (e.type === 'secret') item.secretRef = { name: e.name }
      else item.configMapRef = { name: e.name }
      return item
    })
  }
  if (c.ports.length) {
    container.ports = c.ports
      .filter((p: any) => p.containerPort)
      .map((p: any) => {
        const port: any = { containerPort: p.containerPort, protocol: p.protocol || 'TCP' }
        if (p.name) port.name = p.name
        if (p.hostPort) port.hostPort = p.hostPort
        return port
      })
  }
  const resources: any = {}
  if (c.cpuRequest) resources.requests = { ...(resources.requests || {}), cpu: c.cpuRequest }
  if (c.memoryRequest) resources.requests = { ...(resources.requests || {}), memory: c.memoryRequest }
  if (c.storageRequest) resources.requests = { ...(resources.requests || {}), 'ephemeral-storage': c.storageRequest }
  if (c.cpuLimit) resources.limits = { ...(resources.limits || {}), cpu: c.cpuLimit }
  if (c.memoryLimit) resources.limits = { ...(resources.limits || {}), memory: c.memoryLimit }
  if (c.storageLimit) resources.limits = { ...(resources.limits || {}), 'ephemeral-storage': c.storageLimit }
  if (Object.keys(resources).length) container.resources = resources
  const rp = buildProbe(c.readinessProbe)
  const lp = buildProbe(c.livenessProbe)
  const sp = buildProbe(c.startupProbe)
  if (rp) container.readinessProbe = rp
  if (lp) container.livenessProbe = lp
  if (sp) container.startupProbe = sp
  const postStart = probeToLifecycle(c.postStart)
  const preStop = probeToLifecycle(c.preStop)
  if (postStart || preStop) {
    container.lifecycle = {}
    if (postStart) container.lifecycle.postStart = postStart
    if (preStop) container.lifecycle.preStop = preStop
  }
  if (c.volumeMounts.length) {
    container.volumeMounts = c.volumeMounts
      .filter((m: any) => m.name && m.mountPath)
      .map((m: any) => ({ name: m.name, mountPath: m.mountPath, ...(m.subPath ? { subPath: m.subPath } : {}), ...(m.readOnly ? { readOnly: true } : {}) }))
  }
  const sc: any = {}
  if (c.sc.runAsUser !== '' && c.sc.runAsUser != null && !isNaN(Number(c.sc.runAsUser))) sc.runAsUser = Number(c.sc.runAsUser)
  if (c.sc.runAsGroup !== '' && c.sc.runAsGroup != null && !isNaN(Number(c.sc.runAsGroup))) sc.runAsGroup = Number(c.sc.runAsGroup)
  if (c.sc.privileged) sc.privileged = true
  if (c.sc.runAsNonRoot) sc.runAsNonRoot = true
  if (c.sc.readOnlyRootFilesystem) sc.readOnlyRootFilesystem = true
  const capAdd = (c.sc.capAdd || '').split(',').map((s: string) => s.trim()).filter(Boolean)
  const capDrop = (c.sc.capDrop || '').split(',').map((s: string) => s.trim()).filter(Boolean)
  if (capAdd.length) sc.capabilities = { ...(sc.capabilities || {}), add: capAdd }
  if (capDrop.length) sc.capabilities = { ...(sc.capabilities || {}), drop: capDrop }
  if (Object.keys(sc).length) container.securityContext = sc
  return container
}

function parseVolume(v: any): any {
  let type = 'pvc'
  let pvcName = ''
  let configMapName = ''
  let secretName = ''
  let hostPath = ''
  if (v.persistentVolumeClaim) {
    pvcName = v.persistentVolumeClaim.claimName || ''
  } else if (v.configMap) {
    type = 'configMap'
    configMapName = v.configMap.name || ''
  } else if (v.secret) {
    type = 'secret'
    secretName = v.secret.secretName || ''
  } else if (v.hostPath) {
    type = 'hostPath'
    hostPath = v.hostPath.path || ''
  } else {
    type = 'emptyDir'
  }
  return { name: v.name || '', type, pvcName, configMapName, secretName, hostPath }
}

function buildVolume(v: any): any {
  const out: any = { name: v.name }
  switch (v.type) {
    case 'pvc':
      if (v.pvcName) out.persistentVolumeClaim = { claimName: v.pvcName }
      break
    case 'configMap':
      if (v.configMapName) out.configMap = { name: v.configMapName }
      break
    case 'secret':
      if (v.secretName) out.secret = { secretName: v.secretName }
      break
    case 'hostPath':
      if (v.hostPath) out.hostPath = { path: v.hostPath }
      break
    default:
      out.emptyDir = {}
  }
  return out
}

export function parseWorkload(obj: any, kind: string): any {
  const spec = obj.spec || {}
  const template = kind === 'cronjobs' ? spec.jobTemplate?.spec?.template || {} : spec.template || {}
  const tplSpec = template.spec || {}
  const affinity = tplSpec.affinity || {}
  const nodeTerm = affinity.nodeAffinity?.requiredDuringSchedulingIgnoredDuringExecution?.nodeSelectorTerms?.[0] || {}
  const antiPreferred = affinity.podAntiAffinity?.preferredDuringSchedulingIgnoredDuringExecution?.[0] || {}
  return {
    name: obj.metadata?.name || '',
    namespace: obj.metadata?.namespace || '',
    labels: obj.metadata?.labels || {},
    annotations: obj.metadata?.annotations || {},
    replicas: spec.replicas ?? 1,
    selector: spec.selector?.matchLabels || {},
    serviceAccountName: tplSpec.serviceAccountName || '',
    containers: (tplSpec.containers || []).map(parseContainer),
    initContainers: (tplSpec.initContainers || []).map(parseContainer),
    volumes: (tplSpec.volumes || []).map(parseVolume),
    // Pod 级：镜像与凭证
    imagePullSecrets: (tplSpec.imagePullSecrets || []).map((s: any) => ({ name: s.name || '' })),
    // Pod 级：主机与网络
    hostNetwork: !!tplSpec.hostNetwork,
    hostPID: !!tplSpec.hostPID,
    hostIPC: !!tplSpec.hostIPC,
    dnsPolicy: tplSpec.dnsPolicy || '',
    terminationGracePeriodSeconds: tplSpec.terminationGracePeriodSeconds ?? 30,
    hostAliases: (tplSpec.hostAliases || []).map((a: any) => ({ ip: a.ip || '', hostnamesText: (a.hostnames || []).join(', ') })),
    // Pod 级：安全上下文
    podSc: {
      runAsUser: tplSpec.securityContext?.runAsUser ?? '',
      runAsGroup: tplSpec.securityContext?.runAsGroup ?? '',
      fsGroup: tplSpec.securityContext?.fsGroup ?? '',
      runAsNonRoot: !!tplSpec.securityContext?.runAsNonRoot,
    },
    priorityClassName: tplSpec.priorityClassName || '',
    // 调度
    nodeSelector: tplSpec.nodeSelector || {},
    nodeAffinityRows: (nodeTerm.matchExpressions || []).map((m: any) => ({ key: m.key || '', operator: m.operator || 'In', valuesText: (m.values || []).join(', ') })),
    antiTopologyKey: antiPreferred.podAffinityTerm?.topologyKey || '',
    antiMatchLabels: antiPreferred.podAffinityTerm?.labelSelector?.matchLabels || {},
    tolerations: (tplSpec.tolerations || []).map((t: any) => ({ key: t.key || '', operator: t.operator || 'Exists', value: t.value || '', effect: t.effect || 'NoSchedule' })),
    strategy: {
      type: spec.strategy?.type || 'RollingUpdate',
      maxUnavailable: spec.strategy?.rollingUpdate?.maxUnavailable || '25%',
      maxSurge: spec.strategy?.rollingUpdate?.maxSurge || '25%',
    },
    restartPolicy: tplSpec.restartPolicy || (kind === 'jobs' || kind === 'cronjobs' ? 'Never' : 'Always'),
    // Deployment 专属
    minReadySeconds: spec.minReadySeconds ?? 0,
    revisionHistoryLimit: spec.revisionHistoryLimit ?? 10,
    progressDeadlineSeconds: spec.progressDeadlineSeconds ?? 600,
    // StatefulSet / DaemonSet 专属
    serviceName: spec.serviceName || '',
    podManagementPolicy: spec.podManagementPolicy || 'OrderedReady',
    updateStrategyType: spec.updateStrategy?.type || 'RollingUpdate',
    updatePartition: spec.updateStrategy?.rollingUpdate?.partition ?? 0,
    volumeClaimTemplates: (spec.volumeClaimTemplates || []).map((v: any) => ({
      name: v.metadata?.name || '',
      storageClass: v.spec?.storageClassName || '',
      accessModesText: (v.spec?.accessModes || []).join(', '),
      size: v.spec?.resources?.requests?.storage || '',
    })),
    // CronJob 专属
    schedule: spec.schedule || '',
    concurrencyPolicy: spec.concurrencyPolicy || 'Allow',
    suspend: !!spec.suspend,
    startingDeadlineSeconds: spec.startingDeadlineSeconds ?? '',
    timeZone: spec.timeZone || '',
    successfulJobsHistoryLimit: spec.successfulJobsHistoryLimit ?? 3,
    failedJobsHistoryLimit: spec.failedJobsHistoryLimit ?? 1,
    // Job 专属
    parallelism: spec.parallelism ?? 1,
    completions: spec.completions ?? 1,
    backoffLimit: spec.backoffLimit ?? 6,
    ttlSecondsAfterFinished: spec.ttlSecondsAfterFinished ?? '',
    activeDeadlineSeconds: spec.activeDeadlineSeconds ?? '',
    completionMode: spec.completionMode || 'NonIndexed',
  }
}

// ---------- 表单数据 -> 对象（基于原对象合并，保留未覆盖字段） ----------

export function buildWorkload(base: any, d: any, kind: string): any {
  const obj = clone(base || {})
  obj.metadata = {
    ...(obj.metadata || {}),
    name: d.name,
    ...(d.namespace ? { namespace: d.namespace } : {}),
    ...(Object.keys(d.labels || {}).length ? { labels: d.labels } : {}),
    ...(Object.keys(d.annotations || {}).length ? { annotations: d.annotations } : {}),
  }
  obj.spec = obj.spec || {}

  const tplPath = kind === 'cronjobs' ? ['spec', 'jobTemplate', 'spec', 'template'] : ['spec', 'template']
  let tpl = obj
  for (const p of tplPath) {
    if (!tpl[p]) tpl[p] = {}
    tpl = tpl[p]
  }
  const tplSpec = (tpl.spec = tpl.spec || {})
  const containers = d.containers.filter((c: any) => c.name && c.image).map(buildContainer)
  if (containers.length) tplSpec.containers = containers
  const initContainers = (d.initContainers || []).filter((c: any) => c.name && c.image).map(buildContainer)
  if (initContainers.length) tplSpec.initContainers = initContainers
  else delete tplSpec.initContainers
  tpl.metadata = { ...(tpl.metadata || {}), ...(Object.keys(d.labels || {}).length ? { labels: d.labels } : {}) }
  if (d.serviceAccountName) tplSpec.serviceAccountName = d.serviceAccountName
  const volumes = (d.volumes || []).filter((v: any) => v.name).map(buildVolume)
  if (volumes.length) tplSpec.volumes = volumes
  if (d.restartPolicy) tplSpec.restartPolicy = d.restartPolicy

  // Pod 级：镜像与凭证
  const ips = (d.imagePullSecrets || []).filter((s: any) => s.name).map((s: any) => ({ name: s.name }))
  if (ips.length) tplSpec.imagePullSecrets = ips
  else delete tplSpec.imagePullSecrets

  // Pod 级：主机与网络
  if (d.hostNetwork) tplSpec.hostNetwork = true
  else delete tplSpec.hostNetwork
  if (d.hostPID) tplSpec.hostPID = true
  else delete tplSpec.hostPID
  if (d.hostIPC) tplSpec.hostIPC = true
  else delete tplSpec.hostIPC
  if (d.dnsPolicy) tplSpec.dnsPolicy = d.dnsPolicy
  else delete tplSpec.dnsPolicy
  tplSpec.terminationGracePeriodSeconds = Number(d.terminationGracePeriodSeconds ?? 30)
  const aliases = (d.hostAliases || []).filter((a: any) => a.ip).map((a: any) => ({ ip: a.ip, hostnames: (a.hostnamesText || '').split(',').map((s: string) => s.trim()).filter(Boolean) }))
  if (aliases.length) tplSpec.hostAliases = aliases
  else delete tplSpec.hostAliases

  // Pod 级：安全上下文
  const podSc: any = {}
  if (d.podSc?.runAsUser !== '' && d.podSc?.runAsUser != null && !isNaN(Number(d.podSc.runAsUser))) podSc.runAsUser = Number(d.podSc.runAsUser)
  if (d.podSc?.runAsGroup !== '' && d.podSc?.runAsGroup != null && !isNaN(Number(d.podSc.runAsGroup))) podSc.runAsGroup = Number(d.podSc.runAsGroup)
  if (d.podSc?.fsGroup !== '' && d.podSc?.fsGroup != null && !isNaN(Number(d.podSc.fsGroup))) podSc.fsGroup = Number(d.podSc.fsGroup)
  if (d.podSc?.runAsNonRoot) podSc.runAsNonRoot = true
  if (Object.keys(podSc).length) tplSpec.securityContext = podSc
  else delete tplSpec.securityContext

  if (d.priorityClassName) tplSpec.priorityClassName = d.priorityClassName
  else delete tplSpec.priorityClassName

  // 调度
  if (Object.keys(d.nodeSelector || {}).length) tplSpec.nodeSelector = d.nodeSelector
  else delete tplSpec.nodeSelector
  const expressions = (d.nodeAffinityRows || [])
    .filter((r: any) => r.key)
    .map((r: any) => ({ key: r.key, operator: r.operator || 'In', values: (r.valuesText || '').split(',').map((s: string) => s.trim()).filter(Boolean) }))
  const antiLabels: any = {}
  for (const [k, v] of Object.entries(d.antiMatchLabels || {})) {
    if (k) antiLabels[k] = v
  }
  if (expressions.length || d.antiTopologyKey) {
    const affinity: any = {}
    if (expressions.length) {
      affinity.nodeAffinity = {
        requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{ matchExpressions: expressions }] },
      }
    }
    if (d.antiTopologyKey) {
      affinity.podAntiAffinity = {
        preferredDuringSchedulingIgnoredDuringExecution: [
          { weight: 100, podAffinityTerm: { topologyKey: d.antiTopologyKey, ...(Object.keys(antiLabels).length ? { labelSelector: { matchLabels: antiLabels } } : {}) } },
        ],
      }
    }
    tplSpec.affinity = affinity
  } else {
    delete tplSpec.affinity
  }
  const tolerations = (d.tolerations || []).filter((t: any) => t.key && t.operator).map((t: any) => ({ key: t.key, operator: t.operator, ...(t.value ? { value: t.value } : {}), effect: t.effect }))
  if (tolerations.length) tplSpec.tolerations = tolerations

  if (kind !== 'daemonsets' && kind !== 'cronjobs') {
    obj.spec.replicas = d.replicas
  }
  if (['deployments', 'statefulsets', 'daemonsets', 'jobs', 'replicasets', 'replicationcontrollers'].includes(kind)) {
    if (Object.keys(d.selector || {}).length) {
      obj.spec.selector = { matchLabels: d.selector }
    }
  }
  if (kind === 'deployments') {
    obj.spec.strategy = obj.spec.strategy || {}
    obj.spec.strategy.type = d.strategy.type
    if (d.strategy.type === 'RollingUpdate') {
      obj.spec.strategy.rollingUpdate = {
        maxUnavailable: d.strategy.maxUnavailable || '25%',
        maxSurge: d.strategy.maxSurge || '25%',
      }
    } else {
      delete obj.spec.strategy.rollingUpdate
    }
    obj.spec.minReadySeconds = Number(d.minReadySeconds ?? 0)
    obj.spec.revisionHistoryLimit = Number(d.revisionHistoryLimit ?? 10)
    obj.spec.progressDeadlineSeconds = Number(d.progressDeadlineSeconds ?? 600)
  }
  if (kind === 'statefulsets') {
    if (d.serviceName) obj.spec.serviceName = d.serviceName
    obj.spec.podManagementPolicy = d.podManagementPolicy
    obj.spec.updateStrategy = { type: d.updateStrategyType }
    if (d.updateStrategyType === 'RollingUpdate') {
      obj.spec.updateStrategy.rollingUpdate = { partition: Number(d.updatePartition ?? 0) }
    }
    const vcts = (d.volumeClaimTemplates || []).filter((v: any) => v.name).map((v: any) => ({
      apiVersion: 'v1',
      kind: 'PersistentVolumeClaim',
      metadata: { name: v.name },
      spec: {
        ...(v.storageClass ? { storageClassName: v.storageClass } : {}),
        accessModes: (v.accessModesText || 'ReadWriteOnce').split(',').map((s: string) => s.trim()).filter(Boolean),
        resources: { requests: { storage: v.size || '1Gi' } },
      },
    }))
    if (vcts.length) obj.spec.volumeClaimTemplates = vcts
    else delete obj.spec.volumeClaimTemplates
  }
  if (kind === 'daemonsets') {
    obj.spec.updateStrategy = { type: d.updateStrategyType || 'RollingUpdate' }
  }
  if (kind === 'cronjobs') {
    obj.spec.schedule = d.schedule
    obj.spec.concurrencyPolicy = d.concurrencyPolicy
    if (d.suspend) obj.spec.suspend = true
    if (d.startingDeadlineSeconds !== '' && d.startingDeadlineSeconds != null && !isNaN(Number(d.startingDeadlineSeconds))) obj.spec.startingDeadlineSeconds = Number(d.startingDeadlineSeconds)
    else delete obj.spec.startingDeadlineSeconds
    if (d.timeZone) obj.spec.timeZone = d.timeZone
    else delete obj.spec.timeZone
    obj.spec.successfulJobsHistoryLimit = d.successfulJobsHistoryLimit
    obj.spec.failedJobsHistoryLimit = d.failedJobsHistoryLimit
  }
  if (kind === 'jobs') {
    obj.spec.parallelism = d.parallelism
    obj.spec.completions = d.completions
    obj.spec.backoffLimit = d.backoffLimit
    if (d.ttlSecondsAfterFinished !== '' && d.ttlSecondsAfterFinished != null && !isNaN(Number(d.ttlSecondsAfterFinished))) obj.spec.ttlSecondsAfterFinished = Number(d.ttlSecondsAfterFinished)
    else delete obj.spec.ttlSecondsAfterFinished
    if (d.activeDeadlineSeconds !== '' && d.activeDeadlineSeconds != null && !isNaN(Number(d.activeDeadlineSeconds))) obj.spec.activeDeadlineSeconds = Number(d.activeDeadlineSeconds)
    else delete obj.spec.activeDeadlineSeconds
    if (d.completionMode) obj.spec.completionMode = d.completionMode
    else delete obj.spec.completionMode
  }
  return obj
}

// 新建时生成基础模板
export function workloadTemplate(kind: string, namespace: string): any {
  const title = kind === 'deployments' ? 'Deployment' : kind === 'statefulsets' ? 'StatefulSet' : kind === 'daemonsets' ? 'DaemonSet' : kind === 'cronjobs' ? 'CronJob' : kind === 'jobs' ? 'Job' : kind === 'replicasets' ? 'ReplicaSet' : 'ReplicationController'
  const name = 'my-app'
  const group = kind === 'cronjobs' || kind === 'jobs' ? 'batch/v1' : 'apps/v1'
  const obj: any = {
    apiVersion: group,
    kind: title,
    metadata: { name, namespace: namespace || 'default', labels: { app: name } },
  }
  if (kind === 'cronjobs') {
    obj.spec = {
      schedule: '*/5 * * * *',
      concurrencyPolicy: 'Allow',
      jobTemplate: {
        spec: {
          template: { metadata: { labels: { app: name } }, spec: { restartPolicy: 'Never', containers: [{ name, image: 'busybox:1.36', command: ['echo', 'hello'] }] } },
        },
      },
    }
    return obj
  }
  obj.spec = {
    ...(kind === 'jobs' ? { parallelism: 1, completions: 1, backoffLimit: 6 } : { replicas: 1 }),
  }
  if (kind !== 'daemonsets' && kind !== 'jobs') {
    obj.spec.selector = { matchLabels: { app: name } }
  }
  if (kind === 'jobs') {
    obj.spec.template = { metadata: { labels: { app: name } }, spec: { restartPolicy: 'Never', containers: [{ name, image: 'busybox:1.36', command: ['echo', 'hello'] }] } }
  } else {
    obj.spec.template = { metadata: { labels: { app: name } }, spec: { containers: [{ name, image: 'nginx:1.27' }] } }
  }
  return obj
}
