# kube-console API 文档

- Base URL: `/api`
- 认证: 除登录与 WebSocket 终端外，所有接口需携带 `Authorization: Bearer <token>`（JWT，`jwt.expireIn` 小时有效）
- 集群选择: 集群资源类接口需携带请求头 `X-Cluster: <集群名>`（**URL 编码**，集群名可能含中文）
- 权限: 默认登录即可；标注 **admin** 的接口需管理员角色；CI 写操作（/ci/*）亦限 admin
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
| GET | `/workloads/matrix` | 工作负载矩阵（命名空间 × 类型） |
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
| POST | `/yaml/apply` | 应用 YAML（create-or-update：先查再改，已存在则更新、不存在则创建）`{"yaml"}` → `{created}`；Service/工作负载携带监控注解时自动联动创建 ServiceMonitor/PodMonitor，Route 跨命名空间引用自动创建 ReferenceGrant |
| GET | `/yaml?resource=&namespace=&name=` | 读取任意资源 YAML |
| POST | `/yaml/export` | 按勾选导出资源为多文档 YAML（Kuboard 式逐层选择）`{"resources":[{"kind","namespace","name"}]}`；深度清洗（uid/resourceVersion/generation/creationTimestamp/managedFields/status/last-applied 注解），导出文件可直接再导入；单项失败记入 `skipped` 并继续 |

## 监控（Prometheus，经 kube-apiserver proxy）

`?range=1h|6h|24h`

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/monitor/overview` | 集群级（CPU/内存/磁盘/网络 + 节点/命名空间排行 + 控制面） |
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

## CI（内置 Tekton）与 CD（ArgoCD）

CI 接口全部经 **X-Cluster** 选择集群（集群隔离）；项目级资源落在项目命名空间（命名空间隔离）。
写操作限 **admin**；SSE/WS 端点为公开路由，用 `GET /ci/stream-token` 签发的 60s 短 token 走查询参数鉴权。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/ci/ready` | 就绪探测：`{tektonInstalled, platformNS, serviceAccount, cacheEnabled}`（未装 Tekton 前端降级提示） |
| GET | `/ci/node-types` | 节点插件元数据（19 类，schema 驱动设计器/校验/编译） |
| GET | `/ci/stream-token` | 签发 60s 流 token（SSE/WS 用） |
| GET/POST | `/ci/projects` | 项目列表 / 新建 `{name,displayName,description,namespace}`（namespace 必填，自动创建） |
| PUT/DELETE | `/ci/projects/:id` | 更新（name/namespace 不可改）/ 删除（有流水线或进行中执行拒绝） |
| GET/POST | `/ci/pipelines?projectId=` | 流水线列表（含 latestVersion）/ 新建 |
| GET/PUT/DELETE | `/ci/pipelines/:id` | 详情（+最新 graph）/ 更新 / 删除（级联版本，进行中执行拒绝） |
| POST | `/ci/pipelines/:id/versions` | 保存版本 `{graphJson}`（校验→编译→落库，版本号自增） |
| GET | `/ci/pipelines/:id/versions` | 版本列表 |
| POST | `/ci/pipelines/:id/validate` | 图校验 `{valid, errors}` |
| POST | `/ci/pipelines/:id/compile` | 编译预览（Tekton YAML 文本） |
| POST | `/ci/pipelines/:id/run` | 触发执行 `{versionId?,branch?,tag?}`（tag 优先；git 分支/Tag 覆盖强制全量克隆） |
| POST | `/ci/pipelines/:id/duplicate` | 复制到目标项目 `{targetProjectId, name?}` |
| GET | `/ci/runs?pipelineId=&status=&page=&size=` | 执行历史 `{total, items}` |
| GET | `/ci/runs/:id` | 执行详情 `{run, tasks, pipeline}` |
| GET | `/ci/runs/:id/tasks` | 任务列表（含 results 快照） |
| POST | `/ci/runs/:id/rerun` | 重跑（复用原版本与分支） |
| POST | `/ci/runs/:id/cancel` | 停止（取消 Tekton PipelineRun + 清理 PVC） |
| GET | `/ci/runs/:id/approvals` | 审批节点列表 |
| POST | `/ci/runs/:id/approvals` | 审批决定 `{nodeId, decision: approved\|rejected}`（驳回终止流水线） |
| GET | `/ci/runs/:id/tasks/:taskID/logs?token=` | **SSE** 任务日志（`start` 事件 → `data:` 行帧 → `end` 事件；公开路由） |
| GET | `/ci/ws?token=&runId=&cluster=` | **WebSocket** 执行状态推送（`{type:"run", data}`；公开路由） |
| GET/POST | `/ci/credentials?projectId=` | 凭据列表（含被引用流水线）/ 新建（六类 form；明文即写 K8s Secret 后丢弃） |
| PUT/DELETE | `/ci/credentials/:id` | 轮换（留空字段保持原值）/ 删除（被引用拒绝；Secret 全 ns 删除） |
| GET | `/ci/schedules?pipelineId=` | 定时任务列表 / 新建 `{pipelineId, cron}`（5 段 cron） |
| PUT/DELETE | `/ci/schedules/:id` | 启停 / 改 cron / 删除 |
| GET/POST | `/ci/webhooks?pipelineId=` | Webhook 列表（含回调 URL）/ 生成 `{pipelineId, branch?}`（token 随机 48 hex） |
| PUT/DELETE | `/ci/webhooks/:id` | 启停 / 改分支过滤 / 删除 |
| POST | `/ci/webhook/:token` | **GitLab 回调公开端点**（push/tag/MR；分支过滤 + 事件指纹去重；异步触发） |
| GET | `/ci/globals` | 全局变量列表（节点参数 `${global.KEY}` 引用，编译期注入） |
| POST/PUT/DELETE | `/ci/globals/:id` | 新建 / 更新（key 不可改）/ 删除 |
| GET | `/ci/artifacts?projectId=&runId=&type=&page=&size=` | 制品列表（任务成功自动登记） |
| GET | `/ci/artifacts/:id` | 制品详情 + 血缘（commit/branch/run/构建人） |
| GET | `/ci/artifacts/:id/download` | 流式下载代理（MinIO/Nexus；镜像类提示 docker pull） |
| GET | `/ci/deployments?projectId=&page=&size=` | 发布记录（部署镜像快照 + rollback 追溯） |
| POST | `/ci/deployments/:id/rollback` **admin** | 一键回滚（按快照恢复容器镜像） |
| GET | `/ci/repo/refs?url=&credential=` | Git 分支/Tag（`git ls-remote`，凭据名解析 K8s Secret，5min 缓存） |
| GET | `/ci/k8s/targets?namespace=` | k8s-deploy 节点下拉数据（workload + 容器） |
| GET | `/argocd/apps?namespace=` | ArgoCD 应用列表（读 `applications.argoproj.io` CRD；未安装返回 `installed:false`） |
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
| GET/POST | `/logsources` | ES 日志源（按集群）列表 / 保存（含事件归档开关与索引前缀） |
| DELETE | `/logsources/:cluster` | 删除日志源 |
| POST | `/logsources/test` | 日志源连通性测试 |
| POST | `/logs/search` | 日志检索（全文 + 字段 + 时间范围 + Pod 分布） |
| POST | `/events/archive/search` | 事件归档检索（ES，namespace/type/reason/keyword/object + 时间范围 + 分页） |
| PUT | `/clusters/:name/grafana` | 保存集群 Grafana 地址（iframe 内嵌） |
| GET | `/monitor/grafana-check?url=` | Grafana 可达性测试（服务端尽力而为） |
| GET | `/usage?days=` | 命名空间用量报表（Prometheus 历史指标） |
| GET | `/backups` | Velero 备份/恢复概览（未安装时 `installed:false`） |
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
