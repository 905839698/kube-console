// 简单资源的 Schema 表单字段定义（SchemaForm 直接编辑对象，无需 parse/build）
import type { FieldDef } from './types'

/** 基础元信息字段（名称/命名空间；标签与注解由 ObjectEditor 的 MetaEditor 统一编辑） */
export const metaFields: FieldDef[] = [
  { key: 'metadata.name', label: '名称', type: 'text', required: true },
  { key: 'metadata.namespace', label: '命名空间', type: 'text' },
]

/** 集群级资源元信息（无命名空间） */
export const clusterMetaFields: FieldDef[] = [
  { key: 'metadata.name', label: '名称', type: 'text', required: true },
]

const ACCESS_MODES = ['ReadWriteOnce', 'ReadOnlyMany', 'ReadWriteMany', 'ReadWriteOncePod']

export const simpleFormFields: Record<string, FieldDef[]> = {
  configmaps: [
    ...metaFields,
    { key: 'data', label: '数据', type: 'kv', kvValuePlaceholder: '值', multiline: true },
    { key: 'binaryData', label: '二进制数据 (Base64)', type: 'kv', kvValuePlaceholder: 'Base64 值' },
    { key: 'immutable', label: '不可变', type: 'bool', help: 'immutable=true 后数据不可再修改（需删除重建）' },
  ],
  persistentvolumeclaims: [
    ...metaFields,
    { key: 'spec.storageClassName', label: '存储类', type: 'text', placeholder: '如 local-path / nfs-client' },
    { key: 'spec.volumeName', label: '指定 PV', type: 'text', placeholder: '绑定到指定 PV（可选）' },
    { key: 'spec.accessModes', label: '访问模式', type: 'multi-select', options: ACCESS_MODES },
    { key: 'spec.resources.requests.storage', label: '容量', type: 'text', placeholder: '如 10Gi' },
  ],
  persistentvolumes: [
    ...metaFields,
    { key: 'spec.capacity.storage', label: '容量', type: 'text', placeholder: '如 100Gi' },
    { key: 'spec.accessModes', label: '访问模式', type: 'multi-select', options: ACCESS_MODES },
    { key: 'spec.persistentVolumeReclaimPolicy', label: '回收策略', type: 'select', options: ['Retain', 'Recycle', 'Delete'] },
    { key: 'spec.storageClassName', label: '存储类', type: 'text' },
    { key: 'spec.mountOptions', label: '挂载选项', type: 'strings', placeholder: '逗号分隔，如 noatime, nodiratime' },
    // 卷来源（互斥，按需填写对应类型）
    { key: 'spec.nfs.server', label: 'NFS 服务器', type: 'text' },
    { key: 'spec.nfs.path', label: 'NFS 路径', type: 'text' },
    { key: 'spec.hostPath.path', label: 'HostPath 路径', type: 'text' },
    { key: 'spec.local.path', label: 'Local 路径', type: 'text' },
    { key: 'spec.csi.driver', label: 'CSI 驱动', type: 'text' },
    { key: 'spec.csi.volumeHandle', label: 'CSI VolumeHandle', type: 'text' },
    { key: 'spec.claimRef.namespace', label: '所属 PVC 命名空间', type: 'text' },
    { key: 'spec.claimRef.name', label: '所属 PVC 名称', type: 'text' },
  ],
  storageclasses: [
    ...clusterMetaFields,
    { key: 'provisioner', label: 'Provisioner', type: 'text', required: true, placeholder: '如 rancher.io/local-path' },
    { key: 'reclaimPolicy', label: '回收策略', type: 'select', options: ['Delete', 'Retain', 'Recycle'] },
    { key: 'volumeBindingMode', label: '绑定模式', type: 'select', options: ['Immediate', 'WaitForFirstConsumer'] },
    { key: 'allowVolumeExpansion', label: '允许扩容', type: 'bool' },
    { key: 'mountOptions', label: '挂载选项', type: 'strings', placeholder: '逗号分隔' },
    { key: 'parameters', label: '参数', type: 'kv', kvValuePlaceholder: '值' },
  ],
  serviceaccounts: [
    ...metaFields,
    { key: 'secrets', label: '关联 Secret', type: 'list', items: [{ key: 'name', label: 'Secret 名称', type: 'text' }] },
    { key: 'imagePullSecrets', label: '拉取镜像 Secret', type: 'list', items: [{ key: 'name', label: 'Secret 名称', type: 'text' }] },
  ],
  resourcequotas: [
    ...metaFields,
    { key: 'spec.hard', label: '配额限制', type: 'kv', kvValuePlaceholder: '如 10 / 2Gi' },
    { key: 'spec.scopes', label: '作用范围 (scopes)', type: 'strings', placeholder: '逗号分隔，如 NotTerminated, BestEffort' },
  ],
  limitranges: [
    ...metaFields,
    {
      key: 'spec.limits',
      label: '限制项',
      type: 'list',
      items: [
        { key: 'type', label: '类型', type: 'select', options: ['Container', 'Pod', 'PersistentVolumeClaim', 'PodFixed', 'Object'] },
        { key: 'max.cpu', label: '最大 CPU', type: 'text' },
        { key: 'max.memory', label: '最大内存', type: 'text' },
        { key: 'min.cpu', label: '最小 CPU', type: 'text' },
        { key: 'min.memory', label: '最小内存', type: 'text' },
        { key: 'default.cpu', label: '默认 CPU', type: 'text' },
        { key: 'default.memory', label: '默认内存', type: 'text' },
        { key: 'defaultRequest.cpu', label: '默认请求 CPU', type: 'text' },
        { key: 'defaultRequest.memory', label: '默认请求内存', type: 'text' },
        { key: 'maxLimitRequestRatio.cpu', label: '最大/请求比 CPU', type: 'text' },
        { key: 'maxLimitRequestRatio.memory', label: '最大/请求比 内存', type: 'text' },
      ],
    },
  ],
  endpointslices: [
    ...metaFields,
    { key: 'addressType', label: '地址类型', type: 'select', options: ['IPv4', 'IPv6'] },
    {
      key: 'endpoints',
      label: '端点',
      type: 'list',
      items: [
        { key: 'addresses', label: '地址', type: 'strings', placeholder: '逗号分隔，如 10.0.0.1, 10.0.0.2' },
        { key: 'notReadyAddresses', label: 'NotReady 地址', type: 'strings' },
        { key: 'conditions.ready', label: '就绪', type: 'bool' },
        { key: 'hostname', label: '主机名', type: 'text', placeholder: '可选' },
        { key: 'zones', label: '可用区', type: 'strings' },
        { key: 'deploymentName', label: '部署名称', type: 'text', placeholder: '可选（1.21+）' },
      ],
    },
    {
      key: 'ports',
      label: '端口',
      type: 'list',
      items: [
        { key: 'name', label: '名称', type: 'text', placeholder: '如 http' },
        { key: 'protocol', label: '协议', type: 'select', options: ['TCP', 'UDP', 'SCTP'] },
        { key: 'port', label: '端口', type: 'number' },
        { key: 'appProtocol', label: 'appProtocol', type: 'text', placeholder: '可选，如 kubernetes.io/h2c' },
      ],
    },
  ],
  gatewayclasses: [
    ...clusterMetaFields,
    { key: 'spec.controllerName', label: '控制器', type: 'text', required: true, placeholder: '如 istio.io/gateway-controller' },
    { key: 'spec.description', label: '描述', type: 'text' },
    { key: 'spec.parametersRef.group', label: '参数 Group', type: 'text' },
    { key: 'spec.parametersRef.kind', label: '参数 Kind', type: 'text' },
    { key: 'spec.parametersRef.name', label: '参数名称', type: 'text' },
    { key: 'spec.parametersRef.namespace', label: '参数命名空间', type: 'text' },
  ],
  referencegrants: [
    ...metaFields,
    {
      key: 'spec.from',
      label: '允许来源 (from)',
      type: 'list',
      items: [
        { key: 'group', label: 'Group', type: 'text', placeholder: '如 gateway.networking.k8s.io' },
        { key: 'kind', label: 'Kind', type: 'text', placeholder: '如 HTTPRoute' },
        { key: 'namespace', label: '命名空间', type: 'text', placeholder: '来源命名空间' },
      ],
    },
    {
      key: 'spec.to',
      label: '目标资源 (to)',
      type: 'list',
      items: [
        { key: 'group', label: 'Group', type: 'text', placeholder: '如 "" / gateway.networking.k8s.io' },
        { key: 'kind', label: 'Kind', type: 'text', placeholder: '如 Service' },
        { key: 'name', label: '名称', type: 'text', placeholder: '留空表示该类型全部' },
        { key: 'namespace', label: '命名空间', type: 'text', placeholder: '目标命名空间' },
      ],
    },
  ],
  endpoints: [...metaFields],
}
