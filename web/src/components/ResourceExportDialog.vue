<template>
  <el-dialog
    :model-value="modelValue" title="导出资源" width="840px" top="4vh"
    :close-on-click-modal="false" @update:model-value="(v) => emit('update:modelValue', v)"
  >
    <div class="toolbar">
      <span class="lbl">命名空间</span>
      <el-select v-model="ns" filterable style="width: 240px" placeholder="选择命名空间" @change="loadAll">
        <el-option v-for="n in namespaces" :key="n" :label="n" :value="n" />
      </el-select>
      <el-button size="small" :icon="Refresh" circle :loading="loading" @click="loadAll" />
      <span class="tip">逐层选择：命名空间 → 按类别勾选资源 → 导出多文档 YAML（已深度清洗，可直接再导入）</span>
    </div>

    <div class="groups" v-loading="loading">
      <div v-for="cat in categories" :key="cat.key" class="group">
        <div class="group-head">
          <el-checkbox :model-value="catAll(cat)" :indeterminate="catIndeterminate(cat)" @change="(v: any) => toggleCat(cat, !!v)">
            <b>{{ cat.label }}</b>
          </el-checkbox>
          <span class="count">{{ catTotal(cat) }} 项</span>
        </div>
        <div class="kinds">
          <div v-for="k in cat.kinds" :key="k.kind" class="kind">
            <el-checkbox
              :model-value="kindAll(k)" :indeterminate="kindIndeterminate(k)"
              @change="(v: any) => toggleKind(k, !!v)"
            >{{ k.title }}（{{ (items[k.kind] || []).length }}）</el-checkbox>
            <el-checkbox-group v-model="checked[k.kind]" class="items">
              <el-checkbox v-for="name in items[k.kind] || []" :key="name" :value="name">{{ name }}</el-checkbox>
            </el-checkbox-group>
          </div>
        </div>
      </div>
      <el-empty v-if="!loading && totalItems === 0" description="该命名空间下无可导出资源" />
    </div>

    <template #footer>
      <span class="sel-count">已选 {{ selectedCount }} 项</span>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="exporting" :disabled="!selectedCount" @click="doExport">导出</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { k8sApi } from '../api'
import { useNamespaceStore } from '../store/namespace'
import { downloadText } from '../utils/download'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits(['update:modelValue'])

const nsStore = useNamespaceStore()

// Kuboard 式分类：控制器 / 服务与路由 / 配置 / 其他（均为命名空间级资源）
const categories = [
  {
    key: 'workloads', label: '控制器（工作负载）',
    kinds: [
      { kind: 'deployments', title: 'Deployment' },
      { kind: 'statefulsets', title: 'StatefulSet' },
      { kind: 'daemonsets', title: 'DaemonSet' },
      { kind: 'cronjobs', title: 'CronJob' },
      { kind: 'jobs', title: 'Job' },
    ],
  },
  {
    key: 'svc', label: '服务与路由',
    kinds: [
      { kind: 'services', title: 'Service' },
      { kind: 'ingresses', title: 'Ingress' },
    ],
  },
  {
    key: 'config', label: '配置',
    kinds: [
      { kind: 'configmaps', title: 'ConfigMap' },
      { kind: 'secrets', title: 'Secret' },
    ],
  },
  {
    key: 'other', label: '其他资源',
    kinds: [
      { kind: 'persistentvolumeclaims', title: 'PVC' },
      { kind: 'serviceaccounts', title: 'ServiceAccount' },
      { kind: 'networkpolicies', title: 'NetworkPolicy' },
      { kind: 'horizontalpodautoscalers', title: 'HPA' },
      { kind: 'roles', title: 'Role' },
      { kind: 'rolebindings', title: 'RoleBinding' },
    ],
  },
]
const WORKLOAD_KINDS = new Set(['deployments', 'statefulsets', 'daemonsets', 'cronjobs', 'jobs'])

const namespaces = ref<string[]>([])
const ns = ref('')
const loading = ref(false)
const items = reactive<Record<string, string[]>>({})
const checked = reactive<Record<string, string[]>>({})
const exporting = ref(false)

const totalItems = computed(() => Object.values(items).reduce((n, list) => n + list.length, 0))
const selectedCount = computed(() => Object.values(checked).reduce((n, list) => n + (list?.length || 0), 0))

