<template>
  <div class="workload-form">
    <el-collapse v-model="openSections" :key="listKey">
      <!-- 基本信息 -->
      <el-collapse-item title="基本信息" name="basic">
        <el-form label-width="110px" size="small">
          <el-form-item label="名称"><el-input v-model="d.name" /></el-form-item>
          <el-form-item label="命名空间"><el-input v-model="d.namespace" /></el-form-item>
          <el-form-item label="标签">
            <KvEditor v-model="d.labels" key-placeholder="key" value-placeholder="value" />
          </el-form-item>
          <el-form-item label="注解">
            <KvEditor v-model="d.annotations" key-placeholder="key" value-placeholder="value" />
          </el-form-item>
          <el-form-item label="副本数" v-if="hasReplicas">
            <el-input-number v-model="d.replicas" :min="0" :max="500" />
          </el-form-item>
          <el-form-item label="调度" v-if="kind === 'cronjobs'">
            <el-input v-model="d.schedule" placeholder="cron 表达式，如 */5 * * * *" />
          </el-form-item>
          <el-form-item label="时区" v-if="kind === 'cronjobs'">
            <el-input v-model="d.timeZone" placeholder="如 Asia/Shanghai（可选）" style="width: 240px" />
          </el-form-item>
          <el-form-item label="启动窗口" v-if="kind === 'cronjobs'">
            <el-input v-model="d.startingDeadlineSeconds" placeholder="秒，可选" style="width: 200px" />
          </el-form-item>
          <el-form-item label="并发策略" v-if="kind === 'cronjobs'">
            <el-select v-model="d.concurrencyPolicy" style="width: 200px">
              <el-option v-for="o in ['Allow', 'Forbid', 'Replace']" :key="o" :label="o" :value="o" />
            </el-select>
          </el-form-item>
          <el-form-item label="并行度" v-if="kind === 'jobs'">
            <el-input-number v-model="d.parallelism" :min="0" :max="500" />
          </el-form-item>
          <el-form-item label="完成数" v-if="kind === 'jobs'">
            <el-input-number v-model="d.completions" :min="0" :max="500" />
          </el-form-item>
          <el-form-item label="完成模式" v-if="kind === 'jobs'">
            <el-select v-model="d.completionMode" style="width: 200px">
              <el-option v-for="o in ['NonIndexed', 'Indexed']" :key="o" :label="o" :value="o" />
            </el-select>
          </el-form-item>
          <el-form-item label="失败重试" v-if="kind === 'jobs' || kind === 'cronjobs'">
            <el-input-number v-model="d.backoffLimit" :min="0" :max="100" />
          </el-form-item>
          <el-form-item label="保留时长" v-if="kind === 'jobs'">
            <el-input v-model="d.ttlSecondsAfterFinished" placeholder="完成后保留秒数（可选）" style="width: 200px" />
          </el-form-item>
          <el-form-item label="运行时限" v-if="kind === 'jobs'">
            <el-input v-model="d.activeDeadlineSeconds" placeholder="总运行时限秒数（可选）" style="width: 200px" />
          </el-form-item>
          <el-form-item label="重启策略" v-if="kind === 'jobs' || kind === 'cronjobs'">
            <el-select v-model="d.restartPolicy" style="width: 200px">
              <el-option v-for="o in ['Always', 'OnFailure', 'Never']" :key="o" :label="o" :value="o" />
            </el-select>
          </el-form-item>
          <el-form-item label="暂停" v-if="kind === 'cronjobs'">
            <el-switch v-model="d.suspend" />
          </el-form-item>
          <el-form-item label="标签选择器" v-if="hasSelector">
            <KvEditor v-model="d.selector" key-placeholder="key" value-placeholder="value" />
          </el-form-item>
          <el-form-item label="ServiceAccount">
            <el-input v-model="d.serviceAccountName" placeholder="默认使用 default" />
          </el-form-item>
          <!-- Deployment 专属 -->
          <template v-if="kind === 'deployments'">
            <el-form-item label="最小就绪"><el-input-number v-model="d.minReadySeconds" :min="0" :max="600" /></el-form-item>
            <el-form-item label="历史版本数"><el-input-number v-model="d.revisionHistoryLimit" :min="0" :max="100" /></el-form-item>
            <el-form-item label="进度超时"><el-input-number v-model="d.progressDeadlineSeconds" :min="10" :max="3600" /></el-form-item>
          </template>
          <!-- StatefulSet 专属 -->
          <template v-if="kind === 'statefulsets'">
            <el-form-item label="Headless SVC"><el-input v-model="d.serviceName" placeholder=" governing Service 名称" style="width: 260px" /></el-form-item>
            <el-form-item label="副本管理">
              <el-select v-model="d.podManagementPolicy" style="width: 200px">
                <el-option v-for="o in ['OrderedReady', 'Parallel']" :key="o" :label="o" :value="o" />
              </el-select>
            </el-form-item>
          </template>
          <!-- StatefulSet / DaemonSet 更新策略 -->
          <el-form-item label="更新策略" v-if="kind === 'statefulsets' || kind === 'daemonsets'">
            <el-select v-model="d.updateStrategyType" style="width: 200px">
              <el-option v-for="o in (kind === 'statefulsets' ? ['RollingUpdate', 'OnDelete'] : ['RollingUpdate', 'OnDelete'])" :key="o" :label="o" :value="o" />
            </el-select>
            <el-input-number v-if="kind === 'statefulsets' && d.updateStrategyType === 'RollingUpdate'" v-model="d.updatePartition" :min="0" :max="500" style="margin-left: 10px" />
            <span v-if="kind === 'statefulsets' && d.updateStrategyType === 'RollingUpdate'" class="hint">partition（灰度分区）</span>
          </el-form-item>
        </el-form>
      </el-collapse-item>

      <!-- 初始化容器 -->
      <el-collapse-item :title="`初始化容器 (${(d.initContainers || []).length})`" name="initContainers">
        <ContainerCard
          v-for="(c, ci) in d.initContainers"
          :key="'init-' + ci"
          :c="c"
          :volumes="d.volumes"
          :init="true"
        >
          <template #remove>
            <el-button size="small" type="danger" text @click="d.initContainers.splice(ci, 1)">
              <el-icon><Delete /></el-icon>移除
            </el-button>
          </template>
        </ContainerCard>
        <el-button type="primary" plain size="small" @click="addInitContainer">
          <el-icon><Plus /></el-icon>&nbsp;添加初始化容器
        </el-button>
      </el-collapse-item>

      <!-- 容器 -->
      <el-collapse-item :title="`容器 (${(d.containers || []).length})`" name="containers">
        <ContainerCard
          v-for="(c, ci) in d.containers"
          :key="'c-' + ci"
          :c="c"
          :volumes="d.volumes"
        >
          <template #remove>
            <el-button size="small" type="danger" text @click="removeContainer(ci)">
              <el-icon><Delete /></el-icon>移除
            </el-button>
          </template>
        </ContainerCard>
        <el-button type="primary" plain size="small" @click="addContainer">
          <el-icon><Plus /></el-icon>&nbsp;添加容器
        </el-button>
      </el-collapse-item>

      <!-- 卷 -->
      <el-collapse-item :title="`卷 (${d.volumes.length})`" name="volumes">
        <div v-for="(v, vi) in (d.volumes || [])" :key="vi" class="kv-row">
          <el-input v-model="v.name" placeholder="卷名称" size="small" style="width: 20%" />
          <el-select v-model="v.type" size="small" style="width: 20%">
            <el-option v-for="o in ['pvc', 'emptyDir', 'configMap', 'secret', 'hostPath']" :key="o" :label="o" :value="o" />
          </el-select>
          <el-input v-if="v.type === 'pvc'" v-model="v.pvcName" placeholder="PVC 名称" size="small" style="width: 25%" />
          <el-input v-else-if="v.type === 'configMap'" v-model="v.configMapName" placeholder="ConfigMap 名称" size="small" style="width: 25%" />
          <el-input v-else-if="v.type === 'secret'" v-model="v.secretName" placeholder="Secret 名称" size="small" style="width: 25%" />
          <el-input v-else-if="v.type === 'hostPath'" v-model="v.hostPath" placeholder="宿主机路径" size="small" style="width: 25%" />
          <el-input v-else disabled placeholder="临时目录" size="small" style="width: 25%" />
          <el-button size="small" type="danger" text @click="(d.volumes || (d.volumes = [])).splice(vi, 1)"><el-icon><Delete /></el-icon></el-button>
        </div>
        <el-button size="small" type="primary" plain @click="addVolume">
          <el-icon><Plus /></el-icon>&nbsp;添加卷
        </el-button>
      </el-collapse-item>

      <!-- StatefulSet 卷声明模板 -->
      <el-collapse-item v-if="kind === 'statefulsets'" :title="`卷声明模板 (${(d.volumeClaimTemplates || []).length})`" name="vct">
        <div v-for="(v, vi) in d.volumeClaimTemplates" :key="vi" class="vct-row">
          <el-input v-model="v.name" placeholder="模板名称" size="small" style="width: 18%" />
          <el-input v-model="v.storageClass" placeholder="存储类（可选）" size="small" style="width: 22%" />
          <el-input v-model="v.accessModesText" placeholder="访问模式，如 ReadWriteOnce" size="small" style="width: 30%" />
          <el-input v-model="v.size" placeholder="容量，如 10Gi" size="small" style="width: 16%" />
          <el-button size="small" type="danger" text @click="d.volumeClaimTemplates.splice(vi, 1)"><el-icon><Delete /></el-icon></el-button>
        </div>
        <el-button size="small" type="primary" plain @click="(d.volumeClaimTemplates || (d.volumeClaimTemplates = [])).push({ name: '', storageClass: '', accessModesText: 'ReadWriteOnce', size: '10Gi' })">
          <el-icon><Plus /></el-icon>&nbsp;添加卷声明模板
        </el-button>
      </el-collapse-item>

      <!-- 镜像与凭证 -->
      <el-collapse-item title="镜像与凭证" name="pull">
        <el-form label-width="110px" size="small">
          <el-form-item label="拉取密钥">
            <div class="kv-row-column">
              <div v-for="(s, si) in d.imagePullSecrets" :key="si" class="kv-row">
                <el-input v-model="s.name" placeholder="docker-registry Secret 名称（私有仓库凭证）" size="small" style="width: 70%" />
                <el-button size="small" type="danger" text @click="d.imagePullSecrets.splice(si, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="d.imagePullSecrets.push({ name: '' })">
                <el-icon><Plus /></el-icon>&nbsp;添加拉取密钥
              </el-button>
            </div>
          </el-form-item>
        </el-form>
      </el-collapse-item>

      <!-- 主机与网络 -->
      <el-collapse-item title="主机与网络" name="hostnet">
        <el-form label-width="110px" size="small">
          <el-form-item label="主机网络"><el-switch v-model="d.hostNetwork" /></el-form-item>
          <el-form-item label="主机 PID"><el-switch v-model="d.hostPID" /></el-form-item>
          <el-form-item label="主机 IPC"><el-switch v-model="d.hostIPC" /></el-form-item>
          <el-form-item label="DNS 策略">
            <el-select v-model="d.dnsPolicy" clearable placeholder="默认 ClusterFirst" style="width: 240px">
              <el-option v-for="o in ['ClusterFirst', 'Default', 'ClusterFirstWithHostNet', 'None']" :key="o" :label="o" :value="o" />
            </el-select>
          </el-form-item>
          <el-form-item label="优雅终止">
            <el-input-number v-model="d.terminationGracePeriodSeconds" :min="0" :max="3600" />
            <span class="hint">秒</span>
          </el-form-item>
          <el-form-item label="Hosts 映射">
            <div class="kv-row-column">
              <div v-for="(a, ai) in d.hostAliases" :key="ai" class="kv-row">
                <el-input v-model="a.ip" placeholder="IP，如 10.0.0.1" size="small" style="width: 24%" />
                <el-input v-model="a.hostnamesText" placeholder="主机名，逗号分隔" size="small" style="width: 52%" />
                <el-button size="small" type="danger" text @click="d.hostAliases.splice(ai, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="d.hostAliases.push({ ip: '', hostnamesText: '' })">
                <el-icon><Plus /></el-icon>&nbsp;添加映射
              </el-button>
            </div>
          </el-form-item>
        </el-form>
      </el-collapse-item>

      <!-- 安全上下文 -->
      <el-collapse-item title="安全上下文 (Pod)" name="podsec">
        <el-form label-width="110px" size="small">
          <el-form-item label="运行用户">
            <el-input v-model="d.podSc.runAsUser" placeholder="runAsUser，如 1000" style="width: 200px" />
          </el-form-item>
          <el-form-item label="运行组">
            <el-input v-model="d.podSc.runAsGroup" placeholder="runAsGroup（可选）" style="width: 200px" />
          </el-form-item>
          <el-form-item label="文件组">
            <el-input v-model="d.podSc.fsGroup" placeholder="fsGroup（挂载卷组权限）" style="width: 200px" />
          </el-form-item>
          <el-form-item label="禁止 root">
            <el-switch v-model="d.podSc.runAsNonRoot" />
          </el-form-item>
          <el-form-item label="优先级类">
            <el-input v-model="d.priorityClassName" placeholder="PriorityClassName（可选）" style="width: 240px" />
          </el-form-item>
        </el-form>
      </el-collapse-item>

      <!-- 接入监控：自动创建 PodMonitor（直接采集 Pod /metrics） -->
      <el-collapse-item title="接入监控" name="monitor">
        <el-form label-width="110px" size="small">
          <el-form-item label="启用监控">
            <el-switch v-model="monitorEnabled" />
            <span class="monitor-hint">自动创建 PodMonitor，直接采集 Pod /metrics（无需 Service）</span>
          </el-form-item>
          <template v-if="monitorEnabled">
            <el-form-item label="监控端口">
              <el-select v-model="monitorPort" placeholder="选择容器端口名" style="width: 200px">
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
      </el-collapse-item>

      <!-- 调度 -->
      <el-collapse-item title="调度与更新策略" name="schedule">
        <el-form label-width="110px" size="small">
          <el-form-item label="节点选择">
            <KvEditor v-model="d.nodeSelector" key-placeholder="key" value-placeholder="value" />
          </el-form-item>
          <el-form-item label="节点亲和性">
            <div class="kv-row-column">
              <div class="hint" style="margin-bottom: 4px">required 匹配规则（operator 默认 In，值逗号分隔）</div>
              <div v-for="(r, ri) in d.nodeAffinityRows" :key="ri" class="kv-row">
                <el-input v-model="r.key" placeholder="标签 key，如 kubernetes.io/os" size="small" style="width: 34%" />
                <el-select v-model="r.operator" size="small" style="width: 24%">
                  <el-option v-for="o in ['In', 'NotIn', 'Exists', 'DoesNotExist', 'Gt', 'Lt']" :key="o" :label="o" :value="o" />
                </el-select>
                <el-input v-model="r.valuesText" placeholder="值，逗号分隔（Exists 时留空）" size="small" style="width: 32%" />
                <el-button size="small" type="danger" text @click="d.nodeAffinityRows.splice(ri, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="d.nodeAffinityRows.push({ key: '', operator: 'In', valuesText: '' })">
                <el-icon><Plus /></el-icon>&nbsp;添加匹配规则
              </el-button>
            </div>
          </el-form-item>
          <el-form-item label="Pod 反亲和">
            <div class="kv-row-column">
              <div class="hint" style="margin-bottom: 4px">按拓扑域打散（preferred）：填拓扑键（如 kubernetes.io/hostname）</div>
              <div class="kv-row">
                <el-input v-model="d.antiTopologyKey" placeholder="拓扑键，如 kubernetes.io/hostname" size="small" style="width: 40%" />
                <span class="hint">匹配标签（可选）：</span>
                <KvEditor v-model="d.antiMatchLabels" key-placeholder="key" value-placeholder="value" />
              </div>
            </div>
          </el-form-item>
          <el-form-item label="容忍">
            <div class="kv-row-column">
              <div v-for="(t, ti) in (d.tolerations || [])" :key="ti" class="kv-row">
                <el-input v-model="t.key" placeholder="key" size="small" style="width: 25%" />
                <el-select v-model="t.operator" size="small" style="width: 22%">
                  <el-option v-for="o in ['Exists', 'Equal']" :key="o" :label="o" :value="o" />
                </el-select>
                <el-input v-model="t.value" placeholder="value" size="small" style="width: 20%" />
                <el-select v-model="t.effect" size="small" style="width: 22%">
                  <el-option v-for="o in ['NoSchedule', 'PreferNoSchedule', 'NoExecute']" :key="o" :label="o" :value="o" />
                </el-select>
                <el-button size="small" type="danger" text @click="(d.tolerations || (d.tolerations = [])).splice(ti, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="(d.tolerations || (d.tolerations = [])).push({ key: '', operator: 'Exists', value: '', effect: 'NoSchedule' })">
                <el-icon><Plus /></el-icon>添加容忍
              </el-button>
            </div>
          </el-form-item>
          <el-form-item label="更新策略" v-if="hasStrategy">
            <el-select v-model="d.strategy.type" style="width: 200px">
              <el-option v-for="o in strategyOptions" :key="o" :label="o" :value="o" />
            </el-select>
          </el-form-item>
          <template v-if="kind === 'deployments' && d.strategy.type === 'RollingUpdate'">
            <el-form-item label="最大不可用">
              <el-input v-model="d.strategy.maxUnavailable" placeholder="如 25% 或 1" style="width: 200px" />
            </el-form-item>
            <el-form-item label="最大超出">
              <el-input v-model="d.strategy.maxSurge" placeholder="如 25% 或 1" style="width: 200px" />
            </el-form-item>
          </template>
        </el-form>
      </el-collapse-item>
    </el-collapse>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'
