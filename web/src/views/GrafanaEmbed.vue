<template>
  <div class="grafana-page">
    <div class="toolbar">
      <span class="label">Grafana 面板</span>
      <el-tag size="small" type="info">{{ clusterStore.current }}</el-tag>
      <el-input v-model="dashInput" placeholder="粘贴 dashboard 链接后回车嵌入（留空嵌首页）" clearable
        style="width: 420px" @keyup.enter="embed(dashInput)">
        <template #prefix><el-icon><Link /></el-icon></template>
      </el-input>
      <el-button type="primary" plain @click="embed(dashInput)">嵌入</el-button>
      <el-button v-if="embedUrl" @click="addFav" :disabled="!embedUrl">收藏</el-button>
      <el-select v-if="favs.length" v-model="favSel" placeholder="常用面板" clearable style="width: 240px" @change="onFav">
        <el-option v-for="(f, i) in favs" :key="i" :label="f.label" :value="i" />
      </el-select>
      <el-button v-if="embedUrl" :icon="Refresh" circle @click="reloadFrame" title="刷新面板" />
      <el-button v-if="userStore.isAdmin" :type="configured ? 'default' : 'primary'" @click="cfgVisible = true">
        {{ configured ? '修改地址' : '配置 Grafana 地址' }}
      </el-button>
      <span class="hint" v-if="configured">{{ baseURL }}</span>
    </div>

    <!-- 未配置引导 -->
    <el-empty v-if="!configured" description="当前集群未配置 Grafana 地址">
      <div class="guide" v-if="userStore.isAdmin">
        <p>1. 点击右上角「配置 Grafana 地址」，填入集群内 Grafana 访问地址（如 <code>http://grafana.kuboard:3000</code>）</p>
        <p>2. Grafana 侧需开启 iframe 内嵌与匿名只读（grafana.ini）：</p>
        <pre>[security]
allow_embedding = true
[auth.anonymous]
enabled = true
org_role = Viewer</pre>
        <p>3. 保存后本页将以 kiosk 模式内嵌 Grafana，常用面板可收藏便于切换</p>
      </div>
      <div v-else class="guide"><p>请联系管理员在「集群管理」中配置 Grafana 地址。</p></div>
    </el-empty>

    <!-- 内嵌 iframe -->
    <div v-else class="frame-wrap">
      <el-alert v-if="embedNotice" type="warning" :closable="true" style="margin-bottom: 8px" :title="embedNotice" />
      <iframe v-if="embedUrl" :key="frameKey" :src="embedUrl" class="grafana-frame" frameborder="0" />
    </div>

    <!-- 地址配置 -->
    <el-dialog v-model="cfgVisible" title="Grafana 地址" width="560px">
      <el-form label-width="110px">
        <el-form-item label="集群">
          <el-input :model-value="clusterStore.current" disabled />
        </el-form-item>
        <el-form-item label="Grafana 地址">
          <el-input v-model="cfgURL" placeholder="http://grafana.kuboard:3000" />
          <div class="cfg-tip">服务端与浏览器需能访问该地址；Grafana 需开启 allow_embedding 与匿名只读，否则 iframe 显示空白。</div>
        </el-form-item>
        <el-form-item>
          <el-button :loading="checking" @click="check">测试可达</el-button>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="cfgVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Link, Refresh } from '@element-plus/icons-vue'
import { grafanaApi } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'

const clusterStore = useClusterStore()
const userStore = useUserStore()

const configured = computed(() => !!clusterStore.currentCluster?.grafanaURL)
const baseURL = computed(() => clusterStore.currentCluster?.grafanaURL || '')

const dashInput = ref('')
const embedUrl = ref('')
const favSel = ref<number | ''>('')

const cfgVisible = ref(false)
const cfgURL = ref('')
const saving = ref(false)
const checking = ref(false)
const embedNotice = ref('')
const frameKey = ref(0)

function reloadFrame() {
  frameKey.value++
}

// 常用面板收藏（localStorage 按集群隔离）
const favs = ref<{ label: string; url: string }[]>([])
function favKey() {
  return `kc-grafana-favs-${clusterStore.current}`
}
function loadFavs() {
  try {
    favs.value = JSON.parse(localStorage.getItem(favKey()) || '[]')
  } catch {
    favs.value = []
  }
}
function addFav() {
  if (!embedUrl.value) return
  const label = dashInput.value || 'Grafana 首页'
  if (favs.value.some((f) => f.url === embedUrl.value)) return
  favs.value.push({ label, url: embedUrl.value })
  localStorage.setItem(favKey(), JSON.stringify(favs.value))
  ElMessage.success('已收藏')
}
function onFav(i: number | '') {
  if (i === '' || i === null) return
  const f = favs.value[i]
  if (!f) return
  dashInput.value = f.url
  embed(f.url)
  favSel.value = ''
}

// 构造 kiosk 内嵌地址：保留 dashboard 路径与原有参数，追加/覆盖 orgId=1&kiosk
function embed(input?: string) {
  const raw = (input || '').trim()
  let target: URL
  if (!raw) {
    target = new URL(baseURL.value + '/')
  } else {
    try {
      target = new URL(raw)
      // 允许粘贴「相对本 Grafana 的路径」
      if (target.origin === window.location.origin && raw.startsWith('/')) {
        target = new URL(baseURL.value + raw)
      }
    } catch {
      if (raw.startsWith('/')) target = new URL(baseURL.value + raw)
      else {
        ElMessage.warning('链接格式无效，请粘贴完整 URL 或以 / 开头的路径')
        return
      }
    }
  }
  target.searchParams.set('orgId', '1')
  target.searchParams.set('kiosk', '')
  embedUrl.value = target.toString()
  embedNotice.value = '若下方区域空白：请确认 Grafana 已开启 allow_embedding 与匿名只读（见集群管理/引导说明）'
}

async function save() {
  saving.value = true
  try {
    await grafanaApi.update(clusterStore.current || '', cfgURL.value)
    await clusterStore.load()
    ElMessage.success('已保存')
    cfgVisible.value = false
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function check() {
  checking.value = true
  try {
    const out = await grafanaApi.check(cfgURL.value)
    if (out.ok) ElMessage.success('服务端可达')
    else ElMessage.warning(out.error || '服务端不可达')
  } catch {
    /* 拦截器已提示 */
  } finally {
    checking.value = false
  }
}

watch(cfgVisible, (v) => {
  if (v) cfgURL.value = baseURL.value
})
watch(() => clusterStore.current, () => {
  embedUrl.value = ''
  dashInput.value = ''
  loadFavs()
})

onMounted(loadFavs)
</script>

<style scoped>
.grafana-page { display: flex; flex-direction: column; height: 100%; }
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 12px; flex-wrap: wrap; }
.label { font-weight: 600; }
.hint { color: #909399; font-size: 12px; word-break: break-all; }
.frame-wrap { flex: 1; min-height: 60vh; }
.grafana-frame { width: 100%; height: calc(100vh - 180px); min-height: 560px; border: 1px solid var(--kc-border, #dcdfe6); border-radius: 4px; background: #fff; }
.guide { text-align: left; max-width: 560px; color: #606266; font-size: 13px; line-height: 1.9; }
.guide pre { background: #f5f7fa; padding: 10px 14px; border-radius: 6px; font-size: 12px; line-height: 1.6; }
.cfg-tip { color: #909399; font-size: 12px; line-height: 1.5; margin-top: 4px; }
</style>
