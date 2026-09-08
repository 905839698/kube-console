<!-- 节点面板：按 category 分组，拖拽或点击添加到画布（数据来自 GET /ci/node-types） -->
<template>
  <div class="node-panel">
    <div class="title">节点</div>
    <div class="hint">拖拽或点击节点添加到画布</div>
    <div v-if="loading" class="empty"><el-icon class="is-loading"><Loading /></el-icon></div>
    <div v-else-if="!types.length" class="empty" :class="{ err: !!error }">{{ error ? `加载失败：${error}` : '暂无节点' }}</div>
    <div v-for="(list, cat) in grouped" :key="cat" class="group">
      <div class="cat">{{ cat }}</div>
      <div
        v-for="t in list" :key="t.type"
        class="item" draggable="true"
        @dragstart="onDragStart($event, t)"
        @click="addNode(t)"
      >
        <span v-if="t.icon" class="icon">{{ t.icon }}</span>{{ t.name }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { Loading } from '@element-plus/icons-vue'
import type { CINodeType, CIPNode, CIGraph, CIPEdge } from '../../../api/ci'
import { useNodeTypes } from '../nodes'
import { genNodeId } from '../graphUtils'

const props = defineProps<{ graph: CIGraph }>()
const emit = defineEmits<{ (e: 'update', nodes: CIPNode[], edges: CIPEdge[]): void }>()

const { types, loading, error, load } = useNodeTypes()
onMounted(() => { void load() })

// 点击添加：放在已有节点右侧，避免重叠
function addNode(t: CINodeType) {
  const id = `${t.type}-${genNodeId()}`
  const params: Record<string, unknown> = {}
  ;(t.properties ?? []).forEach((p) => {
    if (p.default !== undefined) params[p.name] = p.default
  })
  const maxX = props.graph.nodes.reduce((m, n) => Math.max(m, n.position?.x ?? 0), 0)
  const position = { x: props.graph.nodes.length ? maxX + 200 : 50, y: 50 }
  emit('update', [...props.graph.nodes, { id, type: t.type, params, position }], props.graph.edges)
}

function onDragStart(e: DragEvent, t: CINodeType) {
  e.dataTransfer?.setData('nodeType', t.type)
}

const grouped = computed(() => {
  const out: Record<string, CINodeType[]> = {}
  for (const t of types.value) (out[t.category] ||= []).push(t)
  return out
})
</script>

<script lang="ts">
export default { name: 'CiNodePanel' }
</script>

<style scoped>
.node-panel {
  width: 200px; border: 1px solid #ebeef5; border-radius: 8px;
  padding: 8px; overflow-y: auto; flex-shrink: 0; background: #fff;
}
.title { font-weight: 600; margin-bottom: 4px; }
.hint { font-size: 11px; color: #909399; margin-bottom: 8px; }
.empty { text-align: center; padding: 12px 0; font-size: 12px; color: #909399; }
.empty.err { color: #f56c6c; }
.group { margin-bottom: 12px; }
.cat { font-size: 12px; color: #909399; margin-bottom: 4px; }
.item {
  cursor: grab; border: 1px dashed #dcdfe6; border-radius: 6px;
  padding: 4px 8px; margin-bottom: 4px; font-size: 13px; background: #fafafa;
}
.item:hover { border-color: #409eff; background: #ecf5ff; }
.icon { margin-right: 6px; }
</style>
