// 表单注册表测试：守护"应有可视化编辑"的 kind 已注册，且模板 apiVersion 正确
// registry 引入了 .vue 组件（element-plus CSS 链在 node 环境下无法加载），这里统一 mock 掉
import { describe, it, expect, vi } from 'vitest'

vi.mock('../../components/forms/SchemaForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/WorkloadForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/ServiceForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/IngressForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/NetworkPolicyForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/HPAForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/RbacForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/GatewayRouteForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/RoutesForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/SecretForm.vue', () => ({ default: {} }))
vi.mock('../../components/forms/ArgoAppForm.vue', () => ({ default: {} }))

import { formRegistry } from '../registry'

describe('formRegistry', () => {
  it('endpointslices 有可视化表单且模板 apiVersion 为 discovery.k8s.io/v1', () => {
    const m: any = formRegistry['endpointslices']
    expect(m).toBeTruthy()
    expect(m.fields.length).toBeGreaterThan(0)
    expect(typeof m.template).toBe('function')
    const obj = m.template('endpointslices', 'prod')
    expect(obj.apiVersion).toBe('discovery.k8s.io/v1')
    expect(obj.kind).toBe('EndpointSlice')
    expect(obj.metadata.namespace).toBe('prod')
    expect(obj.addressType).toBe('IPv4')
  })
})
