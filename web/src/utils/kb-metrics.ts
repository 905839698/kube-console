// 数据库监控面板的取值渲染：Prometheus 向量结果 → 「区分标签 = 值」的行列表
export type KBSeries = { labels: string; value: string }

// 每条序列都相同的标签（命名空间、service、job…）没有信息量
const LABEL_DROP = new Set(['__name__', 'namespace', 'service', 'job', 'container', 'endpoint', 'uid'])
const LABEL_PRIORITY = ['pod', 'instance', 'name', 'topic', 'datname', 'db', 'state', 'mode', 'operation']
export const SERIES_LIMIT = 12

type RawSeries = { metric?: Record<string, string>; value?: [number, string] }

export function kbSeries(list: RawSeries[]): KBSeries[] {
  const maps = (list || []).map((r) => ({ ...(r.metric || {}), __v: r.value?.[1] ?? '-' }))
  if (!maps.length) return []
  const keys = new Set<string>()
  maps.forEach((m) => Object.keys(m).forEach((k) => {
    if (k !== '__v' && !LABEL_DROP.has(k)) keys.add(k)
  }))
  let shown = [...keys].filter((k) => new Set(maps.map((m) => (m as any)[k] ?? '')).size > 1)
  if (!shown.length) shown = LABEL_PRIORITY.filter((k) => maps.some((m) => (m as any)[k] !== undefined))
  return maps
    .sort((a, b) => String((a as any)[shown[0]] ?? '').localeCompare(String((b as any)[shown[0]] ?? '')))
    .slice(0, SERIES_LIMIT)
    .map((m) => ({
      labels: shown.map((k) => `${k}=${(m as any)[k] ?? '-'}`).join(' ') || '-',
      value: m.__v,
    }))
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

// 数值可读化：字节与字节/秒转 KB/MB/GB，秒数按量级转分钟/小时/天
export function fmtMetric(v: string, unit?: string) {
  const n = Number(v)
  if (!isFinite(n)) return v
  if (unit === 'B' || unit === 'B/s') {
    let i = 0
    let x = Math.abs(n)
    while (x >= 1024 && i < BYTE_UNITS.length - 1) { x /= 1024; i++ }
    return `${(Math.round(x * 100) / 100).toLocaleString()} ${BYTE_UNITS[i]}${unit === 'B/s' ? '/s' : ''}`
  }
  if (unit === 's' && n >= 120) {
    if (n >= 86400) return `${(n / 86400).toFixed(1)} 天`
    if (n >= 3600) return `${(n / 3600).toFixed(1)} 小时`
    return `${(n / 60).toFixed(1)} 分钟`
  }
  if (Math.abs(n) >= 1000) return n.toLocaleString('zh-CN', { maximumFractionDigits: 1 })
  return (Math.round(n * 1000) / 1000).toString()
}

// ---------- 图表用 ----------

// 一条序列 = 名称 + [秒级时间戳, 值][]（与 MetricChart 的 ChartSeries 同形）
export interface KbChartSeries { name: string; data: [number, number][]; unit?: string; color?: string }

export interface KbChart {
  title: string
  group: string
  promql: string
  unit?: string
  series: KbChartSeries[]
  /** 该指标的序列总数（图里最多画 KB_CHART_SERIES_LIMIT 条） */
  total: number
  /** 最新值（取第一条序列的末点），无数据为 null */
  last: number | null
}

export const KB_CHART_SERIES_LIMIT = 8

// 集群的两套选择器：exporter 序列按组件 headless service 精确匹配；
// 资源类按 Pod 名前缀（<集群>-<组件>-N），不依赖 exporter
export function kbMetricSelectors(cluster: { namespace: string; name: string; components: { headlessService?: string }[] }) {
  const svcs = cluster.components.map((c) => c.headlessService).filter(Boolean) as string[]
  if (!svcs.length) return { hasExporters: false, sel: '', pod: '' }
  const esc = cluster.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return {
    hasExporters: true,
    sel: `namespace="${cluster.namespace}", service=~"^(${svcs.join('|')})$"`,
    pod: `namespace="${cluster.namespace}", pod=~"^${esc}-.*"`,
  }
}

type RangeSeries = { metric?: Record<string, string>; values?: [number, string][] }

// 图例名：只保留能区分序列的标签（全序列相同的标签没有区分度），
// 都相同时退化成 __name__ 或 pod/instance 等主键
export function kbRangeSeries(list: RangeSeries[], limit = KB_CHART_SERIES_LIMIT): { series: KbChartSeries[]; total: number } {
  const maps = (list || []).map((r) => ({ ...(r.metric || {}), __points: r.values || [] }))
  const keys = new Set<string>()
  maps.forEach((m) => Object.keys(m).forEach((k) => {
    if (k !== '__points' && k !== '__name__' && !LABEL_DROP.has(k)) keys.add(k)
  }))
  let shown = [...keys].filter((k) => new Set(maps.map((m) => (m as any)[k] ?? '')).size > 1)
  if (!shown.length) shown = LABEL_PRIORITY.filter((k) => maps.some((m) => (m as any)[k] !== undefined))
  const named = maps.map((m) => {
    // 只有一个区分标签时直接显示值（图例更短）；多标签才带 k=v 前缀
    const label = shown.length === 1
      ? String((m as any)[shown[0]] ?? '-')
      : shown.map((k) => `${k}=${(m as any)[k] ?? '-'}`).join(',') || m['__name__'] || '值'
    const data: [number, number][] = []
    for (const [ts, v] of m.__points) {
      const n = Number(v)
      if (isFinite(n)) data.push([ts, n])
    }
    return { name: label, data, __last: data.length ? data[data.length - 1][1] : null } as KbChartSeries & { __last: number | null }
  })
  named.sort((a, b) => a.name.localeCompare(b.name))
  const withData = named.filter((s) => s.data.length > 0)
  const series = withData.slice(0, limit).map(({ name, data }) => ({ name, data }))
  return { series, total: named.length }
}
