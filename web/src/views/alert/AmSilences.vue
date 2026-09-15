<template>
  <div v-loading="loading">
    <el-empty v-if="st && !st.configured" description="当前集群尚未接入 Alertmanager">
      <el-button v-if="userStore.isAdmin" type="primary" @click="configDlg = true">接入配置</el-button>
    </el-empty>
    <template v-else>
      <el-card shadow="never">
        <template #header>
          <div class="card-header">
            <div class="header-left">
              <span>静默规则（Alertmanager）</span>
              <el-tag size="small" type="info">命中静默的告警不再推送通知</el-tag>
            </div>
            <div class="header-right">
              <el-button :icon="Refresh" circle @click="load" />
              <el-button type="primary" @click="openCreate">新建静默</el-button>
            </div>
          </div>
        </template>

        <el-table border :data="items" size="default" stripe>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="silenceTag(row.status?.state)" size="small">{{ silenceLabel(row.status?.state) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="匹配器" min-width="280">
            <template #default="{ row }">
              <el-tag v-for="(m, i) in row.matchers || []" :key="i" size="small" class="matcher-tag"
                :type="m.name === 'alertname' ? 'danger' : 'info'" effect="plain">
                {{ m.name }}{{ m.isEqual ? (m.isRegex ? '=~' : '=') : '!=' }}{{ m.value }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="起止时间" min-width="300">
            <template #default="{ row }">{{ fmtTime(row.startsAt) }} ~ {{ fmtTime(row.endsAt) }}</template>
          </el-table-column>
          <el-table-column prop="createdBy" label="创建人" width="130" show-overflow-tooltip />
          <el-table-column prop="comment" label="备注" min-width="180" show-overflow-tooltip>
            <template #default="{ row }">{{ row.comment || '—' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="80" fixed="right" align="center">
            <template #default="{ row }">
              <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>
    </template>

    <!-- 新建静默 -->
    <el-dialog v-model="createDlg" title="新建静默" width="560px">
      <el-form label-width="110px">
        <el-form-item label="匹配器">
          <div class="matcher-row" v-for="(m, i) in form.matchers" :key="i">
            <el-input v-model="m.name" placeholder="标签名，如 alertname" style="width: 150px" />
            <el-select v-model="m.op" style="width: 90px">
              <el-option label="=" value="eq" />
              <el-option label="=~" value="re" />
              <el-option label="!=" value="neq" />
            </el-select>
            <el-input v-model="m.value" placeholder="值" style="flex: 1" />
            <el-button :icon="Delete" circle size="small" :disabled="form.matchers.length <= 1" @click="form.matchers.splice(i, 1)" />
          </div>
          <el-button size="small" text type="primary" @click="form.matchers.push({ name: '', value: '', op: 'eq' })">
            + 添加匹配器
          </el-button>
        </el-form-item>
        <el-form-item label="静默时长">
          <el-select v-model="form.duration" style="width: 200px">
            <el-option v-for="d in DURATIONS" :key="d.v" :label="d.l" :value="d.v" />
            <el-option label="自定义（小时）" value="custom" />
          </el-select>
          <el-input-number v-if="form.duration === 'custom'" v-model="form.customHours" :min="1" :max="8760"
            style="margin-left: 12px; width: 140px" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.comment" type="textarea" :rows="2" placeholder="静默原因" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submit">创建静默</el-button>
      </template>
    </el-dialog>

    <AmConfigDialog v-if="configDlg" v-model="configDlg" @saved="load" />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, watch } from 'vue'
import { Refresh, Delete } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { amApi, type AMSilenceItem, type AMStatusItem, type AMMatcherItem } from '../../api'
import { useClusterStore } from '../../store/cluster'
import { useUserStore } from '../../store/user'
import AmConfigDialog from './AmConfigDialog.vue'
import { confirmDelete } from '../../utils/confirm'

const clusterStore = useClusterStore()
const userStore = useUserStore()
const st = ref<AMStatusItem>()
const items = ref<AMSilenceItem[]>([])
const loading = ref(false)
const configDlg = ref(false)

const DURATIONS = [
  { l: '1 小时', v: '1h' }, { l: '4 小时', v: '4h' }, { l: '8 小时', v: '8h' },
  { l: '1 天', v: '1d' }, { l: '3 天', v: '3d' }, { l: '1 周', v: '1w' },
]
const createDlg = ref(false)
const creating = ref(false)
const form = reactive({
  matchers: [{ name: 'alertname', value: '', op: 'eq' }] as { name: string; value: string; op: string }[],
  duration: '4h',
  customHours: 24,
  comment: '',
})

function openCreate() {
  form.matchers = [{ name: 'alertname', value: '', op: 'eq' }]
  form.duration = '4h'
  form.customHours = 24
  form.comment = ''
  createDlg.value = true
}

async function loadStatus() {
  try {
    st.value = await amApi.status()
  } catch {
    st.value = { configured: false, ok: false }
  }
}

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    await loadStatus()
    if (st.value?.configured) {
      items.value = (await amApi.silences()) || []
    } else {
      items.value = []
    }
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function submit() {
  const matchers: AMMatcherItem[] = []
  for (const m of form.matchers) {
    if (!m.name.trim() || !m.value.trim()) continue
    matchers.push({
      name: m.name.trim(),
      value: m.value.trim(),
      isRegex: m.op === 're',
      isEqual: m.op !== 'neq',
    })
  }
  if (!matchers.length) {
    ElMessage.warning('至少需要一个完整匹配器（标签名 + 值）')
    return
  }
  const hours = form.duration === 'custom' ? form.customHours : { '1h': 1, '4h': 4, '8h': 8, '1d': 24, '3d': 72, '1w': 168 }[form.duration] ?? 4
  const now = new Date()
  creating.value = true
  try {
    await amApi.createSilence({
      matchers,
      startsAt: now.toISOString(),
      endsAt: new Date(now.getTime() + hours * 3600 * 1000).toISOString(),
      comment: form.comment,
    })
    ElMessage.success(`已创建静默（${hours} 小时）`)
    createDlg.value = false
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    creating.value = false
  }
}

async function remove(row: AMSilenceItem) {
  try {
    await confirmDelete(String(row.id), { title: '删除静默', warning: '删除后命中的告警将恢复推送。请输入静默规则 ID 确认。' })
  } catch {
    return
  }
  try {
    await amApi.deleteSilence(row.id)
    ElMessage.success('已删除')
    load()
  } catch {
    /* 拦截器已提示 */
  }
}

function silenceTag(s?: string) {
  return s === 'active' ? 'warning' : s === 'pending' ? 'info' : 'info'
}
function silenceLabel(s?: string) {
  return s === 'active' ? '生效中' : s === 'pending' ? '待生效' : '已过期'
}
function fmtTime(ts?: string): string {
  if (!ts || ts.startsWith('0001-')) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

watch(() => clusterStore.current, load)
onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; }
.matcher-tag { margin-right: 6px; font-family: Consolas, monospace; }
.matcher-row { display: flex; align-items: center; gap: 8px; width: 100%; margin-bottom: 8px; }
</style>
