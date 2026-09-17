// 表单注册表：kind -> 可视化编辑模块
import type { FormModule } from './types'
import { simpleFormFields } from './schemaForms'
import { parseWorkload, buildWorkload, workloadTemplate } from './workload'

import SchemaForm from '../components/forms/SchemaForm.vue'
import WorkloadForm from '../components/forms/WorkloadForm.vue'
import ServiceForm from '../components/forms/ServiceForm.vue'
import IngressForm from '../components/forms/IngressForm.vue'
import NetworkPolicyForm from '../components/forms/NetworkPolicyForm.vue'
import HPAForm from '../components/forms/HPAForm.vue'
import RbacForm from '../components/forms/RbacForm.vue'
import GatewayRouteForm from '../components/forms/GatewayRouteForm.vue'
import RoutesForm from '../components/forms/RoutesForm.vue'
import SecretForm from '../components/forms/SecretForm.vue'
import ArgoAppForm from '../components/forms/ArgoAppForm.vue'
import { serializeRules, deserializeRules } from './rbac'
import { serializeRoute, deserializeRoute } from './routes'
import { parseArgoApp } from './argoApp'

// schema 类资源：SchemaForm 直接编辑对象
const schemaModule = (title: string, kind: string): FormModule => ({
  title,
  fields: simpleFormFields[kind] || [],
})

const workloadModule = (title: string): FormModule => ({
  title,
  metaInForm: true,
  component: WorkloadForm,
  parse: (obj) => parseWorkload(obj, kindOf(obj)),
  build: (base, formData) => buildWorkload(base, formData, kindOf(base)),
  template: (kind, ns) => workloadTemplate(kind, ns),
})

// ArgoCD Application：parse 保证 metadata/spec.source/destination/syncPolicy 结构存在
// （表单直接绑定）；来源类型判定/切换逻辑见 forms/argoApp.ts
function kindOf(obj: any): string {
  const kind = obj?.kind || ''
  const map: Record<string, string> = {
    Deployment: 'deployments',
    StatefulSet: 'statefulsets',
    DaemonSet: 'daemonsets',
    ReplicaSet: 'replicasets',
    ReplicationController: 'replicationcontrollers',
    CronJob: 'cronjobs',
    Job: 'jobs',
  }
  return map[kind] || 'deployments'
}

/** 表单模块注册表（未注册的资源用 YAML 编辑兜底） */
export const argoAppModule = (): FormModule => ({
  title: 'ArgoCD Application',
  metaInForm: true,
  component: ArgoAppForm,
  parse: parseArgoApp,
  build: (_base, formData) => formData,
  template: (_kind, ns) => ({
    apiVersion: 'argoproj.io/v1alpha1',
    kind: 'Application',
    metadata: { name: 'my-app', namespace: ns || 'argocd' },
    spec: {
      project: 'default',
      source: { repoURL: '', path: '', targetRevision: 'main' },
      destination: { server: 'https://kubernetes.default.svc', namespace: 'default' },
      syncPolicy: { automated: {}, selfHeal: true, prune: true },
    },
  }),
})

