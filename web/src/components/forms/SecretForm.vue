<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="类型">
        <el-select v-model="o.type" style="width: 100%">
          <el-option v-for="t in types" :key="t" :label="t" :value="t" />
        </el-select>
      </el-form-item>
      <el-form-item label="不可变">
        <el-switch v-model="o.immutable" />
        <span class="secret-hint" style="margin: 0 0 0 10px">immutable=true 后数据不可再修改（需删除重建）</span>
      </el-form-item>
      <el-form-item label="数据 (base64)">
        <div class="secret-hint">已有 data 已自动解码为明文（UTF-8），保存时编码回 base64</div>
        <div class="secret-hint" v-if="binaryKeys.length">
          键 <span class="mono">{{ binaryKeys.join(', ') }}</span> 的内容不是 UTF-8 文本（二进制），已按 base64 原样展示；未编辑的键保存时保持原值不变。
        </div>
        <KvEditor v-model="decodedData" key-placeholder="KEY" value-placeholder="明文值" multiline />
      </el-form-item>
      <el-form-item label="明文 (stringData)">
        <div class="secret-hint">stringData 中的键保存后自动并入 data</div>
        <KvEditor v-model="o.stringData" key-placeholder="KEY" value-placeholder="明文值" multiline />
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import KvEditor from './KvEditor.vue'
import { b64ToText, textToB64 } from '../../utils/b64'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const types = ['Opaque', 'kubernetes.io/service-account-token', 'kubernetes.io/dockerconfigjson', 'kubernetes.io/tls', 'kubernetes.io/basic-auth', 'kubernetes.io/ssh-auth']

// data（base64 map）<-> 明文 map
const decodedData = computed<Record<string, string>>({
  get: () => decodeMap(o.value.data),
  set: (v) => {
    o.value.data = encodeMap(v)
  },
})

// 无法按 UTF-8 解码的键（二进制内容）：展示与写回都保持原始 base64，避免重编码损坏。
// 非响应式：只在 encodeMap 读取，不参与模板渲染（模板用 binaryKeys 计算属性）。
let binaryData: Record<string, string> = {}

// 二进制键名（提示用）：与 decodeMap 同一判定，避免在 getter 里维护额外状态
const binaryKeys = computed(() =>
  Object.entries(o.value.data || {}).filter(([, v]) => b64ToText(v as string) === null).map(([k]) => k))

function decodeMap(data: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  const bin: Record<string, string> = {}
  for (const [k, v] of Object.entries(data || {})) {
    const text = b64ToText(v)
    if (text === null) {
      bin[k] = v
      out[k] = v // 非 UTF-8：按 base64 原样展示
      continue
    }
    out[k] = text
  }
  binaryData = bin
  return out
}

function encodeMap(map: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(map)) {
    if (!k.trim()) continue
    // 永远编码：编辑器里展示的是 decodeMap 的明文，不能靠「值恰好也是合法 base64」
    // 猜测它已是密文——例如明文 password 会被 btoa(atob()) 回环误判成已编码，
    // 原样写回后 Secret 里的值就不是原明文的 base64（数据损坏）
    // 未被编辑过的二进制值原样写回：重编码会把 base64 文本本身当成明文
    if (binaryData[k] === v) {
      out[k] = v
      continue
    }
    out[k] = textToB64(v)
  }
  return out
}
</script>

<style scoped>
.secret-hint { width: 100%; font-size: 12px; color: #9ca3af; margin-bottom: 6px; }
.mono { font-family: ui-monospace, Consolas, monospace; }
</style>
