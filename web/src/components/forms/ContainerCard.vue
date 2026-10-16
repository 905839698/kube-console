<!-- 容器编辑卡片：供 WorkloadForm 的 containers / initContainers 复用。
     直接对传入容器对象原地编辑（父级深度监听后同步 YAML）。 -->
<template>
  <div class="container-card">
    <div class="container-header">
      <span class="container-title">{{ c.name || (init ? `初始化容器` : `容器`) }}</span>
      <slot name="remove" />
    </div>
    <el-form label-width="110px" size="small">
      <el-row :gutter="8">
        <el-col :span="8"><el-form-item label="名称"><el-input v-model="c.name" /></el-form-item></el-col>
        <el-col :span="10"><el-form-item label="镜像" required>
          <el-input v-model="c.image" placeholder="如 harbor.cqyun.pt.site:8443/library/nginx:1.27" />
          <el-select
            :model-value="imageTag"
            filterable
            allow-create
            default-first-option
            clearable
            size="small"
            :loading="tagLoading"
            :placeholder="tagHint"
            style="width: 100%; margin-top: 4px"
            @change="pickTag"
            @visible-change="(v: boolean) => v && fetchTags()"
          >
            <el-option v-for="t in imageTags" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item></el-col>
        <el-col :span="6"><el-form-item label="镜像拉取">
          <el-select v-model="c.imagePullPolicy" clearable placeholder="默认">
            <el-option v-for="p in ['Always', 'IfNotPresent', 'Never']" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item></el-col>
      </el-row>
      <el-row :gutter="8">
        <el-col :span="12"><el-form-item label="启动命令">
          <el-input v-model="c.command" type="textarea" :rows="2" placeholder="每行一个参数，如&#10;/bin/sh&#10;-c&#10;echo hello" />
        </el-form-item></el-col>
        <el-col :span="12"><el-form-item label="参数">
          <el-input v-model="c.args" type="textarea" :rows="2" placeholder="每行一个参数" />
        </el-form-item></el-col>
      </el-row>
      <el-form-item label="工作目录"><el-input v-model="c.workingDir" placeholder="可选，如 /app" style="width: 300px" /></el-form-item>

      <el-form-item label="环境变量">
        <div class="env-list">
          <div v-for="(e, ei) in c.envList" :key="ei" class="env-row">
            <el-input v-model="e.name" placeholder="变量名" size="small" style="width: 24%" />
            <el-select v-model="e.sourceType" size="small" style="width: 22%" @change="onSourceType(e)">
              <el-option label="文本值" value="value" />
              <el-option label="ConfigMap 键" value="configMapKeyRef" />
              <el-option label="Secret 键" value="secretKeyRef" />
              <el-option label="Pod 字段" value="fieldRef" />
            </el-select>
            <el-input v-if="e.sourceType === 'value'" v-model="e.value" placeholder="值" size="small" style="flex: 1" />
            <template v-else-if="e.sourceType === 'configMapKeyRef' || e.sourceType === 'secretKeyRef'">
              <el-select
                v-model="e.refName"
                filterable
                allow-create
                default-first-option
                clearable
                size="small"
                style="flex: 1"
                :loading="refNamesBusy(envRefKind(e))"
                :placeholder="e.sourceType === 'configMapKeyRef' ? 'ConfigMap 名（展开自动列出）' : 'Secret 名（展开自动列出）'"
                @visible-change="(v: boolean) => v && loadRefNames(envRefKind(e))"
                @change="() => { e.key = ''; if (e.refName) loadRefKeys(envRefKind(e), e.refName) }"
              >
                <el-option v-for="n in refNamesOf(envRefKind(e))" :key="n" :label="n" :value="n" />
              </el-select>
              <el-select
                v-model="e.key"
                filterable
                allow-create
                default-first-option
                clearable
                size="small"
                style="flex: 1"
                :loading="refKeysBusy(envRefKind(e), e.refName)"
                placeholder="键（展开自动列出）"
                @visible-change="(v: boolean) => v && e.refName && loadRefKeys(envRefKind(e), e.refName)"
              >
                <el-option v-for="k in refKeysOf(envRefKind(e), e.refName)" :key="k" :label="k" :value="k" />
              </el-select>
            </template>
            <el-select v-else-if="e.sourceType === 'fieldRef'" v-model="e.fieldPath" filterable allow-create default-first-option size="small" style="flex: 1" placeholder="如 metadata.namespace">
              <el-option v-for="f in FIELD_PATHS" :key="f" :label="f" :value="f" />
            </el-select>
            <el-button size="small" type="danger" text @click="c.envList.splice(ei, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="addEnv"><el-icon><Plus /></el-icon>添加变量</el-button>
        </div>
      </el-form-item>

      <el-form-item label="整体引用">
        <div class="env-list">
          <div v-for="(e, ei) in c.envFrom" :key="ei" class="env-row">
            <el-select v-model="e.type" size="small" style="width: 26%">
              <el-option label="ConfigMap 全部键" value="configMap" />
              <el-option label="Secret 全部键" value="secret" />
            </el-select>
            <el-input v-model="e.name" placeholder="名称" size="small" style="flex: 1" />
            <el-input v-model="e.prefix" placeholder="键前缀（可选）" size="small" style="width: 32%" />
            <el-button size="small" type="danger" text @click="c.envFrom.splice(ei, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="(c.envFrom || (c.envFrom = [])).push({ type: 'configMap', name: '', prefix: '' })">
            <el-icon><Plus /></el-icon>添加引用
          </el-button>
        </div>
      </el-form-item>

      <el-form-item label="端口">
        <div class="port-list">
          <div v-for="(p, pi) in c.ports" :key="pi" class="kv-row">
            <el-input v-model="p.name" placeholder="名称" size="small" style="width: 24%" />
            <el-input-number v-model="p.containerPort" :min="1" :max="65535" size="small" style="width: 24%" placeholder="容器端口" controls-position="right" />
            <el-input-number v-model="p.hostPort" :min="0" :max="65535" size="small" style="width: 22%" placeholder="主机端口(0=不映射)" controls-position="right" />
            <el-select v-model="p.protocol" size="small" style="width: 20%">
              <el-option v-for="pt in ['TCP', 'UDP', 'SCTP']" :key="pt" :label="pt" :value="pt" />
            </el-select>
            <el-button size="small" type="danger" text @click="c.ports.splice(pi, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="(c.ports || (c.ports = [])).push({ name: '', containerPort: 80, hostPort: 0, protocol: 'TCP' })">
            <el-icon><Plus /></el-icon>添加端口
          </el-button>
        </div>
      </el-form-item>

      <el-form-item label="资源">
        <div class="res-grid">
          <div class="res-item"><span>CPU 请求</span><el-input v-model="c.cpuRequest" placeholder="如 100m" :class="{ 'qty-err': qtyErr(c.cpuRequest, 'cpu') }" /><em v-if="qtyErr(c.cpuRequest, 'cpu')" class="qty-msg">{{ qtyErr(c.cpuRequest, 'cpu') }}</em></div>
          <div class="res-item"><span>内存请求</span><el-input v-model="c.memoryRequest" placeholder="如 128Mi" :class="{ 'qty-err': qtyErr(c.memoryRequest, 'memory') }" /><em v-if="qtyErr(c.memoryRequest, 'memory')" class="qty-msg">{{ qtyErr(c.memoryRequest, 'memory') }}</em></div>
          <div class="res-item"><span>存储请求</span><el-input v-model="c.storageRequest" placeholder="ephemeral-storage，可选" :class="{ 'qty-err': qtyErr(c.storageRequest, 'storage') }" /><em v-if="qtyErr(c.storageRequest, 'storage')" class="qty-msg">{{ qtyErr(c.storageRequest, 'storage') }}</em></div>
          <div class="res-item"><span>CPU 上限</span><el-input v-model="c.cpuLimit" placeholder="如 500m" :class="{ 'qty-err': qtyErr(c.cpuLimit, 'cpu') }" /><em v-if="qtyErr(c.cpuLimit, 'cpu')" class="qty-msg">{{ qtyErr(c.cpuLimit, 'cpu') }}</em></div>
          <div class="res-item"><span>内存上限</span><el-input v-model="c.memoryLimit" placeholder="如 256Mi" :class="{ 'qty-err': qtyErr(c.memoryLimit, 'memory') }" /><em v-if="qtyErr(c.memoryLimit, 'memory')" class="qty-msg">{{ qtyErr(c.memoryLimit, 'memory') }}</em></div>
          <div class="res-item"><span>存储上限</span><el-input v-model="c.storageLimit" placeholder="ephemeral-storage，可选" :class="{ 'qty-err': qtyErr(c.storageLimit, 'storage') }" /><em v-if="qtyErr(c.storageLimit, 'storage')" class="qty-msg">{{ qtyErr(c.storageLimit, 'storage') }}</em></div>
        </div>
      </el-form-item>

      <el-form-item label="挂载卷">
        <div class="mount-list">
          <div v-for="(m, mi) in c.volumeMounts" :key="mi" class="kv-row">
            <el-select v-model="m.name" size="small" style="width: 25%" placeholder="卷名">
              <el-option v-for="v in volumes" :key="v.name" :label="v.name" :value="v.name" />
            </el-select>
            <el-input v-model="m.mountPath" placeholder="挂载路径" size="small" style="width: 28%" />
            <el-input v-model="m.subPath" placeholder="子路径" size="small" style="width: 18%" />
            <el-checkbox v-model="m.readOnly" size="small">只读</el-checkbox>
            <el-button size="small" type="danger" text @click="c.volumeMounts.splice(mi, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="(c.volumeMounts || (c.volumeMounts = [])).push({ name: '', mountPath: '', subPath: '', readOnly: false })">
            <el-icon><Plus /></el-icon>添加挂载
          </el-button>
        </div>
      </el-form-item>

      <el-form-item label="就绪探针"><ProbeEditor v-model="c.readinessProbe" /></el-form-item>
      <el-form-item label="存活探针"><ProbeEditor v-model="c.livenessProbe" /></el-form-item>
      <el-form-item label="启动探针"><ProbeEditor v-model="c.startupProbe" /></el-form-item>

      <el-form-item label="启动后命令">
        <ProbeEditor v-model="c.postStart" :title-only="true" />
      </el-form-item>
      <el-form-item label="终止前命令">
        <ProbeEditor v-model="c.preStop" :title-only="true" />
      </el-form-item>

      <el-form-item label="安全上下文">
        <div class="sec-grid">
          <div class="res-item"><span>运行用户 (UID)</span><el-input v-model="c.sc.runAsUser" placeholder="如 1000，留空不设置" /></div>
          <div class="res-item"><span>运行组 (GID)</span><el-input v-model="c.sc.runAsGroup" placeholder="可选" /></div>
          <el-checkbox v-model="c.sc.privileged">特权模式</el-checkbox>
          <el-checkbox v-model="c.sc.runAsNonRoot">禁止 root 运行</el-checkbox>
          <el-checkbox v-model="c.sc.readOnlyRootFilesystem">根文件系统只读</el-checkbox>
          <div class="res-item"><span>额外能力 (add)</span><el-input v-model="c.sc.capAdd" placeholder="逗号分隔，如 NET_ADMIN" /></div>
          <div class="res-item"><span>移除能力 (drop)</span><el-input v-model="c.sc.capDrop" placeholder="逗号分隔，如 ALL" /></div>
        </div>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import ProbeEditor from './ProbeEditor.vue'
