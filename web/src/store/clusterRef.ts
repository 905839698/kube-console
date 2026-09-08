import { ref } from 'vue'

// 当前激活集群名的单一内存来源。
// 页面守卫（判断是否可发起集群请求）与 axios 请求头（X-Cluster）都读它，
// 保证两者永远一致；避免依赖 localStorage（部分浏览器/内嵌环境读取不一致）
// 造成「页面已按新集群发请求但请求头缺失/为空 → 缺少 X-Cluster 请求头」。
// select() 是唯一写入点，同时同步 localStorage 做跨刷新持久化。
export const activeCluster = ref<string>(localStorage.getItem('kc-cluster') || '')
