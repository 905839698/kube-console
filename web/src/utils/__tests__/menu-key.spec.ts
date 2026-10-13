import { describe, expect, it } from 'vitest'
import { menuKeyOfPath } from '../menu-key'

describe('详情路由归到列表页菜单键', () => {
  it('工作负载 / Pod / 节点 / CRD 详情都能对上', () => {
    expect(menuKeyOfPath('/workloads/deployments/foo')).toBe('/workloads/deployments')
    expect(menuKeyOfPath('/pods/app/web-1')).toBe('/workloads/pods')
    expect(menuKeyOfPath('/nodes/n1')).toBe('/nodes')
    expect(menuKeyOfPath('/crd/kubeblocks.io/v1/clusters')).toBe('/resources/clusters')
    expect(menuKeyOfPath('/monitor/namespace/app')).toBe('/monitor')
    expect(menuKeyOfPath('/helm/releases/order-api')).toBe('/helm/releases')
    expect(menuKeyOfPath('/ci/pipelines/7/design')).toBe('/ci')
    expect(menuKeyOfPath('/argocd/apps/argocd/web')).toBe('/argocd')
  })

  it('列表页原样返回（不能被改坏）', () => {
    for (const p of ['/workloads/deployments', '/nodes', '/monitor', '/overview', '/resources/secrets']) {
      expect(menuKeyOfPath(p)).toBe(p)
    }
  })
})