import { k8sApi, registryApi } from '../../api'
import { quantityError } from '../../utils/kube-validators'
import { load as yamlLoad } from 'js-yaml'

const props = defineProps<{ c: any; volumes: any[]; init?: boolean; namespace?: string }>()

const qtyErr = (v: any, kind: 'cpu' | 'memory' | 'storage') => quantityError(v, kind)

// ---- 镜像 Tag 自动下拉：输入镜像后自动拉取该仓库的可选 Tag（Harbor） ----
const imageTags = ref<string[]>([])
const tagLoading = ref(false)
const tagHint = ref('Tag（展开自动拉取）')
const imageBase = computed(() => {
  const img = String(props.c.image || '').trim()
  if (!img.includes('/')) return ''
  if (img.includes('@')) return img.split('@')[0]
  const colon = img.lastIndexOf(':')
  const slash = img.lastIndexOf('/')
  return colon > slash ? img.slice(0, colon) : img
})
const imageTag = computed(() => {
  const img = String(props.c.image || '').trim()
  return img && imageBase.value && img !== imageBase.value ? img.slice(imageBase.value.length + 1) : ''
})

let tagTimer: ReturnType<typeof setTimeout> | undefined
watch(
  () => props.c.image,
  () => {
    if (tagTimer) clearTimeout(tagTimer)
    tagTimer = setTimeout(fetchTags, 700) // 输入防抖
  },
  { immediate: true },
)