import KvEditor from './KvEditor.vue'
import ProbeEditor from './ProbeEditor.vue'
import ContainerCard from './ContainerCard.vue'

const props = defineProps<{ modelValue: any; kind: string }>()
const emit = defineEmits(['update:modelValue'])

const d = reactive<any>({})
const openSections = ref(['basic', 'containers'])

// 列表结构变化（增删 容器/卷/容忍/端口/挂载）时，用变化后的 key 强制重建 el-collapse，
// 确保新增项立即渲染。仅在结构变化时重建，输入字段值变化不受影响（不丢焦点）。
const listKey = computed(
  () =>
    [
      (d.volumes || []).length,
      (d.containers || []).length,
      (d.initContainers || []).length,
      (d.tolerations || []).length,
      (d.imagePullSecrets || []).length,
      (d.hostAliases || []).length,
      (d.nodeAffinityRows || []).length,
      (d.volumeClaimTemplates || []).length,
      (d.containers || []).map((c: any) => (c.ports || []).length + 'x' + (c.volumeMounts || []).length + 'x' + (c.envList || []).length).join(','),
    ].join('|'),
)

// 接入监控（PodMonitor）：配置写入 annotations，后端保存后自动同步
const monitorEnabled = ref(false)
const monitorPort = ref('')
const monitorInterval = ref('30s')
const monitorPath = ref('/metrics')

