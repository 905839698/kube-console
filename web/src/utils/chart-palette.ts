// 折线图统一色板：调用方没显式给 series.color 时按序号取用
// Meadow 序列色：晨露青绿 → 珊瑚橙 → 向日黄 → 冷蓝 → 淡紫 → 灰绿，明度交替便于区分
export const CHART_PALETTE = [
  '#17b89a', '#ff7a59', '#ffc93c', '#58a6ff', '#9b8cff',
  '#8ba29b', '#5fdcbc', '#ffab8f', '#f0b400', '#2b6fbd',
]

// 指标语义色：给 MetricCard / 仪表环 / 状态条用，替代散落的 EP 旧默认色
export const METRIC_COLORS = {
  primary: '#17b89a',
  coral: '#ff7a59',
  sun: '#ffc93c',
  sky: '#58a6ff',
  grape: '#9b8cff',
  mute: '#8ba29b',
}

// 水位阈值配色（与 .kc-usage-bar 的 warn/danger 同口径）
export function levelColor(pct: number | null | undefined): string {
  if (pct == null) return METRIC_COLORS.mute
  if (pct >= 85) return METRIC_COLORS.coral
  if (pct >= 70) return METRIC_COLORS.sun
  return METRIC_COLORS.primary
}

// 序列取色：显式色优先，缺色按序号补（越界回绕）。
// 单独成函数是为了能被测到——之前缺色时传的是空数组，等于清空 ECharts 调色板，
// 曲线一条都不画，而坐标轴/图例/tooltip 全都正常，极难从现象反推。
export function seriesColors(series: { color?: string }[]): string[] {
  return series.map((s, i) => s.color || CHART_PALETTE[i % CHART_PALETTE.length])
}
