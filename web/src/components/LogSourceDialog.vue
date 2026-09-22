<template>
  <el-dialog v-model="visible" :title="title" width="560px">
    <el-form label-width="110px" size="small">
      <el-form-item label="集群">
        <el-select v-model="cfg.clusterName" style="width: 100%">
          <el-option v-for="cl in clusters" :key="cl" :label="cl" :value="cl" />
        </el-select>
      </el-form-item>
      <el-form-item label="ES 命名空间"><el-input v-model="cfg.namespace" placeholder="如 prd-public-service" /></el-form-item>
      <el-form-item label="ES 服务名"><el-input v-model="cfg.service" placeholder="如 elasticsearch-es-http" /></el-form-item>
      <el-form-item label="ES 端口"><el-input-number v-model="cfg.port" :min="1" :max="65535" /></el-form-item>
      <el-form-item label="直连地址">
        <el-input v-model="cfg.directURL" placeholder="可选；跨集群/外部 ES 时填实际可达地址" />
        <div class="form-tip">留空时：匿名 ES 经 apiserver 服务代理访问；安全 ES（填了用户名）自动走集群内 Service DNS 直连（控制台与 ES 同集群即可用）。注意 apiserver 代理会剥离认证头，安全 ES 无法走代理——控制台与 ES 不同集群时请填实际可达地址（如 http://节点IP:NodePort）。</div>
      </el-form-item>
      <el-form-item label="行日志索引前缀">
        <el-input v-model="cfg.indexPrefix" placeholder="如 k8s- 或 k8s-{namespace}-（空=logstash-*）" />
        <div class="form-tip">标准输出日志与「单行文本」容器内采集都写在这里，日志检索按此模式查询。索引按命名空间拆分时用 {namespace} 占位符，如 k8s-{namespace}-。</div>
      </el-form-item>
      <el-form-item label="JSON 索引前缀">
        <el-input v-model="cfg.jsonIndexPrefix" placeholder="如 logstash-（空=logstash-*）" />
        <div class="form-tip">「JSON（字段展开）」采集写入的索引，与行日志分开，避免任意 JSON 字段污染行日志索引 mapping。检索选来源=JSON 时按此模式查询。</div>
      </el-form-item>
      <el-form-item label="用户名"><el-input v-model="cfg.username" placeholder="ES basic auth（可选）" /></el-form-item>
      <el-form-item label="密码"><el-input v-model="cfg.password" type="password" show-password placeholder="留空保持不变" /></el-form-item>

      <el-divider content-position="left">事件归档</el-divider>
      <el-form-item label="启用事件归档">
        <el-switch v-model="cfg.eventEnabled" />
        <span class="form-tip" style="margin-left: 12px">开启后服务端轮询集群 Events 增量写入本 ES（kc-events-* 索引），事件中心可回查历史</span>
      </el-form-item>
      <el-form-item label="事件索引前缀">
        <el-input v-model="cfg.eventIndexPrefix" placeholder="默认 kc-events-" />
        <div class="form-tip">按天滚动：{前缀}yyyy.MM.dd；默认保留 30 天（configs/config.yaml 的 obs.eventRetentionDays）</div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="testSource" :loading="testing">测试连通</el-button>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="saveSource">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { logsApi, clusterApi, type LogSourceItem } from '../api'
import { useClusterStore } from '../store/cluster'

const props = defineProps<{ modelValue: boolean; title?: string }>()
const emit = defineEmits(['update:modelValue', 'saved'])

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const clusterStore = useClusterStore()
const clusters = ref<string[]>([])
const saving = ref(false)
const testing = ref(false)
const cfg = reactive<Partial<LogSourceItem> & { password?: string }>({
  clusterName: '', namespace: '', service: '', port: 9200, directURL: '', indexPrefix: '', jsonIndexPrefix: '',
  username: '', password: '', eventEnabled: false, eventIndexPrefix: 'kc-events-',
})

async function loadConfig() {
  try {
    const list = await logsApi.sources()
    const cur = list.find((s) => s.clusterName === (cfg.clusterName || clusterStore.current))
    if (cur) Object.assign(cfg, cur, { password: '' })
    else cfg.clusterName = clusterStore.current || ''
  } catch { /* ignore */ }
}

async function onClusterChange() {
  // 切换集群时载入该集群已有配置
  try {
    const list = await logsApi.sources()
    const cur = list.find((s) => s.clusterName === cfg.clusterName)
    if (cur) Object.assign(cfg, cur, { password: '' })
    else {
      Object.assign(cfg, { namespace: '', service: '', port: 9200, directURL: '', indexPrefix: '', jsonIndexPrefix: '', username: '', password: '', eventEnabled: false, eventIndexPrefix: 'kc-events-' })
    }
  } catch { /* ignore */ }
}

async function saveSource() {
  saving.value = true
  try {
    await logsApi.saveSource({ ...cfg, clusterName: cfg.clusterName || clusterStore.current || '' })
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  } finally {
    saving.value = false
  }
}

async function testSource() {
  testing.value = true
  try {
    await logsApi.testSource({ ...cfg, clusterName: cfg.clusterName || clusterStore.current || '' })
    ElMessage.success('连通成功')
  } catch {
    /* 拦截器已提示 */
  } finally {
    testing.value = false
  }
}

watch(visible, async (v) => {
  if (!v) return
  try {
    const list = await clusterApi.list()
    clusters.value = list.map((c) => c.name)
  } catch { /* ignore */ }
  await loadConfig()
})
watch(() => cfg.clusterName, () => { if (visible.value) onClusterChange() })
</script>

<style scoped>
.form-tip { color: #909399; font-size: 12px; line-height: 1.5; }
</style>
