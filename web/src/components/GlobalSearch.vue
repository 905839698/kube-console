<template>
  <div class="global-search" ref="wrapEl">
    <el-input
      v-model="q"
      placeholder="全局搜索资源 / 命名空间 / Pod..."
      :prefix-icon="Search"
      clearable
      size="small"
      class="search-input"
      @input="onInput"
      @focus="open = true"
      @keydown.enter="goFirst"
    />
    <transition name="el-zoom-in-top">
      <div v-if="open && q" class="search-panel" @click.stop>
        <div v-loading="loading" class="search-body">
          <template v-if="hasResults">
            <div v-for="group in groups" :key="group.type" class="search-group">
              <div class="group-title">
                {{ group.label }}
                <span class="group-count">{{ group.items.length }}</span>
              </div>
              <div v-for="item in group.items" :key="item.type + item.namespace + item.name" class="search-item" @click="go(item)">
                <el-icon class="item-icon"><component :is="typeIcon(item.type)" /></el-icon>
                <span class="item-name">{{ item.name }}</span>
                <span class="item-ns">{{ item.namespace || '集群级' }}</span>
              </div>
            </div>
          </template>
          <div v-else-if="!loading" class="search-empty">
            <el-icon :size="30"><Search /></el-icon>
            <p>未找到与「{{ q }}」匹配的资源</p>
          </div>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Search, Box, Grid, Share, Setting, Coin, Files, FolderOpened } from '@element-plus/icons-vue'
import { k8sApi, type SearchItem } from '../api'

const router = useRouter()
const q = ref('')
const open = ref(false)
const loading = ref(false)
const results = ref<SearchItem[]>([])
const wrapEl = ref<HTMLElement>()

let timer: ReturnType<typeof setTimeout> | undefined
let searchSeq = 0

const groupMeta: Record<string, { label: string; icon: any }> = {
  namespaces: { label: '命名空间', icon: FolderOpened },
  pods: { label: 'Pod', icon: Grid },
  deployments: { label: 'Deployment', icon: Box },
  statefulsets: { label: 'StatefulSet', icon: Box },
  daemonsets: { label: 'DaemonSet', icon: Box },
  services: { label: 'Service', icon: Share },
  configmaps: { label: 'ConfigMap', icon: Setting },
  storageclasses: { label: 'StorageClass', icon: Coin },
  nodes: { label: '节点', icon: Files },
}

const groups = computed(() =>
  Object.keys(groupMeta)
    .map((type) => ({ type, label: groupMeta[type].label, items: results.value.filter((r) => r.type === type) }))
    .filter((g) => g.items.length > 0),
)

const hasResults = computed(() => results.value.length > 0)

function onInput() {
  clearTimeout(timer)
  open.value = true
  const value = q.value.trim()
  if (!value) {
    results.value = []
    return
  }
  // 请求序号：快速改词时慢的旧响应后到会覆盖新结果（面板与输入框不一致）
  const seq = ++searchSeq
  timer = setTimeout(async () => {
    loading.value = true
    try {
      const res = await k8sApi.search(value)
      if (seq === searchSeq) results.value = res
    } catch {
      if (seq === searchSeq) results.value = []
    } finally {
      if (seq === searchSeq) loading.value = false
    }
  }, 300)
}

function typeIcon(type: string) {
  return groupMeta[type]?.icon || Files
}

function go(item: SearchItem) {
  open.value = false
  q.value = ''
  switch (item.type) {
    case 'namespaces':
      router.push({ path: '/namespaces', query: { search: item.name } })
      break
    case 'nodes':
      router.push(`/nodes/${item.name}`)
      break
    case 'pods':
      router.push({ path: `/pods/${item.namespace}/${item.name}` })
      break
    case 'deployments':
    case 'statefulsets':
    case 'daemonsets':
      router.push({ path: `/workloads/${item.type}/${item.name}`, query: { namespace: item.namespace } })
      break
    default:
      router.push({ path: `/resources/${item.type}`, query: { namespace: item.namespace } })
  }
}

function goFirst() {
  if (!hasResults.value) return
  go(results.value[0])
}

// 点击外部关闭
function onClickOutside(e: MouseEvent) {
  if (wrapEl.value && !wrapEl.value.contains(e.target as Node)) {
    open.value = false
  }
}

onMounted(() => document.addEventListener('mousedown', onClickOutside))
onBeforeUnmount(() => document.removeEventListener('mousedown', onClickOutside))
</script>

<style scoped>
.global-search { position: relative; width: 380px; }
.search-input :deep(.el-input__wrapper) {
  border-radius: 18px;
  background: #f3f4f6;
  box-shadow: none;
}
.search-input :deep(.el-input__wrapper.is-focus) {
  background: #fff;
  box-shadow: 0 0 0 1px var(--el-color-primary) inset;
}
.search-panel {
  position: absolute;
  top: 36px;
  left: 0;
  right: 0;
  background: #fff;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  z-index: 2000;
  overflow: hidden;
}
.search-body { max-height: 420px; overflow-y: auto; padding: 8px 0; }
.search-group { margin-bottom: 4px; }
.group-title {
  font-size: 12px;
  color: #9ca3af;
  padding: 4px 16px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.group-count {
  background: #f3f4f6;
  border-radius: 8px;
  padding: 0 6px;
  font-size: 11px;
}
.search-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 16px;
  cursor: pointer;
}
.search-item:hover { background: var(--el-color-primary-light-9); }
.item-icon { color: var(--el-color-primary); }
.item-name { flex: 1; font-size: 13px; color: #1f2937; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-ns { font-size: 12px; color: #9ca3af; }
.search-empty { text-align: center; color: #9ca3af; padding: 24px 0; }
.search-empty p { margin: 8px 0 0; font-size: 13px; }
</style>
