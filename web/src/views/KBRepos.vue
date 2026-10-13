<template>
  <el-card shadow="never" v-loading="loading">
    <template #header>
      <div class="card-header">
        <div>
          <span class="title">备份仓库（BackupRepo）</span>
          <span class="tip">集群级资源 · 数据库的备份落到这里配置的对象存储；没有仓库时「立即备份 / 定时备份」会被直接拒</span>
        </div>
        <div>
          <el-button size="small" type="primary" :disabled="!canWrite" @click="openCreate">新建仓库</el-button>
          <el-button :icon="Refresh" circle size="small" @click="load" />
        </div>
      </div>
    </template>

    <el-alert v-if="!loading && !repos.length" type="warning" :closable="false" show-icon
      title="还没有备份仓库"
      description="建一个仓库需要：StorageProvider（如 s3）、指向 AK/SK 的凭证 Secret、bucket / region / endpoint 等参数。凭证只在集群里以 Secret 形式被引用，控制台不留存明文。" />

    <el-table v-if="repos.length" border :data="repos" size="small" style="margin-top: 4px">
      <el-table-column label="名称" min-width="180">
        <template #default="{ row }">
          <el-link type="primary" :underline="false" @click="openDetail(row)">{{ row.name }}</el-link>
          <el-tag v-if="row.default" size="small" type="success" effect="plain" style="margin-left: 6px">默认</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Provider" min-width="150">
        <template #default="{ row }">
          {{ row.provider || '-' }}
          <el-tooltip v-if="providerReason(row.provider)" :content="providerReason(row.provider)" placement="top">
            <el-icon class="warn-icon"><QuestionFilled /></el-icon>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="访问方式" width="120">
        <template #default="{ row }">
          {{ row.accessMethod || 'Mount' }}
          <div class="sub-tip">{{ accessHint(row.accessMethod) }}</div>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag size="small" :type="phaseType(row.phase)">{{ row.phase || 'Unknown' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="存储参数" min-width="240">
        <template #default="{ row }">
          <span class="mono">{{ configSummary(row) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="StorageClass" min-width="180">
        <template #default="{ row }">{{ row.storageClass || '-' }}</template>
      </el-table-column>
      <el-table-column prop="createdAt" label="创建时间" width="160" />
      <el-table-column label="操作" width="190" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text :disabled="!canWrite" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" text type="danger" :disabled="!canWrite" @click="del(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <div v-if="!loading && !repos.length" class="tip" style="margin-top: 10px">
      未装 KubeBlocks 的集群没有 BackupRepo 资源，本页会是空的。
    </div>
  </el-card>

  <!-- 新建 / 编辑 -->
  <el-dialog v-model="formDlg" :title="editing ? `编辑仓库 ${form.name}` : '新建备份仓库'" width="640px">
    <el-form label-width="120px" size="small">
      <el-form-item label="名称" required>
        <el-input v-model="form.name" :disabled="!!editing" placeholder="小写字母/数字/-，如 s3-main" />
      </el-form-item>
      <el-form-item label="StorageProvider" required>
        <el-select v-model="form.provider" :disabled="!!editing" style="width: 100%" @change="onProviderChange">
          <el-option v-for="p in providers" :key="p.name" :value="p.name" :label="p.name">
            <span>{{ p.name }}</span>
            <span class="opt-tip">{{ p.phase === 'Ready' ? '就绪' : (p.reason || p.csiDriver || '未就绪') }}</span>
          </el-option>
        </el-select>
        <div v-if="editing" class="sub-tip">provider 绑定后不可修改（CRD 校验），换存储类型请删了重建</div>
        <div v-else-if="!providers.length" class="sub-tip">没读到 StorageProvider：KubeBlocks 未安装或其 CRD 不可读</div>
        <div v-else-if="selected && selected.phase !== 'Ready'" class="sub-tip">
          {{ selected.name }} 显示未就绪是因为它的 CSI 驱动没装（{{ selected.reason || selected.csiDriver }}）——
          只影响 Mount，用 Tool 方式照样能备份到对象存储
        </div>
      </el-form-item>
      <el-form-item label="访问方式" required>
        <el-radio-group v-model="form.accessMethod">
          <el-radio-button value="Tool">Tool</el-radio-button>
          <el-radio-button value="Mount" :disabled="mountBlocked">Mount</el-radio-button>
        </el-radio-group>
        <div class="sub-tip">
          Tool = 备份工具直传对象存储（不需要 CSI）；Mount = 先把桶挂成 PV（需要该 provider 的 CSI 驱动已装）。
          <span v-if="mountBlocked" class="warn-text">当前 provider 未就绪，Mount 用不了：{{ selected?.reason || selected?.csiDriver }}</span>
        </div>
      </el-form-item>

      <template v-if="selected">
        <el-form-item v-for="p in normalParams" :key="p.name" :label="p.name" :required="p.required">
          <el-switch v-if="p.type === 'boolean'" :model-value="boolCfg(p.name)" @change="setCfg(p.name, $event)" />
          <el-input v-else v-model="form.config[p.name]" :placeholder="paramPlaceholder(p)" />
          <div v-if="p.description" class="sub-tip">{{ p.description }}<span v-if="p.default">（默认 {{ p.default }}）</span></div>
        </el-form-item>

        <template v-if="credParams.length">
          <el-form-item label="凭证命名空间" required>
            <el-select v-model="form.secretNamespace" filterable style="width: 100%" @change="loadSecrets">
              <el-option v-for="ns in nsOptions" :key="ns" :value="ns" :label="ns" />
            </el-select>
          </el-form-item>
          <el-form-item label="凭证 Secret" required>
            <el-select v-model="form.secretName" filterable :loading="secretsLoading" style="width: 100%"
              :placeholder="secrets.length ? '选择键齐全的 Secret' : '该命名空间没有键齐全的 Secret'">
              <el-option v-for="s in secrets" :key="s.name" :value="s.name" :label="s.name" />
            </el-select>
            <div class="sub-tip">Secret 必须含键：<span class="mono">{{ credParams.map((p) => p.name).join(' / ') }}</span>；控制台只引用它，不读取内容</div>
          </el-form-item>
        </template>

        <el-form-item v-if="form.accessMethod === 'Mount'" label="备份卷容量" required>
          <el-input v-model="form.volumeCapacity" placeholder="如 100Gi" style="width: 200px" />
          <div class="sub-tip">Mount 方式给备份 PVC 用的容量</div>
        </el-form-item>
      </template>

      <el-form-item label="路径前缀">
        <el-input v-model="form.pathPrefix" placeholder="可选，如 mysql-backup" />
      </el-form-item>
      <el-form-item label="PV 回收策略" required>
        <el-select v-model="form.pvReclaimPolicy" style="width: 220px">
          <el-option value="Delete" label="Delete（删仓库时回收）" />
          <el-option value="Retain" label="Retain（保留 PV）" />
        </el-select>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button size="small" @click="formDlg = false">取消</el-button>
      <el-button size="small" type="primary" :loading="saving" @click="submit">{{ editing ? '保存' : '创建' }}</el-button>
    </template>
  </el-dialog>

  <!-- 详情 -->
  <el-dialog v-model="detailDlg" :title="`仓库 ${detail?.name || ''}`" width="720px">
    <template v-if="detail">
      <div class="sec-title">配置</div>
      <el-table border :data="detailRows" size="small">
        <el-table-column prop="k" label="项" width="200" />
        <el-table-column prop="v" label="值" min-width="300" show-overflow-tooltip />
      </el-table>
      <div class="sec-title" style="margin-top: 14px">状态条件</div>
      <el-table border :data="detail.conditions || []" size="small">
        <el-table-column prop="type" label="类型" width="180" />
        <el-table-column prop="status" label="状态" width="80" />
        <el-table-column prop="reason" label="原因" width="180" />
        <el-table-column prop="message" label="信息" min-width="240" show-overflow-tooltip />
      </el-table>
      <div v-if="!detail.conditions?.length" class="tip" style="margin-top: 6px">operator 还没写入状态</div>
      <div class="tip" style="margin-top: 10px">
        凭证引用：<span class="mono">{{ (detail.secretNamespace || '-') + '/' + (detail.secretName || '-') }}</span>（不回显内容）
      </div>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { QuestionFilled, Refresh } from '@element-plus/icons-vue'
import { k8sApi } from '../api'
import { kbApi, type KBProviderParam, type KBProviderView, type KBRepoDetail, type KBRepoInput, type KBRepoSecret, type KBRepoView } from '../api/kb'
import { usePerm } from '../store/perm'
import { confirmDelete } from '../utils/confirm'

const perm = usePerm()
// BackupRepo 是集群级资源：写权限按集群层判定（与后端 denyClusterWrite 同口径）
const canWrite = computed(() => perm.canWriteCluster())

const loading = ref(false)
const repos = ref<KBRepoView[]>([])
const providers = ref<KBProviderView[]>([])
const nsOptions = ref<string[]>([])

const formDlg = ref(false)
const editing = ref('')
const saving = ref(false)
const form = reactive<KBRepoInput & { config: Record<string, string> }>({
  name: '', provider: '', accessMethod: 'Tool', pvReclaimPolicy: 'Delete',
  pathPrefix: '', volumeCapacity: '', secretName: '', secretNamespace: '', config: {},
})
const secrets = ref<KBRepoSecret[]>([])
const secretsLoading = ref(false)

const detailDlg = ref(false)
const detail = ref<KBRepoDetail | null>(null)
const detailRows = computed(() => {
  const d = detail.value
  if (!d) return []
  const rows = [
    { k: 'Provider', v: d.provider || '-' },
    { k: '访问方式', v: d.accessMethod || 'Mount' },
    { k: 'PV 回收策略', v: d.pvReclaimPolicy || '-' },
    { k: '路径前缀', v: d.pathPrefix || '-' },
    { k: '状态', v: d.phase || '-' },
    { k: '生成的 StorageClass', v: d.storageClass || '-' },
    { k: '凭证 Secret', v: `${d.secretNamespace || '-'}/${d.secretName || '-'}` },
  ]
  if (d.volumeCapacity) rows.push({ k: '备份卷容量', v: d.volumeCapacity })
  for (const [k, v] of Object.entries(d.config || {}).sort((a, b) => a[0].localeCompare(b[0]))) {
    rows.push({ k: `参数 ${k}`, v })
  }
  return rows
})

const selected = computed(() => providers.value.find((p) => p.name === form.provider))
const normalParams = computed(() => (selected.value?.params || []).filter((p) => !p.credential))
const credParams = computed(() => (selected.value?.params || []).filter((p) => p.credential))
// CSI 没装的 provider 挂不出卷：把 Mount 选项按住，别让人建出不能用的仓库
const mountBlocked = computed(() => !!selected.value && selected.value.phase !== 'Ready')

onMounted(load)

async function load() {
  loading.value = true
  try {
    const [r, p] = await Promise.all([kbApi.backupRepos(), kbApi.storageProviders()])
    repos.value = r || []
    providers.value = p || []
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
  if (!nsOptions.value.length) {
    try {
      const list = await k8sApi.namespaces()
      nsOptions.value = (list || []).map((n) => n.name).filter((n) => perm.canReadNS(n))
      if (!form.secretNamespace && nsOptions.value.length) form.secretNamespace = nsOptions.value[0]
    } catch { /* 拿不到 ns 就不给候选 */ }
  }
}

function providerReason(name?: string) {
  const p = providers.value.find((x) => x.name === name)
  return p && p.phase !== 'Ready' ? `${p.reason || '未就绪'}${p.csiDriver ? `（需要 CSI 驱动 ${p.csiDriver}）` : ''}` : ''
}

function accessHint(m?: string) {
  return m === 'Mount' ? '挂载方式' : m === 'Tool' ? '工具直传' : ''
}

function configSummary(row: KBRepoView) {
  const cfg = row.config || {}
  const keys = ['bucket', 'region', 'endpoint'].filter((k) => cfg[k])
  const rest = Object.keys(cfg).filter((k) => !keys.includes(k)).sort()
  const shown = [...keys, ...rest].slice(0, 3).map((k) => `${k}=${cfg[k]}`)
  return shown.length ? shown.join('  ') : '-'
}

function openCreate() {
  editing.value = ''
  Object.assign(form, {
    name: '', provider: defaultProvider(), accessMethod: 'Tool', pvReclaimPolicy: 'Delete',
    pathPrefix: '', volumeCapacity: '', secretName: '', secretNamespace: form.secretNamespace || nsOptions.value[0] || '', config: {},
  })
  applyDefaults()
  formDlg.value = true
  loadSecrets()
}

// 默认 provider：这套 addon 里有 11 个 provider（含 nfs/pvc/ftp），备份对象存储的
// 主路径是 S3 系列，故优先 s3 → 其它 s3 类 → 任意就绪的 → 剩下的
function defaultProvider() {
  const s3ish = /s3|minio|oss|obs|cos|gs/
  const score = (p: KBProviderView) =>
    (p.name === 's3' ? 0 : s3ish.test(p.name) ? 1 : 10) + (p.phase === 'Ready' ? 0 : 3)
  return [...providers.value].sort((a, b) => score(a) - score(b) || a.name.localeCompare(b.name))[0]?.name || ''
}

async function openEdit(row: KBRepoView) {
  try {
    const d = await kbApi.repo(row.name)
    editing.value = row.name
    Object.assign(form, {
      name: d.name, provider: d.provider || '', accessMethod: d.accessMethod || 'Mount',
      pvReclaimPolicy: d.pvReclaimPolicy || 'Delete', pathPrefix: d.pathPrefix || '',
      volumeCapacity: d.volumeCapacity || '', secretName: d.secretName || '',
      secretNamespace: d.secretNamespace || nsOptions.value[0] || '', config: { ...(d.config || {}) },
    })
    formDlg.value = true
    loadSecrets()
  } catch { /* 拦截器已提示 */ }
}

function onProviderChange() {
  form.config = {}
  applyDefaults()
  if (mountBlocked.value) form.accessMethod = 'Tool'
  loadSecrets()
}

function applyDefaults() {
  for (const p of selected.value?.params || []) {
    if (!p.credential && p.default && form.config[p.name] === undefined) form.config[p.name] = p.default
  }
}

function paramPlaceholder(p: KBProviderParam) {
  return p.required ? '必填' : p.default ? `默认 ${p.default}` : '可选'
}

function boolCfg(name: string) {
  const v = form.config[name]
  return v === undefined ? selected.value?.params.find((p) => p.name === name)?.default === 'true' : v === 'true'
}

function setCfg(name: string, v: unknown) {
  form.config[name] = v === true || v === 'true' ? 'true' : 'false'
}

async function loadSecrets() {
  secrets.value = []
  if (!form.provider || !form.secretNamespace) return
  secretsLoading.value = true
  try {
    secrets.value = await kbApi.repoSecrets(form.secretNamespace, form.provider)
  } catch {
    /* 拦截器已提示 */
  } finally {
    secretsLoading.value = false
  }
  if (form.secretName && !secrets.value.some((s) => s.name === form.secretName)) form.secretName = ''
}

async function submit() {
  if (!form.name.trim() || !form.provider) {
    ElMessage.warning('请填名称并选择 StorageProvider')
    return
  }
  const body: KBRepoInput = {
    ...form,
    config: Object.fromEntries(Object.entries(form.config).filter(([, v]) => String(v ?? '') !== '')),
  }
  saving.value = true
  try {
    if (editing.value) await kbApi.updateRepo(editing.value, body)
    else await kbApi.createRepo(body)
    ElMessage.success(editing.value ? '已保存' : '已创建，operator 正在准备仓库')
    formDlg.value = false
    load()
  } catch { /* 拦截器已提示 */ } finally {
    saving.value = false
  }
}

async function del(row: KBRepoView) {
  try {
    await confirmDelete(row.name, {
      title: '删除备份仓库',
      warning: `引用该仓库的备份策略将无法再备份（已产生的备份文件不会被删）；provider ${row.provider || '-'}`,
    })
  } catch { return }
  await kbApi.deleteRepo(row.name)
  ElMessage.success('已删除')
  load()
}

async function openDetail(row: KBRepoView) {
  try {
    detail.value = await kbApi.repo(row.name)
    detailDlg.value = true
  } catch { /* 拦截器已提示 */ }
}

function phaseType(phase: string): 'success' | 'info' | 'danger' | 'warning' {
  if (phase === 'Ready' || phase === 'Available') return 'success'
  if (!phase || phase === 'Unknown') return 'info'
  if (phase === 'NotReady' || phase === 'Failed') return 'danger'
  return 'warning'
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; margin-right: 10px; }
.tip { font-size: 13px; color: #909399; }
.sub-tip { font-size: 13px; color: #909399; line-height: 1.6; }
.warn-text { color: #e6a23c; }
.warn-icon { color: #e6a23c; vertical-align: -2px; margin-left: 4px; cursor: help; }
.opt-tip { float: right; font-size: 13px; color: #a8abb2; margin-left: 20px; }
.sec-title { font-weight: 600; margin-bottom: 6px; }
.mono { font-family: var(--el-font-family-mono, monospace); font-size: 13px; }
</style>
