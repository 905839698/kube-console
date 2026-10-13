// 建集群向导的调度设置（亲和性 + 容忍污点）。表单状态与"发给后端的 payload"分开放：
// 表单里允许有空行、值用逗号分隔的文本；发出去之前统一去空、切值、全空就不带字段。
// 纯函数放这里，界面组件只负责绑控件。

export interface KBTolerationRow {
  key: string
  value: string
  operator: string // Exists | Equal
  effect: string   // '' | NoSchedule | PreferNoSchedule | NoExecute
}

export interface KBNodeMatchRow {
  key: string
  operator: string // In | NotIn | Exists | DoesNotExist | Gt | Lt
  values: string   // 逗号分隔的表单文本
}

export interface KBSchedulingState {
  nodeSelector: Record<string, string>
  tolerations: KBTolerationRow[]
  nodeRequired: KBNodeMatchRow[]
  nodePreferred: KBNodeMatchRow[]
  spread: string // '' | preferred | required
}

// 与 server 的 KBScheduling 对应
export interface KBSchedulingPayload {
  nodeSelector?: Record<string, string>
  tolerations?: { key?: string; value?: string; operator: string; effect?: string }[]
  nodeRequired?: { key: string; operator: string; values?: string[] }[]
  nodePreferred?: { key: string; operator: string; values?: string[] }[]
  spread?: string
}

export const KB_TAINT_EFFECTS = ['', 'NoSchedule', 'PreferNoSchedule', 'NoExecute']
export const KB_TOLERATION_OPS = ['Exists', 'Equal']
export const KB_MATCH_OPS = ['In', 'NotIn', 'Exists', 'DoesNotExist', 'Gt', 'Lt']

export function emptyScheduling(): KBSchedulingState {
  return { nodeSelector: {}, tolerations: [], nodeRequired: [], nodePreferred: [], spread: '' }
}

// 调度状态的深拷贝。不能换 structuredClone：父组件的 ref() 会把对象值包成
// reactive 代理，structuredClone 对代理直接抛 DataCloneError（建集群弹窗里
// 调度设置整块不渲染的根因）。状态全是可序列化的键值/数组，JSON 足够。
export function cloneScheduling(v: KBSchedulingState): KBSchedulingState {
  return JSON.parse(JSON.stringify(v))
}

// 新增行一律给"真正的空"：默认值写着 NoSchedule 的话，用户没填的行也会被当成一条策略发出去
export function newToleration(): KBTolerationRow {
  return { key: '', value: '', operator: 'Exists', effect: '' }
}

export function newMatch(): KBNodeMatchRow {
  return { key: '', operator: 'In', values: '' }
}

function txt(v: unknown): string {
  return String(v ?? '').trim()
}

// 逗号/空格分隔的值文本 → 数组（Exists / DoesNotExist 不看值，交给服务端校验）
export function splitValues(text: string): string[] {
  return text.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean)
}

// 表单里一行"填过的"判定：operator 是控件默认值，不算用户填过
function tolFilled(t: KBTolerationRow): boolean {
  return Boolean(txt(t.key) || txt(t.value) || txt(t.effect))
}

// 全空返回 null：宁可不带 scheduling，也不下发一个"看着配了其实是空对象"的策略
export function kbSchedulingPayload(s: KBSchedulingState | null | undefined): KBSchedulingPayload | null {
  if (!s) return null
  const out: KBSchedulingPayload = {}

  const sel: Record<string, string> = {}
  for (const [k, v] of Object.entries(s.nodeSelector || {})) {
    if (txt(k)) sel[txt(k)] = String(v ?? '')
  }
  if (Object.keys(sel).length) out.nodeSelector = sel

  const tols = (s.tolerations || []).filter(tolFilled)
  if (tols.length) {
    out.tolerations = tols.map((t) => {
      const item: { key?: string; value?: string; operator: string; effect?: string } = {
        operator: txt(t.operator) || 'Exists',
      }
      if (txt(t.key)) item.key = txt(t.key)
      if (txt(t.value)) item.value = txt(t.value)
      if (txt(t.effect)) item.effect = txt(t.effect)
      return item
    })
  }

  const matches = (rows: KBNodeMatchRow[]) =>
    rows.filter((m) => txt(m.key)).map((m) => {
      const op = txt(m.operator) || 'In'
      const item: { key: string; operator: string; values?: string[] } = { key: txt(m.key), operator: op }
      const vals = splitValues(m.values)
      if (vals.length) item.values = vals
      return item
    })
  const req = matches(s.nodeRequired || [])
  if (req.length) out.nodeRequired = req
  const pref = matches(s.nodePreferred || [])
  if (pref.length) out.nodePreferred = pref

  if (txt(s.spread)) out.spread = txt(s.spread)

  return Object.keys(out).length ? out : null
}

// 界面提示用：这套设置会怎么影响调度（也用来判断"用户是不是真的配了东西"）
export function kbSchedulingSummary(s: KBSchedulingState | null | undefined): string {
  const p = kbSchedulingPayload(s)
  if (!p) return '不设调度限制'
  const bits: string[] = []
  if (p.nodeSelector) bits.push(`节点标签 ${Object.keys(p.nodeSelector).length} 项`)
  if (p.tolerations) bits.push(`容忍 ${p.tolerations.length} 条`)
  if (p.nodeRequired) bits.push(`亲和(必须) ${p.nodeRequired.length} 条`)
  if (p.nodePreferred) bits.push(`亲和(尽量) ${p.nodePreferred.length} 条`)
  if (p.spread === 'required') bits.push('必须分散到不同节点')
  if (p.spread === 'preferred') bits.push('尽量分散到不同节点')
  return bits.join(' · ')
}
