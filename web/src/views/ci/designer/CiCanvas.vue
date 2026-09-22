<!-- 流水线画布：@vue-flow/core 移植自 ci-platform react-flow v10。
     DSL 节点 ↔ flow 节点双向同步，lastEmitted/dragging 防环协议与原版一致。 -->
<template>
  <div ref="wrapperRef" class="ci-canvas" @drop="onDrop" @dragover="onDragOver">
    <VueFlow
      :nodes="nodes"
      :edges="edges"
      :connect-on-click="false"
      :delete-key-code="['Backspace', 'Delete']"
      fit-view-on-init
      @nodes-change="onNodesChange"
      @edges-change="onEdgesChange"
      @connect="onConnect"
      @node-drag-start="onNodeDragStart"
      @node-drag-stop="onNodeDragStop"
      @node-click="onNodeClick"
      @pane-click="onPaneClick"
    >
      <!-- 普通节点：左 target / 右 source -->
      <template #node-ci="{ data, selected }">
        <div class="ci-node" :class="{ sel: selected }">
          <span class="del" title="删除节点" @mousedown.stop @click.stop="data.onDelete?.(idOf(data))">×</span>
          <Handle :type="target" :position="Position.Left" class="dot" />
          <div class="lbl">{{ data.label }}</div>
          <div class="sub">{{ data.type }}</div>
          <Handle :type="source" :position="Position.Right" class="dot" />
        </div>
      </template>
      <!-- 条件节点：右出口 yes / no 双句柄 -->
      <template #node-cond="{ data, selected }">
        <div class="cond-node" :class="{ sel: selected }">
          <span class="del red" title="删除节点" @mousedown.stop @click.stop="data.onDelete?.(idOf(data))">×</span>
          <Handle :type="target" :position="Position.Left" class="dot" />
          <div class="lbl">◆ 条件：{{ data.label }}</div>
          <div class="sub">{{ data.expr || 'IF / ELSE' }}</div>
          <Handle id="yes" :type="source" :position="Position.Right" class="dot green" style="top: 16px" />
          <Handle id="no" :type="source" :position="Position.Right" class="dot red" style="top: 38px" />
          <span class="branch yes">是</span>
          <span class="branch no">否</span>
        </div>
      </template>
      <Background />
      <Controls />
      <MiniMap />
    </VueFlow>
    <button class="layout-btn" @click="doAutoLayout">自动布局</button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import dagre from 'dagre'
import {
  VueFlow, Handle, Position,
  useVueFlow, applyNodeChanges, applyEdgeChanges,
  type Node, type Edge, type Connection, type NodeChange, type EdgeChange,
} from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import type { CIPEdge, CIPNode } from '../../../api/ci'
import { useNodeTypes } from '../nodes'
import { edgeId, genNodeId, normalizeDuplicateIds } from '../graphUtils'

const NODE_W = 150
const NODE_H = 50

interface CanvasData {
  label: string
  type: string
  expr: string
  params: Record<string, unknown>
  onDelete?: (id: string) => void
  // 内部记录节点自身 id（slot 作用域拿不到 node.id 时用 data 兜底）
  selfId?: string
}
type VNode = Node<CanvasData>

const props = defineProps<{
  nodes: CIPNode[]
  edges: CIPEdge[]
  selected: CIPNode | null
  onChange: (nodes: CIPNode[], edges: CIPEdge[]) => void
  onSelect: (n: CIPNode | null) => void
}>()

const { types } = useNodeTypes()
const { project, fitView } = useVueFlow()
const wrapperRef = ref<HTMLElement | null>(null)

const typeMeta = computed(() => new Map(types.value.map((t) => [t.type, t])))
const typeLabel = (t: string) => typeMeta.value.get(t)?.name ?? t

