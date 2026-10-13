<!-- 镜像构建器：上传本地构建产物（Java fat jar / Go 二进制 / 前端静态 tar.gz），
     平台按应用类型生成约定 Dockerfile，走托管流水线构建并推送镜像。
     权限口径与流水线一致：ns 可写=改配置/传制品，ns 有角色=触发构建。 -->
<template>
  <div class="toolbar">
    <el-select v-model="filterProject" clearable placeholder="按项目筛选" style="width: 220px" @change="load">
      <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
    </el-select>
    <el-button v-if="canWriteAny" type="primary" size="small" :disabled="!projects.length" @click="openDlg()">新建构建器</el-button>
    <span class="cfg-tip">上传已在本地编译好的产物，平台按约定 Dockerfile 组装镜像推到仓库：不写 Dockerfile、不配 Git 仓库；构建走托管流水线（日志与制品在「执行中心」可查）。</span>
  </div>

  <el-table border :data="items" v-loading="loading" size="small" stripe>
    <el-table-column prop="name" label="名称" min-width="130" show-overflow-tooltip />
    <el-table-column label="类型" width="80" align="center">
      <template #default="{ row }"><el-tag size="small" :type="appTagType(row.appType)">{{ appLabel(row.appType) }}</el-tag></template>
    </el-table-column>
    <el-table-column label="项目" min-width="110" show-overflow-tooltip>
      <template #default="{ row }">{{ projectName(row.projectId) }}</template>
    </el-table-column>
    <el-table-column label="镜像" min-width="220" show-overflow-tooltip>
      <template #default="{ row }">
        <span class="mono">{{ row.registry }}/{{ row.repository }}</span><span class="dim">:{{ row.defaultTag || '构建时填' }}</span>
      </template>
    </el-table-column>
    <el-table-column label="当前制品" min-width="200">
      <template #default="{ row }">
        <template v-if="row.fileName">
          <div>{{ row.fileName }}</div>
          <div class="dim">{{ fmtSize(row.fileSize) }} · sha {{ (row.fileSha256 || '').slice(0, 8) }} · {{ fmtTime(row.uploadedAt) }}</div>
        </template>
        <span v-else class="dim">未上传制品</span>
      </template>
    </el-table-column>
    <el-table-column label="最近构建" width="110" align="center">
      <template #default="{ row }">
        <el-button v-if="row.lastRun" text size="small" class="flat-btn" @click="emit('open-run', row.lastRun.id)">
          <el-tag size="small" :type="statusType(row.lastRun.status)">{{ row.lastRun.status }}</el-tag>
          <span class="dim">#{{ row.lastRun.runNo }}</span>
        </el-button>
        <span v-else class="dim">—</span>
      </template>
    </el-table-column>
    <el-table-column label="操作" width="320" fixed="right">
      <template #default="{ row }">
        <el-button size="small" type="primary" :disabled="!canRunRow(row) || !row.fileName" @click="openRunDlg(row)">构建</el-button>
        <el-button size="small" :disabled="!canWriteRow(row)" @click="pickFile(row)">上传制品</el-button>
        <el-button size="small" text @click="openDlg(row)">编辑</el-button>
        <el-button size="small" text @click="openPreview(row)">预览</el-button>
        <el-button size="small" text type="danger" :disabled="!canWriteRow(row)" @click="del(row)">删除</el-button>
      </template>
    </el-table-column>
  </el-table>
  <div v-if="!items.length && !loading" class="empty-tip">还没有构建器：点「新建构建器」选应用类型、填目标镜像，然后上传制品、一键构建。</div>

  <!-- 制品选择（隐藏 input，按应用类型限后缀；web 收 tar.gz/tgz/zip，zip 由平台归一） -->
  <input ref="fileInput" type="file" style="display: none" :accept="uploadTarget?.appType === 'java' ? '.jar' : (uploadTarget?.appType === 'web' ? '.tar.gz,.tgz,.zip' : '')" @change="onFilePicked" />

  <!-- 新建 / 编辑 -->
  <el-dialog v-model="dlg" :title="form.id ? '编辑构建器' : '新建构建器'" width="640px">
    <el-form label-width="110px" size="small">
      <el-form-item label="所属项目" required>
        <el-select v-model="form.projectId" :disabled="!!form.id" placeholder="选择 CI 项目（构建在其命名空间执行）" style="width: 100%">
          <el-option v-for="p in projects" :key="p.id" :label="(p.displayName || p.name) + ' [' + p.namespace + ']'" :value="p.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="名称" required>
        <el-input v-model="form.name" maxlength="64" placeholder="如 order-center" />
      </el-form-item>
      <el-form-item label="应用类型" required>
        <el-radio-group v-model="form.appType" :disabled="!!form.id">
          <el-radio-button value="java">Java (jar)</el-radio-button>
          <el-radio-button value="go">Go (二进制)</el-radio-button>
          <el-radio-button value="web">前端 (tar.gz/zip)</el-radio-button>
        </el-radio-group>
        <div class="dim">应用类型决定产物格式与 Dockerfile 形态，创建后不可改（换类型请新建）。</div>
        <div v-if="form.appType === 'web'" class="dim">静态包 = dist 目录内容打包：`tar -czf web.tar.gz -C dist .`，或直接选 zip（平台自动归一，多套一层目录也能识别）。</div>
      </el-form-item>
      <el-form-item label="镜像仓库" required>
        <el-input v-model="form.registry" placeholder="如 harbor.example.com[:8443]" />
      </el-form-item>
      <el-form-item label="仓库路径" required>
        <el-input v-model="form.repository" placeholder="如 library/demo（小写，可含 /）" />
      </el-form-item>
      <el-form-item label="推送凭证" required>
        <el-select v-model="form.credentialId" placeholder="账密 / DockerConfig 形态的镜像仓库凭证" style="width: 100%">
          <el-option v-for="c in registryCreds" :key="c.id" :label="c.name + (c.projectId ? '' : '（平台级）')" :value="c.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="基础镜像">
        <el-input v-model="form.baseImage" :placeholder="'留空 = 平台约定：' + defaultBaseHint" />
      </el-form-item>
      <el-form-item label="端口">
        <el-input-number v-model="form.port" :min="0" :max="65535" controls-position="right" style="width: 160px" />
        <span class="dim" style="margin-left: 8px">0 = 默认（Java/Go 8080，前端 80，仅写进镜像 EXPOSE）</span>
      </el-form-item>
      <el-form-item v-if="form.appType === 'java'" label="JVM 参数">
        <el-input v-model="form.javaOpts" placeholder="如 -Xmx512m -XX:+UseG1GC（可选）" />
      </el-form-item>
      <el-form-item v-if="form.appType !== 'web'" label="启动参数">
        <el-input v-model="form.entryArgs" placeholder="如 serve --conf=/app/config.yaml（可选，空格分隔）" />
        <div class="dim">生成镜像的 CMD，运行时追加在启动命令之后；不经 shell（引号 / $ / 反引号不允许），部署时还可用容器 args 覆盖。</div>
      </el-form-item>
      <el-form-item label="默认 Tag">
        <el-input v-model="form.defaultTag" placeholder="留空 = 构建时自动 upload-<北京时间>" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="dlg = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存</el-button>
    </template>
  </el-dialog>

  <!-- 构建 -->
  <el-dialog v-model="runDlg" width="520px" :title="'构建镜像：' + (runTarget?.name || '')">
    <el-descriptions :column="1" border size="small">
      <el-descriptions-item label="制品">{{ runTarget?.fileName }}（{{ fmtSize(runTarget?.fileSize) }}，sha {{ (runTarget?.fileSha256 || '').slice(0, 8) }}）</el-descriptions-item>
      <el-descriptions-item label="上传于">{{ fmtTime(runTarget?.uploadedAt) }}</el-descriptions-item>
      <el-descriptions-item label="目标镜像">{{ runTarget?.registry }}/{{ runTarget?.repository }}</el-descriptions-item>
    </el-descriptions>
    <el-form label-width="80px" size="small" style="margin-top: 12px">
      <el-form-item label="镜像 Tag">
        <el-input v-model="runTag" :placeholder="runTarget?.defaultTag || '留空自动：upload-<北京时间>'" />
      </el-form-item>
    </el-form>
    <div class="dim">点「开始构建」后：任务 Pod 从平台拉回落盘的制品 → BuildKit 按约定 Dockerfile 构建并推送。同一构建器串行执行，重复触发会排队；日志与镜像制品登记在「执行中心 / 制品」。</div>
    <template #footer>
      <el-button @click="runDlg = false">取消</el-button>
      <el-button type="primary" :loading="running" @click="doRun">开始构建</el-button>
    </template>
  </el-dialog>

  <!-- 编译产物预览（排障：看约定 Dockerfile 与托管流水线 YAML） -->
  <el-drawer v-model="prevDlg" size="62%" :title="'托管流水线编译产物：' + (prevTarget?.name || '')">
    <pre v-loading="prevLoading" class="mono yaml-box">{{ prevYaml }}</pre>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  ciApi,
  type CIProject, type CICredential, type CIRun,
  type CIBuilderAppType, type CIBuilderSaveBody, type CIImageBuilderView,
} from '../../api/ci'
import { activeCluster } from '../../store/clusterRef'
import { useUserStore } from '../../store/user'
import { usePerm } from '../../store/perm'
import { confirmDelete } from '../../utils/confirm'

