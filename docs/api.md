# kube-console API 文档

- Base URL: `/api`
- 认证: 除登录与 WebSocket 终端外，所有接口需携带 `Authorization: Bearer <token>`（JWT，`jwt.expireIn` 小时有效）
- 集群选择: 集群资源类接口需携带请求头 `X-Cluster: <集群名>`（**URL 编码**，集群名可能含中文）
- 权限: 写接口逐条按三层 RBAC 判定（命名空间写 / 集群级写 / 平台角色），标注 **admin** 的仅平台管理员、标注 **平台角色** 的对 platform-viewer 只读放开；CI 全模块按项目绑定的命名空间判定（读与运行 = 该 ns 有任意角色，建改删 = 该 ns 可写，平台级凭据与全局变量 = 平台管理员）。读接口默认登录即可，`rbac.enforceMenus: true` 时按「菜单能力」一并收口（能力不足 → 403，`message` 里说明该授什么角色）
- 统一响应: `{"code": 0, "message": "ok", "data": ...}`，`code != 0` 表示失败（HTTP 状态码同步）
- 审计: 认证后的写操作（POST/PUT/DELETE）自动记录审计日志

## 认证

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/auth/login` | 登录 `{"username","password"}` → `{"token","username"}` |
| GET | `/auth/me` | 当前用户 `{"username","role"}` |
| PUT | `/auth/password` | 修改密码 `{"oldPassword","newPassword"}` |

## 集群管理

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/clusters` | 集群列表（名称/server/context/状态/最后连通时间） |
| POST | `/clusters` | 添加集群 `{"name","kubeconfig"}`（注册时校验连通性） |
| PUT | `/clusters/:name` | 更新 kubeconfig |
| DELETE | `/clusters/:name` | 删除集群 |
| GET | `/clusters/:name/connectivity` | 测试连通性并刷新状态 |
| PUT | `/clusters/:name/prometheus` | 配置该集群 Prometheus 地址 |
| GET | `/clusters/certs` | 证书巡检：各集群 TLS 证书到期盘点 |

以下接口均需 `X-Cluster`。

## 集群总览 / 搜索 / 矩阵

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/overview` | 统计卡片 + 节点摘要 + 资源容量 |
| GET | `/search?q=&limit=` | 全局搜索（Pod/工作负载/Service/ConfigMap/命名空间/节点） |
| GET | `/workloads/matrix` | 工作负载矩阵（命名空间 × 类型）。`kinds` 动态：Deployment/StatefulSet/DaemonSet/Pod 恒在，InstanceSet（KubeBlocks 副本载体，读 `spec.replicas` 与 `status.readyReplicas`）仅在 CRD 可列时出现；Pod 格子按容器就绪计（`Running`/`Completed` 记就绪） |
| GET | `/resource-defs?force=1` | 资源定义（discovery 驱动，自定义资源浏览器用） |
| GET | `/gateway-availability` | Gateway API 资源在当前集群可用性与实际版本 |
| GET | `/events?namespace=&type=&involved=&limit=` | 事件中心 |
| GET | `/quotas/overview` | ResourceQuota 按命名空间汇总 |

## 命名空间 / 节点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/namespaces?search=` | 列表 |
| POST | `/namespaces` | 创建 `{"name"}` |
| DELETE | `/namespaces/:name` | 删除 |
| GET | `/nodes` | 节点详情列表（容量/allocatable） |
| GET | `/nodes/:name` | 节点详情（Conditions/污点/节点上 Pod） |
| POST | `/nodes/:name/cordon` | 封锁/解除封锁（body 可空或 `{"uncordon":true}`） |
| POST | `/nodes/:name/drain` | 排水 `{"force":bool,"deleteLocalData":bool}` |
| PUT | `/nodes/:name/taints` | 更新污点 `{"taints":[{key,value,effect}]}` |
| PUT | `/nodes/:name/labels` | 更新标签 `{"labels":{...}}` |
| GET | `/nodes/:name/exec` | **WebSocket** 节点终端（nsenter 调试 Pod；token/cluster 走 query） |

## Pod（已融入工作负载：`/workloads/pods` 同框架）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/pods?namespace=&search=` | Pod 列表（状态/就绪/重启/IP/节点/镜像） |
| GET | `/pods/:name?namespace=` | Pod 详情（容器含 image 字段/Conditions/事件/容忍） |
| GET | `/pods/:name/logs?namespace=&container=&tailLines=&follow=1` | **SSE** 流式日志 |
| DELETE | `/pods/:name?namespace=` | 删除 |
| POST | `/pods/:name/evict?namespace=` | **驱逐**（Eviction API，尊重 PDB；DaemonSet/静态 Pod 拒绝） |
| GET | `/pods/:name/exec` | **WebSocket** 容器终端（无 shell 回退 / 调试容器注入；token/cluster 走 query） |
| GET | `/pods/:name/files?namespace=&container=&path=` | 容器文件列表 |
| GET | `/pods/:name/files/download?...` | 下载文件（流式） |
| POST | `/pods/:name/files/upload?...` | 上传文件（multipart） |
| POST | `/pods/:name/files/action` | 文件操作（新建/重命名/删除）`{namespace,container,action,...}` |

## 工作负载（控制器 + Pod 共用框架）

kind: `deployments | statefulsets | daemonsets | cronjobs | jobs | pods | replicasets | replicationcontrollers`

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/workloads/:kind?namespace=&search=` | 列表（Pod 时含 status/readyStr/restarts/ip/nodeName 扩展字段） |
| GET | `/workloads/:kind/:name?namespace=` | 详情（关联 Pod/事件） |
| GET | `/workloads/:kind/:name/rollouts?namespace=` | 发布历史（回滚时间线） |
| POST | `/workloads/:kind/:name/rollback?namespace=&revision=` | 回滚 |
| PUT | `/workloads/:kind/:name/scale` | 缩放 `{namespace,replicas}` |
| PUT | `/workloads/:kind/:name/restart` | 滚动重启（触发滚动更新） |
| GET | `/workloads/:kind/:name/logcollection?namespace=` | 容器内日志采集状态 `{enabled,config}`（仅 `deployments\|statefulsets\|daemonsets`） |
| PUT | `/workloads/:kind/:name/logcollection?namespace=` | 开启/更新采集 `{container,logPath,format,image}`：注入 fluent-bit Sidecar + 共享 emptyDir，改 Pod 模板触发滚动更新；`format=text` 写行日志索引、`json` 写 JSON 索引（见「日志源配置」）。ES 凭据写该 ns **共享**的 `kc-es-credentials`（同 ns 多个采集者共用一份），并清理旧版按工作负载建的 `<name>-fluent-bit-es` |
| DELETE | `/workloads/:kind/:name/logcollection?namespace=` | 关闭采集（摘除 Sidecar、共享卷与 ConfigMap）；该 ns 内已无其它采集者时一并回收共享凭据 Secret |
| DELETE | `/workloads/:kind/:name?namespace=` | 删除（含 PodMonitor 清理） |

## 通用资源（kind 模式）

kind: `services | ingresses | configmaps | secrets | persistentvolumeclaims | persistentvolumes | storageclasses | networkpolicies | serviceaccounts | roles | rolebindings | clusterroles | clusterrolebindings | resourcequotas | limitranges | horizontalpodautoscalers | endpoints | events | routes` 等（KindMap 全量）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/resources/:kind?namespace=&search=` | 列表（Service 含 `addresses[]`：每端口一条访问地址，LB 外部 IP / ExternalName / 集群内 DNS） |
| GET | `/resources/:kind/:name/yaml?namespace=` | 读取 YAML |
| DELETE | `/resources/:kind/:name?namespace=` | 删除 |

