// 画布 DSL 工具：节点 id 生成 / 重复 id 修复 / 稳定边 id。
import type { CIPEdge, CIPNode } from '../../api/ci'

// 节点 id 生成：节点 id 会成为 Tekton task 名并作为画布/DSL 的唯一键，
// 重复 id 会让 flow 库内部状态乒乓（页面无响应）。
// 「完整毫秒时间戳 + 进程内单调计数器」：同毫秒多次添加靠计数器区分，跨会话靠时间戳区分。
let nodeIdSeq = 0
export function genNodeId(): string {
  nodeIdSeq += 1
  return `${Date.now().toString(36)}-${nodeIdSeq.toString(36)}`
}

interface GraphLike { nodes: CIPNode[]; edges: CIPEdge[] }
interface NormalizeResult { nodes: CIPNode[]; edges: CIPEdge[]; changed: boolean }

// 修复画布图里的重复节点 id（确定性、幂等）：首个节点保留原 id，
// 后续重复者按 -n2 / -n3 递增换新 id；边引用原 id 即指向保留原 id 的首个节点。
// 图无重复时原样返回（changed=false，零开销）。
export function normalizeDuplicateIds(g: GraphLike): NormalizeResult {
  const seen = new Set<string>()
  let changed = false
  const nodes = g.nodes.map((n) => {
    if (!seen.has(n.id)) {
      seen.add(n.id)
      return n
    }
    let fresh = n.id
    let i = 2
    while (seen.has(fresh)) {
      fresh = `${n.id}-n${i}`
      i += 1
    }
    seen.add(fresh)
    changed = true
    return { ...n, id: fresh }
  })
  return changed ? { nodes, edges: g.edges, changed } : { nodes: g.nodes, edges: g.edges, changed }
}

// 稳定边 id：由 源/源句柄/目标 决定，重渲染 / 上报回显后 id 不漂移（选中态、样式不丢失）。
export function edgeId(source: string, sourceHandle: string | null | undefined, target: string): string {
  return `e-${source}-${sourceHandle ?? ''}-${target}`
}
