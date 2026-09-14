<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>配置管理 ({{ filtered.length }})</span>
        <div class="header-right">
          <el-input v-model="search" placeholder="搜索 Data ID / 分组..." :prefix-icon="Search" clearable style="width: 220px" />
          <NamespaceSelect v-model="namespace" multiple width="260px" @change="onNsChange" />
          <el-button :icon="Refresh" circle @click="load" />
          <el-button :disabled="!ready.configured" :loading="exporting" @click="doExport">导出</el-button>
          <el-button :disabled="!ready.configured" @click="importFileInput?.click()">导入</el-button>
          <input ref="importFileInput" type="file" accept=".json" style="display: none" @change="onImportFile" />
          <el-button type="primary" :disabled="!targetNs" @click="openEditor()">新建配置</el-button>
        </div>
      </div>
    </template>

    <template v-if="ready.configured">
      <el-table border :data="filtered" v-loading="loading" stripe>
        <el-table-column prop="dataId" label="Data ID" min-width="280" show-overflow-tooltip />
        <el-table-column prop="group" label="分组" width="150" show-overflow-tooltip />
        <el-table-column prop="type" label="格式" width="100" align="center" />
        <el-table-column prop="namespace" label="命名空间" min-width="130" show-overflow-tooltip />
        <el-table-column label="操作" width="140" fixed="right" align="center">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openEditor(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="removeConfig(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="hint">Nacos 命名空间 = 所选 K8s 命名空间（同名自动同步）；多选命名空间时列表自动合并。</div>
    </template>

    <el-empty v-else description="当前集群尚未接入 Nacos（配置管理不可用）">
      <el-button v-if="userStore.isAdmin" type="primary" @click="$router.push('/nacos')">前往 Nacos 管理</el-button>
    </el-empty>

    <!-- 配置内容编辑对话框 -->
    <el-dialog v-model="editorDlg" :title="editorForm.existing ? `编辑配置 ${editorForm.dataId}` : '新建配置'" width="720px" :close-on-click-modal="false">
      <el-form label-width="90px" size="small">
        <el-form-item label="命名空间">
          <el-select v-model="editorForm.namespace" :disabled="editorForm.existing" style="width: 240px">
            <el-option v-for="ns in nsOptions" :key="ns" :label="ns" :value="ns" />
          </el-select>
        </el-form-item>
        <el-form-item label="Data ID"><el-input v-model="editorForm.dataId" :disabled="editorForm.existing" /></el-form-item>
        <el-form-item label="分组"><el-input v-model="editorForm.group" placeholder="DEFAULT_GROUP" /></el-form-item>
        <el-form-item label="格式">
          <el-select v-model="editorForm.type" style="width: 140px">
            <el-option label="YAML" value="YAML" /><el-option label="JSON" value="JSON" />
            <el-option label="Properties" value="properties" /><el-option label="Text" value="text" />
          </el-select>
        </el-form-item>
        <el-form-item label="内容">
          <el-input v-model="editorForm.content" type="textarea" :rows="16" class="content-area" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editorDlg = false">取消</el-button>
        <el-button type="primary" :loading="publishing" @click="publish">发布</el-button>
      </template>
    </el-dialog>
    <!-- 导入配置（预览 + 逐条发布） -->
    <el-dialog v-model="importVisible" title="导入配置" width="800px" :close-on-click-modal="false">
      <el-form label-width="100px" size="small" style="margin-bottom: 10px">
        <el-form-item label="目标命名空间">
          <el-radio-group v-model="importTarget">
            <el-radio-button value="original">按文件内命名空间</el-radio-button>
            <el-radio-button value="fixed">统一导入到</el-radio-button>
          </el-radio-group>
          <el-select v-if="importTarget === 'fixed'" v-model="importNs" style="width: 220px; margin-left: 8px" placeholder="选择命名空间">
            <el-option v-for="ns in nsOptions" :key="ns" :label="ns" :value="ns" />
          </el-select>
        </el-form-item>
      </el-form>
      <el-table border :data="importFile?.configs || []" size="small" max-height="340">
        <el-table-column prop="dataId" label="Data ID" min-width="220" show-overflow-tooltip />
        <el-table-column prop="group" label="分组" width="140" show-overflow-tooltip />
        <el-table-column prop="type" label="格式" width="90" align="center" />
        <el-table-column label="命名空间" min-width="120" show-overflow-tooltip>
          <template #default="{ row }">{{ importTarget === 'fixed' ? importNs : row.namespace }}</template>
        </el-table-column>
        <el-table-column label="结果" width="140">
          <template #default="{ $index }">
            <span v-if="importResults[$index] === undefined" class="hint-inline">待发布</span>
            <el-tag v-else-if="!importResults[$index]" size="small" type="success">成功</el-tag>
            <el-tooltip v-else :content="importResults[$index]"><el-tag size="small" type="danger">失败</el-tag></el-tooltip>
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="importVisible = false">关闭</el-button>
        <el-button type="primary" :loading="importing" :disabled="importDone" @click="doImport">发布</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { nacosApi, nsParam, type NacosConfigEntry, type NacosConfigExportFile } from '../../api'
import { useClusterStore } from '../../store/cluster'
import { useNamespaceStore } from '../../store/namespace'
import { useUserStore } from '../../store/user'
import NamespaceSelect from '../../components/NamespaceSelect.vue'
import { downloadText } from '../../utils/download'

const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()
const userStore = useUserStore()

const cluster = computed(() => clusterStore.current || '')
const ready = ref<{ configured: boolean; enabled?: boolean }>({ configured: true })
const loading = ref(false)
const entries = ref<NacosConfigEntry[]>([])
const search = ref('')

const editorDlg = ref(false)
const publishing = ref(false)
const editorForm = reactive({ existing: false, namespace: '', dataId: '', group: 'DEFAULT_GROUP', type: 'YAML', content: '' })

// 命名空间选择与顶栏全局选择双向同步（写回 store，其他页面随之联动）
const namespace = ref<string[]>([...nsStore.selected])
function onNsChange(v: string[] | string) {
  namespace.value = (Array.isArray(v) ? v : [v]) as string[]
  nsStore.select(namespace.value)
  load()
}

// 新建配置的目标命名空间：单选用它，多选/全选用第一个具体项
const nsOptions = computed(() =>
  namespace.value.includes('__all__') ? ['default'] : namespace.value.filter((n) => n && n !== '__all__'),
)
const targetNs = computed(() => nsOptions.value[0] || '')

const filtered = computed(() => {
  const kw = search.value.trim().toLowerCase()
  if (!kw) return entries.value
  return entries.value.filter(
    (e) => e.dataId.toLowerCase().includes(kw) || (e.group || '').toLowerCase().includes(kw) || (e.namespace || '').toLowerCase().includes(kw),
  )
})

async function load() {
  if (!cluster.value) return
  try {
    ready.value = await nacosApi.ready(cluster.value)
  } catch {
    ready.value = { configured: true }
  }
  if (!ready.value.configured) {
    entries.value = []
    return
  }
  loading.value = true
  try {
    entries.value = (await nacosApi.configList(cluster.value, nsParam(namespace.value))) || []
  } catch {
    entries.value = []
  } finally {
    loading.value = false
  }
}

function openEditor(row?: NacosConfigEntry) {
  editorForm.existing = !!row
  editorForm.namespace = row?.namespace || targetNs.value
  editorForm.dataId = row?.dataId || ''
  editorForm.group = row?.group || 'DEFAULT_GROUP'
  editorForm.type = row?.type || 'YAML'
  editorForm.content = ''
  editorDlg.value = true
  if (row) {
    nacosApi
      .configContent(cluster.value, row.namespace || '', row.dataId, row.group)
      .then((r) => (editorForm.content = r.content))
      .catch(() => {})
  }
}

async function publish() {
  if (!editorForm.namespace) {
    ElMessage.warning('请选择命名空间')
    return
  }
  if (!editorForm.dataId) {
    ElMessage.warning('Data ID 必填')
    return
  }
  publishing.value = true
  try {
    await nacosApi.configPublish({
      clusterName: cluster.value, namespace: editorForm.namespace,
      dataId: editorForm.dataId, group: editorForm.group || 'DEFAULT_GROUP',
      content: editorForm.content, type: editorForm.type,
    })
    ElMessage.success('已发布')
    editorDlg.value = false
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    publishing.value = false
  }
}

async function removeConfig(row: NacosConfigEntry) {
  try {
    await ElMessageBox.confirm(`删除配置 ${row.dataId}（${row.group}，命名空间 ${row.namespace}）？`, '删除配置', { type: 'warning' })
  } catch {
    return
  }
  try {
    await nacosApi.configDelete(cluster.value, row.namespace || '', row.dataId, row.group)
    ElMessage.success('已删除')
    load()
  } catch {
    /* 拦截器已提示 */
  }
}

// ---- 导入 / 导出 ----
const exporting = ref(false)
const importFileInput = ref<HTMLInputElement>()
const importVisible = ref(false)
const importing = ref(false)
const importDone = ref(false)
const importFile = ref<NacosConfigExportFile | null>(null)
const importTarget = ref<'original' | 'fixed'>('original')
const importNs = ref('')
const importResults = ref<Record<number, string>>({})

// 导出：所选命名空间的全部配置（含内容）为 JSON 文件
async function doExport() {
  exporting.value = true
  try {
    const r = await nacosApi.configExport(cluster.value, nsParam(namespace.value))
    if (!r.configs?.length) {
      ElMessage.warning('当前命名空间筛选下无可导出的配置')
      return
    }
    const text = JSON.stringify({ cluster: r.cluster, exportedAt: r.exportedAt, configs: r.configs }, null, 2)
    downloadText(text, `nacos-configs_${cluster.value}_${new Date().toISOString().slice(0, 10)}.json`, 'application/json')
  } catch {
    /* 拦截器已提示 */
  } finally {
    exporting.value = false
  }
}

// 导入：解析导出文件（或仅 configs 数组），预览后逐条发布
async function onImportFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许再次选择同一文件
  if (!file) return
  try {
    const parsed = JSON.parse(await file.text())
    const configs = Array.isArray(parsed) ? parsed : parsed.configs
    if (!Array.isArray(configs) || !configs.length) {
      ElMessage.warning('文件中未找到配置（需为导出的 JSON 或 configs 数组）')
      return
    }
    importFile.value = { cluster: parsed.cluster, exportedAt: parsed.exportedAt, configs }
    importTarget.value = 'original'
    importNs.value = targetNs.value
    importResults.value = {}
    importDone.value = false
    importVisible.value = true
  } catch (e) {
    ElMessage.error(`解析文件失败：${(e as Error).message || e}`)
  }
}

