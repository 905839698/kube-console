<!-- 新建告警规则：写入现有 PrometheusRule CR 的分组，或创建全新 CR；保存即由 operator 自动同步生效 -->
<template>
  <el-dialog v-model="visible" title="新建告警规则" width="880px" top="6vh">
    <div v-loading="busy">
      <el-form label-width="110px">
        <el-form-item label="写入位置" required>
          <el-radio-group v-model="mode">
            <el-radio-button value="existing">加入现有 PrometheusRule</el-radio-button>
            <el-radio-button value="new">新建 PrometheusRule</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <template v-if="mode === 'existing'">
          <el-form-item label="PrometheusRule" required>
            <el-select v-model="crSel" filterable placeholder="选择 CR（全命名空间）" style="width: 420px" @change="onCrSel">
              <el-option v-for="c in crList" :key="c.namespace + '/' + c.name" :label="`${c.namespace}/${c.name}`" :value="c.namespace + '/' + c.name" />
            </el-select>
          </el-form-item>
          <el-form-item label="目标分组" required v-if="crSel">
            <el-select v-model="groupSel" style="width: 280px">
              <el-option v-for="g in groups" :key="g" :label="g" :value="g" />
              <el-option label="＋ 新建分组" value="__new__" />
            </el-select>
            <el-input
              v-if="groupSel === '__new__'"
              v-model="newGroup"
              placeholder="新分组名（字母/数字/下划线）"
              style="width: 240px; margin-left: 8px"
            />
          </el-form-item>
        </template>

        <template v-else>
          <el-form-item label="命名空间" required>
            <el-input v-model="newNs" placeholder="如 monitor" style="width: 220px" />
          </el-form-item>
          <el-form-item label="CR 名称" required>
            <el-input v-model="newCr" placeholder="小写字母/数字/中划线，如 custom-alerts" style="width: 320px" />
          </el-form-item>
          <el-form-item label="分组名称" required>
            <el-input v-model="newGroup2" placeholder="字母/数字/下划线，如 custom" style="width: 240px" />
          </el-form-item>
        </template>

        <el-form-item label="规则定义" required>
          <div style="width: 100%">
            <YamlEditor v-model="ruleText" style="height: 300px" />
          </div>
        </el-form-item>
      </el-form>
      <el-alert v-if="err" type="error" :closable="false" style="margin-top: 8px" :title="err" />
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">创建</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { load as yamlLoad, dump as yamlDump } from 'js-yaml'
import { k8sApi, type GenericItem } from '../../api'
import YamlEditor from '../../components/YamlEditor.vue'

const PROM_RULE_GVR = { group: 'monitoring.coreos.com', version: 'v1', resource: 'prometheusrules' }

const visible = defineModel<boolean>({ default: false })
const emit = defineEmits(['saved'])

const TEMPLATE = `alert: NewAlert
expr: up == 0
for: 5m
labels:
  severity: warning
annotations:
  summary: 新告警规则
  description: 请修改 expr 与描述
`

const busy = ref(false)
const saving = ref(false)
const err = ref('')
const mode = ref<'existing' | 'new'>('existing')
const crList = ref<GenericItem[]>([])
const crSel = ref('')
const groups = ref<string[]>([])
const groupSel = ref('')
const newGroup = ref('')
const newNs = ref('monitor')
const newCr = ref('custom-alerts')
const newGroup2 = ref('custom')
const ruleText = ref(TEMPLATE)

const NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const GROUP_RE = /^[A-Za-z0-9_]+$/

async function loadCrList() {
  busy.value = true
  try {
    crList.value = await k8sApi.genericList(PROM_RULE_GVR.group, PROM_RULE_GVR.version, PROM_RULE_GVR.resource)
  } catch {
    /* 拦截器已提示 */
  } finally {
    busy.value = false
  }
}

async function onCrSel(val: string) {
  groups.value = []
  groupSel.value = ''
  if (!val) return
  const [ns, ...rest] = val.split('/')
  try {
    const { yaml } = await k8sApi.genericYaml(PROM_RULE_GVR.group, PROM_RULE_GVR.version, PROM_RULE_GVR.resource, ns, rest.join('/'))
    const cr = yamlLoad(yaml)
    groups.value = (cr?.spec?.groups || []).map((g: any) => g.name).filter(Boolean)
  } catch (e: any) {
    err.value = `读取 CR 分组失败：${e?.message || e}`
  }
}

function parseRule(): any {
  let parsed: any
  try {
    parsed = yamlLoad(ruleText.value)
  } catch (e: any) {
    throw new Error(`YAML 解析失败：${e?.message || e}`)
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed) || typeof parsed.alert !== 'string' || !parsed.alert) {
    throw new Error('规则必须是映射（mapping）且包含非空 alert 字段')
  }
  return parsed
}

async function save() {
  err.value = ''
  let rule: any
  try {
    rule = parseRule()
  } catch (e: any) {
    err.value = e.message
    return
  }

  saving.value = true
  try {
    if (mode.value === 'existing') {
      if (!crSel.value) throw new Error('请选择目标 PrometheusRule')
      const targetGroup = groupSel.value === '__new__' ? newGroup.value.trim() : groupSel.value
      if (!targetGroup) throw new Error('请选择或填写目标分组')
      if (!GROUP_RE.test(targetGroup)) throw new Error('分组名只能包含字母/数字/下划线')
      // 保存前重新拉取最新 CR，避免覆盖期间的其他修改
      const [ns, ...rest] = crSel.value.split('/')
      const { yaml } = await k8sApi.genericYaml(PROM_RULE_GVR.group, PROM_RULE_GVR.version, PROM_RULE_GVR.resource, ns, rest.join('/'))
      const cr = yamlLoad(yaml)
      const crGroups: any[] = cr?.spec?.groups || []
      let g = crGroups.find((x) => x.name === targetGroup)
      if (g && (g.rules || []).some((r: any) => r.alert === rule.alert)) {
        throw new Error(`分组 ${targetGroup} 中已存在同名告警 ${rule.alert}`)
      }
      if (!g) {
        g = { name: targetGroup, rules: [] }
        crGroups.push(g)
        if (!cr.spec) cr.spec = {}
        cr.spec.groups = crGroups
      }
      g.rules = g.rules || []
      g.rules.push(rule)
      await k8sApi.applyYaml(yamlDump(cr, { lineWidth: 120, noRefs: true }))
    } else {
      const ns = newNs.value.trim()
      const crName = newCr.value.trim()
      const gName = newGroup2.value.trim()
      if (!NAME_RE.test(ns)) throw new Error('命名空间名不合法（小写字母/数字/中划线）')
      if (!NAME_RE.test(crName)) throw new Error('CR 名称不合法（小写字母/数字/中划线，如 custom-alerts）')
      if (!GROUP_RE.test(gName)) throw new Error('分组名只能包含字母/数字/下划线')
      const cr = {
        apiVersion: 'monitoring.coreos.com/v1',
        kind: 'PrometheusRule',
        metadata: { name: crName, namespace: ns },
        spec: { groups: [{ name: gName, rules: [rule] }] },
      }
      await k8sApi.applyYaml(yamlDump(cr, { lineWidth: 120, noRefs: true }))
    }
    ElMessage.success('规则已创建，prometheus-operator 将自动同步生效')
    visible.value = false
    emit('saved')
  } catch (e: any) {
    err.value = `创建失败：${e?.message || e}`
  } finally {
    saving.value = false
  }
}

onMounted(loadCrList)
</script>
