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
        <div class="secret-hint">已有 data 已自动解码为明文，保存时编码回 base64</div>
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

function decodeMap(data: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(data || {})) {
    try {
      out[k] = atob(v)
    } catch {
      out[k] = v
    }
  }
  return out
}

function encodeMap(map: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(map)) {
    if (!k.trim()) continue
    // 永远编码：编辑器里展示的是 decodeMap 的明文，不能靠「值恰好也是合法 base64」
    // 猜测它已是密文——例如明文 password 会被 btoa(atob()) 回环误判成已编码，
    // 原样写回后 Secret 里的值就不是原明文的 base64（数据损坏）
    out[k] = btoa(unescape(encodeURIComponent(v)))
  }
  return out
}
</script>

<style scoped>
.secret-hint { width: 100%; font-size: 12px; color: #9ca3af; margin-bottom: 6px; }
</style>
