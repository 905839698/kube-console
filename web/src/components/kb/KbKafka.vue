<template>
  <div>
    <div class="ops-bar">
      <el-button size="small" :loading="loading" @click="loadTopics">刷新 topic</el-button>
      <el-button size="small" :disabled="!writable" @click="openCreate">新建 topic</el-button>
      <el-button size="small" :loading="groupsLoading" @click="loadGroups">消费组与 lag</el-button>
      <span class="tip">Topic 管理在 broker 容器内执行 kafka-*.sh（PLAINTEXT 监听）；集群若启用了 SASL，命令会失败并回显原因。</span>
    </div>
    <div v-if="err" class="err-box">{{ err }}</div>

    <el-table border :data="topics" size="small" v-loading="loading" style="margin-top: 10px" row-key="name">
      <el-table-column type="expand">
        <template #default="{ row }">
          <el-table border :data="row.partitions" size="small" class="sub-table">
            <el-table-column prop="partition" label="分区" width="80" />
            <el-table-column prop="leader" label="Leader" width="100" />
            <el-table-column prop="replicas" label="Replicas" min-width="120">
              <template #default="{ row: p }">{{ p.replicas.join(', ') }}</template>
            </el-table-column>
            <el-table-column label="ISR" min-width="120">
              <template #default="{ row: p }">
                <el-tag v-if="p.offline" type="danger" size="small">offline</el-tag>
                <span>{{ p.isr.join(', ') || '-' }}</span>
              </template>
            </el-table-column>
          </el-table>
        </template>
      </el-table-column>
      <el-table-column prop="name" label="Topic" min-width="180" sortable />
      <el-table-column prop="partitionCount" label="分区数" width="90" />
      <el-table-column prop="replicationFactor" label="副本" width="80" />
      <el-table-column label="配置" min-width="260">
        <template #default="{ row }">
          <el-tag v-for="(v, k) in row.configs" :key="k" size="small" class="cfg-tag">{{ k }}={{ v }}</el-tag>
          <span v-if="!Object.keys(row.configs || {}).length" class="tip">默认</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.partitions.some((p: any) => p.offline)" type="warning" size="small">分区不可用</el-tag>
          <el-tag v-else-if="row.partitions.some((p: any) => p.isr.length < p.replicas.length)" type="warning" size="small">ISR 不足</el-tag>
          <el-tag v-else type="success" size="small">正常</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="180">
        <template #default="{ row }">
          <el-button size="small" :disabled="!writable" @click="openConfig(row)">改配置</el-button>
          <el-button size="small" type="danger" :disabled="!writable" @click="drop(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-table v-if="groups.length" border :data="groups" size="small" style="margin-top: 14px" v-loading="groupsLoading">
      <el-table-column prop="group" label="消费组" min-width="150" />
      <el-table-column prop="state" label="状态" width="100" />
      <el-table-column prop="members" label="成员" width="80" />
      <el-table-column prop="topic" label="Topic" min-width="140" />
      <el-table-column prop="partition" label="分区" width="80" />
      <el-table-column prop="currentOffset" label="已提交" width="110" />
      <el-table-column prop="logEndOffset" label="末尾" width="110" />
      <el-table-column label="Lag" width="110">
        <template #default="{ row }">
          <el-tag v-if="Number(row.lag) > 0" type="warning" size="small">{{ row.lag }}</el-tag>
          <span v-else>{{ row.lag || '-' }}</span>
        </template>
      </el-table-column>
    </el-table>

    <el-divider content-position="left">消息预览</el-divider>
    <div class="ops-bar">
      <el-select v-model="consume.topic" filterable placeholder="topic" size="small" style="width: 220px">
        <el-option v-for="t in topics" :key="t.name" :label="t.name" :value="t.name" />
      </el-select>
      <el-input-number v-model="consume.count" :min="1" :max="200" size="small" style="width: 110px" />
      <el-checkbox v-model="consume.fromBeginning" size="small">从头开始</el-checkbox>
      <el-button size="small" type="primary" :loading="consuming" @click="runConsume">拉取</el-button>
      <span class="tip">最多 200 条、8 秒超时；输出为 partition/timestamp/key 与消息体。</span>
    </div>
    <pre v-if="consume.out" class="out-box">{{ consume.out }}</pre>

    <el-dialog v-model="createDlg" title="新建 topic" width="520px">
      <el-form label-width="92px" size="small">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="字母数字 . _ -，如 order-events" />
        </el-form-item>
        <el-form-item label="分区数">
          <el-input-number v-model="form.partitions" :min="1" :max="1000" />
        </el-form-item>
        <el-form-item label="副本数">
          <el-input-number v-model="form.replicationFactor" :min="1" :max="10" />
          <span class="tip" style="margin-left: 8px">不能超过 broker 数</span>
        </el-form-item>
        <el-form-item label="清理策略">
          <el-select v-model="form.cleanup" style="width: 160px">
            <el-option v-for="v in ['delete', 'compact', 'delete,compact']" :key="v" :label="v" :value="v" />
          </el-select>
        </el-form-item>
        <el-form-item label="其它配置">
          <KvEditor v-model="form.configs" key-placeholder="retention.minutes" value-placeholder="10080" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="create">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="cfgDlg" :title="`topic 配置：${cfgTopic}`" width="560px">
      <div class="tip" style="margin-bottom: 6px">改动即时生效（kafka-configs.sh --alter）；留空表示不动。删除某项请把值清空并在下一行填写键名到「删除项」。</div>
      <KvEditor v-model="cfgSet" key-placeholder="retention.bytes" value-placeholder="-1" />
      <el-form-item label="删除项" label-width="70px" size="small" style="margin-top: 10px">
        <el-input v-model="cfgUnset" placeholder="逗号分隔的键名，如 min.insync.replicas" />
      </el-form-item>
      <template #footer>
        <el-button @click="cfgDlg = false">取消</el-button>
        <el-button type="primary" :loading="cfgSaving" @click="saveConfig">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { confirmDelete } from '../../utils/confirm'
