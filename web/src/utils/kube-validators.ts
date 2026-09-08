// K8s 资源量与 cron 的轻量校验（提交前拦截明显格式错误）

const CPU_RE = /^(\d+(?:\.\d+)?m?|\d+)$/
const MEM_RE = /^\d+(?:\.\d+)?(Ei|Pi|Ti|Gi|Mi|Ki|E|P|T|G|M|K)?$/

/** 校验资源量，返回错误信息；空值视为合法（由必填逻辑另行处理） */
export function quantityError(v: string | number | undefined, kind: 'cpu' | 'memory' | 'storage'): string {
  const s = String(v ?? '').trim()
  if (!s) return ''
  if (kind === 'cpu') {
    return CPU_RE.test(s) ? '' : 'CPU 格式：整数/小数，或毫核（如 0.5、500m）'
  }
  return MEM_RE.test(s) ? '' : '格式：数字 + 单位（如 128Mi、2Gi、500M）'
}

/** 端口校验：1-65535 */
export function portError(v: string | number | undefined): string {
  if (v === '' || v == null) return ''
  const n = Number(v)
  if (!Number.isInteger(n) || n < 1 || n > 65535) return '端口范围 1-65535'
  return ''
}

// ------------------- cron 下次执行时间（标准 5 段） -------------------

function expandField(field: string, min: number, max: number): Set<number> {
  const out = new Set<number>()
  for (const part of field.split(',')) {
    let [range, step] = part.split('/')
    const stepN = step ? parseInt(step, 10) : 1
    let lo = min, hi = max
    if (range !== '*' && range !== '?') {
      const [a, b] = range.split('-')
      lo = parseInt(a, 10)
      hi = b !== undefined ? parseInt(b, 10) : lo
    }
    for (let i = lo; i <= hi; i += stepN) if (i >= min && i <= max) out.add(i)
  }
  return out
}

/** 计算标准 5 段 cron 的下次执行时间（本地时区），无法解析返回 null */
export function cronNextRun(expr: string, from = new Date()): Date | null {
  const parts = (expr || '').trim().split(/\s+/)
  if (parts.length !== 5) return null
  const f = parts.map((p) => p.toLowerCase())
  if (f.some((p) => p.includes('l') || p.includes('w') || p.includes('#'))) return null // 高级语法不支持
  let minutes: Set<number>, hours: Set<number>, days: Set<number>, months: Set<number>, weekdays: Set<number>
  try {
    minutes = expandField(f[0], 0, 59)
    hours = expandField(f[1], 0, 23)
    days = expandField(f[2], 1, 31)
    months = expandField(f[3], 1, 12)
    weekdays = expandField(f[4].replace('7', '0'), 0, 6)
  } catch {
    return null
  }
  const d = new Date(from.getTime())
  d.setSeconds(0, 0)
  d.setMinutes(d.getMinutes() + 1)
  const limit = new Date(from.getTime() + 366 * 24 * 3600 * 1000)
  while (d <= limit) {
    if (!months.has(d.getMonth() + 1)) { d.setMonth(d.getMonth() + 1, 1); d.setHours(0, 0, 0, 0); continue }
    const dayMatch = days.has(d.getDate()) && weekdays.has(d.getDay())
    if (f[2] === '*' && f[4] !== '*') { /* 只限周 */ } else if (f[2] !== '*' && f[4] === '*') { /* 只限日 */ }
    if (!dayMatch) { d.setDate(d.getDate() + 1); d.setHours(0, 0, 0, 0); continue }
    if (!hours.has(d.getHours())) { d.setHours(d.getHours() + 1, 0, 0, 0); continue }
    if (!minutes.has(d.getMinutes())) { d.setMinutes(d.getMinutes() + 1, 0, 0); continue }
    return d
  }
  return null
}
