<template>
  <el-dialog
    :model-value="modelValue"
    title="调整镜像版本"
    width="680px"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
  >
    <div v-loading="loading">
      <div v-for="row in rows" :key="(row.init ? 'i-' : '') + row.name" class="img-row">
        <div class="img-row-head">
          <el-tag v-if="row.init" size="small" type="warning">初始化容器</el-tag>
          <span class="img-name">{{ row.name }}</span>
          <span v-if="row.isDigest" class="img-hint">digest 镜像，不支持按 tag 调整</span>
        </div>
        <div class="img-repo">{{ row.repo }}</div>
        <div class="img-tag-row" v-if="!row.isDigest">
          <el-select
            v-model="row.newTag"
            filterable
            allow-create
            default-first-option
            size="small"
            style="width: 260px"
            :loading="row.tagsLoading"
            placeholder="选择或输入 tag"
          >
            <el-option v-for="t in row.tags" :key="t" :label="t" :value="t" />
          </el-select>
          <span v-if="row.tagsError" class="img-hint">{{ row.tagsError }}，可手动输入</span>
        </div>
      </div>
      <el-empty v-if="!loading && !rows.length" description="未找到可调整的容器镜像" :image-size="60" />
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" :disabled="!changed" @click="doUpdate">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { k8sApi, registryApi } from '../api'
import { parseYaml, dumpYaml } from '../forms/utils'

const props = defineProps<{ modelValue: boolean; kind: string; namespace: string; name: string }>()
const emit = defineEmits(['update:modelValue', 'saved'])

interface ImageRow {
  name: string
  init: boolean
  repo: string // 镜像地址（不含 :tag / @digest）
  isDigest: boolean
  tags: string[]
  tagsLoading: boolean
  tagsError: string
  newTag: string
  origTag: string
}

const loading = ref(false)
const saving = ref(false)
const rows = ref<ImageRow[]>([])

// 拆分镜像引用：host/project/repo[:tag|@digest]（tag 分隔符须位于最后一个 / 之后，避免误伤 registry:5000）
function splitImage(image: string): { repo: string; ref: string; isDigest: boolean } {
  const at = image.indexOf('@')
  if (at >= 0) return { repo: image.slice(0, at), ref: image.slice(at + 1), isDigest: true }
  const lastSlash = image.lastIndexOf('/')
  const colon = image.lastIndexOf(':')
  if (colon > lastSlash) return { repo: image.slice(0, colon), ref: image.slice(colon + 1), isDigest: false }
  return { repo: image, ref: 'latest', isDigest: false }
}

// CronJob 的 Pod 模板在 spec.jobTemplate.spec.template，其余工作负载在 spec.template
function findPodSpec(obj: any): any {
  return obj?.spec?.template?.spec || obj?.spec?.jobTemplate?.spec?.template?.spec || null
}

async function load() {
  if (!props.kind || !props.name) return
  loading.value = true
  rows.value = []
  try {
    const { yaml } = await k8sApi.getYaml(props.kind, props.namespace, props.name)
    const podSpec = findPodSpec(parseYaml(yaml))
    if (!podSpec) throw new Error('未找到 Pod 模板')
    const inits: any[] = podSpec.initContainers || []
    const list: ImageRow[] = []
    for (const c of [...inits, ...(podSpec.containers || [])]) {
      if (!c?.image) continue
      const { repo, ref: curRef, isDigest } = splitImage(c.image)
      list.push({ name: c.name, init: inits.some((x: any) => x.name === c.name), repo, isDigest, tags: [], tagsLoading: false, tagsError: '', newTag: isDigest ? '' : curRef, origTag: curRef })
    }
    rows.value = list
    // 按 repo 去重拉取 tag 列表（静默请求，失败降级为手动输入）
    const byRepo = new Map<string, ImageRow[]>()
    for (const row of rows.value) {
      if (row.isDigest) continue
      row.tagsLoading = true
      if (!byRepo.has(row.repo)) byRepo.set(row.repo, [])
      byRepo.get(row.repo)!.push(row)
    }
    await Promise.all([...byRepo.entries()].map(async ([repo, rs]) => {
      let tags: string[] = []
      let error = ''
      try {
        const r = await registryApi.imageTagsQuiet(repo)
        tags = r.tags || []
      } catch (e: any) {
        error = e?.response?.data?.message || e?.message || 'tag 列表获取失败'
      }
      for (const row of rs) {
        row.tags = tags
        row.tagsError = error
        row.tagsLoading = false
      }
    }))
  } catch (e: any) {
    ElMessage.error(`加载镜像信息失败：${e?.message || e}`)
    emit('update:modelValue', false)
  } finally {
    loading.value = false
  }
}

const changed = computed(() => rows.value.some((r) => !r.isDigest && r.newTag && r.newTag !== r.origTag))

async function doUpdate() {
  saving.value = true
  try {
    // 以保存时的最新 YAML 为基线改写镜像，避免覆盖期间的其他修改
    const { yaml } = await k8sApi.getYaml(props.kind, props.namespace, props.name)
    const obj = parseYaml(yaml)
    const podSpec = findPodSpec(obj)
    if (!podSpec) throw new Error('未找到 Pod 模板')
    let count = 0
    for (const c of [...(podSpec.initContainers || []), ...(podSpec.containers || [])]) {
      const row = rows.value.find((r) => r.name === c.name && !r.isDigest && r.newTag && r.newTag !== r.origTag)
      if (row) {
        c.image = `${row.repo}:${row.newTag}`
        count++
      }
    }
    if (!count) return
    await k8sApi.applyYaml(dumpYaml(obj))
    ElMessage.success(`镜像已更新（${count} 个容器），将触发滚动更新`)
    emit('update:modelValue', false)
    emit('saved')
  } catch (e: any) {
    ElMessage.error(`更新镜像失败：${e?.message || e}`)
  } finally {
    saving.value = false
  }
}

watch(() => props.modelValue, (v) => {
  if (v) load()
})
</script>

<style scoped>
.img-row { border: 1px dashed var(--kc-border, #dcdfe6); border-radius: 4px; padding: 10px; margin-bottom: 8px; }
.img-row-head { display: flex; align-items: center; gap: 6px; }
.img-name { font-weight: 600; font-size: 13px; }
.img-repo { font-family: ui-monospace, Consolas, monospace; font-size: 12px; color: #606266; margin: 4px 0 6px; word-break: break-all; }
.img-hint { font-size: 12px; color: #e6a23c; }
</style>