## 任意 GVR（CRD 浏览）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/generic/:group/:version/:resource?namespace=&search=` | 列表（group 空 = core） |
| GET | `/generic/:group/:version/:resource/:name/yaml?namespace=` | 读取 YAML |
| DELETE | `/generic/:group/:version/:resource/:name?namespace=` | 删除 |

## YAML 应用与导入导出

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/yaml/apply` | 应用 YAML（create-or-update：先查再改，已存在则更新、不存在则创建）`{"yaml"}` → `{created}`；Service/工作负载携带监控注解时自动联动创建 ServiceMonitor/PodMonitor（同样会贴上该集群 Prometheus 的 `serviceMonitorSelector`/`podMonitorSelector` 标签，否则 operator 不加载），Route 跨命名空间引用自动创建 ReferenceGrant |
| GET | `/yaml?resource=&namespace=&name=` | 读取任意资源 YAML |
| POST | `/yaml/export` | 按勾选导出资源为多文档 YAML（Kuboard 式逐层选择）`{"resources":[{"kind","namespace","name"}]}`；深度清洗（uid/resourceVersion/generation/creationTimestamp/managedFields/status/last-applied 注解），导出文件可直接再导入；单项失败记入 `skipped` 并继续 |

## 监控（Prometheus，经 kube-apiserver proxy）

`?range=1h|6h|24h|7d`（步长分别为 15s/60s/300s/3600s；未知值回落到 `6h`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/monitor/overview` | 集群级（CPU/内存/磁盘/网络 + 节点/命名空间排行）。PV 两块口径分开：`pvCount`/`pvTotalGi` 取 kube-state-metrics 的 `kube_persistentvolume_capacity_bytes`（就是各 PV `spec.capacity`，与 `kubectl get pv` 对得上）；`pvUsedPct`/`pvTopUsed` 只能用 kubelet 的 `kubelet_volume_stats_*`，那是**卷所在文件系统**的用量——local-path / 共享盘会把整块节点盘算进来，故 UI 上标"挂载点口径"。控制面字段已移除（迁到下面两个接口） |
| GET | `/monitor/controlplane` | 控制平面组件列表（API Server / Controller Manager / Scheduler / kube-proxy / CoreDNS / etcd / Prometheus）：`up` 就绪统计 + 每组件 1 个概要指标（共约 8 个 instant 查询）；组件 job 名随监控栈发行版用候选列表匹配，未采集的组件如实报"未接入" |
| GET | `/monitor/controlplane/:component?range=1h\|6h\|24h` | 单组件详情：全部指标卡（apiserver 含在途读/写、Goroutine、RSS；etcd 含后端 commit P99、提案失败率、实例数；coredns 含缓存命中率、P99 解析延迟…）+ 趋势（≤4 条 range，step 随档位放宽 30s/3m/15m、点数封顶 1500）。未知组件 400 |
| GET | `/monitor/query?promql=` | 通用 PromQL 查询 |
| GET | `/monitor/node/:name` | 节点级 |
| GET | `/monitor/namespace/:name` | 命名空间级 |
| GET | `/monitor/workload?namespace=&kind=&name=` | 工作负载级（按容器聚合） |
| GET | `/monitor/pod?namespace=&name=` | Pod 级 |
| GET | `/monitor/prometheus-check` | Prometheus 连通性测试 |
| GET | `/monitor/alerts` | 告警规则（firing/pending/inactive + 分组 + 规则检查；每条规则含 `source` = 来源 PrometheusRule CR `ns/name`，为空表示规则直接来自 rulefiles，规则编辑/新建/删除走 `/generic/.../prometheusrules` 接口由前端完成） |

### Alertmanager 接入

除「连接配置/主配置 YAML」外均需 `X-Cluster`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/monitor/am/status` | 当前集群 AM 可用性（configured/ok/error） |
| GET | `/monitor/am/alerts` | 实时告警（含 silenced/inhibited 状态） |
| GET/POST | `/monitor/am/silences` | 静默列表 / 创建（matchers + startsAt/endsAt） |
| DELETE | `/monitor/am/silences/:id` | 删除静默 |
| GET | `/alertevents?cluster=&state=&name=&namespace=&days=&page=&size=` | 告警历史（Alertmanager 轮询归档，跨集群） |
| GET | `/alertevents/stats?days=7` | 告警统计（触发/恢复/平均持续/Top 告警） |
| GET/POST/DELETE | `/alertmanager[/:cluster]` | （admin）AM 连接配置 CRUD |
| POST | `/alertmanager/test` | （admin）连通测试 `{clusterName}` |
| GET/PUT | `/monitor/am/config-yaml` | （admin）AM 主配置 YAML 读写（Secret 内 alertmanager.yaml[.gz]） |

## Helm 应用管理

release 操作需 `X-Cluster`；仓库源为全局配置。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/helm/releases?namespace=&search=` | Release 跨命名空间列表 |
| GET | `/helm/releases/:name/info?namespace=` | 详情（manifest/values/notes） |
| GET | `/helm/releases/:name/history?namespace=` | 版本历史 |
| PUT | `/helm/releases/:name/rollback?namespace=&revision=&wait=` | 回滚 |
| PUT | `/helm/releases/:name/upgrade` | 升级 `{namespace,repo,chart,version,values,wait,timeout}` |
| DELETE | `/helm/releases/:name?namespace=&keepHistory=` | 卸载 |
| POST | `/helm/releases` | 安装 `{repo,chart,version,name,namespace,createNamespace,values,wait,timeout,atomic}` |
| GET | `/helm/repos` | 仓库源列表 |
| POST | `/helm/repos` | 添加 `{name,url,username,password}` |
| PUT | `/helm/repos/:id` | 更新 |
| DELETE | `/helm/repos/:id` | 删除 |
| PUT | `/helm/repos/:id/refresh` | 刷新 index（内存缓存 TTL 30min） |
| GET | `/helm/repos/:id/charts?search=` | 浏览 chart（多版本折叠） |

## 数据库（KubeBlocks）

