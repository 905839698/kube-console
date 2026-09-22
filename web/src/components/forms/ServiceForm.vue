<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="类型">
        <el-select v-model="o.spec.type" style="width: 220px">
          <el-option v-for="t in ['ClusterIP', 'NodePort', 'LoadBalancer', 'ExternalName']" :key="t" :label="t" :value="t" />
        </el-select>
      </el-form-item>
      <el-form-item label="ClusterIP">
        <el-input v-model="o.spec.clusterIP" placeholder="留空自动分配" />
      </el-form-item>
      <el-form-item label="外部流量">
        <el-select :model-value="o.spec.externalTrafficPolicy" clearable style="width: 200px" placeholder="默认 Cluster"
          @update:model-value="(v) => setTrafficPolicy('externalTrafficPolicy', v)">
          <el-option v-for="t in ['Cluster', 'Local']" :key="t" :label="t" :value="t" />
        </el-select>
      </el-form-item>
      <el-form-item label="内部流量">
        <el-select :model-value="o.spec.internalTrafficPolicy" clearable style="width: 200px" placeholder="默认 Cluster"
          @update:model-value="(v) => setTrafficPolicy('internalTrafficPolicy', v)">
          <el-option v-for="t in ['Cluster', 'Local']" :key="t" :label="t" :value="t" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="o.spec.type === 'LoadBalancer'" label="LB IP">
        <el-input v-model="o.spec.loadBalancerIP" placeholder="指定 LB IP（可选）" />
      </el-form-item>
      <template v-if="o.spec.type !== 'ExternalName'">
        <el-form-item label="选择器">
          <!-- 标签 key/value 级联下拉：选项来自所选命名空间内 Pod 的现有标签，可输入自定义值 -->
          <div style="width: 100%">
            <div v-for="(row, i) in selectorRows" :key="i" class="kv-row">
              <el-select v-model="row.key" size="small" style="width: 46%" filterable allow-create placeholder="标签 key（选择或输入）" @change="row.value = ''">
                <el-option v-for="k in labelKeys" :key="k" :label="k" :value="k" />
              </el-select>
              <span class="sep">=</span>
              <el-select v-model="row.value" size="small" style="width: 46%" filterable allow-create placeholder="标签 value（选择或输入）">
                <el-option v-for="v in nsLabels[row.key] || []" :key="v" :label="v" :value="v" />
              </el-select>
              <el-button size="small" type="danger" text @click="selectorRows.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <el-button size="small" type="primary" plain @click="selectorRows.push({ key: '', value: '' })">
              <el-icon><Plus /></el-icon>添加标签
            </el-button>
            <span class="monitor-hint">选项来自该命名空间下 Pod 的现有标签</span>
          </div>
        </el-form-item>
        <el-form-item label="端口">
          <div v-for="(p, i) in o.spec.ports || []" :key="i" class="kv-row">
            <el-input v-model="p.name" placeholder="名称" size="small" style="width: 16%" />
            <el-input-number v-model="p.port" :min="1" :max="65535" size="small" style="width: 15%" controls-position="right" />
            <span class="sep">→</span>
            <el-input :model-value="targetPortText(p)" placeholder="targetPort（端口或名称）" size="small" style="width: 17%" @update:model-value="(v) => setTargetPort(p, v)" />
            <el-input-number v-if="o.spec.type === 'NodePort'" v-model="p.nodePort" :min="30000" :max="32767" size="small" style="width: 15%" controls-position="right" placeholder="nodePort" />
            <el-select v-model="p.protocol" size="small" style="width: 12%">
              <el-option v-for="t in ['TCP', 'UDP', 'SCTP']" :key="t" :label="t" :value="t" />
            </el-select>
            <el-select v-if="p.protocol === 'TCP'" :model-value="p.appProtocol" clearable size="small" style="width: 12%" placeholder="L7"
              @update:model-value="(v) => setAppProtocol(p, v)">
              <el-option v-for="t in ['HTTP', 'HTTPS', 'auto']" :key="t" :label="t" :value="t" />
            </el-select>
            <el-button size="small" type="danger" text @click="o.spec.ports.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="addPort">
            <el-icon><Plus /></el-icon>添加端口
          </el-button>
        </el-form-item>
        <el-form-item label="会话保持">
          <el-select :model-value="o.spec.sessionAffinity" clearable style="width: 180px"
            @update:model-value="(v) => setSessionAffinity(v)">
            <el-option v-for="t in ['ClientIP']" :key="t" :label="t" :value="t" />
          </el-select>
          <template v-if="o.spec.sessionAffinity === 'ClientIP'">
            <el-input-number
              :model-value="o.spec.sessionAffinityConfig?.clientIP?.timeoutSeconds ?? 10800"
              :min="1" :max="86400" size="small" style="width: 140px; margin-left: 10px" controls-position="right"
              @update:model-value="(v) => setSessionTimeout(v)"
            />
            <span class="monitor-hint">粘滞超时（秒）</span>
          </template>
        </el-form-item>
        <el-form-item label="外部 IP">
          <el-input :model-value="externalIPsText" placeholder="逗号分隔，如 10.0.0.5, 10.0.0.6" style="width: 360px" @update:model-value="setExternalIPs" />
        </el-form-item>
        <el-form-item label="未就绪端点">
          <el-switch v-model="o.spec.publishNotReadyAddresses" />
          <span class="monitor-hint">将未就绪的 Pod 也加入端点（常见于 Headless Service）</span>
        </el-form-item>
      </template>
      <el-form-item v-else label="ExternalName">
        <el-input v-model="o.spec.externalName" placeholder="外部域名" />
      </el-form-item>

      <!-- 接入监控：自动创建 ServiceMonitor -->
      <el-form-item label="接入监控">
        <el-switch v-model="monitorEnabled" />
        <span class="monitor-hint">自动创建 ServiceMonitor（Prometheus 采集 /metrics）</span>
      </el-form-item>
      <template v-if="monitorEnabled">
        <el-form-item label="监控端口">
          <el-select v-model="monitorPort" placeholder="选择端口" style="width: 200px">
            <el-option v-for="p in portNames" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item label="采集间隔">
          <el-input v-model="monitorInterval" placeholder="如 30s" style="width: 120px" />
        </el-form-item>
        <el-form-item label="监控路径">
          <el-input v-model="monitorPath" placeholder="/metrics" style="width: 200px" />
        </el-form-item>
      </template>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'