const portNames = computed(() =>
  (d.containers || []).flatMap((c: any) => (c.ports || []).map((p: any) => p.name).filter(Boolean)),
)

// 从 annotations 读回监控配置
watch(
  () => d.annotations,
  (ann) => {
    monitorEnabled.value = ann?.['monitoring.coreos.com/podmonitor'] === 'true'
    monitorPort.value = ann?.['monitoring.coreos.com/port'] || ''
    monitorInterval.value = ann?.['monitoring.coreos.com/interval'] || '30s'
    monitorPath.value = ann?.['monitoring.coreos.com/path'] || '/metrics'
  },
  { immediate: true },
)

// 监控配置变化 -> 写回 annotations（进入 YAML，后端自动同步 PodMonitor）
watch([monitorEnabled, monitorPort, monitorInterval, monitorPath], () => {
  const ann = d.annotations || (d.annotations = {})
  if (monitorEnabled.value) {
    ann['monitoring.coreos.com/podmonitor'] = 'true'
    if (monitorPort.value) ann['monitoring.coreos.com/port'] = monitorPort.value
    if (monitorInterval.value) ann['monitoring.coreos.com/interval'] = monitorInterval.value
    if (monitorPath.value) ann['monitoring.coreos.com/path'] = monitorPath.value
  } else {
    delete ann['monitoring.coreos.com/podmonitor']
    delete ann['monitoring.coreos.com/port']
    delete ann['monitoring.coreos.com/interval']
    delete ann['monitoring.coreos.com/path']
  }
})

