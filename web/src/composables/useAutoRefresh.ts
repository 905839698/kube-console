// 轮询节流：页面隐藏（切走标签页）时自动暂停，回到前台立即刷新一次
import { onBeforeUnmount, onMounted, ref } from 'vue'

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