const props = defineProps<{
  projects: CIProject[]
  credentials: CICredential[]
}>()
const emit = defineEmits<{ (e: 'open-run', runId: number): void }>()

const userStore = useUserStore()
const perm = usePerm()

const items = ref<CIImageBuilderView[]>([])
const loading = ref(false)
const filterProject = ref<number | undefined>()

const isAdmin = computed(() => !!perm.access.value.platformAdmin || userStore.isAdmin)
const canWriteAny = computed(() => isAdmin.value
  || !!perm.access.value.allNsWrite
  || Object.keys(perm.access.value.namespaces || {}).length > 0)
// 行级权限按构建器项目绑定的 ns 判定（与后端一致）
const canWriteRow = (row: CIImageBuilderView) => isAdmin.value || perm.canWriteNS(row.projectNamespace)
const canRunRow = (row: CIImageBuilderView) => isAdmin.value || perm.canReadNS(row.projectNamespace)

const registryCreds = computed(() => props.credentials.filter((c) => ['basic', 'dockerconfig'].includes(c.form)))

const appLabels: Record<string, string> = { java: 'Java', go: 'Go', web: '前端' }
const appLabel = (t: string) => appLabels[t] || t
const appTagType = (t: string) => (t === 'java' ? 'warning' : t === 'go' ? 'success' : 'primary')
const statusType = (s: string) => (['success'].includes(s) ? 'success' : ['failed', 'cancelled'].includes(s) ? 'danger' : 'info')

