// 轮询节流：页面隐藏（切走标签页）时自动暂停，回到前台立即刷新一次
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

export function useAutoRefresh(fn: () => void | Promise<unknown>, intervalMs = 30000, enabled = ref(true)) {
  let timer: ReturnType<typeof setInterval> | undefined

  const start = () => {
    if (timer || !enabled.value) return
    timer = setInterval(() => {
      if (document.visibilityState === 'visible') fn()
    }, intervalMs)
  }
  const stop = () => {
    if (timer) clearInterval(timer)
    timer = undefined
  }

  const onVisibility = () => {
    if (document.visibilityState === 'visible') fn()
  }

  // enabled 从 false 切 true 时补启动（onMounted 时已为 false 的场景：
  // 如 EventList「自动刷新」开关默认关、用户勾选——旧实现定时器永不建立）
  watch(enabled, (v) => {
    if (v) start()
    else stop()
  })

  onMounted(() => {
    start()
    document.addEventListener('visibilitychange', onVisibility)
  })
  onBeforeUnmount(() => {
    stop()
    document.removeEventListener('visibilitychange', onVisibility)
  })

  return { start, stop, enabled }
}
