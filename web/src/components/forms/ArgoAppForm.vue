<!-- ArgoCD Application 可视化表单：source / destination / project / syncPolicy 核心字段。
     高级字段（sources 多源、helm 参数、syncOptions、ignoreDifferences 等）在 YAML tab 编辑。 -->
<template>
  <el-form label-width="120px" size="small">
    <el-divider content-position="left">基础</el-divider>
    <el-form-item label="名称">
      <el-input :model-value="o.metadata?.name" disabled />
      <span class="hint">名称创建后不可改</span>
    </el-form-item>
    <el-form-item label="命名空间">
      <el-input :model-value="o.metadata?.namespace" disabled placeholder="应用对象所在 ns（通常是 argocd）" />
    </el-form-item>
    <el-form-item label="Project">
      <el-input v-model="o.spec.project" placeholder="default" style="width: 260px" />
    </el-form-item>

    <el-divider content-position="left">Source（Git 仓库 / Helm Chart）</el-divider>
    <el-form-item label="来源类型">
      <el-radio-group v-model="sourceType" @change="onSourceTypeChange">
        <el-radio-button value="git">Git 目录</el-radio-button>
        <el-radio-button value="helm">Helm Chart</el-radio-button>
      </el-radio-group>
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
import { computed } from 'vue'
import KvEditor from './KvEditor.vue'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue', 'change'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

// parse 保证 spec.source/destination/syncPolicy 结构存在，这里直接绑定
const sourceType = computed(() => (o.value?.spec?.source?.chart ? 'helm' : 'git'))
function onSourceTypeChange(v: string) {
  const src = o.value.spec.source
  if (v === 'helm' && !src.chart) {
    src.chart = ''
  } else if (v === 'git') {
    delete src.chart
    if (!src.path) src.path = ''
  }
  emit('change')
}

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
    if (arr.length) o.value.spec.source.helm.valueFiles = arr
    else delete o.value.spec.source.helm?.valueFiles
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
</style>
