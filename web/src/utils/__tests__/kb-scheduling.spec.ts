import { describe, expect, it } from 'vitest'
import { reactive, ref } from 'vue'
import {
  cloneScheduling, emptyScheduling, kbSchedulingPayload, kbSchedulingSummary, newMatch, newToleration, splitValues,
} from '../kb-scheduling'

describe('建集群的调度设置', () => {
  it('cloneScheduling 能拷贝 reactive 代理（structuredClone 对代理抛 DataCloneError，弹窗里调度设置整块不渲染的根因）', () => {
    // 复现原始炸点：ref()/reactive() 包装后的代理喂给 structuredClone 必炸
    const proxied = ref(emptyScheduling()).value
    expect(() => structuredClone(proxied)).toThrow()
    const out = cloneScheduling(proxied)
    expect(out).toEqual({ nodeSelector: {}, tolerations: [], nodeRequired: [], nodePreferred: [], spread: '' })
    expect(out).not.toBe(proxied)
    // 填了内容的状态也能完整带出来，且产出纯对象（回流父组件后可安全再次克隆）
    const s = reactive(emptyScheduling())
    s.spread = 'preferred'
    s.tolerations = [newToleration()]
    const cloned = cloneScheduling(s)
    expect(cloned.spread).toBe('preferred')
    expect(cloned.tolerations).toHaveLength(1)
  })
  it('全空不发 scheduling 字段（而不是发一个空对象）', () => {
    expect(kbSchedulingPayload(emptyScheduling())).toBeNull()
    expect(kbSchedulingPayload(null)).toBeNull()
    const s = emptyScheduling()
    s.tolerations = [newToleration()] // 控件默认给的空行
    s.nodeRequired = [newMatch()]
    expect(kbSchedulingPayload(s)).toBeNull()
    expect(kbSchedulingSummary(s)).toBe('不设调度限制')
  })

  it('只发填了的项，并去掉键值两端空格', () => {
    const s = emptyScheduling()
    s.nodeSelector = { ' node-role.kubernetes.io/db ': 'true' }
    const t = newToleration()
    t.key = 'dedicated'
    t.operator = ''
    t.effect = ''
    s.tolerations = [t]
    const m = newMatch()
    m.key = 'topology.kubernetes.io/zone'
    m.values = 'cn-a, cn-b  cn-c'
    s.nodeRequired = [m]
    const p = kbSchedulingPayload(s)!
    expect(p.nodeSelector).toEqual({ 'node-role.kubernetes.io/db': 'true' })
    // operator 空按 Exists（与 K8s 同口径）；没填的 effect 不下发
    expect(p.tolerations).toEqual([{ key: 'dedicated', operator: 'Exists' }])
    expect(splitValues(m.values)).toEqual(['cn-a', 'cn-b', 'cn-c'])
    expect(p.nodeRequired?.[0].values).toEqual(['cn-a', 'cn-b', 'cn-c'])
    expect(p.spread).toBeUndefined()
  })

  it('Exists / DoesNotExist 不看值：值文本为空时不带 values 键', () => {
    const s = emptyScheduling()
    const m = newMatch()
    m.key = 'db'
    m.operator = 'Exists'
    m.values = '  '
    s.nodePreferred = [m]
    expect(kbSchedulingPayload(s)!.nodePreferred).toEqual([{ key: 'db', operator: 'Exists' }])
  })

  it('摘要写给人看：污点/亲和/打散各一项不漏', () => {
    const s = emptyScheduling()
    s.tolerations = [newToleration()]
    s.tolerations[0].key = 'dedicated'
    s.tolerations[0].operator = 'Equal'
    s.tolerations[0].value = 'db'
    s.spread = 'required'
    expect(kbSchedulingSummary(s)).toBe('容忍 1 条 · 必须分散到不同节点')
  })
})