// ---- DSL ↔ flow 转换（params 放 data，上报时从 data 取回，避免闭包旧值） ----
function toVNodes(nodes: CIPNode[]): VNode[] {
  return nodes.map((n) => {
    const isCond = n.type === 'condition'
    const expr = isCond
      ? `${(n.params?.param as string) ?? ''} ${(n.params?.op as string) ?? ''} ${(n.params?.value as string) ?? ''}`
      : ''
    return {
      id: n.id,
      type: isCond ? 'cond' : 'ci',
      position: n.position ?? { x: 0, y: 0 },
      data: { label: typeLabel(n.type), type: n.type, expr, params: n.params ?? {}, onDelete: deleteById, selfId: n.id },
      ...(isCond ? {} : { sourcePosition: Position.Right }),
      targetPosition: Position.Left,
    }
  })
}
function toVEs(edges: CIPEdge[]): Edge[] {
  return edges.map((e) => {
    const isBranch = e.branch === 'yes' || e.branch === 'no'
    return {
      id: edgeId(e.source, e.branch || null, e.target),
      source: e.source,
      target: e.target,
      sourceHandle: e.branch || undefined,
      animated: true,
      style: {
        stroke: e.branch === 'yes' ? '#52c41a' : e.branch === 'no' ? '#ff4d4f' : '#999',
        strokeWidth: isBranch ? 2 : 1.5,
      },
      label: isBranch ? (e.branch === 'yes' ? '是' : '否') : undefined,
      labelStyle: { fontSize: 10, fill: e.branch === 'yes' ? '#52c41a' : '#ff4d4f' },
      labelBgStyle: { fill: '#fff' },
    }
  })
}
function toDSLNodes(ns: VNode[]): CIPNode[] {
  return ns.map((n) => ({
    id: n.id,
    type: n.data?.type ?? n.id,
    params: n.data?.params ?? {},
    position: { x: n.position.x, y: n.position.y },
  }))
}
function toDSLEdges(es: Edge[]): CIPEdge[] {
  return es.map((e) => {
    const branch = (e.sourceHandle as string) || ''
    return branch ? { source: e.source, target: e.target, branch } : { source: e.source, target: e.target }
  })
}

const nodes = ref<VNode[]>(toVNodes(props.nodes))
const edges = ref<Edge[]>(toVEs(props.edges))

// 节点右上角 ×：连带移除关联边
function deleteById(id: string) {
  // 删的正是当前选中节点时同步清选择，否则属性面板继续显示幽灵节点的参数
  if (props.selected?.id === id) props.onSelect(null)
  nodes.value = nodes.value.filter((n) => n.id !== id)
  edges.value = edges.value.filter((e) => e.source !== id && e.target !== id)
}

// 记录最近一次上报的 DSL 内容，区分「外部 props 变化」与「自己上报的回显」
const lastEmitted = ref({ n: '', e: '' })
// 拖拽中逐帧 setNodes 不逐帧上报（避免整页逐帧重渲染），dragStop 一次性上报
const dragging = ref(false)

// flow → DSL 上报
watch([nodes, edges], () => {
  const dn = toDSLNodes(nodes.value)
  const de = toDSLEdges(edges.value)
  lastEmitted.value = { n: JSON.stringify(dn), e: JSON.stringify(de) }
  if (dragging.value) return
  props.onChange(dn, de)
})

function onNodeDragStart() { dragging.value = true }
function onNodeDragStop() {
  dragging.value = false
  props.onChange(toDSLNodes(nodes.value), toDSLEdges(edges.value))
}

// 外部（导入 / 加载 / 属性面板 / 类型元数据到达）→ flow 内部状态
let labelRev = -1
watch(() => [props.nodes, props.edges, types.value], () => {
  const rev = types.value.length
  const norm = normalizeDuplicateIds({ nodes: props.nodes, edges: props.edges })
  const key = { n: JSON.stringify(norm.nodes), e: JSON.stringify(norm.edges) }
  const contentSame = key.n === lastEmitted.value.n && key.e === lastEmitted.value.e
  if (contentSame && labelRev === rev) return
  labelRev = rev
  nodes.value = toVNodes(norm.nodes)
  edges.value = toVEs(norm.edges)
}, { deep: false })

// 节点变更（位置/选中/删除）；删除时过滤端点悬空边
function onNodesChange(changes: NodeChange[]) {
  nodes.value = applyNodeChanges(changes, nodes.value)
  const removed = changes.filter((c) => c.type === 'remove').map((c) => c.id)
  if (removed.length > 0) {
    const ids = new Set(removed)
    edges.value = edges.value.filter((e) => !ids.has(e.source) && !ids.has(e.target))
  }
}
function onEdgesChange(changes: EdgeChange[]) {
  edges.value = applyEdgeChanges(changes, edges.value)
}

// 连线：禁自环 + 禁重复（同 id 重复边会让 vue-flow 渲染异常，DSL 里也是冗余边）
function onConnect(conn: Connection) {
  const { source, target, sourceHandle } = conn
  if (!source || !target || source === target) return
  const id = edgeId(source, sourceHandle, target)
  if (edges.value.some((e) => e.id === id || (e.source === source && e.target === target))) return
  edges.value = [...edges.value, {
    id,
    source, target,
    sourceHandle: sourceHandle || undefined,
    animated: true,
  }]
}

