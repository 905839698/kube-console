<template>
  <el-drawer v-model="visible" :title="`日志 - ${pod?.name || ''}`" size="75%" destroy-on-close>
    <div class="logs-toolbar">
      <el-select v-model="container" size="small" style="width: 200px">
        <el-option v-for="c in containers" :key="c" :label="c" :value="c" />
      </el-select>
      <el-select v-model="tailLines" size="small" style="width: 110px">
        <el-option v-for="n in [100, 500, 1000, 5000]" :key="n" :label="`最近 ${n} 行`" :value="n" />
      </el-select>
      <el-switch v-model="follow" active-text="跟随" inactive-text="暂停" size="small" @change="onFollowChange" />
      <el-button size="small" @click="start">重新加载</el-button>
      <el-button size="small" type="danger" plain @click="stop">停止</el-button>
    </div>
    <div ref="logBox" class="log-box" @scroll="onScroll">
      <pre class="log-content">{{ logText || '（无日志输出）' }}</pre>
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useUserStore } from '../store/user'
import { k8sApi, type PodItem } from '../api'

const props = defineProps<{ modelValue: boolean; pod?: PodItem; container?: string }>()
const emit = defineEmits(['update:modelValue'])

const userStore = useUserStore()
const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const containers = ref<string[]>([])
const container = ref('')
const tailLines = ref(500)
const follow = ref(true)
const logText = ref('')
const logBox = ref<HTMLElement>()
const autoScroll = ref(true)

let controller: AbortController | null = null
let reading = false

// 打开抽屉时获取容器列表
watch(visible, async (v) => {
  if (v && props.pod) {
    try {
      const detail = await k8sApi.podDetail(props.pod.namespace, props.pod.name)
      containers.value = detail.containers.map((c) => c.name)
      // 优先使用调用方指定的容器
      if (props.container && containers.value.includes(props.container)) {
        container.value = props.container
      } else {
        container.value = containers.value[0] || ''
      }
      start()
    } catch {
      ElMessage.error('获取容器列表失败')
    }
  } else {
    stop()
  }
})

watch(container, () => {
  if (visible.value) start()
})

function start() {
  if (!props.pod || !container.value) return
  stop()
  logText.value = ''
  autoScroll.value = true
  reading = true
  controller = new AbortController()
  const url = `/api/pods/${props.pod.name}/logs?namespace=${props.pod.namespace}&container=${container.value}&tailLines=${tailLines.value}&follow=${follow.value ? 1 : 0}`
  fetch(url, {
    headers: {
      Authorization: `Bearer ${userStore.token}`,
      // 集群名可能含非 ASCII，编码后传输（后端 ClusterName 解码）
      'X-Cluster': encodeURIComponent(localStorage.getItem('kc-cluster') || ''),
    },
    signal: controller.signal,
  })
    .then(async (resp) => {
      if (!resp.ok || !resp.body) {
        const text = await resp.text()
        ElMessage.error(text.slice(0, 200))
        reading = false
        return
      }
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      const pump = async (): Promise<void> => {
        const { done, value } = await reader.read()
        if (done) {
          reading = false
          return
        }
        logText.value += decoder.decode(value, { stream: true })
        if (autoScroll.value) {
          await nextTick()
          if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
        }
        await pump()
      }
      await pump()
    })
    .catch((e) => {
      if (e.name !== 'AbortError') {
        ElMessage.error('读取日志失败: ' + e.message)
      }
      reading = false
    })
}

function stop() {
  controller?.abort()
  controller = null
  reading = false
}

function onFollowChange(v: boolean) {
  if (v) {
    autoScroll.value = true
    nextTick(() => {
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    })
  }
}

function onScroll() {
  if (!logBox.value) return
  const el = logBox.value
  autoScroll.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

onBeforeUnmount(stop)
</script>

<style scoped>
.logs-toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 10px; }
.log-box { height: calc(100vh - 180px); overflow-y: auto; background: #0d1117; border-radius: 6px; padding: 12px; }
.log-content { color: #c9d1d9; font-family: Consolas, Menlo, monospace; font-size: 12.5px; line-height: 1.55; margin: 0; white-space: pre-wrap; word-break: break-all; }
</style>