async function fetchTags() {
  const base = imageBase.value
  if (!base) return
  // 无 registry 前缀（如 nginx）的镜像不属于已配置仓库，不发请求
  if (!base.includes('/')) {
    imageTags.value = []
    tagHint.value = 'Tag（镜像未带仓库前缀，不查询）'
    return
  }
  tagLoading.value = true
  tagHint.value = 'Tag 拉取中...'
  try {
    const r = await registryApi.imageTagsQuiet(base)
    imageTags.value = r?.tags || []
    tagHint.value = imageTags.value.length ? `Tag（${imageTags.value.length} 个可用）` : 'Tag（仓库中无 Tag）'
  } catch {
    imageTags.value = []
    tagHint.value = 'Tag（未配置镜像仓库或地址不在仓库中）'
  } finally {
    tagLoading.value = false
  }
}
onBeforeUnmount(() => {
  if (tagTimer) clearTimeout(tagTimer)
})

function pickTag(tag: string) {
  if (!imageBase.value) return
  // 清空 tag（下拉 clearable）时把镜像上的旧 tag 一并去掉，而不是留着一个看似选了又没生效的值
  props.c.image = tag ? `${imageBase.value}:${tag}` : imageBase.value
}

function addEnv() {
  ;(props.c.envList || (props.c.envList = [])).push({ name: '', sourceType: 'value', value: '', refName: '', key: '', fieldPath: '' })
}

