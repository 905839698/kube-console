<!-- 属性面板：按节点 schema 动态生成表单（schema 是唯一事实来源，与后端校验/渲染一致）。
     动态数据源级联：git-branch/tag（repoRefs 400ms 防抖）、upstream-task/task-result（画布推导）、
     k8s-ns（kube-console 命名空间）、k8s-deployments/k8s-container（k8sTargets 300ms 防抖）、
     credential（当前集群凭据列表）。下拉均可手输兜底（allow-create）。 -->
<template>
  <div class="prop-panel">
    <template v-if="node">
      <div class="title">{{ meta?.name ?? node.type }}</div>
      <div class="hint">节点 ID：{{ node.id }}</div>
      <div v-for="p in visibleProps" :key="p.name" class="field">
        <div class="field-label">{{ p.name }}<span v-if="p.required" class="req">*</span></div>

        <!-- 下拉类（静态 options / 动态数据源）：可选可手输 -->
        <el-select
          v-if="kind(p) === 'select-dynamic' || kind(p) === 'select-static'"
          v-model="form[p.name]"
          size="small" clearable filterable allow-create default-first-option
          :loading="metaOf(p).loading"
          :placeholder="metaOf(p).loading ? '加载中…' : metaOf(p).hint || '选择或直接输入'"
          style="width: 100%"
          @change="(v: string) => onDynamicSelect(p, v)"
        >
          <el-option v-for="o in optionsOf(p)" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
        <!-- 常规字段 -->
        <el-input-number v-else-if="kind(p) === 'number'" v-model="form[p.name]" size="small" :controls="false" style="width: 100%" />
        <el-switch v-else-if="kind(p) === 'boolean'" v-model="form[p.name]" />
        <el-input v-else-if="kind(p) === 'text'" v-model="form[p.name]" type="textarea" :rows="3" size="small" />
        <el-input v-else v-model="form[p.name]" size="small" />

        <div v-if="metaOf(p).error" class="err">{{ metaOf(p).error }}</div>
        <div v-else-if="p.description" class="desc">{{ p.description }}</div>
      </div>
      <el-divider style="margin: 12px 0" />
      <el-button type="primary" size="small" style="width: 100%" @click="apply">应用</el-button>
    </template>
    <div v-else class="placeholder">选中节点以编辑属性</div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ciApi, type CIGraph, type CIPNode, type CIPropSchema, type CICredential } from '../../../api/ci'
import { k8sApi, type NamespaceItem } from '../../../api'
import { useNodeTypes } from '../nodes'

interface Props { node: CIPNode | null; graph?: CIGraph }
const props = withDefaults(defineProps<Props>(), { graph: undefined })
const emit = defineEmits<{ (e: 'params', params: Record<string, unknown>): void }>()

const { types } = useNodeTypes()
const meta = computed(() => types.value.find((t) => t.type === props.node?.type))

// 表单模型：切换节点时按 node.params 重建（避免上一节点残留值）
const form = reactive<Record<string, unknown>>({})
let formNodeId = ''
watch(() => props.node?.id, (id) => {
  if (!id || id === formNodeId) return
  formNodeId = id
  Object.keys(form).forEach((k) => delete form[k])
  for (const [k, v] of Object.entries(props.node?.params ?? {})) form[k] = v
  for (const p of meta.value?.properties ?? []) {
    if (!(p.name in form) && p.default !== undefined) form[p.name] = p.default
  }
}, { immediate: true })

function apply() { emit('params', { ...form }) }

// ---- 字段可见性：条件节点按“判断依据”显隐（param / task+result） ----
const visibleProps = computed(() => {
  const ps = meta.value?.properties ?? []
  if (props.node?.type !== 'condition') return ps
  const source = (form['source'] as string) ?? 'param'
  return ps.filter((p) => (source === 'taskResult' ? p.name !== 'param' : p.name !== 'task' && p.name !== 'result'))
})

// ---- 字段分派：下拉类 vs 常规输入 ----
type Kind = 'select-dynamic' | 'select-static' | 'number' | 'boolean' | 'text' | 'string'
function kind(p: CIPropSchema): Kind {
  if (p.optionsSource) return 'select-dynamic'
  if (p.type === 'credential') return 'select-dynamic'
  if (p.type === 'number') return 'number'
  if (p.type === 'boolean') return 'boolean'
  if (p.type === 'text') return 'text'
  if (p.type === 'select' || (p.options && p.options.length > 0)) return 'select-static'
  return 'string'
}

