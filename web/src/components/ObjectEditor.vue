<template>
  <div class="object-editor">
    <el-tabs v-model="activeTab">
      <!-- 可视化表单 -->
      <el-tab-pane v-if="hasForm" label="可视化表单" name="form">
        <div v-loading="parsing">
          <!-- 统一的标签/注解编辑：各专用表单不再重复内嵌（WorkloadForm 自带、路由表单不暴露注解） -->
          <MetaEditor v-if="showMeta" :object="objRef" :no-annotations="!!module.noAnnotations" />
          <!-- schema 表单：直接编辑对象 -->
          <SchemaForm v-if="module.fields" :fields="module.fields" :object="objRef" @change="syncFromForm" />
          <!-- 专用表单组件（creating：新建/编辑模式，表单据此决定名称等身份字段可否编辑） -->
          <component
            v-else-if="module.component"
            :is="module.component"
            v-model="formData"
            :kind="kind"
            :namespaced="namespaced"
            :creating="creating"
            @change="syncFromForm"
          />
        </div>
      </el-tab-pane>
      <!-- YAML -->
      <el-tab-pane label="YAML" name="yaml">
        <div class="yaml-pane">
          <YamlEditor v-model="yamlText" :readonly="false" />
        </div>
      </el-tab-pane>
    </el-tabs>
    <div class="editor-footer">
      <el-button type="primary" :loading="saving" @click="save">
        <el-icon><Check /></el-icon>&nbsp;{{ creating ? '创建' : '保存' }}
      </el-button>
      <el-button @click="emit('cancel')">取消</el-button>
      <span v-if="yamlError" class="yaml-error">{{ yamlError }}</span>
    </div>

    <!-- 保存前变更对比 -->
    <el-dialog v-model="diffVisible" title="保存前变更对比" width="880px" top="4vh">
      <div class="diff-box">
        <div v-for="(r, i) in diffRows" :key="i" class="diff-row" :class="'diff-' + r.type">
          <span class="diff-no">{{ r.oldNo || '' }}</span>
          <span class="diff-no">{{ r.newNo || '' }}</span>
          <pre class="diff-text">{{ r.text }}</pre>
        </div>
      </div>
      <template #footer>
        <el-button @click="diffVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doApply">确认保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import YamlEditor from './YamlEditor.vue'
import SchemaForm from './forms/SchemaForm.vue'
import MetaEditor from './forms/MetaEditor.vue'
import { formRegistry } from '../forms/registry'
import { parseYaml, dumpYaml, clone } from '../forms/utils'
import { diffLines } from '../utils/diff'
import { k8sApi } from '../api'

const props = defineProps<{
  kind: string
  /** 初始 YAML（编辑模式）或空（新建模式） */
  yaml?: string
  creating?: boolean
  namespace?: string
  namespaced?: boolean
}>()
const emit = defineEmits(['saved', 'cancel'])

const module = computed(() => formRegistry[props.kind])
// 初始 YAML 解析失败时为 true：表单数据没法构建，若仍渲染表单 tab，组件读
// o.metadata.name 之类的字段会在 {} 上抛 TypeError 直接白屏——此时只给 YAML tab
const formBroken = ref(false)
// 是否有真实表单（组件或非空字段定义）；否则该资源视为无表单，直接展示原始 YAML
const hasForm = computed(() => {
  if (formBroken.value) return false
  const m = module.value
  return !!m && (!!m.component || (m.fields?.length ?? 0) > 0)
})
const activeTab = ref('form')
// 统一元信息编辑：路由表单不暴露注解；WorkloadForm 自带标签/注解
const showMeta = computed(() => !!module.value && !module.value.metaInForm)
const diffVisible = ref(false)
const diffRows = computed(() => diffLines(props.yaml || '', yamlText.value))

const objRef = ref<Record<string, any>>({})
const formData = ref<any>({})
const baseObj = ref<Record<string, any>>({})
const yamlText = ref('')
const yamlError = ref('')
const saving = ref(false)
const parsing = ref(false)

let syncTimer: ReturnType<typeof setTimeout> | undefined

// 注意：必须在 setup 同步初始化（子组件 mounted 早于父组件，渲染时需要表单数据已就绪）
init()

function init() {
  // 无表单模板的资源（如 EndpointSlice）默认展示 YAML 视图，避免激活不存在的表单 tab 导致空白
  if (!hasForm.value) {
    activeTab.value = 'yaml'
  }
  try {
    if (props.yaml && props.yaml.trim()) {
      objRef.value = parseYaml(props.yaml)
    } else if (module.value?.template) {
      objRef.value = module.value.template(props.kind, props.namespace || 'default')
    } else {
      objRef.value = minimalTemplate()
    }
    baseObj.value = clone(objRef.value)
    if (module.value?.parse) {
      formData.value = module.value.parse(objRef.value)
    } else {
      formData.value = objRef.value
    }
    // 用 parse 之前的干净快照生成 YAML：部分模块的 parse 会原地给表单态加
    // 展示字段（如 RBAC 规则的 *Text 文本框），objRef 已被污染不能直接 dump
    yamlText.value = dumpYaml(baseObj.value)
  } catch (e) {
    yamlError.value = (e as Error).message
    // YAML 解析失败时退化为纯 YAML 编辑：隐藏表单 tab（formData 未构建，
    // 渲染表单组件会在 {} 上读字段抛 TypeError 白屏）
    formBroken.value = true
    activeTab.value = 'yaml'
    yamlText.value = props.yaml || ''
  }
}

