// 工作负载表单模块：Deployment/StatefulSet/DaemonSet/CronJob/Job/ReplicaSet/ReplicationController
import type { FormModule } from './types'
import { clone, textToList } from './utils'

// ---------- 探针 / 生命周期 辅助 ----------

// IntOrString：纯数字 -> number，否则按命名端口原样保留（字符串）
function toIntOrString(v: any, def: number) {
  const t = String(v ?? '').trim()
  if (t === '') return def
  return /^\d+$/.test(t) ? Number(t) : t
}

function probe(p: any, defPath: string, defPort: number) {
  if (!p) return { enabled: false, type: 'httpGet', path: '/', port: String(defPort), command: '', initialDelaySeconds: 5, periodSeconds: 10, failureThreshold: 3, timeoutSeconds: 1, successThreshold: 1 }
  let type = 'httpGet'
  let path = '/'
  let port = String(defPort)
  let command = ''
  if (p.httpGet) {
    path = p.httpGet.path || '/'
    port = p.httpGet.port != null ? String(p.httpGet.port) : String(defPort)
  } else if (p.tcpSocket) {
    type = 'tcpSocket'
    port = p.tcpSocket.port != null ? String(p.tcpSocket.port) : String(defPort)
  } else if (p.exec) {
    type = 'exec'
    // 换行分隔（每行一个参数）：按空格 join/split 会把带空格的参数（如 sh -c 脚本）拆碎
    command = (p.exec.command || []).join('\n')
  }
  return {
    enabled: true, type, path, port, command,
    initialDelaySeconds: p.initialDelaySeconds ?? 5,
    periodSeconds: p.periodSeconds ?? 10,
    failureThreshold: p.failureThreshold ?? 3,
    timeoutSeconds: p.timeoutSeconds ?? 1,
    successThreshold: p.successThreshold ?? 1,
  }
}

function buildProbe(p: any): any {
  if (!p?.enabled) return undefined
  const base: any = {}
  for (const k of ['initialDelaySeconds', 'periodSeconds', 'failureThreshold', 'timeoutSeconds', 'successThreshold']) {
    if (p[k] !== undefined && String(p[k]).trim() !== '' && Number.isFinite(Number(p[k]))) base[k] = Math.floor(Number(p[k]))
  }
  if (p.type === 'httpGet') return { ...base, httpGet: { path: p.path || '/', port: toIntOrString(p.port, 80) } }
  if (p.type === 'tcpSocket') return { ...base, tcpSocket: { port: toIntOrString(p.port, 80) } }
  return { ...base, exec: { command: textToList(p.command) } }
}

// 生命周期钩子 <-> 探针同构表单（enabled/type/path/port/command）
function lifecycleToProbe(h: any) {
  return { ...probe(h, '/', 80), initialDelaySeconds: 5, periodSeconds: 10 }
}

function probeToLifecycle(p: any): any {
  if (!p?.enabled) return undefined
  if (p.type === 'httpGet') return { httpGet: { path: p.path || '/', port: toIntOrString(p.port, 80) } }
  if (p.type === 'tcpSocket') return { tcpSocket: { port: toIntOrString(p.port, 80) } }
  return { exec: { command: textToList(p.command) } }
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
    // 每行一个参数（与 textToList 对称）；按空格 join 会在 build 时被整串当成单个参数
    command: (c.command || []).join('\n'),
    args: (c.args || []).join('\n'),
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
    // 原始 securityContext：build 时合并，保留表单未建模的字段（seccompProfile/allowPrivilegeEscalation/windowsOptions 等）
    scRaw: c.securityContext || {},
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
  // securityContext 与原始值合并：建模字段以表单为准（清空即移除），未建模字段原样保留
  const sc: any = { ...(c.scRaw || {}) }
  const setScNum = (key: string, v: any) => {
    if (v !== '' && v != null && String(v).trim() !== '' && Number.isFinite(Number(v))) sc[key] = Number(v)
    else delete sc[key]
  }
  setScNum('runAsUser', c.sc.runAsUser)
  setScNum('runAsGroup', c.sc.runAsGroup)
  if (c.sc.privileged) sc.privileged = true
  else delete sc.privileged
  if (c.sc.runAsNonRoot) sc.runAsNonRoot = true
  else delete sc.runAsNonRoot
  if (c.sc.readOnlyRootFilesystem) sc.readOnlyRootFilesystem = true
  else delete sc.readOnlyRootFilesystem
  const capAdd = (c.sc.capAdd || '').split(',').map((x: string) => x.trim()).filter(Boolean)
  const capDrop = (c.sc.capDrop || '').split(',').map((x: string) => x.trim()).filter(Boolean)
  if (capAdd.length || capDrop.length) {
    const caps: any = { ...(sc.capabilities || {}) }
    if (capAdd.length) caps.add = capAdd
    else delete caps.add
    if (capDrop.length) caps.drop = capDrop
    else delete caps.drop
    sc.capabilities = caps
  } else {
    delete sc.capabilities
  }
  if (Object.keys(sc).length) container.securityContext = sc
  return container
}

