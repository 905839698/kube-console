// 详情路由 → 它所属的列表页菜单键。后端 resourceCapFor 判接口能力时用的是同一套对应关系，
// 两边一致才不会出现在"菜单看得见、点详情被挡"或反之的错位。
export function menuKeyOfPath(path: string): string {
  const seg = path.split('/').filter(Boolean)
  if (!seg.length) return '/'
  switch (seg[0]) {
    case 'workloads':
      return seg.length >= 3 ? '/workloads/' + seg[1] : path
    case 'pods':
      return seg.length >= 3 ? '/workloads/pods' : path
    case 'crd':
      return seg.length >= 4 ? '/resources/' + seg[3] : path
    case 'ci':
      // /ci/pipelines/7/design 是流水线设计器，归属 CI 菜单
      return seg.length >= 3 ? '/ci' : path
    case 'monitor':
      return seg[1] === 'namespace' ? '/monitor' : path
    case 'nodes':
      return seg.length >= 2 ? '/nodes' : path
    case 'helm':
      return seg[1] === 'releases' && seg.length >= 3 ? '/helm/releases' : path
    case 'argocd':
      return seg.length >= 3 ? '/argocd' : path
    default:
      return path
  }
}