权限按**集群所在命名空间**判定：读=该 ns 有任意角色（view+）；写/运维/删除=该 ns 可写（edit/admin）。未装 KubeBlocks 时列表与引擎目录返回空。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/kb/engines` | 可安装引擎目录（`{installed, engines[]}`，拓扑/组件/副本上下限/存储卷/exporter/可改配置均来自 ClusterDefinition + ComponentDefinition）。组件的 `compDef` 已解析成**具体模板名**，`compDefPattern` 保留 addon 原始写法，`compDefs[]` 给出全部可选模板与版本；`replicasMax=0` 表示该版本模板未声明上限。**分片型拓扑**（redis 的 `cluster`、mongodb 的 `sharding`）不写 `components` 而写 `shardings[{name, shardingDef}]`，目录按 `ShardingDefinition` 解析出模板与 `shardsLimit` 上下限（`shardingDef` 同样可能是前缀，如 `mongo-shard-`）。**`ownsDataVolume`**（组件与其 `compDefs[]` 选项上都有）= 该模板在 podSpec（`ComponentDefinition.spec.runtime.volumes`）里自带名为 `data` 的卷（mongo-mongos 是 emptyDir），这种组件不能再配 `storage`：同名 `volumeClaimTemplates` 一起下发时 KB 合成 InstanceSet 会失败，组件建不出来、集群永久停在 Creating。注意别和 `spec.volumes` 里的 `data` 混——那份是"要求 Cluster 下发 PVC"的声明（mysql / postgresql / redis / mongo-config-server 都在那儿） |
| GET | `/kb/clusters?namespace=` | 集群列表（只含可读 ns；组件就绪数取 InstanceSet status）。除 `clusterDef` 外还给 `engine`：无拓扑（standalone，`spec` 只有 `componentSpecs`）的集群没有 `clusterDef`，按组件模板的 `app.kubernetes.io/name` 标签反推引擎，否则这类集群在引擎页里会整个不可见 |
| POST | `/kb/clusters?namespace=` | 创建 `{name,clusterDef,topology,terminationPolicy,components[],shardings[],scheduling?}`（apps.kubeblocks.io/v1 形态）。`components[].compDef` 允许传模式（如 `^mysql-\d+\.\d+.*$`），服务端按 `serviceVersion`（空则取最高版本）解析成真实 ComponentDefinition 后再下发。**分片型拓扑**（redis 的 `cluster`、mongodb 的 `sharding`）不填 `components` 而填 `shardings[{name,shardingDef,shards,replicas,compDef,资源/存储}]` → 下发 `spec.shardings[].template`；`shardingDef` 同样允许前缀/正则（mongodb 写的是 `mongo-shard-`），分片数按 ShardingDefinition 的 `shardsLimit` 校验（实测 apiserver 与 webhook 都不拦越界值，只能服务端拦）。**`scheduling`（集群级调度，对所有组件生效）**：`{nodeSelector{},tolerations[{key,value,operator,effect}],nodeRequired[{key,operator,values[]}],nodePreferred[...],spread:"preferred"|"required"}` → 转成 `spec.schedulingPolicy`（corev1 Affinity/Toleration）；`spread` 用 `app.kubernetes.io/instance=<集群>` 生成按 hostname 的实例间反亲和。提交前逐项校验（标签键值语法、operator/effect 枚举、Equal 必须有值、Exists 不许有值），并**先做一次 dry-run Create** 确认这套 addon 的 CRD 认 `spec.schedulingPolicy`——structural schema 会静默剪掉未知字段，不探就会出现"配了但没生效"的集群。模板自带 `data` 卷的组件（`ownsDataVolume`，如 mongodb sharding 的 `mongos`）若仍带 `storage`，**创建前就报错并指名该组件**（同名卷冲突会让集群卡在 Creating，比"建出一个不动的集群"好诊断）。**Elasticsearch 单组件形态**（`single-node` / `mdit` 拓扑，只有一个组件）由服务端补 `spec.componentSpecs[].configs[].variables.mode = single-node`（配置项名取自模板的 `spec.configs[].name`，真机是 `es-cm`）并拒 `replicas > 1`——缺这个变量时模板按 multi-node 渲染，产出空的 `cluster.initial_master_nodes`，ES 启动即报 `null-valued setting` 退出；多组件形态（master + dit）不注入，注入反而会让各组件各自成集群 |
| GET/DELETE | `/kb/clusters/:namespace/:name` | 详情（含组件 headless service 与实例标签、集群 conditions、分片拓扑及其成员组件）/ 删除 |
| GET/POST | `/kb/clusters/:namespace/:name/ops` | 运维工单列表（分页拉全后按 `spec.clusterName` 过滤，kubectl 手建的无标签工单不丢）/ 下发，下发后**回读工单状态**再回执 `{name, phase, message}`（apiserver 会静默收下形状不对的工单，只回 200 就等于"点了没反应"）；type 全集（1.0.2 CRD 枚举）：Restart\|Stop\|Start\|HorizontalScaling\|VerticalScaling\|Reconfiguring\|VolumeExpansion\|Upgrade\|Switchover\|Expose\|RebuildInstance。VolumeExpansion 传 `volumes[{name,storage}]`（卷名须来自组件 volumeClaimTemplates，只许扩）；Upgrade 传 `serviceVersion`（须在 ComponentVersion 升级链内，链外报可选项）；Switchover 可传 `instance`；Expose 传 `serviceType/exposeName/enable`；RebuildInstance 传 `backupName/inPlace` |
| GET | `/kb/clusters/:namespace/:name/ops/:ops` | 工单详情：`status.conditions` 时间线、progress、变更前旧值（`lastConfiguration`）、`cancelable` 标记 |
| POST | `/kb/clusters/:namespace/:name/ops/:ops/cancel` | 取消进行中的工单（patch `spec.cancel=true`；终态工单直接拒绝） |
| GET | `/kb/clusters/:namespace/:name/versions` | 各组件可升级版本链（ComponentVersion.compatibilityRules，`compDefs` 精确/前缀两种规则，可选版本取**每条规则自己的 releases 名单**），候选降序含当前版本 |
| GET | `/kb/clusters/:namespace/:name/connection` | 连接信息：组件 Service（排除 headless 与发布期临时 Service）、内网 DNS、NodePort、账号用户名与密码所在 Secret（密码一律不回显） |
| GET | `/kb/clusters/:namespace/:name/events` | 该集群资源的 Warning 事件（按 `<cluster>-` 前缀过滤，倒序最多 50 条） |
| POST | `/kb/clusters/:namespace/:name/monitor-access` | 一键创建 ServiceMonitor 接入 exporter 抓取 → `{created[],note,warning}`。**建之前先读该集群 Prometheus CR 的 `spec.serviceMonitorSelector.matchLabels` 并贴到对象 labels 上**（prometheus-operator 只加载 selector 匹配得上的 ServiceMonitor，标签不对就是"创建成功但永远没数据、且不报错"）；已存在的历史对象缺标签会在再点一次时补齐，`note` 说明贴了哪些标签、修了几条；selector 用 `matchExpressions`（贴标签满足不了）时给 `warning`，不谎报成功 |
| GET | `/kb/clusters/:namespace/:name/config-schema/:component` | 参数表单数据源（1.0 的 `parameters.kubeblocks.io` 契约：ParamConfigRenderer 按 compDef 精确匹配 → ParametersDefinition 的 JSON Schema + 动/静/不可变清单 + 生效方式，当前值取 ComponentParameter `<cluster>-<component>`；`schemaInJSON` 兼容内嵌对象与 JSON 字符串两种实测形态，schema 外的手工键保留为 `inSchema=false`）。无 ParametersDefinition 的组件返回空表单 + 说明，降级走原始文件 |
| POST | `/kb/clusters/:namespace/:name/config-schema/:component` | 表单化改参 `{parameters:{k:v}}`：本地先校验（不可变参数 / 未知键 / 枚举 / 布尔），通过后下发 Reconfiguring 工单 |
| GET | `/kb/clusters/:namespace/:name/configmaps` | 该集群的配置载体列表（排除 scripts/env 类） |
| GET/PUT | `/kb/clusters/:namespace/:name/configmaps/:cm` | 读取 / 覆盖配置内容（改完需重启组件生效；参数修改优先走 config-schema 表单） |
| GET | `/kb/repos` | 备份仓库列表（BackupRepo，集群级；空 = 未配置备份目的地，创建备份会在本地直接报错）。带 `config`（provider 的非敏感参数：bucket/region/endpoint…），凭证字段永不出现 |
| GET/POST | `/kb/repos/:name`、`/kb/repos` | 仓库详情 / 新建。**权限按集群层**（BackupRepo 不属于 ns）：读=登录，写=集群层可写（cluster-admin / platform-admin）。入参 `{name,provider,accessMethod,pvReclaimPolicy,pathPrefix?,volumeCapacity?,secretName,secretNamespace,config{}}`，服务端按 StorageProvider 的 `parametersSchema` 校验后才下发：必填参数齐全、**未知键直接拒**（写错的键会被 operator 静默忽略，备份时才发现连不上）、`accessMethod=Mount` 要求该 provider 的 CSI 驱动已就绪（否则提示改用 Tool）、`volumeCapacity` 走 Quantity 解析、凭证 Secret 必须存在且含 `credentialFields` 规定的键 |
| PUT/DELETE | `/kb/repos/:name` | 更新 / 删除。`storageProviderRef` 不可改（CRD 的 CEL 校验），换 provider 只能删了重建；`config` 里 schema 认得的键由表单权威覆盖，schema 外的手工键保留 |
| GET | `/kb/storage-providers` | StorageProvider 清单（名字 / phase / `csiDriverName` / 未就绪原因 / 参数与凭证字段），备份仓库表单的数据源 |
| GET | `/kb/repo-secrets?namespace=&provider=` | 该 ns 里**键齐全**的凭证 Secret（只回名字，绝不回内容）；要 ns 可读 |
| GET | `/kb/clusters/:namespace/:name/backup/policies` | 各组件备份策略（BackupPolicy，标签+命名双口径归属；方法清单、凭据 Secret 名、仓库状态） |
| GET/PUT | `/kb/clusters/:namespace/:name/backup/schedule` | 备份计划读取 / 更新 `{component, items[{method,enabled,cronExpression,retention}]}`（按项名对位，只改这三个字段，operator 填的 parameters 原样保留；cron 5/6 段本地校验） |
| GET/POST | `/kb/clusters/:namespace/:name/backups` | 备份记录（按 `spec.backupPolicyName → 集群策略` 归属过滤）/ 手动备份 `{component, method?, retention?}`（缺省方法取策略第一个；generateName 防重名；无可用 BackupRepo 时本地报错） |
| GET/DELETE | `/kb/clusters/:namespace/:name/backups/:backup` | 备份详情（各 action 执行明细、卷快照数、失败原因）/ 删除 |
| POST | `/kb/clusters/:namespace/:name/backup/restore` | 用备份恢复出新集群：复制源集群 spec（组件结构/资源/存储类），清身份元数据，按 1.0 注解协议 `kubeblocks.io/restore-from-backup` 声明恢复来源 `{name, component, backupName, backupNamespace?, volumeRestorePolicy?, terminationPolicy?}`；备份与组件前置校验 |
| POST | `/kb/clusters/:namespace/:name/backup/rebuild` | 用备份重建组件实例（RebuildInstance 工单）`{component, backupName, inPlace}` |
| GET | `/kb/clusters/:namespace/:name/backup/restores` | 恢复任务记录（Restore CR，按备份归属过滤） |
| GET | `/kb/clusters/:namespace/:name/accounts` | 系统账号 Secret 的键名（不回显明文） |
| GET | `/kb/clusters/:namespace/:name/pods` | 该集群实例 Pod（按 `app.kubernetes.io/instance` 选择，含角色/容器就绪/重启/节点/IP） |
| GET | `/kb/clusters/:namespace/:name/cli` | 镜像 CLI 自动登录入口 `{engine, entries[{component,pod,container,command}]}`；命令只引用容器内 `$ENV`，配 `GET /pods/:name/exec?cmd=` 使用（查看=ns 可读，exec 本身按 KubeSphere 口径要 ns 可写） |
| GET/POST | `/kb/clusters/:namespace/:name/kafka/topics` | topic 列表（`kafka-topics.sh --describe` 解析）/ 创建 `{name,partitions,replicationFactor,configs}` |
| PUT/DELETE | `/kb/clusters/:namespace/:name/kafka/topics/:topic` | 改配置 `{set,unset}`（`kafka-configs.sh --alter`）/ 删除 topic |
| GET | `/kb/clusters/:namespace/:name/kafka/groups` | 消费组概览与分区 Lag |
| POST | `/kb/clusters/:namespace/:name/kafka/consume` | 消息预览 `{topic,count,fromBeginning,partition,atOffset}`，服务端收口 ≤200 条 / 8s |
| GET | `/kb/clusters/:namespace/:name/es/health`、`/es/indices`、`/es/indices/:index` | 集群健康、索引列表、索引定义（分片/别名/字段映射） |
| DELETE/POST | `/kb/clusters/:namespace/:name/es/indices/:index`、`/es/search` | 删除索引（禁通配）/ DSL 查询 `{index,size,body}`（size 服务端收口 200） |

运维工单的下发响应除 `name` 外还带 `phase`/`message`（建单后立即回读一次）：KubeBlocks 对同一集群串行处理运维，新工单常停在 `Pending` 排队——不回传状态时前端只能提示"已下发"，用户看到的就是"点了没反应"。水平扩缩容按当前副本算差值走 `scaleOut/scaleIn.replicaChanges`：CRD 里没有 `replicas` 字段，传了会被 apiserver **静默丢弃**并生成一张什么都不做的空工单；前端在目标副本数与当前一致时直接拒绝下发并说明当前值。

Kafka 与 ES 的管理接口都在 **Pod 内执行官方 CLI**（控制台不通集群内网络）：脚本名走正则白名单，topic / 索引名 / 路径做字符校验后作为**独立 argv** 或 stdin 传给 `sh`，不拼进 shell 字符串；查看类接口要 ns 可读，变更类要 ns 可写。Kafka 侧假定 PLAINTEXT 监听（KB addon 默认），启用 SASL 时命令失败并回显原因；执行客户端脚本前会先清掉容器里一切 `*JMX*` 环境变量（bitnami broker 导出的 JMX 参数会被 `kafka-run-class.sh` 带给客户端 JVM，导致它去 bind 已被 broker 占用的 5555 端口而报 `AgentConfigurationError / BindException`），并把这类无关噪音从报错文案里剔除。

## CI（内置 Tekton）与 CD（ArgoCD）

CI 接口全部经 **X-Cluster** 选择集群（集群隔离）；**项目 1:1 绑定一个已存在的命名空间**，流水线与凭据都归属某个项目，
因此权限全部按「资源所属 ns」复用三层角色判定（无独立 CI 成员表）：

| 档位 | 能力 | 判定 |
| --- | --- | --- |
| 读 | 项目/流水线/运行/日志/制品/发布记录列表与详情 | 该 ns 有任意角色（`view` 及以上）；不可见的 ns 其资源在列表与下拉中直接不出现 |
| 触发 | 运行 / 重跑 / 停止 / 审批 | 同上（`view` 也能跑） |
| 写 | 建改删流水线与版本、项目凭据、定时与 Webhook、回滚、编辑/删除项目 | 该 ns 可写（`edit`/`admin` 或集群/平台写角色） |
| 平台级 | 平台级凭据（`projectId=null`）、全局变量 | 平台管理员 |

SSE/WS 为公开路由，用 `GET /ci/stream-token` 签发的 60s 短 token 走查询参数鉴权，**同样按 run 所属 ns 收口**（构建日志不外泄）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/ci/ready` | 就绪探测：`{tektonInstalled, platformNS, serviceAccount, cacheEnabled}`（未装 Tekton 前端降级提示） |
| GET | `/ci/node-types` | 节点插件元数据（20 类，schema 驱动设计器/校验/编译） |
| GET | `/ci/stream-token` | 签发 60s 流 token（SSE/WS 用） |
| GET/POST | `/ci/projects` | 项目列表（只含可读 ns，附 `namespaceMissing` 标注绑定 ns 是否已被外部删除）/ 新建 `{name,displayName,description,namespace}`：namespace 必须是集群里**已存在**且未被其他项目占用的 ns（不再自动建 ns） |
| PUT/DELETE | `/ci/projects/:id` | 更新（name/namespace 不可改，改绑一律拒绝）/ 删除：**级联删除**流水线、版本、运行与任务、计数器、制品、发布记录、定时、webhook 及交付、项目凭据（含已下发 Secret）；K8s ns 与业务资源不动。有进行中的执行时拒绝 |
| GET/POST | `/ci/pipelines?projectId=` | 流水线列表（含 latestVersion）/ 新建 |
| GET/PUT/DELETE | `/ci/pipelines/:id` | 详情（+最新 graph）/ 更新 / 删除（级联版本，进行中执行拒绝） |
| POST | `/ci/pipelines/:id/versions` | 保存版本 `{graphJson}`（校验→编译→落库，版本号自增） |
| GET | `/ci/pipelines/:id/versions` | 版本列表 |
| POST | `/ci/pipelines/:id/validate` | 图校验 `{valid, errors}` |
| POST | `/ci/pipelines/:id/compile` | 编译预览（Tekton YAML 文本） |
| POST | `/ci/pipelines/:id/run` | 触发执行 `{versionId?,branch?,tag?}`（tag 优先；git 分支/Tag 覆盖强制全量克隆）。**同流水线串行**：同一条流水线已有未结束的 run 时，本次返回一条 `status:"queued"` 记录，只落库、不建 Tekton 对象；由 syncer 在前一个进终态后按入队顺序放行（不同流水线同轮各放行一个，同项目可并行）。排队期间集群不可达就保持 queued 下一轮再试；流水线/版本已被删除或启动失败则置 `failed` 并把原因写进 `message` |
| POST | `/ci/pipelines/:id/duplicate` | 复制到目标项目 `{targetProjectId, name?}` |
| GET | `/ci/runs?pipelineId=&status=&page=&size=` | 执行历史 `{total, items}`（只列可读 ns 的流水线；传不可读的 pipelineId 返回 404） |
| GET | `/ci/runs/:id` | 执行详情 `{run, tasks, pipeline}`；`run.cacheInfo` 是依赖缓存情况（"已启用：ci-ws-p3 内 kc-cache/…"／"未启用：<原因>"），`run.message` 是平台侧失败原因 |
| GET | `/ci/runs/:id/tasks` | 任务列表（含 results 快照） |
| POST | `/ci/runs/:id/rerun` | 重跑（复用原版本与分支） |
| POST | `/ci/runs/:id/cancel` | 停止。**排队中（queued）的 run 走"出队"**：不访问集群（集群正宕机时也要能取消），清空启动参数后直接置 cancelled；已提交的才去取消 Tekton PipelineRun。workspace PVC 不在这里回收（见下方工作区说明） |
| GET | `/ci/runs/:id/approvals` | 审批节点列表 |
| POST | `/ci/runs/:id/approvals` | 审批决定 `{nodeId, decision: approved\|rejected}`（驳回终止流水线） |
| GET | `/ci/runs/:id/tasks/:taskID/logs?token=` | **SSE** 任务日志（`start` 事件 → `data:` 行帧 → `end` 事件；公开路由） |
| GET | `/ci/ws?token=&runId=&cluster=` | **WebSocket** 执行状态推送（`{type:"run", data}`；公开路由） |
| GET/POST | `/ci/credentials?projectId=` | 凭据列表（平台级仅平台管理员可见；项目凭据按 ns 可读性过滤）/ 新建（六类 form；明文即写 K8s Secret 后丢弃；非管理员必须指定 projectId） |
| PUT/DELETE | `/ci/credentials/:id` | 轮换（留空字段保持原值）/ 删除（被引用拒绝；Secret 从平台 ns + 归属项目 ns 及历史扇出副本一并清理） |
| GET | `/ci/schedules?pipelineId=` | 定时任务列表 / 新建 `{pipelineId, cron}`（5 段 cron） |
| PUT/DELETE | `/ci/schedules/:id` | 启停 / 改 cron / 删除 |
| GET/POST | `/ci/webhooks?pipelineId=` | Webhook 列表（含回调 URL）/ 生成 `{pipelineId, branch?}`（token 随机 48 hex） |
| PUT/DELETE | `/ci/webhooks/:id` | 启停 / 改分支过滤 / 删除 |
| POST | `/ci/webhook/:token` | **GitLab 回调公开端点**（push/tag/MR；分支过滤 + 事件指纹去重；异步触发） |
| GET | `/ci/globals` | 全局变量列表（节点参数 `${global.KEY}` 引用，编译期注入） |
| POST/PUT/DELETE | `/ci/globals/:id` | 新建 / 更新（key 不可改）/ 删除 |
| GET | `/ci/builders?projectId=` | 镜像构建器列表（附最近一次执行 `lastRun` 与 `projectNamespace`；只列可读 ns） |
| POST/PUT/DELETE | `/ci/builders(/:id)` | 新建 / 更新（项目与应用类型不可改）/ 删除（连带托管流水线、版本、令牌与已上传制品；进行中构建 409）。字段：`{projectId,name,appType:java\|go\|web,registry,repository,credentialId,baseImage?,port?,javaOpts?,entryArgs?,defaultTag?}`——`credentialId` 必须是账密/DockerConfig 形态且属本项目的推送凭证（保存即校验，不留悬空引用到运行期）；`entryArgs`（java/go 可选）为容器启动参数，空格分隔、生成约定 Dockerfile 的 CMD 追加在 ENTRYPOINT 后（exec 数组不经 shell，引号/$/反引号拒绝；java 与 JAVA_OPTS 组合时用 `"$@"` 透传） |
| POST | `/ci/builders/:id/artifact` | 上传构建产物（multipart 字段 `file`；Java=.jar、前端=.tar.gz/.tgz/.zip、Go=二进制；大小受 `ci.builderUploadMaxMB` 限制）。**zip 静态包由平台归一成 tar.gz 落盘**（条目名 path.Clean 防 zip-slip、解包总字节受同一上限约束防解压炸弹、伪 zip/空包当场拒绝），sha256 口径 = 归一后产物。落盘算 sha256 + 轮转下载令牌 + **立即生成托管流水线版本**（校验/编译失败当场报错）；上传/编辑 = ns 可写 |
| POST | `/ci/builders/:id/run` | 触发一次镜像构建 `{tag?}`（tag 缺省取 `defaultTag`，再缺省 `upload-<北京时间>`）。托管流水线 = artifact-fetch（任务 Pod 凭令牌回平台拉制品）→ build-image（preShell 写约定 Dockerfile：jar→temurin-jre `java -jar`、二进制→alpine、tar.gz→nginx 静态站）；**要求配置 `ci.serverUrl`**（任务 Pod 在集群内回调平台的地址），缺配置直接报明确错误。同构建器串行（排队口径与流水线一致） |
| GET | `/ci/builders/:id/preview` | 托管流水线编译产物预览（Tekton YAML，内含约定 Dockerfile；排障用，不落版本） |
| GET | `/ci/artifact/:token` | **制品下载公开端点**：任务 Pod 无用户身份，随机 48hex 令牌收口（`ci_builder_tokens`，上传时轮转、每构建器保留最近 5 个以便进行中的 run 继续可下载） |
| GET | `/ci/artifacts?projectId=&runId=&type=&page=&size=` | 制品列表（任务成功自动登记；按可读项目集合过滤） |
| GET | `/ci/artifacts/:id` | 制品详情 + 血缘（commit/branch/run/构建人） |
| GET | `/ci/artifacts/:id/download` | 流式下载代理（MinIO/Nexus；镜像类提示 docker pull） |
| GET | `/ci/deployments?projectId=&page=&size=` | 发布记录（部署镜像快照 + rollback 追溯） |
| POST | `/ci/deployments/:id/rollback` **admin** | 一键回滚（按快照恢复容器镜像） |
| GET | `/ci/repo/refs?url=&credential=` | Git 分支/Tag（`git ls-remote`，凭据名解析 K8s Secret，5min 缓存） |
| GET | `/ci/k8s/targets?namespace=` | k8s-deploy 节点下拉数据（workload + 容器） |
| GET | `/ci/projects/:id/cache` | 项目构建缓存页：承载盘（状态/容量/StorageClass）、缓存子目录、在用的流水线与节点、旧独立缓存盘、是否有执行在跑。要该 ns 有任意角色 |
| POST | `/ci/projects/:id/cache/clear` | 清空依赖缓存 = 删承载盘 + 重建空盘（顺带回收旧命名的独立缓存盘）。要该 ns 可写；**有 pending/running 的执行时 409 拒绝**（正在用的盘被抽掉会让那次失败得莫名其妙）；重建失败不判死，回 `warning` |