import { k8sApi } from '../../api'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue', 'change'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

// ---- 标签选择器：行数组 <-> spec.selector 对象；key/value 下拉选项取自命名空间内 Pod 的现有标签 ----
const selectorRows = ref<{ key: string; value: string }[]>([])
const nsLabels = ref<Record<string, string[]>>({})
const labelKeys = computed(() => Object.keys(nsLabels.value).sort())
const nsLabelCache: Record<string, Record<string, string[]>> = {}
let labelsLoading = ''

function syncRowsFromSelector() {
  selectorRows.value = Object.entries(o.value?.spec?.selector || {}).map(([key, value]) => ({ key, value: String(value) }))
}
syncRowsFromSelector()
watch(selectorRows, () => {
  const sel: Record<string, string> = {}
  for (const r of selectorRows.value) {
    if (r.key) sel[r.key] = r.value
  }
  o.value.spec = o.value.spec || {}
  o.value.spec.selector = sel
  emit('change')
}, { deep: true })

// 外部整体替换 selector（如 YAML → 表单切换重解析）时重新同步行；
// 比较时忽略未填 key 的待编辑行，避免自身写回把刚添加的空行清掉
watch(() => o.value?.spec?.selector, (sel) => {
  const rows = Object.entries(sel || {}).map(([key, value]) => ({ key, value: String(value) }))
  const cur = selectorRows.value.filter((r) => r.key)
  if (JSON.stringify(rows) !== JSON.stringify(cur)) selectorRows.value = rows
})

async function ensureLabels(ns: string) {
  if (nsLabelCache[ns]) {
    nsLabels.value = nsLabelCache[ns]
    return
  }
  if (labelsLoading === ns) return
  labelsLoading = ns
  try {
    const pods = await k8sApi.workloads('pods', ns)
    const m: Record<string, Record<string, boolean>> = {}
    for (const p of pods || []) {
      for (const [k, v] of Object.entries(p.labels || {})) {
        ;(m[k] = m[k] || {})[v] = true
      }
    }
    const out: Record<string, string[]> = {}
    for (const [k, vs] of Object.entries(m)) out[k] = Object.keys(vs).sort()
    nsLabelCache[ns] = out
    if ((o.value?.metadata?.namespace || '') === ns) nsLabels.value = out
  } catch {
    nsLabelCache[ns] = {}
  } finally {
    labelsLoading = ''
  }
}
watch(() => o.value?.metadata?.namespace, (ns) => { if (ns) ensureLabels(ns) }, { immediate: true })

// 接入监控配置：写入 Service annotations，后端保存后自动同步 ServiceMonitor
const monitorEnabled = ref(false)
const monitorPort = ref('')
const monitorInterval = ref('30s')
const monitorPath = ref('/metrics')

