<!-- 告警规则查看/编辑：从源 PrometheusRule CR 读取单条规则 YAML，保存时只替换该条并整体写回 CR（operator 自动同步生效） -->
<template>
  <el-dialog v-model="visible" :title="`告警规则 ${alert}`" width="860px" top="6vh">
    <div v-loading="loading">
      <el-alert
        v-if="loaded && !errorMsg"
        type="info"
        :closable="false"
        style="margin-bottom: 10px"
        :title="`源 PrometheusRule：${source} ／ 分组：${group}。保存后由 prometheus-operator 自动同步生效（通常数秒内），无需手动 reload。`"
      />
      <el-alert v-if="errorMsg" type="error" :closable="false" style="margin-bottom: 10px" :title="errorMsg" />
      <div v-if="loaded && !errorMsg" style="height: 420px">
        <YamlEditor v-model="ruleText" :readonly="!editing" style="height: 100%" />
      </div>
    </div>
    <template #footer>
      <el-button @click="visible = false">关闭</el-button>
      <el-button v-if="!editing" type="primary" plain :disabled="!loaded || !!errorMsg" @click="editing = true">编辑</el-button>
      <el-button v-else type="primary" :loading="saving" @click="save">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { load as yamlLoad, dump as yamlDump } from 'js-yaml'
import { k8sApi } from '../../api'
import YamlEditor from '../../components/YamlEditor.vue'

const props = defineProps<{
  group: string
  alert: string
  /** 源 PrometheusRule CR：ns/name */
  source: string
  /** 打开时直接进入编辑态 */
  initialEditing?: boolean
}>()
const emit = defineEmits(['saved'])
const visible = defineModel<boolean>({ default: false })

const PROM_RULE_GVR = { group: 'monitoring.coreos.com', version: 'v1', resource: 'prometheusrules' }

const loading = ref(false)
const saving = ref(false)
const loaded = ref(false)
const editing = ref(false)
const errorMsg = ref('')
const ruleText = ref('')
let crObj: any = null

// 在 CR 的 spec.groups 中定位 (group, alert)；找不到返回 null
function locate(groups: any[]): { gIdx: number; rIdx: number } | null {
  for (let gi = 0; gi < groups.length; gi++) {
    const rules = groups[gi]?.rules || []
    for (let ri = 0; ri < rules.length; ri++) {
      if (groups[gi].name === props.group && rules[ri]?.alert === props.alert) {
        return { gIdx: gi, rIdx: ri }
      }
    }
  }
  return null
}

async function load() {
  loading.value = true
  loaded.value = false
  errorMsg.value = ''
  editing.value = !!props.initialEditing
  ruleText.value = ''
  crObj = null
  const [ns, ...rest] = props.source.split('/')
  try {
    const { yaml } = await k8sApi.genericYaml(PROM_RULE_GVR.group, PROM_RULE_GVR.version, PROM_RULE_GVR.resource, ns, rest.join('/'))
    crObj = yamlLoad(yaml)
    const groups = crObj?.spec?.groups || []
    const loc = locate(groups)
    if (!loc) {
      errorMsg.value = '未在源 CR 中定位到该规则（可能已被修改或移动），请刷新列表后重试'
      return
    }
    // 只展示/编辑单条规则；保存时再合并回完整 CR
    ruleText.value = yamlDump(groups[loc.gIdx].rules[loc.rIdx], { lineWidth: 120, noRefs: true })
    loaded.value = true
  } catch (e: any) {
    errorMsg.value = `读取源 CR 失败：${e?.message || e}`
  } finally {
    loading.value = false
  }
}

async function save() {
  let parsed: any
  try {
    parsed = yamlLoad(ruleText.value)
  } catch (e: any) {
    errorMsg.value = `YAML 解析失败：${e?.message || e}`
    return
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed) || typeof parsed.alert !== 'string') {
    errorMsg.value = '规则必须是映射（mapping）且包含 alert 字段'
    return
  }
  saving.value = true
  try {
    // 深拷贝 CR，只替换目标规则，其余内容原样保留
    const clone = JSON.parse(JSON.stringify(crObj))
    const loc = locate(clone?.spec?.groups || [])
    if (!loc) throw new Error('定位规则失败（源 CR 可能已变化）')
    clone.spec.groups[loc.gIdx].rules[loc.rIdx] = parsed
    // 直接 dump 完整 CR（不走 cleanEmpty，避免误删合法的空值注解）
    await k8sApi.applyYaml(yamlDump(clone, { lineWidth: 120, noRefs: true }))
    ElMessage.success('规则已保存，prometheus-operator 将自动同步生效')
    visible.value = false
    emit('saved')
  } catch (e: any) {
    errorMsg.value = `保存失败：${e?.message || e}`
  } finally {
    saving.value = false
  }
}

// 打开时加载（父组件用 v-if 控制实例重建，挂载即打开）
onMounted(load)
</script>