async function doImport() {
  const cfgs = importFile.value?.configs || []
  const fixed = importTarget.value === 'fixed' ? importNs.value : ''
  if (importTarget.value === 'fixed' && !fixed) {
    ElMessage.warning('请选择目标命名空间')
    return
  }
  importing.value = true
  let ok = 0
  for (let i = 0; i < cfgs.length; i++) {
    const it = cfgs[i]
    const ns = fixed || it.namespace
    if (!it.dataId || !ns) {
      importResults.value[i] = !it.dataId ? '缺少 dataId' : '缺少命名空间'
      continue
    }
    try {
      await nacosApi.configPublish({
        clusterName: cluster.value, namespace: ns,
        dataId: it.dataId, group: it.group || 'DEFAULT_GROUP',
        content: it.content || '', type: it.type || 'YAML',
      })
      importResults.value[i] = ''
      ok++
    } catch (e) {
      importResults.value[i] = (e as Error).message || String(e)
    }
  }
  importing.value = false
  importDone.value = true
  if (ok === cfgs.length) ElMessage.success(`已发布 ${ok} 条配置`)
  else ElMessage.warning(`发布完成：成功 ${ok} / ${cfgs.length}，失败项见列表`)
  load()
}

// 顶栏全局命名空间变化时立即跟随（页面选择已写回 store 时值相同，不会循环）
watch(
  () => nsStore.selected,
  (v) => {
    if (JSON.stringify(v) !== JSON.stringify(namespace.value)) {
      namespace.value = [...v]
      load()
    }
  },
  { deep: true },
)
watch(cluster, load)

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.hint { color: #909399; font-size: 12px; margin-top: 10px; }
.hint-inline { color: #909399; font-size: 12px; }
.content-area :deep(textarea) { font-family: Consolas, monospace; font-size: 12.5px; }
</style>
