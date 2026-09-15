<!-- 容器文件浏览器：通过 exec(ls/cat/rm/mkdir/mv) 浏览和操作容器内文件 -->
<template>
  <div class="file-browser">
    <div class="fb-toolbar">
      <el-select v-model="container" size="small" style="width: 200px" placeholder="选择容器" @change="() => cd(currentPath)">
        <el-option v-for="c in containers" :key="c.name" :label="c.name" :value="c.name" />
      </el-select>
      <el-breadcrumb separator="/" class="fb-crumb">
        <el-breadcrumb-item>
          <el-link :underline="false" @click="cd('/')">/</el-link>
        </el-breadcrumb-item>
        <el-breadcrumb-item v-for="(seg, i) in crumbSegments" :key="i">
          <el-link :underline="false" @click="cd(seg.path)">{{ seg.name }}</el-link>
        </el-breadcrumb-item>
      </el-breadcrumb>
      <div class="fb-actions">
        <el-button size="small" @click="mkdirPrompt">新建文件夹</el-button>
        <el-upload :show-file-list="false" :auto-upload="false" :on-change="onPickUpload" :disabled="uploading">
          <el-button size="small" :loading="uploading">上传文件</el-button>
        </el-upload>
        <el-button size="small" :icon="Refresh" circle @click="() => cd(currentPath)" />
      </div>
    </div>

    <el-table border :data="entries" v-loading="loading" size="small" @row-dblclick="(row: FileEntryItem) => row.type === 'dir' && cd(joinPath(currentPath, row.name))">
      <el-table-column label="名称" min-width="280">
        <template #default="{ row }">
          <el-icon v-if="row.type === 'dir'" style="vertical-align: -2px"><FolderOpened /></el-icon>
          <el-icon v-else style="vertical-align: -2px"><Document /></el-icon>
          <el-link v-if="row.type === 'dir'" :underline="false" style="margin-left: 6px" @click="cd(joinPath(currentPath, row.name))">{{ row.name }}</el-link>
          <span v-else style="margin-left: 6px">{{ row.name }}</span>
          <span v-if="row.linkTarget" class="link-target"> → {{ row.linkTarget }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="size" label="大小" width="110" align="right" />
      <el-table-column prop="mtime" label="修改时间" width="150" />
      <el-table-column prop="mode" label="权限" width="110" />
      <el-table-column label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <el-button v-if="row.type !== 'dir'" size="small" text type="primary" @click="download(row)">下载</el-button>
          <el-button size="small" text type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
    <div v-if="!loading && !entries.length" class="empty">目录为空（或容器没有 sh，可改用「调试容器」后重试）</div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, FolderOpened, Document } from '@element-plus/icons-vue'
import { filesApi, type FileEntryItem } from '../api'
import { confirmDelete } from '../utils/confirm'

const props = defineProps<{ namespace: string; pod: string; containers: { name: string }[] }>()

const container = ref(props.containers[0]?.name || '')
const entries = ref<FileEntryItem[]>([])
const currentPath = ref('/')
const loading = ref(false)
const uploading = ref(false)

const crumbSegments = computed(() => {
  const segs = currentPath.value.split('/').filter(Boolean)
  return segs.map((name, i) => ({ name, path: '/' + segs.slice(0, i + 1).join('/') }))
})

function joinPath(dir: string, name: string) {
  return (dir === '/' ? '' : dir) + '/' + name
}

async function cd(path: string) {
  loading.value = true
  try {
    const res = await filesApi.list(props.namespace, props.pod, container.value, path)
    currentPath.value = res.path || path
    entries.value = (res.entries || []).filter((e) => e.name !== '.')
  } finally {
    loading.value = false
  }
}

async function download(row: FileEntryItem) {
  const url = filesApi.downloadUrl(props.namespace, props.pod, container.value, joinPath(currentPath.value, row.name))
  const a = document.createElement('a')
  a.href = url
  a.download = row.name
  a.click()
}

async function remove(row: FileEntryItem) {
  await confirmDelete(row.name, { title: '删除', warning: `将删除${row.type === 'dir' ? '目录' : '文件'}「${row.name}」` })
  await filesApi.action(props.namespace, props.pod, container.value, { action: 'rm', path: joinPath(currentPath.value, row.name) })
  ElMessage.success('已删除')
  await cd(currentPath.value)
}

async function mkdirPrompt() {
  const { value } = await ElMessageBox.prompt('文件夹名称', '新建文件夹', { inputPattern: /\S+/, inputErrorMessage: '名称不能为空' })
  await filesApi.action(props.namespace, props.pod, container.value, { action: 'mkdir', path: joinPath(currentPath.value, value.trim()) })
  await cd(currentPath.value)
}

async function onPickUpload(uploadFile: { raw?: File }) {
  const file = uploadFile.raw
  if (!file) return
  uploading.value = true
  try {
    await filesApi.upload(props.namespace, props.pod, container.value, joinPath(currentPath.value, file.name), file)
    ElMessage.success(`已上传 ${file.name}`)
    await cd(currentPath.value)
  } finally {
    uploading.value = false
  }
}

onMounted(() => cd('/'))
</script>

<style scoped>
.fb-toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.fb-crumb { flex: 1; }
.fb-actions { display: flex; gap: 8px; align-items: center; }
.link-target { color: #909399; font-size: 12px; }
.empty { text-align: center; color: #909399; padding: 30px 0; }
</style>
