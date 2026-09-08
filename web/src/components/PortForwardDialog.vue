<!-- 端口转发对话框：为当前 Pod 建立端口转发，展示全部活跃隧道并可停止 -->
<template>
  <el-dialog v-model="visible" title="端口转发" width="640px" @open="onOpen">
    <el-form inline>
      <el-form-item label="目标端口">
        <el-input-number v-model="port" :min="1" :max="65535" />
      </el-form-item>
      <el-form-item label="本地端口">
        <el-input-number v-model="localPort" :min="0" :max="65535" placeholder="0=自动" />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" :loading="starting" @click="start">开始转发</el-button>
      </el-form-item>
    </el-form>
    <el-alert type="info" :closable="false" style="margin-bottom: 12px"
      :title="`本地端口填 0 表示由服务端自动分配；启动后访问 http://127.0.0.1:<本地端口> 即达 Pod 端口`" />

    <el-table :data="forwards" size="small" v-loading="loading">
      <el-table-column prop="pod" label="Pod" min-width="160" show-overflow-tooltip />
      <el-table-column prop="remotePort" label="目标端口" width="90" align="center" />
      <el-table-column label="本地地址" min-width="180">
        <template #default="{ row }">
          <el-link type="primary" :underline="false" @click="copyAddr(row)">127.0.0.1:{{ row.localPort }}</el-link>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="80" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="danger" @click="stop(row)">停止</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { pfApi, type PortForwardItem } from '../api'

const props = defineProps<{ namespace: string; pod: string }>()
const visible = defineModel<boolean>({ default: false })

const forwards = ref<PortForwardItem[]>([])
const loading = ref(false)
const starting = ref(false)
const port = ref(80)
const localPort = ref(0)

async function onOpen() {
  await load()
  // 该 Pod 已有转发时，预填其目标端口
  const own = forwards.value.find((f) => f.pod === props.pod)
  if (own) port.value = own.remotePort
}

async function load() {
  loading.value = true
  try {
    forwards.value = await pfApi.list()
  } finally {
    loading.value = false
  }
}

async function start() {
  starting.value = true
  try {
    const t = await pfApi.start(props.namespace, props.pod, port.value, localPort.value)
    ElMessage.success(`转发已建立：127.0.0.1:${t.localPort} → ${props.pod}:${t.remotePort}`)
    await load()
  } finally {
    starting.value = false
  }
}

async function stop(row: PortForwardItem) {
  await pfApi.stop(row.id)
  ElMessage.success('已停止')
  await load()
}

async function copyAddr(row: PortForwardItem) {
  try {
    await navigator.clipboard.writeText(`http://127.0.0.1:${row.localPort}`)
    ElMessage.success('地址已复制')
  } catch {
    ElMessage.info(`http://127.0.0.1:${row.localPort}`)
  }
}
</script>
