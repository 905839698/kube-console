<!-- 任务日志：SSE（EventSource）流式展示 TaskRun 日志（?token= 查询鉴权）。
     重连策略：EventSource 自动重连会复用原 URL（60s 流 token 已过期 → 必然 401），
     故 onerror 时主动 close、重新取 token 建连（指数退避，约 5 分钟后放弃）。 -->
<template>
  <div>
    <div class="log-meta">
      <span>{{ running ? '● 实时日志（运行中）' : '○ 日志（已结束）' }}</span>
      <span>{{ truncated ? `仅保留最近 ${lines.length} 行` : `${lines.length} 行` }}</span>
    </div>
    <pre ref="boxRef" class="log-box" @scroll="onScroll">
      <template v-if="loading && !lines.length"><span class="waiting">等待日志（Pod 可能仍在启动）…</span></template>
      <template v-else>{{ content }}</template>
      <span v-if="error" class="warn">{{ error }}</span>
    </pre>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { taskLogsUrl } from '../../api/ci'

const props = defineProps<{ runId: number; taskId: number; running: boolean }>()

const MAX_LINES = 5000
const RECONNECT_BASE_MS = 2000
const RECONNECT_MAX_MS = 15000
const RECONNECT_MAX_TRIES = 20

const lines = ref<string[]>([])
const truncated = ref(false)
const loading = ref(true)
const error = ref('')
const boxRef = ref<HTMLElement | null>(null)
let autoScroll = true
const content = computed(() => lines.value.join('\n'))

let es: EventSource | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
let runningFlag = props.running

function cleanup() {
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
  es?.close()
  es = null
}

function connect() {
  cleanup()
  void taskLogsUrl(props.runId, props.taskId)
    .then((url) => {
      es = new EventSource(url)
      es.onmessage = (e) => {
        tries = 0
        loading.value = false
        error.value = ''
        const text = e.data
        if (text === '[end]') { es?.close(); es = null; return }
        lines.value = lines.value.length >= MAX_LINES
          ? [...lines.value.slice(lines.value.length - MAX_LINES + 1), text]
          : [...lines.value, text]
        if (lines.value.length >= MAX_LINES) truncated.value = true
      }
      es.addEventListener('start', () => { loading.value = false; error.value = '' })
      es.addEventListener('end', () => { loading.value = false; es?.close(); es = null })
      es.onerror = () => {
        es?.close()
        es = null
        if (!runningFlag) return // 任务已结束，流自然断开
        tries += 1
        if (tries > RECONNECT_MAX_TRIES) { error.value = '日志连接多次重连失败，请关闭后重新打开'; return }
        const delay = Math.min(RECONNECT_BASE_MS * 2 ** (tries - 1), RECONNECT_MAX_MS)
        error.value = `日志连接中断，${Math.round(delay / 1000)}s 后自动重连…`
        reconnectTimer = setTimeout(connect, delay)
      }
    })
    .catch((e: any) => {
      tries += 1
      if (tries > RECONNECT_MAX_TRIES) { error.value = `日志连接失败: ${e?.message ?? e}`; return }
      const delay = Math.min(RECONNECT_BASE_MS * 2 ** (tries - 1), RECONNECT_MAX_MS)
      reconnectTimer = setTimeout(connect, delay)
    })
}
let tries = 0

watch(() => [props.runId, props.taskId], () => {
  lines.value = []
  truncated.value = false
  loading.value = true
  error.value = ''
  autoScroll = true
  tries = 0
  connect()
}, { immediate: true })

watch(() => props.running, (v) => { runningFlag = v })
onBeforeUnmount(cleanup)

function onScroll() {
  const el = boxRef.value
  if (!el) return
  // 离开底部 → 暂停自动滚动；滚回底部附近 → 恢复
  autoScroll = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}
watch(lines, () => {
  if (autoScroll && boxRef.value) boxRef.value.scrollTop = boxRef.value.scrollHeight
})
</script>

<style scoped>
.log-meta { font-size: 12px; color: #909399; margin-bottom: 4px; display: flex; justify-content: space-between; }
.log-box {
  margin: 0; height: 420px; overflow-y: auto; background: #1e1e1e; color: #d4d4d4;
  padding: 12px; border-radius: 8px; font-size: 12px; line-height: 1.6;
  white-space: pre-wrap; word-break: break-all;
}
.waiting { color: #888; }
.warn { color: #faad14; white-space: normal; }
</style>
