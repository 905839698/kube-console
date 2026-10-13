// 数据库引擎的子菜单定义：key 与 KubeBlocks 的 ClusterDefinition 同名
// （某引擎 addon 未装时，页面按它给「未安装 addon」提示而不是空白）。
// 图标取 Element Plus 线性图标（与其余菜单同一套风格、同色不撞色），按数据形态取义：
// 簿册=关系表、闪电=内存缓存、消息框=消息流、便签=文档、放大镜=检索、柱图=分析型。
export interface KBEngineMeta {
  key: string
  label: string
  icon: string
}

export const KB_ENGINES: KBEngineMeta[] = [
  { key: 'mysql', label: 'MySQL', icon: 'Notebook' },
  { key: 'redis', label: 'Redis', icon: 'Lightning' },
  { key: 'kafka', label: 'Kafka', icon: 'MessageBox' },
  { key: 'mongodb', label: 'MongoDB', icon: 'Memo' },
  { key: 'elasticsearch', label: 'Elasticsearch', icon: 'Search' },
  { key: 'postgresql', label: 'PostgreSQL', icon: 'Histogram' },
]

// 父菜单与引擎项也不能同图标；未知引擎（自装 addon 起了别的名字）给中性兜底，不至于显示空白
export const KB_GROUP_ICON = 'Management'
// 备份仓库不属于任何引擎，但它是数据库页签里"没有它就备不了份"的前置条件，放在同一分组里
export const KB_REPO_ICON = 'FolderOpened'
const FALLBACK_ICON = 'Coin'

export function kbEngineMeta(key: string): KBEngineMeta {
  return KB_ENGINES.find((e) => e.key === key) || { key, label: key.toUpperCase(), icon: FALLBACK_ICON }
}

// 集群归属引擎：无拓扑（standalone）集群没有 clusterDef，后端按组件模板的
// app.kubernetes.io/name 标签反推，猜出来的名字可能多一段（elasticsearch-master），
// 故允许 "<引擎>-…" 前缀。
// 允许传整个集群或只传引擎名（两种调用点都出现过）；拿不到值时判 false 而不是抛异常——
// 早先这里按对象取 c.engine，被传成字符串后抛 TypeError，异常死在发请求之前，
// 现象就是「索引与查询点刷新没反应」，极难从界面反推。
export function kbMatchesEngine(c: { engine?: string } | string | null | undefined, engine: string) {
  const got = typeof c === 'string' ? c : c?.engine
  if (!got || !engine) return false
  return got === engine || got.startsWith(engine + '-')
}