// 切换来源类型时清空不相关字段，避免脏数据写回 YAML
function onSourceType(e: any) {
  e.value = ''
  e.refName = ''
  e.key = ''
  e.fieldPath = ''
  if (e.sourceType === 'secretKeyRef' || e.sourceType === 'configMapKeyRef') loadRefNames(envRefKind(e))
}

// ---- 环境变量引用下拉：Secret/ConfigMap 名称与键自动列出，Pod 字段给常用 fieldPath ----
const FIELD_PATHS = [
  'metadata.name', 'metadata.namespace', 'metadata.uid', 'metadata.generateName',
  'metadata.labels', 'metadata.annotations',
  'spec.nodeName', 'spec.serviceAccountName',
  'status.hostIP', 'status.podIP', 'status.podIPs',
]

// 列表按 命名空间/kind 缓存、键按 命名空间/kind/名称 缓存。
// busy 必须是响应式：loading prop 若读普通对象，请求完成后不会有渲染重算它，
// 下拉会永久停在「加载中」（真机复现过）。失败不写缓存，下次展开可重试。
const refNames = ref<Record<string, string[]>>({})
const refKeys = ref<Record<string, string[]>>({})
const refBusy = ref<Record<string, boolean>>({})
function setRefBusy(ck: string, v: boolean) {
  refBusy.value = { ...refBusy.value, [ck]: v }
}

function envRefKind(e: any) {
  return e.sourceType === 'secretKeyRef' ? 'secrets' : 'configmaps'
}
function refNamesOf(kind: string) {
  return refNames.value[`${props.namespace || ''}/${kind}`] || []
}
function refKeysOf(kind: string, name: string) {
  return refKeys.value[`${props.namespace || ''}/${kind}/${name}`] || []
}
function refNamesBusy(kind: string) {
  return !!refBusy.value[`${props.namespace || ''}/${kind}`]
}
function refKeysBusy(kind: string, name: string) {
  return !!refBusy.value[`${props.namespace || ''}/${kind}/${name}`]
}

async function loadRefNames(kind: string) {
  const ns = props.namespace || ''
  const ck = `${ns}/${kind}`
  if (!ns || refNames.value[ck] || refBusy.value[ck]) return
  setRefBusy(ck, true)
  try {
    const list = await k8sApi.resources(kind, ns)
    refNames.value = { ...refNames.value, [ck]: list.map((x) => x.name) }
  } catch {
    /* 失败不缓存：保持可重试 */
  } finally {
    setRefBusy(ck, false)
  }
}

async function loadRefKeys(kind: string, name: string) {
  const ns = props.namespace || ''
  const ck = `${ns}/${kind}/${name}`
  if (!ns || !name || refKeys.value[ck] || refBusy.value[ck]) return
  setRefBusy(ck, true)
  try {
    const r = await k8sApi.resourceYaml(kind, ns, name)
    const obj: any = yamlLoad(r.yaml) || {}
    const keys = [
      ...Object.keys(obj.data || {}),
      ...Object.keys(obj.binaryData || {}),
      ...Object.keys(obj.stringData || {}),
    ]
    refKeys.value = { ...refKeys.value, [ck]: [...new Set(keys)] }
  } catch {
    /* 失败不缓存：保持可重试 */
  } finally {
    setRefBusy(ck, false)
  }
}

// 打开编辑时预取已有引用行的名称/键，下拉展开即有选项
onMounted(() => {
  for (const e of props.c.envList || []) {
    if (e.sourceType === 'secretKeyRef' || e.sourceType === 'configMapKeyRef') {
      loadRefNames(envRefKind(e))
      if (e.refName) loadRefKeys(envRefKind(e), e.refName)
    }
  }
})
</script>

<style scoped>
.container-card { border: 1px solid var(--el-border-color-light); border-radius: 6px; padding: 12px; margin-bottom: 12px; }
.container-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.container-title { font-weight: 600; color: var(--el-text-color-primary); }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; width: 100%; }
.env-list, .port-list, .mount-list { width: 100%; }
.env-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; width: 100%; }
.res-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 8px; width: 100%; }
.sec-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 8px; width: 100%; align-items: center; }
.res-item { display: flex; flex-direction: column; gap: 2px; }
.res-item > span { font-size: 13px; color: var(--el-text-color-secondary); }
.qty-err :deep(.el-input__inner) { color: var(--el-color-danger); }
.qty-msg { color: var(--el-color-danger); font-size: 12px; font-style: normal; line-height: 1.4; }
</style>