const portNames = computed(() => (o.value?.spec?.ports || []).map((p: any) => p.name).filter(Boolean))

// clearable 下拉清空时 Element Plus 写回空串，而 K8s 这些字段不允许空值（如
// externalTrafficPolicy: "" 直接被 API 拒绝）——统一改成「留空 = 删字段」
function setTrafficPolicy(key: 'externalTrafficPolicy' | 'internalTrafficPolicy', v: string) {
  o.value.spec = o.value.spec || {}
  if (v) o.value.spec[key] = v
  else delete o.value.spec[key]
}
function setSessionAffinity(v: string) {
  o.value.spec = o.value.spec || {}
  if (v) o.value.spec.sessionAffinity = v
  else {
    delete o.value.spec.sessionAffinity
    delete o.value.spec.sessionAffinityConfig
  }
}
function setAppProtocol(p: any, v: string) {
  if (v) p.appProtocol = v
  else delete p.appProtocol
}

// ExternalName Service 只允许 externalName：切过去时清掉不兼容的残留字段
//（ports/selector 还在对象里会被 API 拒绝，界面上虽然隐藏了端口区但数据仍在）
watch(() => o.value?.spec?.type, (t) => {
  const spec = o.value?.spec
  if (!spec || t !== 'ExternalName') return
  delete spec.ports
  delete spec.selector
  delete spec.sessionAffinity
  delete spec.sessionAffinityConfig
  delete spec.externalIPs
  delete spec.externalTrafficPolicy
  delete spec.internalTrafficPolicy
})

// targetPort 支持端口号或端口名（IntOrString）
function targetPortText(p: any): string {
  return p.targetPort == null ? '' : String(p.targetPort)
}
function setTargetPort(p: any, v: string) {
  if (v === '') {
    delete p.targetPort
    return
  }
  p.targetPort = /^\d+$/.test(v) ? Number(v) : v
}

// ClientIP 粘滞超时
function setSessionTimeout(v: number) {
  o.value.spec.sessionAffinityConfig = o.value.spec.sessionAffinityConfig || {}
  o.value.spec.sessionAffinityConfig.clientIP = { timeoutSeconds: v }
}

// externalIPs 数组 <-> 逗号文本
const externalIPsText = computed(() => (o.value.spec?.externalIPs || []).join(', '))
function setExternalIPs(v: string) {
  const arr = (v || '').split(',').map((s) => s.trim()).filter(Boolean)
  if (arr.length) o.value.spec.externalIPs = arr
  else delete o.value.spec.externalIPs
}

function initMonitor() {
  const ann = o.value?.metadata?.annotations || {}
  monitorEnabled.value = ann['monitoring.coreos.com/servicemonitor'] === 'true'
  monitorPort.value = ann['monitoring.coreos.com/port'] || ''
  monitorInterval.value = ann['monitoring.coreos.com/interval'] || '30s'
  monitorPath.value = ann['monitoring.coreos.com/path'] || '/metrics'
}
initMonitor()

watch([monitorEnabled, monitorPort, monitorInterval, monitorPath], () => {
  const obj = o.value
  if (!obj) return
  obj.metadata = obj.metadata || {}
  obj.metadata.annotations = obj.metadata.annotations || {}
  const ann = obj.metadata.annotations
  if (monitorEnabled.value) {
    ann['monitoring.coreos.com/servicemonitor'] = 'true'
    if (monitorPort.value) ann['monitoring.coreos.com/port'] = monitorPort.value
    if (monitorInterval.value) ann['monitoring.coreos.com/interval'] = monitorInterval.value
    if (monitorPath.value) ann['monitoring.coreos.com/path'] = monitorPath.value
  } else {
    delete ann['monitoring.coreos.com/servicemonitor']
    delete ann['monitoring.coreos.com/port']
    delete ann['monitoring.coreos.com/interval']
    delete ann['monitoring.coreos.com/path']
  }
  emit('change')
})

function ensure() {
  o.value.spec = o.value.spec || {}
  o.value.spec.type = o.value.spec.type || 'ClusterIP'
  o.value.spec.ports = o.value.spec.ports || []
  o.value.spec.selector = o.value.spec.selector || {}
}

function addPort() {
  ensure()
  o.value.spec.ports.push({ name: '', port: 80, targetPort: 80, protocol: 'TCP' })
}
</script>

<style scoped>
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.sep { color: #909399; }
.monitor-hint { color: #909399; font-size: 12px; margin-left: 10px; }
</style>