// ---- 动态选项 / 元信息（渲染期调用，读响应式源故自动更新） ----
interface Opt { value: string; label: string }
interface Meta { loading: boolean; error?: string; hint?: string }

// 异步数据源状态（git refs / k8s targets）
const fetched = reactive<Record<string, { loading: boolean; error?: string; options: Opt[] }>>({})
const fetchedOf = (p: CIPropSchema) => (fetched[p.name] ||= { loading: false, options: [] })

// 凭据列表（credential 字段选项）
const credentials = ref<CICredential[]>([])
watch(() => props.node?.id, () => {
  void ciApi.credentials().then((cs) => { credentials.value = cs }).catch(() => { credentials.value = [] })
}, { immediate: true })

// 命名空间列表（k8s-ns 字段选项；kube-console 自身 API，随 X-Cluster）
const namespaces = ref<NamespaceItem[]>([])
function ensureNamespaces() {
  if (namespaces.value.length) return
  void k8sApi.namespaces().then((ns) => { namespaces.value = ns }).catch(() => undefined)
}

// upstream-task：画布中产出 result 的普通节点
function upstreamOptions(): Opt[] {
  const hasResults = (nodeType: string) => (types.value.find((t) => t.type === nodeType)?.results?.length ?? 0) > 0
  return (props.graph?.nodes ?? []).filter((n) => n.type !== 'condition' && hasResults(n.type)).map((n) => ({ value: n.id, label: n.id }))
}
// task-result：跟随同表单 task 选择
function taskResultOptions(): Opt[] {
  const task = (form['task'] as string) ?? ''
  const nodeType = (props.graph?.nodes ?? []).find((n) => n.id === task)?.type
  return (nodeType ? types.value.find((t) => t.type === nodeType)?.results ?? [] : []).map((r) => ({ value: r.value, label: r.value }))
}

function optionsOf(p: CIPropSchema): Opt[] {
  switch (p.optionsSource) {
    case 'k8s-ns':
      ensureNamespaces()
      return namespaces.value.map((ns) => ({ value: ns.name, label: ns.name }))
    case 'git-branch':
    case 'git-tag':
    case 'k8s-deployments':
    case 'k8s-container':
      return fetchedOf(p).options
    case 'upstream-task':
      return upstreamOptions()
    case 'task-result':
      return taskResultOptions()
    default:
      break
  }
  if (p.type === 'credential') {
    // 下拉标签带凭证形态：token/kubeconfig 等形态不含 username/password，
    // 误选到 registry/构建类节点会出现"未注入 registry 凭证"的运行时 WARN
    return credentials.value.map((c) => ({
      value: c.name,
      label: `${c.name}（${CRED_FORM_LABELS[c.form] ?? c.form}）`,
    }))
  }
  return p.options ?? []
}

// 凭证 form → 中文形态标签（与凭据管理页一致）
const CRED_FORM_LABELS: Record<string, string> = {
  basic: '账密',
  token: 'Token',
  dockerconfig: 'DockerConfig',
  kubeconfig: 'KubeConfig',
  aksk: 'AK/SK',
  raw: 'RAW',
}

function metaOf(p: CIPropSchema): Meta {
  const base: Meta = { loading: false }
  switch (p.optionsSource) {
    case 'git-branch':
    case 'git-tag':
    case 'k8s-deployments':
    case 'k8s-container': {
      const s = fetchedOf(p)
      base.loading = s.loading
      base.error = s.error
      break
    }
    case 'k8s-ns':
      ensureNamespaces()
      break
    case 'upstream-task':
      base.hint = upstreamOptions().length === 0 ? '画布中没有产出 result 的节点（或节点类型未加载完成）' : undefined
      break
    case 'task-result': {
      const task = (form['task'] as string) ?? ''
      base.hint = !task ? '先选择上游节点' : taskResultOptions().length === 0 ? '该节点未产出 result' : undefined
      break
    }
  }
  if (p.optionsSource === 'k8s-deployments' && !(form['namespace'] as string)) base.hint = '先选择 namespace'
  if (p.optionsSource === 'k8s-container' && !(form['deployTarget'] as string)) base.hint = '先选择 deployment'
  return base
}