#### 执行工作区、依赖缓存与排队（workspace PVC）

- **一块盘一个项目**：workspace PVC 名为 `ci-ws-p<项目ID>`（依赖缓存也在它里面，见下）。原来是每个 run 新建一块 `ci-ws-<流水线>-<run>-<随机>`，每次触发都要等存储供给+绑定，终态再删——盘没了，下次还得再等。
- **同流水线串行、不同流水线并行**：工作区 PVC 内**按流水线分了子目录**（任务级 subPath `pl-<流水线ID>`，kubelet 首次运行时自动创建），同项目的不同流水线互不踩、可同时执行。同一流水线的并发触发互踩同一份工作目录，第二个及以后的触发进 `queued` 队列，由 syncer 按 FIFO 放行（`pumpQueue`：流水线内没有已提交的 run 才放行一个，不同流水线同轮各放行一个）。改为子目录之前的盘根内容成为残留，需要时手动清理。
- **盘只在项目删除时回收**：`releaseWorkspace`（终态/取消/启动补偿）与 reconcile 的孤儿 PVC 回收都显式跳过 `ci-ws-p*`；老命名继续回收，升级窗口期的残留盘不会堆着。`DELETE /ci/projects/:id` 成功后删该项目的盘（集群不可达只记日志，不挡删除）。
- **工作区清空**：`git-clone` 在 `git clone` 前用 shell 通配清掉工作目录（含 `.git`），保证每次都是干净起点；不清空的话共享盘上的第二次运行会直接撞 `destination path '.' already exists`。
- **依赖缓存不单独建盘**：PipelineRun 把 `cache` 工作区绑到**同一个 claim 的 `subPath: kc-cache`**，一个 TaskRun 只碰一块 PVC。独立第二块 `ci-cache-p<项目>` 在本地盘 RWO 下的死结是：需要同时挂载两块盘的 TaskRun 必须落在两块盘都能 attach 的节点上，选不出来就永久 Pending，原因只在 Pod 事件里。存量遗留盘由缓存页列出并可一键回收（不在后台悄悄删别人的盘）。
- **覆盖范围**：go / maven / gradle / python / npm 五类构建节点，只重定向下载与构建缓存目录（`GOMODCACHE`/`GOCACHE`、`maven.repo.local`、`GRADLE_USER_HOME`、`PIP_CACHE_DIR`、`npm_config_cache`/`YARN_CACHE_FOLDER`/pnpm `store-dir`），不复用 `node_modules`、`target` 这类安装结果。
- **不用缓存必须说原因**：`run.cacheInfo` 写「已启用：<盘名> 内 kc-cache/（与工作区同盘），N 个节点使用：…」或「未启用：平台构建缓存开关 ci.cacheEnabled 处于关闭状态」。原来这条只写服务日志，界面看到的是一次"跑了但莫名慢"的正常执行。
- 缓存运维接口：`GET /ci/projects/:id/cache`（看盘与在用方）、`POST /ci/projects/:id/cache/clear`（清空，见上表）。