function names(k: { kind: string }): string[] {
  return items[k.kind] || []
}
function kindAll(k: { kind: string }): boolean {
  return names(k).length > 0 && (checked[k.kind]?.length || 0) === names(k).length
}
function kindIndeterminate(k: { kind: string }): boolean {
  const c = checked[k.kind]?.length || 0
  return c > 0 && c < names(k).length
}
function toggleKind(k: { kind: string }, v: boolean) {
  checked[k.kind] = v ? [...names(k)] : []
}
function catTotal(cat: (typeof categories)[number]): number {
  return cat.kinds.reduce((n, k) => n + names(k).length, 0)
}
function catSelected(cat: (typeof categories)[number]): number {
  return cat.kinds.reduce((n, k) => n + (checked[k.kind]?.length || 0), 0)
}
function catAll(cat: (typeof categories)[number]): boolean {
  return catTotal(cat) > 0 && cat.kinds.every((k) => kindAll(k))
}
function catIndeterminate(cat: (typeof categories)[number]): boolean {
  const sel = catSelected(cat)
  return sel > 0 && sel < catTotal(cat)
}
function toggleCat(cat: (typeof categories)[number], v: boolean) {
  cat.kinds.forEach((k) => toggleKind(k, v))
}

async function loadAll() {
  if (!ns.value) return
  loading.value = true
  const kinds = categories.flatMap((c) => c.kinds.map((k) => k.kind))
  await Promise.all(
    kinds.map(async (kind) => {
      try {
        // 控制器走工作负载接口（含 Pod 扩展字段），其余走通用资源接口
        const list = WORKLOAD_KINDS.has(kind) ? await k8sApi.workloads(kind, ns.value) : await k8sApi.resources(kind, ns.value)
        items[kind] = list.map((x) => x.name)
      } catch {
        items[kind] = [] // 该类型在当前集群不可用等，静默置空
      }
      checked[kind] = []
    }),
  )
  loading.value = false
}

async function initOnce() {
  if (!namespaces.value.length) {
    try {
      namespaces.value = (await k8sApi.namespaces()).map((n) => n.name)
    } catch {
      namespaces.value = ['default']
    }
  }
  if (!ns.value) {
    const cur = nsStore.selected.filter((n) => n !== '__all__')
    ns.value = cur[0] || 'default'
  }
  if (ns.value) loadAll()
}

async function doExport() {
  const resources: { kind: string; namespace: string; name: string }[] = []
  for (const cat of categories) {
    for (const k of cat.kinds) {
      for (const name of checked[k.kind] || []) {
        resources.push({ kind: k.kind, namespace: ns.value, name })
      }
    }
  }
  if (!resources.length) return
  exporting.value = true
  try {
    const r = await k8sApi.exportYaml(resources)
    const skipped = r.skipped?.length || 0
    if (!r.yaml.trim()) {
      ElMessage.error('选中的资源均导出失败')
      return
    }
    downloadText(r.yaml, `export_${ns.value}_${new Date().toISOString().slice(0, 10)}.yaml`, 'text/yaml;charset=utf-8')
    if (skipped) {
      const first = r.skipped[0]
      ElMessage.warning(`已导出 ${resources.length - skipped} 项，${skipped} 项失败跳过（如 ${first.kind}/${first.name}：${first.error}）`)
    } else {
      ElMessage.success(`已导出 ${resources.length} 项`)
    }
    emit('update:modelValue', false)
  } catch {
    /* 拦截器已提示 */
  } finally {
    exporting.value = false
  }
}

watch(
  () => props.modelValue,
  (v) => {
    if (v) initOnce()
  },
)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.lbl { font-size: 13px; color: #606266; }
.tip { font-size: 12px; color: #909399; }
.groups { max-height: calc(100vh - 340px); min-height: 200px; overflow-y: auto; }
.group { border: 1px solid #e4e7ed; border-radius: 6px; padding: 10px 14px; margin-bottom: 10px; }
.group-head { display: flex; align-items: center; gap: 12px; margin-bottom: 4px; }
.count { font-size: 12px; color: #909399; }
.kinds { display: flex; flex-direction: column; gap: 4px; margin-left: 22px; }
.items { display: flex; flex-wrap: wrap; gap: 0 16px; margin-left: 22px; }
.items :deep(.el-checkbox) { margin-right: 0; width: auto; min-width: 180px; }
.sel-count { float: left; line-height: 32px; font-size: 13px; color: #606266; }
</style>