export const formRegistry: Record<string, FormModule> = {
  // 工作负载
  deployments: workloadModule('Deployment'),
  statefulsets: workloadModule('StatefulSet'),
  daemonsets: workloadModule('DaemonSet'),
  replicasets: workloadModule('ReplicaSet'),
  replicationcontrollers: workloadModule('ReplicationController'),
  cronjobs: workloadModule('CronJob'),
  jobs: workloadModule('Job'),
  // 服务发现
  services: {
    title: 'Service',
    component: ServiceForm,
    template: (kind, ns) => ({
      apiVersion: 'v1',
      kind: 'Service',
      metadata: { name: 'my-app', namespace: ns || 'default' },
      spec: { type: 'ClusterIP', selector: { app: 'my-app' }, ports: [{ name: 'http', port: 80, targetPort: 80, protocol: 'TCP' }] },
    }),
  },
  ingresses: {
    title: 'Ingress',
    component: IngressForm,
    template: (kind, ns) => ({
      apiVersion: 'networking.k8s.io/v1',
      kind: 'Ingress',
      metadata: { name: 'my-app', namespace: ns || 'default' },
      spec: { rules: [{ host: 'example.com', http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'my-app', port: { number: 80 } } } }] } }] },
    }),
  },
  // 路由（应用层 path → Service 转发规则，存于 annotations）
  routes: {
    title: '路由',
    noAnnotations: true,
    component: RoutesForm,
    template: (kind, ns) => ({
      apiVersion: 'v1',
      kind: 'ConfigMap',
      metadata: { name: 'my-routes', namespace: ns || 'default', annotations: { 'console.kube.io/routes': '[{"path":"/","service":"my-app","port":80,"protocol":"HTTP","host":""}]' } },
      data: { routes: '[]' },
    }),
  },
  // 配置中心
  configmaps: schemaModule('ConfigMap', 'configmaps'),
  secrets: {
    title: 'Secret',
    component: SecretForm,
    template: (kind, ns) => ({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: 'my-secret', namespace: ns || 'default' },
      type: 'Opaque',
      stringData: { password: 'your-password' },
    }),
  },
  serviceaccounts: schemaModule('ServiceAccount', 'serviceaccounts'),
  // 存储
  persistentvolumeclaims: schemaModule('PVC', 'persistentvolumeclaims'),
  persistentvolumes: schemaModule('PV', 'persistentvolumes'),
  storageclasses: schemaModule('StorageClass', 'storageclasses'),
  // 网络
  networkpolicies: {
    title: 'NetworkPolicy',
    component: NetworkPolicyForm,
    template: (kind, ns) => ({
      apiVersion: 'networking.k8s.io/v1',
      kind: 'NetworkPolicy',
      metadata: { name: 'my-policy', namespace: ns || 'default' },
      spec: { podSelector: { matchLabels: {} }, policyTypes: ['Ingress'], ingress: [{ from: [], ports: [] }] },
    }),
  },
  // 弹性
  horizontalpodautoscalers: {
    title: 'HPA',
    component: HPAForm,
    template: (kind, ns) => ({
      apiVersion: 'autoscaling/v2',
      kind: 'HorizontalPodAutoscaler',
      metadata: { name: 'my-app', namespace: ns || 'default' },
      spec: { scaleTargetRef: { apiVersion: 'apps/v1', kind: 'Deployment', name: 'my-app' }, minReplicas: 1, maxReplicas: 5, metrics: [{ type: 'Resource', resource: { name: 'cpu', target: { type: 'Utilization', averageUtilization: 80 } } }] },
    }),
  },
  // 安全 RBAC
  roles: {
    title: 'Role',
    component: RbacForm,
    parse: (obj) => deserializeRules(obj),
    build: (base, formData) => serializeRules(formData),
    template: (kind, ns) => ({ apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'Role', metadata: { name: 'my-role', namespace: ns || 'default' }, rules: [] }),
  },
  clusterroles: {
    title: 'ClusterRole',
    component: RbacForm,
    parse: (obj) => deserializeRules(obj),
    build: (base, formData) => serializeRules(formData),
    template: () => ({ apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'ClusterRole', metadata: { name: 'my-clusterrole' }, rules: [] }),
  },
  rolebindings: {
    title: 'RoleBinding',
    component: RbacForm,
    template: (kind, ns) => ({ apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'RoleBinding', metadata: { name: 'my-binding', namespace: ns || 'default' }, roleRef: { apiGroup: 'rbac.authorization.k8s.io', kind: 'Role', name: 'my-role' }, subjects: [] }),
  },
  clusterrolebindings: {
    title: 'ClusterRoleBinding',
    component: RbacForm,
    template: () => ({ apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'ClusterRoleBinding', metadata: { name: 'my-binding' }, roleRef: { apiGroup: 'rbac.authorization.k8s.io', kind: 'ClusterRole', name: 'my-clusterrole' }, subjects: [] }),
  },
  // 配额
  resourcequotas: schemaModule('ResourceQuota', 'resourcequotas'),
  limitranges: schemaModule('LimitRange', 'limitranges'),
  // EndpointSlice：apiVersion 为 discovery.k8s.io/v1，需自定义模板（默认模板硬编码 v1）
  endpointslices: {
    title: 'EndpointSlice',
    fields: simpleFormFields['endpointslices'],
    template: (kind, ns) => ({
      apiVersion: 'discovery.k8s.io/v1',
      kind: 'EndpointSlice',
      metadata: { name: 'my-slice', namespace: ns || 'default', labels: { 'kubernetes.io/service-name': '' } },
      addressType: 'IPv4',
      endpoints: [],
      ports: [],
    }),
  },
  // Gateway API
  gatewayclasses: schemaModule('GatewayClass', 'gatewayclasses'),
  referencegrants: schemaModule('ReferenceGrant', 'referencegrants'),
  gateways: {
    title: 'Gateway',
    component: GatewayRouteForm,
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1',
      kind: 'Gateway',
      metadata: { name: 'my-gateway', namespace: ns || 'default' },
      spec: { gatewayClassName: '', listeners: [{ name: 'http', protocol: 'HTTP', port: 80, hostname: '' }] },
    }),
  },
  httproutes: {
    title: 'HTTPRoute',
    component: GatewayRouteForm,
    parse: (obj) => deserializeRoute(obj),
    build: (base, formData) => serializeRoute(formData),
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1',
      kind: 'HTTPRoute',
      metadata: { name: 'my-route', namespace: ns || 'default' },
      spec: { parentRefs: [{ name: 'my-gateway', namespace: ns || 'default' }], hostnames: ['example.com'], rules: [{ matches: [{ path: { value: '/', type: 'PathPrefix' } }], backendRefs: [{ name: 'my-app', port: 80 }] }] },
    }),
  },
  grpcroutes: {
    title: 'GRPCRoute',
    component: GatewayRouteForm,
    parse: (obj) => deserializeRoute(obj),
    build: (base, formData) => serializeRoute(formData),
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1',
      kind: 'GRPCRoute',
      metadata: { name: 'my-grpc-route', namespace: ns || 'default' },
      spec: { parentRefs: [{ name: 'my-gateway', namespace: ns || 'default' }], rules: [{ matches: [{ method: { service: '', method: '' } }], backendRefs: [{ name: 'my-app', port: 50051 }] }] },
    }),
  },
  tlsroutes: {
    title: 'TLSRoute',
    component: GatewayRouteForm,
    parse: (obj) => deserializeRoute(obj),
    build: (base, formData) => serializeRoute(formData),
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1',
      kind: 'TLSRoute',
      metadata: { name: 'my-tls-route', namespace: ns || 'default' },
      spec: { parentRefs: [{ name: 'my-gateway', namespace: ns || 'default' }], rules: [{ backendRefs: [{ name: 'my-app', port: 443 }] }] },
    }),
  },
  tcproutes: {
    title: 'TCPRoute',
    component: GatewayRouteForm,
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1alpha2',
      kind: 'TCPRoute',
      metadata: { name: 'my-tcp-route', namespace: ns || 'default' },
      spec: { parentRefs: [{ name: 'my-gateway', namespace: ns || 'default' }], rules: [{ protocol: 'TCP', backendRefs: [{ name: 'my-app', port: 3306 }] }] },
    }),
  },
  udproutes: {
    title: 'UDPRoute',
    component: GatewayRouteForm,
    template: (kind, ns) => ({
      apiVersion: 'gateway.networking.k8s.io/v1alpha2',
      kind: 'UDPRoute',
      metadata: { name: 'my-udp-route', namespace: ns || 'default' },
      spec: { parentRefs: [{ name: 'my-gateway', namespace: ns || 'default' }], rules: [{ protocol: 'UDP', backendRefs: [{ name: 'my-app', port: 53 }] }] },
    }),
  },
  // ArgoCD（CRD resource 名 applications；ArgoCD 应用页与 CRD 浏览页共用）
  applications: argoAppModule(),
}

/** 是否有可视化表单 */
export function hasForm(kind: string): boolean {
  return !!formRegistry[kind]
}