function minimalTemplate(): Record<string, any> {
  return {
    apiVersion: 'v1',
    kind: kindTitle(props.kind),
    metadata: { name: 'my-app', namespace: props.namespace || 'default' },
  }
}

/** 表单变更 -> 重新生成 YAML（防抖） */
function syncFromForm() {
  clearTimeout(syncTimer)
  syncTimer = setTimeout(flushSyncFromForm, 400)
}

/** 立即执行表单 -> YAML 同步（保存前调用：400ms 防抖窗口内点保存，
    不刷新的话 diff 与提交的都是旧 YAML，最后一次编辑被丢掉） */
function flushSyncFromForm() {
  clearTimeout(syncTimer)
  syncTimer = undefined
  try {
    const obj = module.value?.build ? module.value.build(baseObj.value, formData.value) : (formData.value as Record<string, any>)
    yamlText.value = dumpYaml(obj)
    yamlError.value = ''
  } catch (e) {
    yamlError.value = (e as Error).message
  }
}

// 组件型表单直接编辑对象：深度监听 formData 自动同步 YAML
// （组件未 emit change 时也能感知，如 ServiceForm 的接入监控配置）
watch(
  formData,
  () => {
    if (activeTab.value === 'form') syncFromForm()
  },
  { deep: true },
)

// YAML 编辑 -> 同步表单数据
watch(yamlText, (v) => {
  if (activeTab.value !== 'yaml') return
  try {
    const obj = parseYaml(v)
    objRef.value = obj
    if (module.value?.parse) {
      formData.value = module.value.parse(obj)
    } else {
      formData.value = obj
    }
    yamlError.value = ''
  } catch {
    // YAML 不完整时静默，保存时校验
  }
})

async function save() {
  // 表单 tab 下若有未落地的防抖同步（400ms 窗口内点保存），先刷新，
  // 否则 diff/提交的是旧 YAML，最后一次编辑被静默丢掉
  if (activeTab.value === 'form' && syncTimer !== undefined) {
    flushSyncFromForm()
  }
  if (!yamlText.value.trim()) {
    ElMessage.warning('内容不能为空')
    return
  }
  // 校验 YAML 可解析
  try {
    parseYaml(yamlText.value)
  } catch (e) {
    yamlError.value = (e as Error).message
    ElMessage.error('YAML 解析失败: ' + (e as Error).message)
    return
  }
  // 编辑模式且内容有变化：弹出变更对比（既是确认，也方便核对改动）
  if (props.yaml && props.yaml.trim() && props.yaml !== yamlText.value) {
    diffVisible.value = true
    return
  }
  await confirmAndApply()
}

async function doApply() {
  diffVisible.value = false
  await confirmAndApply()
}

async function confirmAndApply() {
  try {
    await ElMessageBox.confirm(props.creating ? '将创建该资源，是否继续？' : '将更新该资源，是否继续？', props.creating ? '创建资源' : '保存修改', { type: 'warning' })
  } catch {
    return
  }
  saving.value = true
  try {
    const { created } = await k8sApi.applyYaml(yamlText.value)
    ElMessage.success(created ? '资源创建成功' : '资源更新成功')
    emit('saved', yamlText.value)
  } finally {
    saving.value = false
  }
}

function kindTitle(kind: string): string {
  const titles: Record<string, string> = {
    deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
    replicasets: 'ReplicaSet', replicationcontrollers: 'ReplicationController',
    cronjobs: 'CronJob', jobs: 'Job', pods: 'Pod', services: 'Service',
    ingresses: 'Ingress', configmaps: 'ConfigMap', secrets: 'Secret',
    persistentvolumeclaims: 'PersistentVolumeClaim', persistentvolumes: 'PersistentVolume',
    storageclasses: 'StorageClass', networkpolicies: 'NetworkPolicy',
    serviceaccounts: 'ServiceAccount', horizontalpodautoscalers: 'HorizontalPodAutoscaler',
    roles: 'Role', rolebindings: 'RoleBinding', clusterroles: 'ClusterRole',
    clusterrolebindings: 'ClusterRoleBinding', resourcequotas: 'ResourceQuota',
    limitranges: 'LimitRange', gatewayclasses: 'GatewayClass', gateways: 'Gateway',
    httproutes: 'HTTPRoute', tcproutes: 'TCPRoute', udproutes: 'UDPRoute',
    grpcroutes: 'GRPCRoute', tlsroutes: 'TLSRoute',
    referencegrants: 'ReferenceGrant', routes: '路由', namespaces: 'Namespace', nodes: 'Node',
  }
  return titles[kind] || kind
}
</script>

<style scoped>
.diff-box { max-height: 56vh; overflow: auto; border: 1px solid var(--kc-border); border-radius: 4px; font: 12px/1.6 ui-monospace, Consolas, monospace; }
.diff-row { display: flex; }
.diff-no { flex: 0 0 42px; text-align: right; padding-right: 8px; color: #909399; user-select: none; }
.diff-text { flex: 1; margin: 0; white-space: pre-wrap; word-break: break-all; }
.diff-add { background: rgba(103, 194, 58, 0.12); }
.diff-del { background: rgba(245, 108, 108, 0.12); text-decoration: line-through; }
</style>
<style scoped>
/* YAML 编辑器高度自适应（随视口） */
.yaml-pane { height: calc(100vh - 360px); min-height: 320px; }
.editor-footer { display: flex; gap: 8px; align-items: center; margin-top: 12px; }
.yaml-error { color: #f56c6c; font-size: 12px; }
.empty-hint { color: #909399; font-size: 13px; padding: 20px 0; text-align: center; }
</style>