function parseVolume(v: any): any {
  let type = 'pvc'
  let pvcName = ''
  let configMapName = ''
  let secretName = ''
  let hostPath = ''
  let emptyDir: any = {}
  let raw: any = null
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
  } else if (v.emptyDir) {
    type = 'emptyDir'
    emptyDir = v.emptyDir || {} // 保留 sizeLimit / medium
  } else {
    // downwardAPI / projected / 带 items 的 secret 等未建模类型：整体保留，
    // 避免保存时被静默改写成空 emptyDir（卷源丢失）
    type = 'custom'
    raw = v
  }
  return { name: v.name || '', type, pvcName, configMapName, secretName, hostPath, emptyDir, raw }
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
    case 'emptyDir':
      out.emptyDir = { ...(v.emptyDir || {}) }
      break
    default:
      // custom：原卷源原样写回（跳过 name，已由 out.name 提供）
      if (v.raw) {
        for (const [k, val] of Object.entries(v.raw)) {
          if (k !== 'name') out[k] = clone(val)
        }
      }
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
    // 原始 affinity / Pod securityContext：build 时作为合并基底，保留表单未建模的内容
    //（多 term、matchFields、preferred 权重、podAffinity、seccompProfile 等）
    rawAffinity: tplSpec.affinity || {},
    podScRaw: tplSpec.securityContext || {},
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

// 数字守卫：非法输入（NaN/空串）回退默认值，避免把 NaN/字符串写进 K8s 对象
function numOr(v: any, def: number): number {
  if (v === '' || v == null) return def
  const n = Number(v)
  return Number.isFinite(n) ? Math.floor(n) : def
}