### ArgoCD

| GET | `/argocd/apps` | ArgoCD 应用列表（跨全部命名空间读 `applications.argoproj.io` CRD；未安装返回 `installed:false`）。条目含 `project`（spec.project）与 `specError`（`status.conditions` 的 InvalidSpecError 原文——spec 非法如指向不存在的 AppProject 时 sync/health 恒为 Unknown，原因只在这条 condition） |
| GET | `/argocd/apps/:namespace/:name` | 完整 Application 对象 YAML（可视化编辑器加载） |
| GET | `/argocd/projects` | AppProject 列表（`appprojects.argoproj.io` CRD，含 `sourceRepos`/`destinations`）。应用 `spec.project` 必须指向其中之一，否则 ArgoCD 拒绝加载（只报 "app is not allowed in project X, or the project does not exist"）；前端表单据此选项目并校验仓库/目标是否被允许 |
| GET | `/argocd/repos` | 仓库列表（**ArgoCD v3**：Repository CRD 已移除，以带标签 `argocd.argoproj.io/secret-type=repository` 的 Secret 存储；凭据只返回有无标志，不返回明文） |
| POST | `/argocd/repos` **admin** | 新建/更新（upsert）。主机级 URL（如 `http://gitlab.xxx.com`）= 凭据模板，该域下仓库自动继承账号密码 |
| PUT/DELETE | `/argocd/repos/:name` **admin** | 更新（密码留空 = 保留原凭据）/ 删除 |
| POST | `/argocd/apps/:namespace/:name/refresh` **admin** | 触发 ArgoCD 刷新（`refresh=normal` annotation） |