const hasReplicas = computed(() => ['deployments', 'statefulsets', 'replicasets', 'replicationcontrollers'].includes(props.kind))
const hasSelector = computed(() => ['deployments', 'statefulsets', 'daemonsets', 'replicasets', 'replicationcontrollers'].includes(props.kind))
const hasStrategy = computed(() => ['deployments', 'statefulsets'].includes(props.kind))
const strategyOptions = computed(() => (props.kind === 'statefulsets' ? ['RollingUpdate', 'OnDelete'] : ['RollingUpdate', 'Recreate']))

// 归一化表单数据结构（子组件渲染早于父组件数据填充，必须保证嵌套字段存在）
function normalize(v: any): any {
  const base = {
    name: '', namespace: '', labels: {}, annotations: {}, replicas: 1, selector: {},
    serviceAccountName: '', containers: [], initContainers: [], volumes: [], nodeSelector: {}, tolerations: [],
    strategy: { type: 'RollingUpdate', maxUnavailable: '25%', maxSurge: '25%' },
    restartPolicy: 'Always', schedule: '', concurrencyPolicy: 'Allow', suspend: false,
    startingDeadlineSeconds: '', timeZone: '',
    successfulJobsHistoryLimit: 3, failedJobsHistoryLimit: 1, parallelism: 1, completions: 1, backoffLimit: 6,
    ttlSecondsAfterFinished: '', activeDeadlineSeconds: '', completionMode: 'NonIndexed',
    minReadySeconds: 0, revisionHistoryLimit: 10, progressDeadlineSeconds: 600,
    serviceName: '', podManagementPolicy: 'OrderedReady', updateStrategyType: 'RollingUpdate', updatePartition: 0,
    volumeClaimTemplates: [], imagePullSecrets: [],
    hostNetwork: false, hostPID: false, hostIPC: false, dnsPolicy: '', terminationGracePeriodSeconds: 30, hostAliases: [],
    podSc: { runAsUser: '', runAsGroup: '', fsGroup: '', runAsNonRoot: false },
    priorityClassName: '',
    nodeAffinityRows: [], antiTopologyKey: '', antiMatchLabels: {},
  }
  const data = { ...base, ...(v || {}) }
  data.labels = v?.labels || {}
  data.annotations = v?.annotations || {}
  data.selector = v?.selector || {}
  data.nodeSelector = v?.nodeSelector || {}
  data.containers = v?.containers || []
  data.initContainers = v?.initContainers || []
  data.volumes = v?.volumes || []
  data.tolerations = v?.tolerations || []
  data.imagePullSecrets = v?.imagePullSecrets || []
  data.hostAliases = v?.hostAliases || []
  data.nodeAffinityRows = v?.nodeAffinityRows || []
  data.antiMatchLabels = v?.antiMatchLabels || {}
  data.volumeClaimTemplates = v?.volumeClaimTemplates || []
  // 复用 v 已有引用（勿每次新建对象）：否则 round-trip 里 Object.assign(d, normalize(v))
  // 会把 d.strategy 换成新引用 -> 触发 watch(d) -> 再 emit -> 无限递归更新
  data.strategy = v?.strategy || { ...base.strategy }
  data.podSc = v?.podSc || { runAsUser: '', runAsGroup: '', fsGroup: '', runAsNonRoot: false }
  return data
}

