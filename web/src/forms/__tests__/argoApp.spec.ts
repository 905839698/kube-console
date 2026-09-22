// ArgoCD Application 表单逻辑测试：守护来源类型切换（chart 空串也必须算 Helm）
// 与 parse 的结构兜底（表单直接绑定 metadata/spec.source，缺结构会写崩）
import { describe, it, expect } from 'vitest'
import { parseArgoApp, argoSourceType, setArgoSourceType, globMatch, projectIssues } from '../argoApp'

describe('parseArgoApp', () => {
  it('补齐 metadata/spec 结构，保留已有值', () => {
    const o = parseArgoApp({ metadata: { name: 'demo', namespace: 'argocd' }, spec: { source: { repoURL: 'http://git/x.git', path: 'apps/demo' } } })
    expect(o.metadata.name).toBe('demo')
    expect(o.spec.source.repoURL).toBe('http://git/x.git')
    expect(o.spec.source.path).toBe('apps/demo')
    expect(o.spec.destination.server).toBe('https://kubernetes.default.svc')
    expect(o.spec.syncPolicy.selfHeal).toBe(true)
    expect(o.spec.project).toBe('default')
  })

  it('metadata 缺失时也建出来（名称输入框直接绑定 o.metadata.name）', () => {
    const o = parseArgoApp({})
    expect(o.metadata).toEqual({ name: '', namespace: '' })
  })
})

describe('argoSourceType', () => {
  it('无 chart 视为 Git 目录', () => {
    expect(argoSourceType({ repoURL: 'http://git/x.git', path: 'apps/demo' })).toBe('git')
    expect(argoSourceType(undefined)).toBe('git')
  })

  it('chart 存在即为 Helm——空串也算（还没填 Chart 名）', () => {
    // 回归：曾经用 chart 的真值判断，切到 Helm 后空串是 falsy，界面立刻弹回 Git
    expect(argoSourceType({ repoURL: 'http://helm/', chart: '' })).toBe('helm')
    expect(argoSourceType({ repoURL: 'http://helm/', chart: 'charts/my-app' })).toBe('helm')
  })
})

describe('setArgoSourceType', () => {
  it('切到 Helm：建立 chart，空 path 清掉，其余字段不动', () => {
    const o = parseArgoApp({ metadata: { name: 'demo' }, spec: { source: { repoURL: 'http://git/x.git', path: '', targetRevision: 'main' } } })
    setArgoSourceType(o, 'helm')
    expect(argoSourceType(o.spec.source)).toBe('helm')
    expect(o.spec.source.chart).toBe('')
    expect('path' in o.spec.source).toBe(false)
    expect(o.spec.source.repoURL).toBe('http://git/x.git')
    expect(o.spec.source.targetRevision).toBe('main')
  })

  it('切到 Helm：非空 path 保留（便于切回 Git）', () => {
    const o = parseArgoApp({ spec: { source: { repoURL: 'http://git/x.git', path: 'manifests/demo' } } })
    setArgoSourceType(o, 'helm')
    expect(o.spec.source.path).toBe('manifests/demo')
    expect(o.spec.source.chart).toBe('')
  })

  it('切回 Git：去掉 chart，补出 path', () => {
    const o = parseArgoApp({ spec: { source: { repoURL: 'http://helm/', chart: 'charts/my-app' } } })
    setArgoSourceType(o, 'git')
    expect(argoSourceType(o.spec.source)).toBe('git')
    expect('chart' in o.spec.source).toBe(false)
    expect(o.spec.source.path).toBe('')
  })

  it('切到 Helm 不覆盖已填的 Chart 名', () => {
    const o = parseArgoApp({ spec: { source: { repoURL: 'http://helm/', chart: 'charts/my-app' } } })
    setArgoSourceType(o, 'helm')
    expect(o.spec.source.chart).toBe('charts/my-app')
  })
})

describe('globMatch', () => {
  it('* 匹配任意（含 /），非通配按字面量', () => {
    expect(globMatch('*', 'http://gitlab.x/a/b.git')).toBe(true)
    expect(globMatch('http://gitlab.x/cec/*', 'http://gitlab.x/cec/helm-chart/qa.git')).toBe(true)
    expect(globMatch('http://gitlab.x/cec/*', 'http://gitlab.x/other/qa.git')).toBe(false)
    expect(globMatch('qa-*', 'qa-yangjc')).toBe(true)
    expect(globMatch('qa-*', 'prd-yangjc')).toBe(false)
    // 正则元字符必须按字面量处理（仓库地址里 . 很常见）
    expect(globMatch('http://a.b/x', 'http://aXb/x')).toBe(false)
  })
})

describe('projectIssues', () => {
  const target = { repoURL: 'http://gitlab.cqyxpt.site/cec/helm-chart/qa-service-manager.git', server: 'https://kubernetes.default.svc', namespace: 'qa-yangjc' }

  it('宽松项目（* / *）不提示', () => {
    expect(projectIssues({ name: 'default', sourceRepos: ['*'], destinations: [{ server: '*', namespace: '*' }] }, target)).toEqual([])
  })

  it('项目未加载（undefined）不提示，避免误报', () => {
    expect(projectIssues(undefined, target)).toEqual([])
  })

  it('sourceRepos 未包含该仓库 → 提示', () => {
    const issues = projectIssues({ name: 'team-a', sourceRepos: ['http://gitlab.x/team-a/*'], destinations: [{ server: '*', namespace: '*' }] }, target)
    expect(issues).toHaveLength(1)
    expect(issues[0]).toContain('sourceRepos')
  })

  it('destinations 未允许目标 ns → 提示', () => {
    const issues = projectIssues({ name: 'team-a', sourceRepos: ['*'], destinations: [{ server: '*', namespace: 'team-a-*' }] }, target)
    expect(issues).toHaveLength(1)
    expect(issues[0]).toContain('destinations')
  })

  it('目标用集群 name 标识 / 项目未写 namespace 时不提示（无法判断，宁可不报）', () => {
    expect(projectIssues({ name: 'p', sourceRepos: ['*'], destinations: [{ name: 'cluster1', namespace: 'qa-yangjc' }] }, target)).toEqual([])
    expect(projectIssues({ name: 'p', sourceRepos: ['*'], destinations: [{ server: 'https://kubernetes.default.svc' }] }, target)).toEqual([])
  })

  it('应用侧未填 server（按集群 name 指定目标）时不做 destinations 误报', () => {
    const named = { repoURL: 'http://gitlab.cqyxpt.site/cec/x.git', server: '', namespace: 'qa-yangjc' }
    expect(projectIssues({ name: 'p', sourceRepos: ['*'], destinations: [{ server: 'https://kubernetes.default.svc', namespace: 'qa-*' }] }, named)).toEqual([])
  })

  it('仓库/目标都允许时不提示；两者都不允许时两条都提示', () => {
    const proj = { name: 'p', sourceRepos: ['http://gitlab.cqyxpt.site/cec/helm-chart/*'], destinations: [{ server: 'https://kubernetes.default.svc', namespace: 'qa-*' }] }
    expect(projectIssues(proj, target)).toEqual([])
    expect(projectIssues({ name: 'p', sourceRepos: ['http://other/*'], destinations: [{ server: 'https://other', namespace: 'other' }] }, target)).toHaveLength(2)
  })
})