function projectName(id: number) {
  const p = props.projects.find((x) => x.id === id)
  return p ? (p.displayName || p.name) : `#${id}`
}
function fmtTime(s?: string | null) { return s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '-' }
function fmtSize(n?: number) {
  if (!n) return '—'
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

async function load() {
  loading.value = true
  try {
    const r = await ciApi.builders(filterProject.value)
    items.value = r.items ?? []
  } catch { items.value = [] } finally { loading.value = false }
}
onMounted(load)
watch(activeCluster, load)

// ---- 新建 / 编辑 ----
const dlg = ref(false)
const saving = ref(false)
const emptyForm = () => ({
  id: undefined as number | undefined, projectId: undefined as number | undefined,
  name: '', appType: 'java' as CIBuilderAppType, registry: '', repository: '',
  credentialId: undefined as number | undefined, baseImage: '', port: 0, javaOpts: '', entryArgs: '', defaultTag: '',
})
const form = reactive(emptyForm())

const defaultBaseHint = computed(() => ({
  java: 'eclipse-temurin:17-jre', go: 'alpine:3.19', web: 'nginx:1.27-alpine',
}[form.appType] || ''))

function openDlg(row?: CIImageBuilderView) {
  Object.assign(form, emptyForm())
  if (row) {
    Object.assign(form, {
      id: row.id, projectId: row.projectId, name: row.name, appType: row.appType,
      registry: row.registry, repository: row.repository, credentialId: row.credentialId,
      baseImage: row.baseImage || '', port: row.port || 0, javaOpts: row.javaOpts || '', entryArgs: row.entryArgs || '', defaultTag: row.defaultTag || '',
    })
  } else if (props.projects.length === 1) {
    form.projectId = props.projects[0].id
  }
  dlg.value = true
}

async function save() {
  if (!form.projectId || !form.name || !form.registry || !form.repository || !form.credentialId) {
    ElMessage.warning('项目/名称/镜像仓库/仓库路径/推送凭证都必填')
    return
  }
  const body: CIBuilderSaveBody = {
    projectId: form.projectId, name: form.name, appType: form.appType,
    registry: form.registry.trim(), repository: form.repository.trim(), credentialId: form.credentialId,
    baseImage: form.baseImage?.trim(), port: form.port, javaOpts: form.javaOpts, entryArgs: form.entryArgs?.trim(), defaultTag: form.defaultTag?.trim(),
  }
  saving.value = true
  try {
    if (form.id) await ciApi.updateBuilder(form.id, body)
    else await ciApi.createBuilder(body)
    ElMessage.success('已保存')
    dlg.value = false
    await load()
  } catch { /* 后端错误已由 http 层 toast（明确原因，不静默） */ } finally { saving.value = false }
}

async function del(row: CIImageBuilderView) {
  try {
    await confirmDelete(row.name, { warning: '将连同托管流水线与已上传制品一起删除' })
  } catch { return }
  try {
    await ciApi.deleteBuilder(row.id)
    ElMessage.success('已删除')
    await load()
  } catch { /* 进行中构建 409 等已 toast */ }
}

// ---- 制品上传 ----
const fileInput = ref<HTMLInputElement>()
const uploadTarget = ref<CIImageBuilderView>()

function pickFile(row: CIImageBuilderView) {
  uploadTarget.value = row
  if (fileInput.value) {
    fileInput.value.value = ''
    fileInput.value.click()
  }
}

async function onFilePicked(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  const row = uploadTarget.value
  if (!f || !row) return
  try {
    const out = await ciApi.builderUpload(row.id, f)
    ElMessage.success(`制品已上传（${fmtSize(out.fileSize)}，sha ${(out.fileSha256 || '').slice(0, 8)}），可以构建了`)
    await load()
  } catch { /* 超限/后缀错误已 toast 明确原因 */ }
}

// ---- 构建 ----
const runDlg = ref(false)
const running = ref(false)
const runTarget = ref<CIImageBuilderView>()
const runTag = ref('')

function openRunDlg(row: CIImageBuilderView) {
  runTarget.value = row
  runTag.value = ''
  runDlg.value = true
}

async function doRun() {
  const row = runTarget.value
  if (!row) return
  running.value = true
  try {
    const run = await ciApi.builderRun(row.id, runTag.value.trim() || undefined)
    runDlg.value = false
    ElMessage.success(`构建已提交（#${run.runNo}${run.status === 'queued' ? '，前面有构建在跑，已排队' : ''}）`)
    emit('open-run', run.id)
    await load()
  } catch { /* 缺 serverUrl / tag 非法等已 toast */ } finally { running.value = false }
}

// ---- 预览 ----
const prevDlg = ref(false)
const prevLoading = ref(false)
const prevTarget = ref<CIImageBuilderView>()
const prevYaml = ref('')

async function openPreview(row: CIImageBuilderView) {
  prevTarget.value = row
  prevYaml.value = ''
  prevDlg.value = true
  prevLoading.value = true
  try {
    const r = await ciApi.builderPreview(row.id)
    prevYaml.value = r.yaml
  } catch { prevYaml.value = '（生成失败：可能尚未上传制品，错误原因见右上角提示）' } finally { prevLoading.value = false }
}

defineExpose({ load })
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; flex-wrap: wrap; }
.cfg-tip { color: var(--el-text-color-secondary); }
.empty-tip { padding: 18px 0; color: var(--el-text-color-secondary); text-align: center; }
.dim { color: var(--el-text-color-secondary); }
.mono { font-family: Consolas, 'Courier New', monospace; }
.flat-btn { padding: 0 4px; }
.yaml-box { white-space: pre-wrap; word-break: break-all; font-size: 13px; line-height: 1.6; margin: 0; }
</style>
