// ArgoCD Application 表单的纯逻辑（与 workload.ts / routes.ts 同层，便于单测）
export type ArgoSourceType = 'git' | 'helm'

/** 表单加载前：保证 metadata/spec.source/destination/syncPolicy 结构存在（表单直接绑定） */
export function parseArgoApp(obj: any): any {
  const o = obj || {}
  o.metadata = o.metadata || { name: '', namespace: '' }
  o.spec = o.spec || {}
  o.spec.source = o.spec.source || { repoURL: '', path: '', targetRevision: 'main' }
  o.spec.destination = o.spec.destination || { server: 'https://kubernetes.default.svc', namespace: '' }
  o.spec.syncPolicy = o.spec.syncPolicy || { automated: {}, selfHeal: true, prune: true }
  if (!o.spec.project) o.spec.project = 'default'
  return o
}

/**
 * 当前来源类型：以 spec.source.chart 是否存在（!== undefined）为准。
 * 不能用 chart 的真值判断——切到 Helm 时 chart 仍是空串（用户还没填 Chart 名），
 * 空串是 falsy，用真值判断会让界面立刻弹回「Git 目录」，即无法切到 Helm。
 */
export function argoSourceType(source: any): ArgoSourceType {
  return source?.chart === undefined ? 'git' : 'helm'
}

/**
 * 切换来源类型（直接改对象，调用方随后重新渲染 YAML）：
 *   - Helm：用 chart 寻址，空 path 不留在 YAML 里（非空 path 保留，便于切回 Git）
 *   - Git ：用 path 寻址，去掉 chart
 */
export function setArgoSourceType(obj: any, type: ArgoSourceType): void {
  obj.spec = obj.spec || {}
  obj.spec.source = obj.spec.source || {}
  const src = obj.spec.source
  if (type === 'helm') {
    if (src.chart === undefined) src.chart = ''
    if (!src.path) delete src.path
    return
  }
  delete src.chart
  if (src.path === undefined) src.path = ''
}

// ---------------- AppProject（spec.project）校验 ----------------
// ArgoCD 对 spec.project 的校验失败时只给一句
// "app is not allowed in project X, or the project does not exist"，
// 且 sync/health 恒为 Unknown —— 这里在保存前就把具体原因说清楚。

export interface ArgoProjectLike {
  name: string
  sourceRepos?: string[]
  destinations?: { server?: string; name?: string; namespace?: string }[]
}

export interface ArgoAppTarget {
  repoURL?: string
  /** 目标集群 server（如 https://kubernetes.default.svc） */
  server?: string
  /** 目标命名空间 */
  namespace?: string
}

/** glob 匹配：* 匹配任意字符（含 /），其余字符按字面量处理（ArgoCD 的 sourceRepos/destinations 用通配） */
export function globMatch(pattern: string, value: string): boolean {
  const p = pattern || ''
  if (p === '*') return true
  if (!p.includes('*')) return p === value
  const re = new RegExp('^' + p.split('*').map((s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$')
  return re.test(value || '')
}

/**
 * 返回「即使项目存在、ArgoCD 也会拒绝该应用」的原因（空数组 = 现有信息下没发现问题）。
 * 只在能确定时提示：项目未声明 sourceRepos/destinations（或目标用集群 name 标识）时保持沉默，
 * 避免给出误报。project 传 undefined（列表未加载）时一律不提示。
 */
export function projectIssues(project: ArgoProjectLike | undefined, target: ArgoAppTarget): string[] {
  if (!project) return []
  const issues: string[] = []
  const repos = (project.sourceRepos || []).filter(Boolean)
  const repoURL = (target.repoURL || '').trim()
  if (repoURL && repos.length > 0 && !repos.some((p) => globMatch(p, repoURL))) {
    issues.push(`AppProject「${project.name}」的 sourceRepos 未允许该仓库（当前允许：${repos.join('、')}）`)
  }
  const dests = project.destinations || []
  const ns = (target.namespace || '').trim()
  const server = (target.server || '').trim()
  const allowed = dests.some((d) => {
    // 目标是集群 name 标识时无法拿 server 比较，按允许处理（宁可不提示也不误报）
    const serverOK = !d.server || globMatch(d.server, server)
    // 项目未写 namespace（ArgoCD 语义 = 任意 ns）时同样按允许处理
    const nsOK = !d.namespace || globMatch(d.namespace, ns)
    return serverOK && nsOK
  })
  if (dests.length > 0 && !allowed) {
    issues.push(`AppProject「${project.name}」的 destinations 未允许该目标（集群 ${server || '当前集群'} / ns ${ns || '-'}）`)
  }
  return issues
}