export function buildWorkload(base: any, d: any, kind: string): any {
  const obj = clone(base || {})
  // labels/annotations 无条件写回：用户清空后旧值必须随之移除（不能静默保留）
  obj.metadata = {
    ...(obj.metadata || {}),
    name: d.name,
    ...(d.namespace ? { namespace: d.namespace } : {}),
    labels: { ...(d.labels || {}) },
    annotations: { ...(d.annotations || {}) },
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
  tpl.metadata = { ...(tpl.metadata || {}), labels: { ...(d.labels || {}) } }
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
  tplSpec.terminationGracePeriodSeconds = numOr(d.terminationGracePeriodSeconds, 30)
  const aliases = (d.hostAliases || []).filter((a: any) => a.ip).map((a: any) => ({ ip: a.ip, hostnames: (a.hostnamesText || '').split(',').map((s: string) => s.trim()).filter(Boolean) }))
  if (aliases.length) tplSpec.hostAliases = aliases
  else delete tplSpec.hostAliases

  // Pod 级：安全上下文（与原始值合并，保留 sysctls/seLinuxOptions/fsGroupChangePolicy 等未建模字段）
  const podSc: any = { ...(d.podScRaw || {}) }
  const setPodScNum = (key: string, v: any) => {
    if (v !== '' && v != null && String(v).trim() !== '' && Number.isFinite(Number(v))) podSc[key] = Number(v)
    else delete podSc[key]
  }
  setPodScNum('runAsUser', d.podSc?.runAsUser)
  setPodScNum('runAsGroup', d.podSc?.runAsGroup)
  setPodScNum('fsGroup', d.podSc?.fsGroup)
  if (d.podSc?.runAsNonRoot) podSc.runAsNonRoot = true
  else delete podSc.runAsNonRoot
  if (Object.keys(podSc).length) tplSpec.securityContext = podSc
  else delete tplSpec.securityContext

  if (d.priorityClassName) tplSpec.priorityClassName = d.priorityClassName
  else delete tplSpec.priorityClassName

  // 调度
  if (Object.keys(d.nodeSelector || {}).length) tplSpec.nodeSelector = d.nodeSelector
  else delete tplSpec.nodeSelector
  const expressions = (d.nodeAffinityRows || [])
    .filter((r: any) => r.key)
    .map((r: any) => ({ key: r.key, operator: r.operator || 'In', values: (r.valuesText || '').split(',').map((x: string) => x.trim()).filter(Boolean) }))
  const antiLabels: any = {}
  for (const [k, v] of Object.entries(d.antiMatchLabels || {})) {
    if (k) antiLabels[k] = v
  }
  // 以原始 affinity 为基底（保留多 term / matchFields / preferred 权重 / podAffinity），
  // 只覆盖表单建模的部分：nodeAffinity required 的首个 term 与反亲和 preferred 的首条
  const stripEmpty = (o: any, key: string) => {
    if (o && typeof o[key] === 'object' && o[key] !== null && !Array.isArray(o[key]) && Object.keys(o[key]).length === 0) delete o[key]
  }
  const affinity: any = clone(d.rawAffinity || {})
  if (expressions.length) {
    const na: any = affinity.nodeAffinity || (affinity.nodeAffinity = {})
    const req: any = na.requiredDuringSchedulingIgnoredDuringExecution || (na.requiredDuringSchedulingIgnoredDuringExecution = {})
    const terms: any[] = req.nodeSelectorTerms?.length ? req.nodeSelectorTerms : []
    terms[0] = { ...(terms[0] || {}), matchExpressions: expressions }
    req.nodeSelectorTerms = terms
  } else {
    const req: any = affinity.nodeAffinity?.requiredDuringSchedulingIgnoredDuringExecution
    if (req?.nodeSelectorTerms?.length) {
      req.nodeSelectorTerms[0] = { ...req.nodeSelectorTerms[0] }
      delete req.nodeSelectorTerms[0].matchExpressions
      if (Object.keys(req.nodeSelectorTerms[0]).length === 0) req.nodeSelectorTerms.shift()
      if (!req.nodeSelectorTerms.length) delete req.nodeSelectorTerms
      stripEmpty(affinity.nodeAffinity, 'requiredDuringSchedulingIgnoredDuringExecution')
      stripEmpty(affinity, 'nodeAffinity')
    }
  }
  if (d.antiTopologyKey) {
    const pa: any = affinity.podAntiAffinity || (affinity.podAntiAffinity = {})
    const arr: any[] = pa.preferredDuringSchedulingIgnoredDuringExecution || []
    const first: any = arr[0] || {}
    const term0: any = { ...(first.podAffinityTerm || {}) }
    term0.topologyKey = d.antiTopologyKey
    if (Object.keys(antiLabels).length) term0.labelSelector = { ...(term0.labelSelector || {}), matchLabels: antiLabels }
    else if (term0.labelSelector && !term0.labelSelector.matchExpressions) delete term0.labelSelector
    const w = Number(first.weight)
    arr[0] = { ...first, weight: Number.isFinite(w) && w > 0 ? w : 100, podAffinityTerm: term0 }
    pa.preferredDuringSchedulingIgnoredDuringExecution = arr
  } else {
    const arr: any[] = affinity.podAntiAffinity?.preferredDuringSchedulingIgnoredDuringExecution
    if (arr?.length) {
      arr.shift()
      if (!arr.length) delete affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution
      stripEmpty(affinity, 'podAntiAffinity')
    }
  }
  if (Object.keys(affinity).length) tplSpec.affinity = affinity
  else delete tplSpec.affinity
  const tolerations = (d.tolerations || []).filter((t: any) => t.key && t.operator).map((t: any) => ({ key: t.key, operator: t.operator, ...(t.value ? { value: t.value } : {}), effect: t.effect }))
  if (tolerations.length) tplSpec.tolerations = tolerations

  // Job 没有 replicas / selector 字段（batch/v1 严格 schema，写入会被 API 拒绝）
  if (kind !== 'daemonsets' && kind !== 'cronjobs' && kind !== 'jobs') {
    obj.spec.replicas = numOr(d.replicas, 1)
  }
  if (['deployments', 'statefulsets', 'daemonsets', 'replicasets', 'replicationcontrollers'].includes(kind)) {
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
    obj.spec.minReadySeconds = numOr(d.minReadySeconds, 0)
    obj.spec.revisionHistoryLimit = numOr(d.revisionHistoryLimit, 10)
    obj.spec.progressDeadlineSeconds = numOr(d.progressDeadlineSeconds, 600)
  }
  if (kind === 'statefulsets') {
    if (d.serviceName) obj.spec.serviceName = d.serviceName
    obj.spec.podManagementPolicy = d.podManagementPolicy
    obj.spec.updateStrategy = { type: d.updateStrategyType }
    if (d.updateStrategyType === 'RollingUpdate') {
      obj.spec.updateStrategy.rollingUpdate = { partition: numOr(d.updatePartition, 0) }
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
    // 保留原 rollingUpdate 的 maxUnavailable/maxSurge（表单未建模，但为合法字段）
    obj.spec.updateStrategy = { ...(obj.spec.updateStrategy || {}) }
    obj.spec.updateStrategy.type = d.updateStrategyType || 'RollingUpdate'
    if (obj.spec.updateStrategy.type === 'RollingUpdate') {
      obj.spec.updateStrategy.rollingUpdate = obj.spec.updateStrategy.rollingUpdate || {}
    } else {
      delete obj.spec.updateStrategy.rollingUpdate
    }
  }
  if (kind === 'cronjobs') {
    obj.spec.schedule = d.schedule
    obj.spec.concurrencyPolicy = d.concurrencyPolicy
    if (d.suspend) obj.spec.suspend = true
    if (d.startingDeadlineSeconds !== '' && d.startingDeadlineSeconds != null && !isNaN(Number(d.startingDeadlineSeconds))) obj.spec.startingDeadlineSeconds = Number(d.startingDeadlineSeconds)
    else delete obj.spec.startingDeadlineSeconds
    if (d.timeZone) obj.spec.timeZone = d.timeZone
    else delete obj.spec.timeZone
    obj.spec.successfulJobsHistoryLimit = numOr(d.successfulJobsHistoryLimit, 3)
    obj.spec.failedJobsHistoryLimit = numOr(d.failedJobsHistoryLimit, 1)
  }
  if (kind === 'jobs') {
    obj.spec.parallelism = numOr(d.parallelism, 1)
    obj.spec.completions = numOr(d.completions, 1)
    obj.spec.backoffLimit = numOr(d.backoffLimit, 6)
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
