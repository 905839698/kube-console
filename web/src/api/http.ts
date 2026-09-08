// axios 封装：注入 JWT 与当前集群头，统一错误处理
import axios, { type AxiosRequestConfig } from 'axios'
import { ElMessage } from 'element-plus'
import router from '../router'
import { useUserStore } from '../store/user'
import { activeCluster } from '../store/clusterRef'

export interface ApiResp<T = any> {
  code: number
  message: string
  data: T
}

function makeInstance(silent: boolean) {
  const inst = axios.create({ baseURL: '/api', timeout: 60000 })
  inst.interceptors.request.use((config) => {
    const user = useUserStore()
    if (user.token) {
      config.headers.Authorization = `Bearer ${user.token}`
    }
    // 与页面守卫同源：读 activeCluster（select() 唯一写入），localStorage 兜底
    const cluster = activeCluster.value || localStorage.getItem('kc-cluster')
    if (cluster) {
      // 集群名可能含非 ASCII（如中文“浪潮”）。浏览器会把非 ISO-8859-1 头值剥离为空，
      // 导致后端收到空头报“缺少 X-Cluster 请求头”；故编码后传输，后端再解码还原。
      config.headers['X-Cluster'] = encodeURIComponent(cluster)
    }
    return config
  })
  inst.interceptors.response.use(
    (resp) => resp,
    (error) => {
      const status = error.response?.status
      if (status === 401) {
        const user = useUserStore()
        user.logout()
        if (router.currentRoute.value.path !== '/login') {
          router.push('/login')
        }
        return Promise.reject(error)
      }
      if (!silent) {
        const msg = error.response?.data?.message || error.message || '请求失败'
        ElMessage.error(msg)
      }
      return Promise.reject(error)
    },
  )
  return inst
}

const http = makeInstance(false)
const httpSilent = makeInstance(true)

/** 请求并解包统一响应结构，失败时抛出包含 message 的错误
 *  silent=true 时不弹全局 toast（供 CVE 徽标、Tag 下拉等辅助探测类请求使用，
 *  调用方自行按业务降级展示） */
export async function request<T = any>(config: AxiosRequestConfig, opts?: { silent?: boolean }): Promise<T> {
  const inst = opts?.silent ? httpSilent : http
  const resp = await inst.request<ApiResp<T>>(config)
  const body = resp.data
  if (body.code !== 0) {
    if (!opts?.silent) {
      ElMessage.error(body.message || '请求失败')
    }
    throw new Error(body.message || '请求失败')
  }
  return body.data
}

export { http }
export default http