## 镜像仓库（Harbor）

**admin** 组：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/registry/config` | 当前配置（url/username/insecure） |
| POST | `/registry/config` | 保存配置（密码留空保持不变） |
| POST | `/registry/test` | 连通性测试 → `{ok,projects}` |
| GET | `/registry/projects?search=` | 项目列表 |
| GET | `/registry/projects/:project/repos?search=` | 项目内镜像仓库 |
| GET | `/registry/tags?project=&repo=` | 镜像 Tag（制品）列表 |
| GET | `/registry/scan?project=&repo=&reference=` | 扫描状态/概览（C·H·M·L 计数） |
| POST | `/registry/scan?project=&repo=&reference=` | 触发扫描（409 视为成功） |
| GET | `/registry/vulns?project=&repo=&reference=` | CVE 明细（Trivy 报告，双格式：3.x 1.1 + 2.x 旧格式；含 CVSS/修复状态/链接） |

登录即可（只读探测，供 VulnChip / Tag 下拉静默调用）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/registry/image-tags?image=` | 按完整镜像引用取可选 Tag（校验属于已配置仓库） |
| GET | `/registry/image-vulns?image=` | 按完整镜像引用取扫描概览 + CVE 报告 |

## 授权管理

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/authz/grant` | 向导式授权（生成 Role/RoleBinding 或 ClusterRoleBinding） |
| GET | `/authz/bindings` | 已授绑定列表 |
| DELETE | `/authz/bindings/:kind/:name` | 回收 |
| GET | `/authz/user-permissions?username=&group=` | 权限逆查（某用户/组的全部权限） |
| GET | `/authz/my-permissions` | 当前用户在 `X-Cluster` 集群的生效 K8s RBAC 视角（权限查询页用） |

## 权限管理（角色 / 绑定 / 快照）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/rbac/roles` | 角色列表（内置 + 自定义） |
| POST/PUT/DELETE | `/rbac/roles[/:id]` | **admin** 自定义角色 CRUD（按 API 授权项生成 ClusterRole `kc-role-<name>`） |
| GET | `/rbac/permission-items` | 授权项目录（资源组 × 读/写），角色表单用 |
| GET | `/rbac/bindings` | 授权绑定列表（含旧版 `kc-grant-*`） |
| POST/DELETE | `/rbac/bindings[/:id]` | **admin** 创建/回收，同事务物化 K8s RBAC（`kc-rbac-<id>`） |
| GET | `/rbac/user-roles?username=` | 某用户的角色明细 |
| GET | `/rbac/my-permissions` | **当前用户权限快照**（前端菜单与按钮渲染依据）：`platformAdmin` / `platformViewer` / `allNsWrite` / `clusterWrite` / `namespaces`（可写 ns）/ `namespacesRead`（有任意角色的 ns）/ `allNsRead` / `roles[]` / **`menus[]`** |

