<template>
  <div class="schema-form">
    <el-form label-width="130px" label-position="left" size="default">
      <el-form-item v-for="f in fields" :key="f.key" :label="f.label" :required="f.required">
        <!-- 文本 -->
        <el-input v-if="f.type === 'text'" v-model="model[f.key]" :placeholder="f.placeholder || ''" @input="syncField(f)" />
        <!-- 数字 -->
        <el-input-number v-else-if="f.type === 'number'" v-model="model[f.key]" :min="0" :controls="true" style="width: 180px" @change="syncField(f)" />
        <!-- 下拉 -->
        <el-select v-else-if="f.type === 'select'" v-model="model[f.key]" clearable style="width: 100%" @change="syncField(f)">
          <el-option v-for="opt in f.options || []" :key="opt" :label="opt" :value="opt" />
        </el-select>
        <!-- 多选 -->
        <el-select v-else-if="f.type === 'multi-select'" v-model="model[f.key]" multiple clearable style="width: 100%" @change="syncField(f)">
          <el-option v-for="opt in f.options || []" :key="opt" :label="opt" :value="opt" />
        </el-select>
        <!-- 布尔 -->
        <el-switch v-else-if="f.type === 'bool'" v-model="model[f.key]" @change="syncField(f)" />
        <!-- 键值对 -->
        <KvEditor v-else-if="f.type === 'kv'" :model-value="model[f.key] || {}" :value-placeholder="f.kvValuePlaceholder" :multiline="f.multiline" @update:model-value="(v) => setKv(f.key, v)" />
        <!-- 字符串数组（逗号分隔，如 mountOptions / scopes / ipFamilies） -->
        <el-input v-else-if="f.type === 'strings'" :model-value="strArr(f.key)" @input="setStrArr(f, $event)" :placeholder="f.placeholder || '逗号分隔，如 a, b, c'" />
        <!-- 数组（子字段列表） -->
        <el-form v-else-if="f.type === 'list'" class="sub-form">
          <div v-for="(item, idx) in (model[f.key] || [])" :key="idx" class="sub-item">
            <el-form-item v-for="sf in f.items || []" :key="sf.key" :label="sf.label" class="sub-field">
              <el-input v-if="sf.type === 'text'" :model-value="getPath(item, sf.key)" @input="(v) => setItemPath(item, sf, v)" />
              <el-input-number v-else-if="sf.type === 'number'" :model-value="getPath(item, sf.key)" :min="0" style="width: 140px" @change="(v) => setItemPath(item, sf, v)" />
              <el-select v-else-if="sf.type === 'select'" :model-value="getPath(item, sf.key)" clearable style="width: 100%" @change="(v) => setItemPath(item, sf, v)">
                <el-option v-for="opt in sf.options || []" :key="opt" :label="opt" :value="opt" />
              </el-select>
              <el-switch v-else-if="sf.type === 'bool'" :model-value="!!getPath(item, sf.key)" @change="(v) => setItemPath(item, sf, v)" />
              <el-input v-else-if="sf.type === 'strings'" :model-value="strArrItem(item, sf.key)" @input="(v) => setStrArrItem(item, sf, v)" placeholder="逗号分隔" />
              <el-input v-else-if="sf.type === 'kv'" :model-value="JSON.stringify(getPath(item, sf.key) || {})" disabled size="small" />
            </el-form-item>
            <el-button size="small" type="danger" text @click="removeListItem(f.key, idx)">
              <el-icon><Delete /></el-icon>
            </el-button>
          </div>
          <el-button size="small" type="primary" plain @click="addListItem(f)">
            <el-icon><Plus /></el-icon>&nbsp;添加
          </el-button>
        </el-form>
      </el-form-item>
    </el-form>
    <div v-if="!fields.length" class="empty-hint">该资源暂无可视化字段，请使用 YAML 编辑</div>
  </div>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'
import type { FieldDef } from '../../forms/types'
import { getPath, setPath } from '../../forms/utils'
import KvEditor from './KvEditor.vue'

const props = defineProps<{ fields: FieldDef[]; object: Record<string, any> }>()
const emit = defineEmits(['change'])

// 表单模型：与对象共享引用（直接读写对象字段）
const model = reactive<Record<string, any>>({})

function refresh() {
  for (const f of props.fields) {
    model[f.key] = getPath(props.object, f.key)
  }
}

watch(
  () => props.object,
  () => refresh(),
  { deep: true },
)
watch(
  () => props.fields,
  () => refresh(),
  { deep: true },
)
refresh()

function emitChange() {
  emit('change', props.object)
}

/** 标量字段写回对象并通知变更 */
function syncField(f: FieldDef) {
  setPath(props.object, f.key, model[f.key])
  emitChange()
}

function setKv(key: string, v: Record<string, string>) {
  setPath(props.object, key, v)
  emitChange()
}

/** 字符串数组 <-> 逗号分隔文本 */
function strArr(key: string): string {
  const v = getPath(props.object, key)
  return Array.isArray(v) ? v.join(', ') : (v || '')
}
function setStrArr(f: FieldDef, val: string) {
  const arr = (val || '').split(',').map((s) => s.trim()).filter(Boolean)
  setPath(props.object, f.key, arr)
  emitChange()
}

// 列表子项的路径读写（支持点号路径，如 max.cpu）
function setItemPath(item: Record<string, any>, sf: FieldDef, val: any) {
  setPath(item, sf.key, val)
  emitChange()
}
function strArrItem(item: Record<string, any>, key: string): string {
  const v = getPath(item, key)
  return Array.isArray(v) ? v.join(', ') : (v || '')
}
function setStrArrItem(item: Record<string, any>, sf: FieldDef, val: string) {
  const arr = (val || '').split(',').map((s) => s.trim()).filter(Boolean)
  setPath(item, sf.key, arr)
  emitChange()
}

function addListItem(f: FieldDef) {
  const list = getPath(props.object, f.key) || []
  const item: Record<string, any> = {}
  for (const sf of f.items || []) {
    setPath(item, sf.key, sf.type === 'number' ? 0 : sf.type === 'bool' ? false : sf.type === 'kv' ? {} : sf.type === 'strings' ? [] : '')
  }
  list.push(item)
  setPath(props.object, f.key, list)
  emitChange()
}

function removeListItem(key: string, idx: number) {
  const list = getPath(props.object, key) || []
  list.splice(idx, 1)
  if (list.length === 0) {
    setPath(props.object, key, undefined)
  }
  emitChange()
}
</script>

<style scoped>
.sub-form { width: 100%; }
.sub-item { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 8px; border: 1px dashed #dcdfe6; border-radius: 4px; padding: 8px; margin-bottom: 8px; width: 100%; }
/* 子字段过宽时会挤压标签（如 LimitRange 12 项），按最小宽度自动换行 */
.sub-field { flex: 1 1 240px; min-width: 240px; margin-bottom: 8px; }
.empty-hint { color: #909399; font-size: 13px; padding: 20px 0; text-align: center; }
</style>
