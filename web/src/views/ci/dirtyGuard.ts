// 设计器未保存修改守卫（vue-router beforeEach 替代 react useBlocker）：
// 设计器页注册确认回调；路由守卫在离开设计器路由前询问。
import { ref } from 'vue'

export const ciDesignDirty = ref(false)
let confirmFn: ((message: string) => Promise<boolean>) | null = null

export function setCiDesignGuard(fn: ((message: string) => Promise<boolean>) | null) {
  confirmFn = fn
}

// 路由守卫调用：脏且已注册回调时弹确认；未脏或设计器未注册则放行
export async function askCiDesignLeave(): Promise<boolean> {
  if (!ciDesignDirty.value || !confirmFn) return true
  return confirmFn('有未保存的画布修改，离开将丢失，确定离开？')
}