`menus` 是后端按「该用户在当前集群能读哪些资源」推导出的可见菜单键数组：项键就是侧边栏 index（`/overview`、`/workloads/deployments`、`/resources/secrets`、`/kb/mysql`…），分组键加 `group:` 前缀（`group:workload-group`）。前端侧边栏只渲染这份集合，路由守卫也按它挡（详情路由先归到所属列表页键）。能力口径见 README「菜单按能力显隐与接口收口」。

字段缺省语义：老后端或权限服务未启用时 `menus` 不下发，前端按"全部可见"处理（不会清空侧边栏）。

## 平台管理（**admin**）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/users` | 用户列表 / 创建 |
| PUT | `/users/:id/password` | 重置密码 |
| PUT | `/users/:id/role` | 改角色 `{role}` |
| PUT | `/users/:id/group` | 调整用户组 |
| DELETE | `/users/:id` | 删除 |
| GET | `/audit?page=&size=&username=&method=` | 审计日志 |
| GET | `/groups` | 用户组列表（登录即可读，供授权向导选择） |
| POST | `/groups` | 新建/更新组 |
| DELETE | `/groups/:id` | 删除组 |
| GET/POST | `/notify/channels` | 通知渠道列表 / 保存（钉钉加签等；用于 CI 执行事件推送） |
| DELETE | `/notify/channels/:id` | 删除渠道 |
| POST | `/notify/channels/:id/test` | 测试渠道连通 |
| GET | `/notify/logs?page=&size=` | 推送历史 |
| GET/POST | `/logsources` | ES 日志源（按集群）列表 / 保存：连接信息 + `indexPrefix`（行日志：标准输出与单行文本采集）+ `jsonIndexPrefix`（JSON 采集，空=`logstash-`）+ 事件归档开关与 `eventIndexPrefix` |
| DELETE | `/logsources/:cluster` | 删除日志源 |
| POST | `/logsources/test` | 日志源连通性测试 |
| POST | `/logs/search` | 日志检索 `{namespace,pod,container,source,keyword,level,minutes\|from/to,page,size}`；`source`=空(全部)/`stdout`(标准输出)/`file`(文本文件)/`json`(JSON 采集)，按来源只查对应索引模式 |
| POST | `/events/archive/search` | 事件归档检索（ES，namespace/type/reason/keyword/object + 时间范围 + 分页） |
| PUT | `/clusters/:name/grafana` | 保存集群 Grafana 地址（iframe 内嵌） |
| GET | `/monitor/grafana-check?url=` | Grafana 可达性测试（服务端尽力而为） |
| GET | `/usage?days=` | 命名空间用量报表（Prometheus 历史指标） |
| GET | `/velero/overview` | 备份概览：跨 ns 列举 Velero Backup/Schedule/Restore，含进度（progressItems/快照数）、耗时、validationWarnings/Errors、计划下次执行时间（服务端按 cron 计算）、`locations`（BackupStorageLocation 列表）与 `namespace`（Velero 安装 ns，新建 CR 落点）；未安装返回 `installed:false`，RBAC 缺失按列表分片报 `partial`（不再把"读不了"说成"没装"） |
| GET | `/velero/events?namespace=&kind=&name=` | 备份/恢复/计划的关联事件（失败信息主要落点；资源级错误明细在备份 tarball 内，控制台不解析对象存储） |
| GET | `/velero/volumes?backup=` | 该备份的卷备份明细（PodVolumeBackup CR，按 `velero.io/backup-name` 标签跨 ns 列举）：Pod/卷/节点/phase/message，未完成的排前面——「部分失败」定位到具体哪个卷坏掉；备份详情抽屉展示 |
| POST | `/velero/backups` **admin** | 手动发起一次备份 `{namespace?,name?,includedNamespaces?,excludedNamespaces?,snapshotVolumes?,fsBackup?,storageLocation?,ttlHours?}`（namespace 留空服务端按 BSL→备份→计划推导；name 缺省 `manual-<时间戳>`；模板字段留空 = 全集群范围、Velero 默认保留期 720h） |
| POST | `/velero/schedules` **admin** | 新建备份计划 `{name,cron,paused?,...同上模板字段}`（cron 服务端按标准 5 段校验并给中文示例错误，非法不落 Velero） |
| POST | `/velero/restores` **admin** | 发起恢复 `{namespace(源备份 ns),backupName,name?,includedNamespaces?,restorePVs?,preserveNodePorts?}`（Restore 与源备份同 ns；name 缺省 `<备份名>-rst-<时间戳>`） |
| PUT | `/velero/schedules/:namespace/:name/pause` **admin** | 启停备份计划（`{paused}`） |
| PUT | `/velero/schedules/:namespace/:name` **admin** | 编辑备份计划：整体替换 cron/paused/备份模板（`{cron,paused?,...模板字段}`；cron 服务端校验并给中文示例错误，名称在路径不可改） |
| DELETE | `/velero/schedules/:namespace/:name` **admin** | 删除备份计划（不连带历史备份；删除后不再产生新的定时备份） |
| DELETE | `/velero/backups/:namespace/:name` **admin** | 删除备份记录（**连带对象存储中的备份数据**，前端需输入名称确认） |
| DELETE | `/velero/restores/:namespace/:name` **admin** | 删除恢复任务记录（已恢复资源不受影响） |
| GET/POST | `/tokens` | 个人 API Token 列表 / 创建 |
| DELETE | `/tokens/:id` | 吊销 Token |