// 从节点面板拖入：屏幕坐标 → 画布坐标
function onDrop(e: DragEvent) {
  e.preventDefault()
  const type = e.dataTransfer?.getData('nodeType')
  if (!type) return
  const id = `${type}-${genNodeId()}`
  const meta = typeMeta.value.get(type)
  const params: Record<string, unknown> = {}
  ;(meta?.properties ?? []).forEach((p) => {
    if (p.default !== undefined) params[p.name] = p.default
  })
  const bounds = wrapperRef.value?.getBoundingClientRect()
  const pos = project({ x: e.clientX - (bounds?.left ?? 0), y: e.clientY - (bounds?.top ?? 0) })
  nodes.value = [...nodes.value, {
    id,
    type: type === 'condition' ? 'cond' : 'ci',
    position: { x: pos.x - NODE_W / 2, y: pos.y - NODE_H / 2 },
    data: { label: meta?.name ?? type, type, expr: '', params, onDelete: deleteById, selfId: id },
    ...(type === 'condition' ? {} : { sourcePosition: Position.Right }),
    targetPosition: Position.Left,
  }]
}
function onDragOver(e: DragEvent) {
  // 拖拽中逐帧触发：不要 setData（浏览器对 dragover 里写数据有限制且无意义，
  // 数据在 NodePanel 的 dragstart 上写入即可）
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
}

// Dagre 自动布局（LR）
function doAutoLayout() {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 40, ranksep: 80 })
  g.setDefaultEdgeLabel(() => ({}))
  nodes.value.forEach((n) => g.setNode(n.id, { width: NODE_W, height: NODE_H }))
  edges.value.forEach((e) => g.setEdge(e.source, e.target))
  dagre.layout(g)
  nodes.value = nodes.value.map((n) => {
    const pos = g.node(n.id) as { x: number; y: number } | undefined
    if (!pos) return n
    return { ...n, position: { x: pos.x - NODE_W / 2, y: pos.y - NODE_H / 2 } }
  })
  fitView({ padding: 0.15 })
}

// 选中：优先取 props.nodes 里的权威节点，新拖入的节点用 flow data 兜底
function onNodeClick({ node: n }: { event: unknown; node: VNode }) {
  const found = props.nodes.find((x) => x.id === n.id)
  if (found) {
    props.onSelect(found)
    return
  }
  props.onSelect({
    id: n.id,
    type: n.data?.type ?? n.id,
    params: n.data?.params ?? {},
    position: { x: n.position.x, y: n.position.y },
  })
}
function onPaneClick() { props.onSelect(null) }
function idOf(d: CanvasData) { return d.selfId ?? '' }
</script>

<style scoped>
.ci-canvas { width: 100%; height: 100%; position: relative; }
.ci-canvas :deep(.vue-flow) { width: 100%; height: 100%; }
.layout-btn {
  position: absolute; top: 8px; right: 8px; z-index: 10;
  padding: 4px 10px; border-radius: 6px; border: 1px solid #d9d9d9;
  background: #fff; cursor: pointer; font-size: 12px;
}
.ci-node, .cond-node {
  position: relative; padding: 8px 12px; border-radius: 8px;
  font-size: 13px; min-width: 120px; background: #e6f4ff; border: 1px solid #91caff;
}
.ci-node.sel { border-color: #1677ff; background: #bae0ff; }
.cond-node { min-width: 140px; background: #fff7e6; border-color: #ffd591; }
.cond-node.sel { border-color: #fa8c16; background: #ffe7ba; }
.lbl { font-weight: 600; }
.sub { color: #999; font-size: 11px; }
.del {
  position: absolute; top: -9px; right: -9px; width: 18px; height: 18px; border-radius: 50%;
  background: #fff; border: 1px solid #ff4d4f; color: #ff4d4f; font-size: 12px;
  line-height: 15px; text-align: center; cursor: pointer; z-index: 2;
}
.dot { background: #1677ff; }
.dot.green { background: #52c41a; }
.dot.red { background: #ff4d4f; }
.branch { position: absolute; right: -26px; font-size: 10px; }
.branch.yes { top: 8px; color: #52c41a; }
.branch.no { top: 30px; color: #ff4d4f; }
</style>
