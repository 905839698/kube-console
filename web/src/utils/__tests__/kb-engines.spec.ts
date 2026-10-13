import * as icons from '@element-plus/icons-vue'
import { describe, expect, it } from 'vitest'
import { KB_ENGINES, KB_GROUP_ICON, KB_REPO_ICON, kbEngineMeta, kbMatchesEngine } from '../kb-engines'

describe('数据库子菜单定义', () => {
  it('六个引擎与 KubeBlocks ClusterDefinition 同名', () => {
    expect(KB_ENGINES.map((e) => e.key)).toEqual(['mysql', 'redis', 'kafka', 'mongodb', 'elasticsearch', 'postgresql'])
  })

  it('图标不能重复（父菜单与备份仓库项也不能与任一项重复）', () => {
    const set = new Set([...KB_ENGINES.map((e) => e.icon), KB_GROUP_ICON, KB_REPO_ICON])
    expect(set.size).toBe(KB_ENGINES.length + 2)
  })

  it('图标名必须是 Element Plus 里真实存在的组件', () => {
    const names = Object.keys(icons)
    const missing = KB_ENGINES.filter((e) => !names.includes(e.icon)).map((e) => `${e.key}:${e.icon}`)
    expect(missing, `不存在的图标: ${missing.join(', ')}`).toEqual([])
    expect(names).toContain(KB_GROUP_ICON)
    expect(names).toContain(KB_REPO_ICON)
  })

  it('未知引擎也有兜底名字与图标', () => {
    const m = kbEngineMeta('clickhouse')
    expect(m.label).toBe('CLICKHOUSE')
    expect(m.icon).toBeTruthy()
    expect(kbEngineMeta('redis').label).toBe('Redis')
  })

  // 「索引与查询点刷新没反应」的回归：调用点传对象也传字符串，取不到值时必须判 false
  // 而不是抛 TypeError——异常死在发请求之前，界面上什么提示都没有。
  describe('kbMatchesEngine', () => {
    it('认引擎名相同的集群，也认多出一段的推导结果', () => {
      expect(kbMatchesEngine({ engine: 'elasticsearch' }, 'elasticsearch')).toBe(true)
      expect(kbMatchesEngine({ engine: 'elasticsearch-master' }, 'elasticsearch')).toBe(true)
      expect(kbMatchesEngine('redis-cluster', 'redis')).toBe(true)
    })

    it('不同引擎判 false（前缀必须整段匹配）', () => {
      expect(kbMatchesEngine({ engine: 'postgresql' }, 'mysql')).toBe(false)
      expect(kbMatchesEngine({ engine: 'redis' }, 'redis-cluster')).toBe(false)
      // redis-cluster 属于 redis，但 kafka-cluster 不属于 kafka 之外的名字
      expect(kbMatchesEngine({ engine: 'kafka-ha' }, 'kafka')).toBe(true)
      expect(kbMatchesEngine({ engine: 'mysql' }, 'elasticsearch')).toBe(false)
    })

    it('空值/undefined 返回 false 而不是抛异常', () => {
      expect(kbMatchesEngine(undefined, 'kafka')).toBe(false)
      expect(kbMatchesEngine(null, 'kafka')).toBe(false)
      expect(kbMatchesEngine({}, 'kafka')).toBe(false)
      expect(kbMatchesEngine({ engine: '' }, 'kafka')).toBe(false)
      expect(kbMatchesEngine({ engine: 'kafka' }, '')).toBe(false)
      expect(kbMatchesEngine('', 'kafka')).toBe(false)
    })
  })
})
