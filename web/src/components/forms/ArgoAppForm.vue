<!-- ArgoCD Application 可视化表单：source / destination / project / syncPolicy 核心字段。
     高级字段（sources 多源、helm 参数、syncOptions、ignoreDifferences 等）在 YAML tab 编辑。 -->
<template>
  <el-form label-width="120px" size="small">
    <el-divider content-position="left">基础</el-divider>
    <el-form-item label="名称">
      <!-- 创建时可填：名称就是 Application 对象名；编辑时锁定（改名等于新建另一个对象） -->
      <el-input v-model="o.metadata.name" :disabled="!creating" placeholder="如 my-app" />
      <span class="hint">{{ creating ? 'Application 对象名（小写字母/数字/-/.）' : '名称创建后不可改' }}</span>
    </el-form-item>
    <el-form-item label="命名空间">
      <el-input v-model="o.metadata.namespace" :disabled="!creating" placeholder="应用对象所在 ns（通常是 argocd）" />
      <span v-if="!creating" class="hint">对象所在 ns 不可改（迁移 ns 等于新建对象）</span>
    </el-form-item>
    <el-form-item label="Project">
      <!-- 必须选已存在的 AppProject：填了不存在的项目名，ArgoCD 会拒绝加载整个应用，
           状态恒为 Unknown 并报 app is not allowed in project X, or the project does not exist -->
      <el-select v-if="projects" v-model="projectName" filterable placeholder="default" style="width: 300px">
        <el-option v-for="p in projectOptions" :key="p.value" :value="p.value" :label="p.label" />
      </el-select>
      <!-- 项目列表拉取失败时退回手填，不阻塞编辑；同样走 projectName 归一（清空 = default，
           避免存下空串——spec.project 是 CRD 必填字段，空串会被 API server 拒绝） -->
      <el-input v-else v-model="projectName" placeholder="default" style="width: 260px" />
      <span class="hint">AppProject（必须是集群中已创建的项目）</span>
    </el-form-item>
    <el-alert v-for="(w, i) in projectWarnings" :key="i" type="warning" :closable="false" show-icon class="warn" :title="w" />

    <el-divider content-position="left">Source（Git 仓库 / Helm Chart）</el-divider>
    <el-form-item label="来源类型">
      <el-radio-group v-model="sourceType">
        <el-radio-button value="git">Git 目录</el-radio-button>
        <el-radio-button value="helm">Helm Chart</el-radio-button>
      </el-radio-group>
      <span class="hint">Helm 走 chart（相对仓库根目录），Git 走 path</span>
    </el-form-item>
    <el-form-item label="仓库地址" required>
      <el-input v-model="o.spec.source.repoURL" placeholder="https://github.com/org/repo.git 或 http://helm-repo/" style="width: 460px" />
    </el-form-item>
    <template v-if="sourceType === 'helm'">
      <el-form-item label="Chart 名" required>
        <el-input v-model="o.spec.source.chart" placeholder="相对仓库根目录的 chart 路径，如 charts/my-app" style="width: 320px" />
      </el-form-item>
      <el-form-item label="Value 文件">
        <el-input v-model="valueFilesText" placeholder="helm.valueFiles，逗号分隔，如 values-prod.yaml" style="width: 380px" />
      </el-form-item>
    </template>
    <el-form-item v-else label="路径">
      <el-input v-model="o.spec.source.path" placeholder="仓库内清单目录，如 manifests/my-app" style="width: 320px" />
    </el-form-item>
    <el-form-item label="Revision">
      <el-input v-model="o.spec.source.targetRevision" placeholder="分支 / tag / commit，如 main" style="width: 260px" />
    </el-form-item>

    <el-divider content-position="left">Destination（部署目标）</el-divider>
    <el-form-item label="集群 Server">
      <el-input v-model="o.spec.destination.server" placeholder="https://kubernetes.default.svc（当前集群）或集群 URL" style="width: 420px" />
    </el-form-item>
    <el-form-item label="目标命名空间">
      <el-input v-model="o.spec.destination.namespace" placeholder="应用部署到的 ns（不是 Application 对象自身所在 ns）" style="width: 260px" />
    </el-form-item>

    <el-divider content-position="left">同步策略</el-divider>
    <el-form-item label="自动同步">
      <el-switch v-model="automated" />
      <span class="hint">关闭则只能手动同步（ArgoCD UI / argocd app sync）</span>
    </el-form-item>
    <template v-if="automated">
      <el-form-item label="自愈 (selfHeal)">
        <el-switch v-model="o.spec.syncPolicy.selfHeal" />
        <span class="hint">集群内手动改动被回滚到 Git 声明状态</span>
      </el-form-item>
      <el-form-item label="清理 (prune)">
        <el-switch v-model="o.spec.syncPolicy.prune" />
        <span class="hint">Git 中删除的资源同步时从集群删除</span>
      </el-form-item>
    </template>
    <el-form-item label="syncOptions">
      <el-input v-model="syncOptionsText" placeholder="逗号分隔，如 CreateNamespace=true, PruneLast=true" style="width: 420px" />
    </el-form-item>

    <el-form-item label="标签">
      <KvEditor v-model="o.metadata.labels" key-placeholder="key" value-placeholder="value" />
    </el-form-item>
  </el-form>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import KvEditor from './KvEditor.vue'
import { argocdApi, type ArgoCDProject } from '../../api'
import { argoSourceType, projectIssues, setArgoSourceType } from '../../forms/argoApp'

