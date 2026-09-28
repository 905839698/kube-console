import { describe, expect, it } from 'vitest'
import { fmtMetric, kbRangeSeries, kbMetricSelectors, KB_CHART_SERIES_LIMIT, kbSeries, SERIES_LIMIT } from '../kb-metrics'

const s = (metric: Record<string, string>, value: string) => ({ metric, value: [0, value] as [number, string] })

describe('kbSeries', () => {
  it('只展示能区分序列的标签，公共标签被丢掉', () => {
    const rows = kbSeries([
      s({ __name__: 'pg_stat_activity_count', namespace: 'ns1', service: 'pg-headless', job: 'kb', instance: '10.0.0.1:9187', state: 'idle' }, '3'),
      s({ __name__: 'pg_stat_activity_count', namespace: 'ns1', service: 'pg-headless', job: 'kb', instance: '10.0.0.1:9187', state: 'active' }, '1'),
    ])
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.labels)).toEqual(['state=active', 'state=idle'])
    expect(rows[0].value).toBe('1')
  })

  it('单序列时回落到 pod/instance 等主键，不留空白', () => {
    const [one] = kbSeries([s({ namespace: 'ns', pod: 'db-mysql-0' }, '42')])
    expect(one.labels).toBe('pod=db-mysql-0')
    expect(one.value).toBe('42')
  })

  it('没有实例标签时显示占位符而不是空串', () => {
    expect(kbSeries([s({ namespace: 'ns' }, '1')])[0].labels).toBe('-')
  })

  it('序列数收口，避免一格刷出几十行', () => {
    const many = Array.from({ length: SERIES_LIMIT + 30 }, (_, i) => s({ pod: `p-${i}` }, String(i)))
    expect(kbSeries(many)).toHaveLength(SERIES_LIMIT)
    expect(kbSeries(many)[0].labels).toBe('pod=p-0')
  })

  it('空结果返回空数组', () => {
    expect(kbSeries([])).toEqual([])
  })
})

describe('fmtMetric', () => {
  it('字节与字节/秒换算', () => {
    expect(fmtMetric('1536', 'B')).toBe('1.5 KB')
    expect(fmtMetric('536870912', 'B/s')).toBe('512 MB/s')
    expect(fmtMetric('999', 'B')).toBe('999 B')
  })

  it('秒数按时长升级单位', () => {
    expect(fmtMetric('30', 's')).toBe('30')
    expect(fmtMetric('600', 's')).toBe('10.0 分钟')
    expect(fmtMetric('7200', 's')).toBe('2.0 小时')
    expect(fmtMetric('172800', 's')).toBe('2.0 天')
  })

  it('比率与计数保持原样', () => {
    expect(fmtMetric('0.987654', undefined)).toBe('0.988')
    expect(fmtMetric('12345', undefined)).toBe('12,345')
    expect(fmtMetric('NaN', 'B')).toBe('NaN')
  })
})

describe('kbMetricSelectors', () => {
  it('有 headless service 时给出 service 与 pod 两套选择器', () => {
    const s = kbMetricSelectors({ namespace: 'ns', name: 'pg-cluster', components: [{ headlessService: 'pg-cluster-postgresql-headless' }] })
    expect(s.hasExporters).toBe(true)
    expect(s.sel).toBe('namespace="ns", service=~"^(pg-cluster-postgresql-headless)$"')
    expect(s.pod).toBe('namespace="ns", pod=~"^pg-cluster-.*"')
  })
  it('没有组件服务时不拼出空的 service 正则（会误匹配空名）', () => {
    const s = kbMetricSelectors({ namespace: 'ns', name: 'a', components: [] })
    expect(s.hasExporters).toBe(false)
    expect(s.sel).toBe('')
  })
})

describe('kbRangeSeries', () => {
  const pts = (n: number) => Array.from({ length: n }, (_, i) => [1700000000 + i * 60, String(i)] as [number, string])
  it('图例只留能区分的标签，点数转数字并保留末值', () => {
    const { series, total } = kbRangeSeries([
      { metric: { namespace: 'ns', service: 'svc', pod: 'db-mysql-0' }, values: pts(3) },
      { metric: { namespace: 'ns', service: 'svc', pod: 'db-mysql-1' }, values: pts(2) },
    ])
    expect(total).toBe(2)
    expect(series.map((s) => s.name)).toEqual(['db-mysql-0', 'db-mysql-1'])
    expect(series[0].data[0]).toEqual([1700000000, 0])
  })
  it('无点/非数字的序列不进图，但计入总数', () => {
    const { series, total } = kbRangeSeries([
      { metric: { pod: 'a' }, values: [] },
      { metric: { pod: 'b' }, values: [[1, 'NaN'], [2, '1']] },
    ])
    expect(total).toBe(2)
    expect(series).toHaveLength(1)
    expect(series[0].data).toEqual([[2, 1]])
  })
  it('序列数收口', () => {
    const many = Array.from({ length: KB_CHART_SERIES_LIMIT + 12 }, (_, i) => ({ metric: { pod: 'p' + i }, values: pts(1) }))
    expect(kbRangeSeries(many).series).toHaveLength(KB_CHART_SERIES_LIMIT)
  })
})

describe('seriesColors', () => {
  it('缺色按序号补、显式色优先、超出回绕', async () => {
    const { seriesColors, CHART_PALETTE } = await import('../chart-palette')
    const got = seriesColors([{}, { color: '#111' }, {}, {}])
    expect(got).toEqual([CHART_PALETTE[0], '#111', CHART_PALETTE[2], CHART_PALETTE[3]])
    const long = seriesColors(Array.from({ length: CHART_PALETTE.length + 2 }, () => ({})))
    expect(long[CHART_PALETTE.length]).toBe(CHART_PALETTE[0])
    expect(new Set(long).size).toBe(CHART_PALETTE.length)
  })
})
