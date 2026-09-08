<template>
  <Teleport to="body">
    <div v-if="visible" class="terminal-overlay">
      <div class="terminal-bar">
        <span class="terminal-title">
          <el-icon><Cpu /></el-icon>
          终端 - {{ namespace }}/{{ pod }}{{ container ? ' / ' + container : '' }}{{ debugMode ? '（调试容器）' : '' }}
        </span>
        <div class="terminal-actions">
          <span v-if="status" class="terminal-status" :class="{ warn: !connected }">{{ status }}</span>
          <el-button size="small" :icon="Brush" @click="enterDebug" :disabled="debugMode" title="注入 busybox 调试容器（无 shell 镜像可用）">调试容器</el-button>
          <el-button size="small" :icon="Refresh" @click="reconnect" :disabled="connected">重连</el-button>
          <el-button size="small" :icon="Close" @click="close">关闭</el-button>
        </div>
      </div>
      <div ref="termBox" class="term-box" @click="focusTerm" />
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Close, Cpu, Refresh, Brush } from '@element-plus/icons-vue'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'

const props = defineProps<{
  visible: boolean
  namespace: string
  pod: string
  container?: string
  /** 节点终端模式：pod 传节点名，经 nsenter 调试 Pod 进入宿主机 shell */
  node?: boolean
}>()
const emit = defineEmits(['update:visible'])

const userStore = useUserStore()
const clusterStore = useClusterStore()

const termBox = ref<HTMLElement>()
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let resizeObserver: ResizeObserver | null = null
let disposed = false

const status = ref('')
const connected = ref(false)
const debugMode = ref(false)

// 动态挂载（v-if 首次创建时 visible 可能已是 true，watch 无变化事件）：
// onMounted 检查 + watch 变化双保险
onMounted(() => {
  if (props.visible) {
    mountTerminal()
  }
})

watch(
  () => props.visible,
  (v) => {
    if (v) {
      nextTick().then(() => mountTerminal())
    } else {
      destroyTerminal()
    }
  },
)

onBeforeUnmount(() => destroyTerminal())

function mountTerminal() {
  if (!termBox.value) return
  disposed = false
  term = new Terminal({
    fontFamily: 'Consolas, "Courier New", monospace',
    fontSize: 13,
    cursorBlink: true,
    theme: { background: '#0d1117', foreground: '#e6edf3' },
    scrollback: 5000,
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termBox.value)
  fitAddon.fit()
  term.focus()

  // 输入 → stdin；尺寸变化 → resize 控制消息
  term.onData((data) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(new TextEncoder().encode(data))
    }
  })
  term.onResize(({ cols, rows }) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  })

  // 容器尺寸变化（窗口缩放等）时自适应并同步终端尺寸
  resizeObserver = new ResizeObserver(() => {
    if (!fitAddon || disposed) return
    try {
      fitAddon.fit()
      if (term) {
        const { cols, rows } = term
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', cols, rows }))
        }
      }
    } catch {
      /* 容器尚未挂载完成 */
    }
  })
  resizeObserver.observe(termBox.value)

  connect()
}

function connect() {
  if (!term) return
  status.value = '连接中...'
  connected.value = false
  term.writeln('\x1b[90m正在连接 Shell...\x1b[0m')

  const cluster = clusterStore.current || localStorage.getItem('kc-cluster') || ''
  const token = userStore.token
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://'
  const url = props.node
    ? `${proto}${location.host}/api/nodes/${encodeURIComponent(props.pod)}/exec?token=${encodeURIComponent(token)}&cluster=${encodeURIComponent(cluster)}`
    : `${proto}${location.host}/api/pods/${encodeURIComponent(props.pod)}/exec?token=${encodeURIComponent(token)}&cluster=${encodeURIComponent(cluster)}&namespace=${encodeURIComponent(props.namespace)}${props.container ? `&container=${encodeURIComponent(props.container)}` : ''}${debugMode.value ? '&debug=1' : ''}`

  ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    connected.value = true
    status.value = '已连接'
    if (term) {
      const { cols, rows } = term
      ws?.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  }
  ws.onmessage = (ev) => {
    if (!term) return
    if (typeof ev.data === 'string') {
      // 控制消息（notice=切换 shell 提示，exit=会话结束），其余按文本输出
      try {
        const j = JSON.parse(ev.data)
        if (j.type === 'notice') {
          term.writeln(`\r\n\x1b[90m${j.message || ''}\x1b[0m`)
          return
        }
        if (j.type === 'exit') {
          connected.value = false
          status.value = '已断开'
          term.writeln(`\r\n\x1b[90m${j.message || '会话已结束'}\x1b[0m`)
          return
        }
      } catch {
        /* 普通文本输出 */
      }
      term.write(ev.data)
    } else {
      term.write(new Uint8Array(ev.data))
    }
  }
  ws.onclose = () => {
    connected.value = false
    if (!disposed && status.value !== '已断开') {
      status.value = '连接断开'
      term?.writeln('\r\n\x1b[90m连接已断开，可点击"重连"\x1b[0m')
    }
  }
  ws.onerror = () => {
    connected.value = false
    status.value = '连接失败'
  }
}

function reconnect() {
  if (ws) {
    try { ws.close() } catch { /* 忽略 */ }
  }
  ws = null
  connect()
}

// 进入调试容器模式：注入 busybox 临时容器后重连（无 shell 的 distroless 镜像）
function enterDebug() {
  debugMode.value = true
  reconnect()
}

function focusTerm() {
  term?.focus()
}

function close() {
  emit('update:visible', false)
}

function destroyTerminal() {
  disposed = true
  if (ws) {
    try { ws.close() } catch { /* 忽略 */ }
  }
  ws = null
  resizeObserver?.disconnect()
  resizeObserver = null
  if (term) {
    term.dispose()
    term = null
  }
  fitAddon = null
  status.value = ''
  connected.value = false
  debugMode.value = false
}
</script>

<style scoped>
.terminal-overlay {
  position: fixed;
  inset: 0;
  z-index: 3000;
  background: #0d1117;
  display: flex;
  flex-direction: column;
}
.terminal-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 44px;
  padding: 0 16px;
  background: #161b22;
  border-bottom: 1px solid #30363d;
  color: #e6edf3;
}
.terminal-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
}
.terminal-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.terminal-status {
  font-size: 12px;
  color: #8b949e;
}
.terminal-status.warn {
  color: #e6a23c;
}
.term-box {
  flex: 1;
  padding: 8px 12px;
  overflow: hidden;
}
</style>