const props = defineProps<{ modelValue: any; creating?: boolean }>()
const emit = defineEmits(['update:modelValue', 'change'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})
// 是否新建：名称/命名空间仅在新建时可编辑（创建模式由 ObjectEditor 传入）
const creating = computed(() => !!props.creating)

// 来源类型：可写 computed，点击即落到对象上（spec.source.chart 的存在与否决定形态）。
// 用 computed 而非本地 ref，YAML tab 改动也能立刻反映到单选框上。
const sourceType = computed({
  get: () => argoSourceType(o.value?.spec?.source),
  set: (v: 'git' | 'helm') => {
    setArgoSourceType(o.value, v)
    emit('change')
  },
})

const automated = computed({
  get: () => !!o.value?.spec?.syncPolicy?.automated,
  set: (v: boolean) => {
    if (v) {
      o.value.spec.syncPolicy.automated = {}
    } else {
      delete o.value.spec.syncPolicy.automated
    }
    emit('change')
  },
})

// ---- AppProject：只允许选已存在的项目；项目不匹配（仓库/目标未被允许）时保存前提示 ----
// null = 未加载或拉取失败（退回手填输入框，不阻塞编辑）
const projects = ref<ArgoCDProject[] | null>(null)

onMounted(async () => {
  try {
    const r = await argocdApi.projects()
    projects.value = r.installed ? r.items || [] : null
  } catch {
    projects.value = null
  }
})

const projectName = computed({
  get: () => o.value?.spec?.project || 'default',
  set: (v: string) => {
    o.value.spec.project = v || 'default'
    emit('change')
  },
})

// 应用对象所在 ns：ArgoCD 按【应用自己所在 ns】解析 AppProject，
// 同名项目在不同 ns 是两个不同的项目，匹配必须带 ns
const appNs = computed(() => String(o.value?.metadata?.namespace || '').trim())

// 选项：本应用 ns 内的现有项目 + 当前值（当前值不在时也要列出来，否则下拉显示成空、看不出问题）
const projectOptions = computed(() => {
  const all = projects.value || []
  const inNs = appNs.value ? all.filter((p) => p.namespace === appNs.value) : all
  const opts = inNs.map((p) => ({ value: p.name, label: p.description ? `${p.name}（${p.description}）` : p.name }))
  const cur = projectName.value
  if (cur && !inNs.some((p) => p.name === cur)) {
    // 区分「整个集群没有」与「在别的 ns」（后者 ArgoCD 同样解析不到）
    const elsewhere = all.find((p) => p.name === cur && p.namespace !== appNs.value)
    opts.unshift({ value: cur, label: elsewhere ? `${cur}（在 ${elsewhere.namespace}，非本应用 ns）` : `${cur}（不存在）` })
  }
  return opts
})

const projectWarnings = computed(() => {
  const all = projects.value
  if (!all) return []
  const name = projectName.value
  const inNs = appNs.value ? all.filter((p) => p.namespace === appNs.value) : all
  const known = inNs.find((p) => p.name === name)
  if (!known) {
    const elsewhere = all.find((p) => p.name === name)
    if (elsewhere && appNs.value) {
      return [`AppProject「${name}」存在于 ${elsewhere.namespace}，但当前应用对象在 ${appNs.value}——ArgoCD 只解析应用自身 ns 内的项目，该应用会被拒绝加载（状态恒为 Unknown）。请在 ${appNs.value} 内创建同名项目，或把应用移到 ${elsewhere.namespace}。`]
    }
    const existing = inNs.map((p) => p.name).join('、') || '（无）'
    return [`AppProject「${name}」在 ${appNs.value || '应用所在 ns'} 内不存在（现有：${existing}）。ArgoCD 会拒绝加载该应用并报 “app is not allowed in project ${name}, or the project does not exist”，状态恒为 Unknown——请改选已存在的项目，或先在 ArgoCD 中创建该项目。`]
  }
  return projectIssues(known, {
    repoURL: o.value?.spec?.source?.repoURL,
    server: o.value?.spec?.destination?.server,
    namespace: o.value?.spec?.destination?.namespace,
  })
})

// 数组 ↔ 逗号文本
function arrToText(a: unknown[]): string {
  return (a || []).join(', ')
}
function textToArr(v: string): string[] {
  return v.split(/[,，]/).map((s) => s.trim()).filter(Boolean)
}
const valueFilesText = computed({
  get: () => arrToText(o.value?.spec?.source?.helm?.valueFiles || []),
  set: (v: string) => {
    const arr = textToArr(v)
    const src = o.value.spec.source
    // helm 块可能还不存在（刚从 Git 切过来），按需创建/回收，避免写 undefined 报错
    if (arr.length) {
      src.helm = src.helm || {}
      src.helm.valueFiles = arr
    } else if (src.helm) {
      delete src.helm.valueFiles
    }
    emit('change')
  },
})
const syncOptionsText = computed({
  get: () => arrToText(o.value?.spec?.syncPolicy?.syncOptions || []),
  set: (v: string) => {
    const arr = textToArr(v)
    if (arr.length) o.value.spec.syncPolicy.syncOptions = arr
    else delete o.value.spec.syncPolicy.syncOptions
    emit('change')
  },
})
</script>

<style scoped>
.hint { color: #909399; font-size: 12px; margin-left: 10px; }
.warn { margin: 0 0 12px 0; }
</style>