## Nacos 微服务集成

OpenAPI 客户端自动探测版本风格（1.x/2.x 走 v1 API，3.x 走 `/v3/admin` + `/v3/auth`）；集群经 `cluster` 参数或 `X-Cluster` 选择。

**微服务页面接口（登录即可用）**——服务发现与配置管理，跟随全局命名空间选择（`namespaces` 支持逗号分隔多选，`*` 展开为全部命名空间）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/nacos/ready?cluster=` | 当前集群是否已接入 Nacos `{configured, enabled}` |
| GET | `/nacos/services?cluster=&namespaces=` | 服务发现列表（3.x 含分组/集群数/实例数/健康数，1.x/2.x 仅名称），多命名空间合并，每项带 `namespace`；单命名空间异常不阻塞整体 |
| GET | `/nacos/instances?cluster=&namespace=&service=&group=` | 服务实例列表（IP:端口/健康/权重/集群/元数据） |
| DELETE | `/nacos/service?cluster=&namespace=&service=&group=` | 删除 Nacos 侧服务注册记录（不影响 K8s 资源） |
| GET | `/nacos/configs?cluster=&namespaces=` | 配置列表（多命名空间合并，每项带 `namespace`） |
| GET | `/nacos/config-content?cluster=&namespace=&dataId=&group=` | 读取配置内容 |
| POST | `/nacos/config-publish` | 发布/更新配置 `{clusterName,namespace,dataId,group,content,type}` |
| DELETE | `/nacos/config-content?cluster=&namespace=&dataId=&group=` | 删除配置 |
| GET | `/nacos/config-export?cluster=&namespaces=` | 批量导出配置（含内容）为 JSON：`{cluster, exportedAt, configs:[{namespace,dataId,group,type,content}]}`，配合前端导入实现跨环境迁移 |

**接入与治理接口（admin）**：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/nacos/config` | 按集群接入配置列表 / 保存（密码留空保持不变） |
| DELETE | `/nacos/config/:cluster` | 删除配置（清理映射与 Webhook） |
| POST | `/nacos/config/test` | 连通测试 `{clusterName}` |
| GET | `/nacos/status` | 各集群同步状态 + 命名空间映射（含注入凭据） |
| POST | `/nacos/sync/:cluster` | 立即同步 |
| POST | `/nacos/reset-password` | 轮换 ns 用户密码并同步注入 Secret `{clusterName, namespace}` |
| GET | `/nacos/namespaces`、`/nacos/users` | Nacos 侧命名空间 / 用户浏览 |

## WebSocket 端点

浏览器无法自定义请求头，token/cluster 走 **query 参数**：

| 路径 | 说明 |
| --- | --- |
| `GET /pods/:name/exec?namespace=&container=&token=&cluster=` | Pod 容器终端 |
| `GET /nodes/:name/exec?token=&cluster=` | 节点宿主机终端（自动 nsenter 调试 Pod） |

## SSE 端点

| 路径 | 说明 |
| --- | --- |
| `GET /pods/:name/logs?namespace=&container=&tailLines=&follow=` | Pod 日志流（`data:` 帧，`end` 结束） |
| `GET /ci/runs/:id/tasks/:taskID/logs`（`?token=`） | CI 任务实时日志（SSE 编码） |