// 表单自持数据 d。
// 只处理「外部来源」的 modelValue 变化（如 YAML 标签页修改后回传），
// 忽略本表单自身 emit 产生的回传（v === lastEmitted），打断
// emit -> modelValue -> Object.assign(d) -> 再次 emit 的循环。
let lastEmitted = null
watch(
  d,
  () => {
    const e = { ...d }
    lastEmitted = e
    emit('update:modelValue', e)
  },
  { deep: true },
)

watch(
  () => props.modelValue,
  (v) => {
    if (v === lastEmitted) return // 来自本表单自己的 emit，跳过，避免 round-trip
    Object.assign(d, normalize(v))
  },
  { immediate: true },
)

function newContainer() {
  return {
    name: '', image: '', imagePullPolicy: '', workingDir: '', command: '', args: '',
    envList: [], envFrom: [], ports: [],
    cpuRequest: '', memoryRequest: '', storageRequest: '', cpuLimit: '', memoryLimit: '', storageLimit: '',
    readinessProbe: { enabled: false, type: 'httpGet', path: '/', port: 80, command: '', initialDelaySeconds: 5, periodSeconds: 10 },
    livenessProbe: { enabled: false, type: 'httpGet', path: '/', port: 80, command: '', initialDelaySeconds: 15, periodSeconds: 20 },
    startupProbe: { enabled: false, type: 'httpGet', path: '/', port: 8080, command: '', initialDelaySeconds: 10, periodSeconds: 10 },
    postStart: { enabled: false, type: 'httpGet', path: '/', port: 80, command: '', initialDelaySeconds: 5, periodSeconds: 10 },
    preStop: { enabled: false, type: 'httpGet', path: '/', port: 80, command: '', initialDelaySeconds: 5, periodSeconds: 10 },
    volumeMounts: [],
    sc: { runAsUser: '', runAsGroup: '', privileged: false, runAsNonRoot: false, readOnlyRootFilesystem: false, capAdd: '', capDrop: '' },
  }
}

function addContainer() {
  d.containers.push(newContainer())
}

function addInitContainer() {
  d.initContainers.push(newContainer())
}

function removeContainer(idx: number) {
  (d.containers || (d.containers = [])).splice(idx, 1)
}

function addVolume() {
  d.volumes.push({ name: '', type: 'pvc', pvcName: '', configMapName: '', secretName: '', hostPath: '' })
}
</script>

<style scoped>
.workload-form { max-width: 980px; }
.container-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; width: 100%; }
.kv-row-column { width: 100%; }
.vct-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; width: 100%; }
.monitor-hint { color: #909399; font-size: 12px; margin-left: 10px; }
.hint { color: #909399; font-size: 12px; }
</style>