// ---- git refs：url + credential 变化后 400ms 防抖加载 ----
let gitSeq = 0
watch([() => (form['url'] as string) ?? '', () => (form['credential'] as string) ?? ''], async (val) => {
  const [url, credential] = val
  const targets = (meta.value?.properties ?? []).filter((p) => p.optionsSource === 'git-branch' || p.optionsSource === 'git-tag')
  if (!targets.length) return
  const seq = ++gitSeq
  for (const t of targets) fetchedOf(t).options = []
  if (!/^https?:\/\//i.test((url || '').trim())) return
  await sleep(400)
  if (seq !== gitSeq || (form['url'] as string || '') !== url) return
  for (const t of targets) fetchedOf(t).loading = true
  try {
    const refs = await ciApi.repoRefs(url.trim(), credential || undefined)
    if (seq !== gitSeq) return
    for (const t of targets) {
      const names = t.optionsSource === 'git-branch' ? refs.branches : refs.tags
      fetchedOf(t).options = names.map((v) => ({ value: v, label: v }))
      fetchedOf(t).error = undefined
    }
  } catch (e: any) {
    if (seq !== gitSeq) return
    for (const t of targets) fetchedOf(t).error = e?.message || '加载失败'
  } finally {
    if (seq === gitSeq) for (const t of targets) fetchedOf(t).loading = false
  }
})

// ---- k8s 级联：namespace → deployments → container（300ms 防抖） ----
let k8sSeq = 0
watch([() => (form['namespace'] as string) ?? '', () => (form['deployTarget'] as string) ?? ''], () => {
  const propsOfNode = meta.value?.properties ?? []
  if (!propsOfNode.some((p) => p.optionsSource === 'k8s-deployments' || p.optionsSource === 'k8s-container')) return
  const seq = ++k8sSeq
  const pDep = propsOfNode.find((p) => p.optionsSource === 'k8s-deployments')
  const pCon = propsOfNode.find((p) => p.optionsSource === 'k8s-container')
  const ns = (form['namespace'] as string) ?? ''
  const target = (form['deployTarget'] as string) ?? ''
  const timer = setTimeout(async () => {
    if (!ns || seq !== k8sSeq) return
    if (pDep) fetchedOf(pDep).loading = true
    if (pCon) fetchedOf(pCon).loading = true
    try {
      const t = await ciApi.k8sTargets(ns)
      if (seq !== k8sSeq) return
      const ws = t.workloads ?? []
      if (pDep) {
        fetchedOf(pDep).options = ws.map((w) => ({ value: w.name, label: `${w.name}（${w.kind}）` }))
        fetchedOf(pDep).error = undefined
      }
      if (pCon) {
        const w = ws.find((x) => x.name === target)
        fetchedOf(pCon).options = (w?.containers ?? []).map((v) => ({ value: v, label: v }))
        fetchedOf(pCon).error = undefined
      }
    } catch (e: any) {
      if (seq !== k8sSeq) return
      if (pDep) fetchedOf(pDep).error = e?.message || '加载失败'
      if (pCon) fetchedOf(pCon).error = e?.message || '加载失败'
    } finally {
      if (seq === k8sSeq) {
        if (pDep) fetchedOf(pDep).loading = false
        if (pCon) fetchedOf(pCon).loading = false
      }
    }
  }, 300)
  // 值清空时立即复位（不等防抖）
  if (!ns) {
    clearTimeout(timer)
    if (pDep) fetchedOf(pDep).options = []
    if (pCon) fetchedOf(pCon).options = []
  }
})

// 选中 workload 后同步 deployTargetKind（模板按 kind 拼 deployment/xxx）
function onDynamicSelect(p: CIPropSchema, v: string) {
  if (p.optionsSource !== 'k8s-deployments' || !v) return
  void ciApi.k8sTargets((form['namespace'] as string) || undefined)
    .then((t) => {
      const w = (t.workloads ?? []).find((x) => x.name === v)
      if (w) form['deployTargetKind'] = w.kind
    })
    .catch(() => undefined)
}

function sleep(ms: number) { return new Promise((r) => setTimeout(r, ms)) }
</script>

<style scoped>
.prop-panel {
  width: 280px; border: 1px solid #ebeef5; border-radius: 8px;
  padding: 16px; overflow-y: auto; flex-shrink: 0; background: #fff;
}
.title { font-weight: 600; margin-bottom: 4px; }
.hint { font-size: 12px; color: #909399; margin-bottom: 12px; }
.placeholder { color: #909399; padding: 8px 0; }
.field { margin-bottom: 10px; }
.field-label { font-size: 12px; color: #606266; margin-bottom: 4px; }
.req { color: #f56c6c; margin-left: 2px; }
.err { font-size: 11px; color: #e6a23c; margin-top: 2px; }
.desc { font-size: 11px; color: #c0c4cc; margin-top: 2px; }
</style>