import { kbApi, kbMatchesEngine, type KBKafkaGroup, type KBKafkaTopic, type KBCluster } from '../../api/kb'
import KvEditor from '../forms/KvEditor.vue'

const props = defineProps<{ cluster: KBCluster; writable: boolean }>()
const topics = ref<KBKafkaTopic[]>([])
const groups = ref<KBKafkaGroup[]>([])
const loading = ref(false)
const groupsLoading = ref(false)
const err = ref('')

const createDlg = ref(false)
const creating = ref(false)
const form = reactive({ name: '', partitions: 3, replicationFactor: 2, cleanup: 'delete', configs: {} as Record<string, string> })

const cfgDlg = ref(false)
const cfgTopic = ref('')
const cfgSet = ref<Record<string, string>>({})
const cfgUnset = ref('')
const cfgSaving = ref(false)

const consume = reactive({ topic: '', count: 20, fromBeginning: true, out: '' })
const consuming = ref(false)

watch(() => props.cluster?.name, () => {
  topics.value = []
  groups.value = []
  err.value = ''
  consume.out = ''
  if (props.cluster) loadTopics()
}, { immediate: true })

async function loadTopics() {
  if (!props.cluster) return
  // 双保险：抽屉理论上只会挂在本引擎页签下（父组件切引擎即关抽屉），
  // 这里再按引擎兜底一次，防止任何残留状态把 kafka 接口打到别的引擎集群上
  if (!kbMatchesEngine(props.cluster, 'kafka')) {
    err.value = '当前集群不是 Kafka 引擎，已跳过加载'
    topics.value = []
    return
  }
  loading.value = true
  err.value = ''
  try {
    topics.value = await kbApi.kafkaTopics(props.cluster.namespace, props.cluster.name)
  } catch (e: any) {
    err.value = e?.message || '取 topic 失败'
    topics.value = []
  } finally { loading.value = false }
}

async function loadGroups() {
  if (!props.cluster) return
  groupsLoading.value = true
  try {
    groups.value = await kbApi.kafkaGroups(props.cluster.namespace, props.cluster.name)
  } catch (e: any) {
    err.value = e?.message || '取消费组失败'
  } finally { groupsLoading.value = false }
}

function openCreate() {
  Object.assign(form, { name: '', partitions: 3, replicationFactor: 2, cleanup: 'delete', configs: {} })
  createDlg.value = true
}

async function create() {
  if (!form.name.trim()) { ElMessage.warning('topic 名必填'); return }
  const configs: Record<string, string> = { 'cleanup.policy': form.cleanup, ...form.configs }
  creating.value = true
  try {
    await kbApi.kafkaCreateTopic(props.cluster.namespace, props.cluster.name, {
      name: form.name.trim(), partitions: form.partitions, replicationFactor: form.replicationFactor, configs,
    })
    ElMessage.success('topic 已创建')
    createDlg.value = false
    loadTopics()
  } catch { /* 拦截器已提示 */ } finally { creating.value = false }
}

function openConfig(row: KBKafkaTopic) {
  cfgTopic.value = row.name
  cfgSet.value = { ...row.configs }
  cfgUnset.value = ''
  cfgDlg.value = true
}

async function saveConfig() {
  const unset = cfgUnset.value.split(',').map((s) => s.trim()).filter(Boolean)
  cfgSaving.value = true
  try {
    await kbApi.kafkaAlterTopic(props.cluster.namespace, props.cluster.name, cfgTopic.value, { set: cfgSet.value, unset })
    ElMessage.success('配置已提交')
    cfgDlg.value = false
    loadTopics()
  } catch { /* 拦截器已提示 */ } finally { cfgSaving.value = false }
}

async function drop(row: KBKafkaTopic) {
  try {
    await confirmDelete(row.name, { title: '删除 topic', warning: '消息与消费位点一并丢弃，不可撤销' })
  } catch { return }
  try {
    await kbApi.kafkaDeleteTopic(props.cluster.namespace, props.cluster.name, row.name)
    ElMessage.success('已删除')
    loadTopics()
  } catch { /* 拦截器已提示 */ }
}

async function runConsume() {
  if (!consume.topic) { ElMessage.warning('请选择 topic'); return }
  consuming.value = true
  consume.out = ''
  err.value = ''
  try {
    const r = await kbApi.kafkaConsume(props.cluster.namespace, props.cluster.name, {
      topic: consume.topic, count: consume.count, fromBeginning: consume.fromBeginning,
    })
    consume.out = r.output || '（窗口内没有新消息）'
  } catch (e: any) {
    err.value = e?.message || '拉取失败'
  } finally { consuming.value = false }
}
</script>

<style scoped>
.ops-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tip { font-size: 13px; color: #9ca3af; }
.err-box { margin-top: 8px; padding: 8px 10px; background: #fef2f2; color: #b91c1c; font-size: 13px; white-space: pre-wrap; border-radius: 4px; }
.cfg-tag { margin: 0 6px 4px 0; }
.sub-table { margin: 6px 12px; width: calc(100% - 24px); }
.out-box { margin-top: 10px; max-height: 320px; overflow: auto; background: #f8fafc; border: 1px solid #e5e7eb; padding: 8px; font-size: 13px; white-space: pre-wrap; }
</style>
