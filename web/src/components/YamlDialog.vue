<template>
  <el-dialog v-model="visible" :title="title" width="860px" top="5vh" destroy-on-close>
    <div class="yaml-dialog-body">
      <YamlEditor v-model="yaml" :readonly="readonly" />
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button v-if="!readonly" type="primary" :loading="applying" @click="onApply">应用 YAML</el-button>
      <el-button v-else @click="readonly = false">编辑</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { k8sApi } from '../api'
import YamlEditor from './YamlEditor.vue'

const props = defineProps<{
  modelValue: boolean
  /** 初始 YAML（打开时展示；为空则进入新建模式） */
  yaml?: string
  readonly?: boolean
}>()
const emit = defineEmits(['update:modelValue', 'applied'])

const visible = ref(props.modelValue)
const yaml = ref(props.yaml || '')
const readonly = ref(props.readonly ?? true)
const applying = ref(false)

watch(
  () => props.modelValue,
  (v) => {
    visible.value = v
    if (v) {
      yaml.value = props.yaml || ''
      readonly.value = props.readonly ?? true
    }
  },
)
watch(visible, (v) => emit('update:modelValue', v))

async function onApply() {
  if (!yaml.value.trim()) {
    ElMessage.warning('YAML 内容不能为空')
    return
  }
  try {
    await ElMessageBox.confirm('将创建或更新该资源，是否继续？', '应用 YAML', { type: 'warning' })
  } catch {
    return
  }
  applying.value = true
  try {
    const { created } = await k8sApi.applyYaml(yaml.value)
    ElMessage.success(created ? '资源创建成功' : '资源更新成功')
    visible.value = false
    emit('applied')
  } finally {
    applying.value = false
  }
}
</script>

<style scoped>
.yaml-dialog-body { height: 60vh; }
</style>
