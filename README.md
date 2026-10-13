# Kube Console — Kubernetes 管理平台

参考 [Kuboard](https://www.kuboard.cn/) 设计的一体化 Kubernetes 管理平台（云厂商控制台风格，浅色/深色双主题），把日常运维要用的东西收在一个登录态里：**多集群接入与全资源浏览编辑、工作负载与 Pod 运维（终端 / 文件 / 驱逐 / 日志采集）、Prometheus 监控与 Alertmanager 告警、ES 日志与事件归档检索、内置 Tekton CI + ArgoCD / Helm 交付、Harbor 镜像与 Trivy 漏洞、Nacos 微服务集成、KubeSphere 式三层授权与操作审计**。

资源数据不缓存、每次直连 kube-apiserver；外部系统（Prometheus / ES / Harbor / Tekton / ArgoCD / Velero / Nacos）未接入时相关页面优雅降级并给出配置指引，不报错。

> 以下截图取自一套接入真实生产集群（14 节点 / 18 命名空间 / 124 Deployment）的部署实例。

## 界面与交互

- **主题**：Kuboard 风格——深蓝主色（`#2B4B83`）+ 浅色侧边栏（白底、活动项浅蓝底 + 深蓝文字）+ 深蓝表头白字列表 + 分区卡片式详情页（状态胶囊、键值网格、资源用量进度条与环形仪表）；深色模式在用户菜单切换；主题色集中定义于 `web/src/styles/theme.css`
- **全局搜索**：顶栏搜索框，输入即搜（Pod / 工作负载 / Service / ConfigMap / 命名空间 / 节点），结果按类型分组，点击直达详情
- **全局命名空间选择**：顶栏命名空间多选（含「全选」），命名空间相关页面联动跟随；筛选状态与 URL 同步（`?namespace=` 刷新/分享可复现）
- **全局集群切换**：顶栏下拉切换已注册集群（`X-Cluster` 请求头）；集群不可达时展示告警面板与「重新检测」，不会把请求挂死
- **左树右面板**：工作负载 / Pod 详情页左侧资源树（工作负载 → Pod → 容器，状态色点），右侧面板联动
- **进详情与文本复制**：工作负载 / Pod 列表**不绑定行点击跳转**（否则想选中或双击复制名称、IP、镜像时会被带进详情页），进详情走蓝色名称链接或操作 → 详情；其余列表（资源 / 节点 / CI 运行 / ArgoCD 应用）仍支持行点击直达详情，且**只要拖选出选区就不跳转**，复制不受影响。Service 列表点名称展示 **selector 匹配的后端 Pod 列表**（状态/就绪/重启/IP/节点），YAML 详情走操作列
- **个人能力**：修改密码、**个人 API Token**（创建 / 吊销，脚本化调用 `Authorization: Bearer`）、深色模式

### 集群总览（监控下钻的第一级）

节点 / Pod / 工作负载 / 命名空间统计卡，CPU·内存·磁盘·网络使用率与趋势，Requests/Limits 占可分配比例，**PV 容量与卷用量**（数量与总容量取 kube-state-metrics 的 PV `spec.capacity`，与 `kubectl get pv` 一致；已用率与 PVC Top 取 kubelet volume stats，即卷所在文件系统的口径，UI 上据实标注），节点状态表与资源使用排行——全部经 kube-apiserver service proxy 取 Prometheus，无需对外暴露监控端口。**控制平面组件健康已迁到「可观测性 → 控制平面组件」独立页**（见下），总览不再每次刷新都背着 ~18 个控制面查询。

![集群总览](docs/images/01-overview.png)

## 菜单结构

一级菜单 13 项（总览 + 12 个分组）：

```
集群总览
集群          集群管理（仅平台角色）· 命名空间 / 节点 / 自定义资源
工作负载       矩阵视图 / Deployment / StatefulSet / DaemonSet / CronJob / Job / Pod / HPA
服务与网络     服务与路由（服务 / EndpointSlice / 路由 / Ingress 类）· 网关（Gateway API 全量）· 网络策略
配置中心       ConfigMap / Secret / ServiceAccount
存储           PVC / PV / StorageClass
可观测性       监控 / 控制平面组件 / 告警 / Grafana 面板 / 事件中心 / 日志检索
CI / CD       CI 流水线 / ArgoCD 应用 / ArgoCD 仓库 / Helm Releases / Chart 仓库
微服务         服务发现 / 配置管理（Nacos，登录即可用，跟随顶栏命名空间）
数据库         MySQL / Redis / Kafka / MongoDB / Elasticsearch / PostgreSQL 六个引擎子菜单（`/kb/:engine`，
              与 KubeBlocks ClusterDefinition 同名；某引擎 addon 未装时给提示而非空白页）+ 备份仓库
安全 (RBAC)    Role / RoleBinding / ClusterRole / ClusterRoleBinding
配额           ResourceQuota / LimitRange
平台管理       用户 / 授权 / 角色 / 审计 / 通知 / Nacos 管理 / 镜像仓库 / 用量报表 / 备份概览
              （platform-admin 全量；platform-viewer 只读授权·角色·审计·报表·集群）
```

隐藏路由（不在菜单，由列表跳转进入）：工作负载详情 `/workloads/:kind/:name`、Pod 详情 `/pods/:ns/:name`、节点详情 `/nodes/:name`、命名空间监控 `/monitor/namespace/:name`、Helm Release 详情 `/helm/releases/:name`、CRD 浏览 `/crd/:group/:version/:resource`、流水线设计器 `/ci/pipelines/:id/design`。

上面每一项都**不是写死显示**的：后端按「该用户在当前集群能读哪些资源」推导出可见菜单键集合（`/rbac/my-permissions` 的 `menus` 字段），侧边栏与路由守卫都只渲染这份集合，直接敲 URL 也会被 redirected 到他自己能看的第一个页面。详见 [权限管理 → 菜单按能力显隐](#菜单按能力显隐与接口收口)。

## 功能全景

### 集群与资源

| 模块 | 功能 |
| --- | --- |
| 集群管理 | kubeconfig 粘贴注册 / 编辑 / 删除 / 连通性测试，多集群界面切换；Prometheus 地址按集群配置 + 连通性测试；**Grafana 地址按集群配置**（iframe 内嵌面板）；**熔断**：连接异常的集群自动标记，避免请求长时间挂起；**权限**：页面与 `/clusters` 完整列表仅 platform-admin（viewer 只读），普通用户经 `/my-clusters` 只取顶栏切换所需最小字段（名称/状态/Grafana 地址） |
| 命名空间 | 列表 / 创建 / 删除 / 搜索，一键进入**命名空间监控**独立页 |
| 节点 | 列表与详情（容量、Conditions、污点、节点上 Pod、监控面板）；**运维操作**：封锁/解除封锁（cordon）、排水（drain，可选 force / 忽略 DaemonSet / emptyDir 处理 / 宽限期 / 超时）、污点与标签编辑；**节点终端**：自动创建 nsenter 调试 Pod 进入宿主机 shell（**需集群级写权限**） |
| 自定义资源 | discovery 驱动的**资源浏览器**：按 API group 折叠展示全部官方资源与 CRD（KubeBlocks / Elastic / Tekton / Gateway API 等），搜索 / 强刷 / 浏览 / 删除 / YAML 编辑 |
| 配额 | ResourceQuota 总览（按命名空间汇总 used/hard，可编辑）+ LimitRange |

### 工作负载与 Pod

![工作负载列表](docs/images/02-workloads.png)
![工作负载矩阵视图](docs/images/12-matrix.png)

| 模块 | 功能 |
| --- | --- |
| 控制器列表 | Deployment / StatefulSet / DaemonSet / CronJob / Job / HPA：搜索、**批量删除 / 批量重启**、行内缩放 / 滚动重启 / 回滚 / 删除、**导出 / 导入**、新建（表单 + YAML）；Pod 融入同一列表框架（状态 / 就绪 / 重启 / IP / 节点 / 镜像列） |
| 矩阵视图 | 命名空间 × 工作负载类型二维矩阵（ready/total 着色），点格子直达对应列表。列为 Deployment / StatefulSet / DaemonSet / **InstanceSet** / **Pod**：InstanceSet 是 KubeBlocks 副本的实际载体（读 `spec.replicas` 与 `status.readyReplicas`，格子跳通用 CRD 页），**只在装了 addon 的集群出现**；Pod 列按容器就绪计（`Running`/`Completed` 记就绪），不参与行合计，免得与控制器副本重复计数 |
| 工作负载详情 | 左树右面板：关联 Pod 与事件、**缩放 / 滚动重启 / 发布历史与一键回滚**（rollouts 时间线）、**调整镜像**（Tag 下拉来自已配置仓库，可手填）、容器级监控面板、日志采集开关、YAML / 表单编辑 |
| Pod 详情 | 容器与初始化容器状态、Conditions、事件、**容忍**、Pod 监控；**Web 终端**、**日志**、**容器文件浏览器**（浏览 / 下载 / 上传 / 新建 / 重命名 / 删除）、**镜像 CVE 徽标**（点击弹明细）、驱除（Eviction API，尊重 PDB）与删除 |

![工作负载详情](docs/images/03-workload-detail.png)
![Pod 详情](docs/images/04-pod-detail.png)

| 模块 | 功能 |
| --- | --- |
| Web 终端 | Pod 内 Shell（WebSocket exec）：容器无 shell 时自动回退 `/bin/bash`/`/bin/ash`；distroless 镜像注入临时调试容器（`kubectl debug` 风格，镜像 `k8s.debugImage` 可配，默认 `busybox:1.36`）；终端尺寸自适应。**权限**：Pod 终端与文件浏览走 pods/exec，需该命名空间写权限（edit/admin 或平台管理员） |
| 容器内日志采集 | 给 Deployment / StatefulSet / DaemonSet 注入 **fluent-bit Sidecar**，与目标容器共享 emptyDir 后 tail 容器内日志文件写集群 ES（复用「平台管理-日志源配置」）。两条索引：**单行文本**写行日志索引（与标准输出同库），**JSON** 字段展开并保留原始行、写独立 JSON 索引；开启或改配置会修改 Pod 模板触发滚动更新，关闭即摘除 Sidecar 与 ConfigMap。ES 凭据落在**每命名空间共享**的 `kc-es-credentials`（不再按工作负载复制多份），该 ns 最后一个采集者关闭时回收；旧版 `<工作负载>-fluent-bit-es` 在下次保存时自动清理 |
| 可视化编辑 | 表单 + YAML 双视图：工作负载（容器 / 镜像 / 环境变量 / 端口 / 资源限制 / 探针 / 挂载 / 卷 / 容忍 / 调度 / 更新策略）、Service（**标签选择器级联下拉**：key/value 选项来自命名空间内 Pod 现有标签）、Ingress、ConfigMap、Secret、PVC、PV、StorageClass、NetworkPolicy、HPA、ServiceAccount、Role / ClusterRole（规则编辑器）、RoleBinding / ClusterRoleBinding、ResourceQuota、LimitRange、Gateway API 全量资源（**路由后端三级级联下拉**：命名空间 → Service → 端口名）；其余资源 YAML 编辑兜底 |
| 镜像 Tag 自动补全 | 编辑镜像字段后自动拉取该仓库可选 Tag 下拉（Harbor，700ms 防抖） |

### 可观测性

**监控**（PromQL 即席查询 + 五级下钻）

| 层级 | 入口 |
| --- | --- |
| 集群 | 集群总览（见上文截图） |
| 命名空间 | `/monitor/namespace/:name` 独立页 |
| 节点 | 节点详情 → 监控 |
| 工作负载 | 工作负载详情 → 监控（按容器聚合） |
| Pod | Pod 详情 → 监控 |

「可观测性 → 监控」页提供 **PromQL 控制台**：表达式输入 + 即时/区间图（ECharts）+ 结果指标表 + 时间范围 `1h/6h/24h/7d`，内置快捷查询（集群 CPU / 集群内存 / 节点 CPU 使用率 / Pod 数量 / API 请求 QPS / 节点负载），用于临时验证指标与容量。

**控制平面组件**（可观测性独立页 `/monitor/controlplane`）：从集群总览迁出，做成**两级按需查询**——列表只查 `up` + 每组件 1 个概要指标（API Server / Controller Manager / Scheduler / kube-proxy / CoreDNS / etcd / Prometheus 就绪状态与健康色），点开抽屉才查该组件**全部指标卡 + 趋势图**（ECharts，`1h/6h/24h` 档位，step 由后端放宽，点数封顶）。指标更全：apiserver 加在途读写/Goroutine/RSS，etcd 加后端 commit P99/提案失败/实例数，coredns 加缓存命中率/P99 解析延迟等。**分级的目的**：总览每次刷新不再背着 ~18 个控制面 instant + 3 个 range 查询，未点开的组件零成本，对 Prometheus 压力反而低于旧的整块拉取。

![PromQL 监控查询](docs/images/05-monitor.png)
![命名空间监控](docs/images/07-namespace-monitor.png)

| 模块 | 功能 |
| --- | --- |
| 告警规则 | 基于 Prometheus `/api/v1/rules`：firing/pending/inactive、严重度统计、过滤、规则详情与活动实例；每条规则标注来源 PrometheusRule CR，支持**在线查看 / 编辑 / 新建 / 删除**（单条规则粒度写回 CR，prometheus-operator 数秒内热重载）；**规则检查**扫描缺 severity / namespace / summary / runbook 等配置缺陷 |
| Alertmanager | 接入集群 AM（apiserver proxy / 直连，未配置优雅降级）：**实时告警**（含 silenced/inhibited、30s 自动刷新）、**静默管理**（告警行一键静默 + 自定义匹配器/时长）、**主配置 YAML 在线编辑**（Secret 内 `alertmanager.yaml[.gz]` 自动识别，内置钉钉/邮件/企微/Slack receiver 模板片段；恢复通知 = `send_resolved: true`）、**告警历史归档**（服务端轮询 AM 按指纹状态机落库，触发/恢复/持续时长 + 统计卡片，默认保留 90 天） |
| 事件中心 | 实时：全集群 K8s 事件聚合（30s 自动刷新，页面隐藏自动暂停），按类型 / 对象 / 命名空间过滤；**归档检索**：服务端轮询 Events 增量写入 ES（`kc-events-*` 按天滚动索引，复用日志源连接，默认保留 30 天），任意历史时段回查 |
| 日志检索 | 对接 **Elasticsearch**（按集群配置日志源，支持连通性测试）：全文 + 字段查询、**来源筛选**（标准输出 / 文本文件 / JSON 采集，分别命中行日志索引与 JSON 索引）、Pod 分布 Top 侧栏点击即过滤、相对或绝对时间范围、**导出 CSV** |
| Grafana 面板 | 按集群配置地址后 iframe 内嵌（kiosk 模式，支持粘贴任意 dashboard 链接 + 常用面板收藏）；需 Grafana 开启 `allow_embedding` 与匿名只读 |
| 通知管理 | CI 流水线执行事件（成功/失败/待审批）推送到启用渠道；渠道支持**钉钉**（加签）与通用 webhook，可测连通；推送历史可查（K8s 告警通知走 Alertmanager） |

![告警中心](docs/images/06-alerts.png)
![日志检索](docs/images/08-logsearch.png)

### 数据库（KubeBlocks）

| 模块 | 功能 |
| --- | --- |
| 集群列表 | 按引擎分子菜单（MySQL / Redis / Kafka / MongoDB / Elasticsearch / PostgreSQL），每页只列该引擎、且**跟随顶栏命名空间选择**（列表可见性按当前用户在各 ns 的角色收口），显示拓扑 / 状态 / 各组件就绪数 |
| 创建向导 | 引擎与拓扑**由 CRD 元数据驱动**：`ClusterDefinition.spec.topologies` 给拓扑与组件，`ComponentDefinition` 给副本上下限、存储卷、serviceVersion、是否带 exporter；版本下拉即该引擎可用的模板列表，命名空间候选=顶栏作用域 ∩ 我有写权限的 ns。**分片型拓扑**（Redis cluster、MongoDB sharding）走 `ShardingDefinition`：向导按分片组出行、多一列「分片数」（上下限取自 `shardsLimit`，Redis 是 3-64、MongoDB 是 1-128），提交时下发 `spec.shardings[].template`。**调度设置**（同页可填，也可整个不填）：节点标签选择、**容忍污点**、节点亲和（必须满足 / 尽量满足）、同组实例**打散**（按主机名的实例间反亲和，可选"尽量/必须"）→ 下发集群级 `spec.schedulingPolicy`，对全部组件生效；专用 DB 节点带污点时必须在这里加容忍，否则 Pod 一直 Pending。取值在提交前逐项校验（枚举、Equal 必须有值、Exists 不许有值），并先 dry-run 一次确认该 addon 的 CRD 认这个字段——不认就直说，不会留下"配了但没生效"的集群。**无独立存储的组件不给存储**：模板自己在 podSpec 里带来 `data` 卷的组件（MongoDB sharding 的 `mongos` 是 emptyDir，声明在 `ComponentDefinition.spec.runtime.volumes`）在向导里存储格直接禁用并标"模板自带"，后端同样拒收——同名卷一起下发会让 KB 合成 InstanceSet 失败，集群永久停在 Creating 且一个 Pod 都没有（`spec.volumes` 里的 `data` 是"要求下发 PVC"的声明，两处不能混）。**Elasticsearch 单组件形态**（`single-node` / `mdit` 拓扑，只有一个组件）由服务端自动注入模板变量 `mode=single-node`（挂在 `spec.componentSpecs[].configs[].variables`，配置项名取自该模板自己的 `spec.configs[].name`）并把副本锁在 1：不注入时模板按 multi-node 渲染，而它只认 `ALL_CMP_REPLICA_LIST` 里的 `master:` 段（真机：多组件集群给 `dit:…;master:…`，单组件集群不给前缀），于是产出空的 `cluster.initial_master_nodes`，ES 启动即报 null-valued setting 退出、Pod 一直 CrashLoop；多组件形态（multi-node / m-dit）不插手，注入反而会把集群降级成单机 |
| 组件详情 | 组件表（模板 / 版本 / 就绪 / 状态 / 资源 / headless service），实例状态取 `InstanceSet.status.readyReplicas`；详情头部一行显示**调度摘要**（节点标签 / 容忍了哪些污点 / 亲和条数 / 是否打散），建完能当场确认设置落上了 |
| Pod | 列出该集群全部实例 Pod（组件、角色 primary/secondary、容器就绪与重启数、节点、IP），点容器名即进 WebTerminal |
| 查询终端 | MySQL / Redis / PostgreSQL / MongoDB 一键进镜像自带 CLI（`mysql` / `redis-cli` / `psql` / `mongosh`），登录参数由**容器内的环境变量**展开，明文密码不经过控制台；默认连主节点 Pod |
| Kafka 管理 | topic 列表（分区/ISR/leader/配置，展开看每个分区）、创建、改配置（`kafka-configs.sh --alter`）、删除、消费组与 Lag、有界消息预览（≤200 条 / 8s 超时）；均在 broker 容器内执行官方脚本 |
| Elasticsearch 管理 | 集群健康、`_cat/indices` 索引表（文档数/存储/段）、点行看索引定义（分片、别名、字段映射）、删除索引、DSL 查询控制台（返回条数服务端收口 200）；在 es 容器内 `curl 127.0.0.1:9200`，认证用容器内 `$ELASTIC_PASSWORD` |
| 运维 | 一键下发 **OpsRequest**：重启 / 停止 / 启动 / 水平扩缩容（按当前副本算 `scaleOut/scaleIn.replicaChanges`）/ 垂直改规格 / 改配置（`Reconfiguring`）/ 卷扩容 / 切换 / 暴露，工单列表展示 phase 与进度、`status.conditions` 时间线与变更前旧值，进行中的工单可取消。**提交后回读工单状态再回执**，且扩缩容目标与当前一致时直接拒发——否则 apiserver 会收下这个"无变化"工单，现象就是"点了没反应" |
| 版本升级 | 候选版本来自 `ComponentVersion.compatibilityRules` 升级链（精确名与 `-` 前缀两种写法都认，取每条规则自己的 releases 名单，降序含当前版本），链外版本直接拒；Upgrade 工单按组件下发 |
| 连接信息 | 组件 Service（排除 headless 与发布期临时 Service）、内网 DNS、NodePort、账号用户名与密码所在 Secret（明文一律不回显） |
| 备份 | 备份策略与计划（各组件可用方法、cron、保留份数，可在线改）、手动备份、备份记录（各 action 明细 / 卷快照数 / 失败原因）、**恢复到新集群**（复制源集群 spec，按 1.0 的 `kubeblocks.io/restore-from-backup` 注解协议声明来源）、**用备份重建实例**（原地或新建）。仓库取 **BackupRepo**（S3 / OSS 等，管理员先建），未配或不可用时在页顶明确说明，而不是让工单静默失败 |
| 配置 | 两种模式：**参数表单**（按 1.0 的 `parameters.kubeblocks.io` 契约渲染，表格高度按视口撑满、支持按参数名/说明过滤与「只看已改」——MySQL 一个 my.cnf 就几百个键——ParamConfigRenderer → ParametersDefinition 的 JSON Schema，逐项标注动态/静态/不可变与生效方式，当前值取 `ComponentParameter`；只提交改过的项，动态参数热载、静态参数由 operator 滚动重启）与**原始文件**（列出集群渲染出的配置 ConfigMap，排除 scripts/env 类，在线编辑保存）。没有 ParametersDefinition 的组件表单页给出说明并降级到原始文件 |
| 账号 | 列出 `<集群>-<组件>-account-*` Secret 的**键名**（绝不回显明文） |
| 事件 | 该集群相关对象（Pod / InstanceSet / PVC 等，名字以 `<集群>-` 开头）的 Warning 事件，倒序最多 50 条 |
| 监控 | 指标以**曲线图**呈现（复用与控制台监控页同一套 `MetricChart`）：可切时间窗（1h/6h/24h/7d）、按分组过滤（引擎指标 / 资源）、默认隐藏无数据项；每张小图带标题、当前值、图例（只标能区分序列的标签，如各 Pod）与 PromQL 悬浮说明，单图最多画 8 条序列并标注总数。「一键接入 exporter 抓取」按钮也在本页签，**建 ServiceMonitor 前会先读该集群 Prometheus CR 的 `spec.serviceMonitorSelector` 并把 matchLabels 贴到对象上**——prometheus-operator 只加载 selector 匹配得上的对象，标签不对就是"创建成功但永远没数据、还不报错"（kube-prometheus-stack 默认要求 `release: <release 名>`）；再点一次会把历史上漏贴的既有对象一并补齐，回执里写清贴了什么、修了几条；若该 Prometheus 用 `matchExpressions` 挑选（贴标签满足不了），直接给告警而不是假报成功。指标源：mysql 17 / pg 12 / ES 22 / redis 16 / kafka 8 / mongo 6 项引擎指标（mysql、pg、es 的名字在本集群 Prometheus 逐个核对过），外加各引擎共用的容器资源组（cAdvisor CPU/内存/网络/磁盘写 + kube-state 重启与未就绪），未接 exporter 时只有资源曲线并明确说明原因 |

| 备份仓库 | 数据库分组下独立一页（`/kb/repos`，BackupRepo 是**集群级**资源）。表单按 `StorageProvider.parametersSchema` 现取现渲染：provider 下拉（带 phase 与未就绪原因）、访问方式 Tool/Mount、参数（bucket/region/endpoint/insecure…）、凭证只**引用**某个含 `accessKeyId`/`secretAccessKey` 的 Secret（下拉只列键齐的，控制台不读取也不回显内容）、路径前缀、PV 回收策略。**S3 走 `s3` / `s3-compatible` provider**；本集群这套 addon 的 S3 类 provider 因未装 `ru.yandex.s3.csi` 而 NotReady，此时 Mount 选项按住不可选（服务端同样会拒），选 Tool 即可用（datasafed 直传，不依赖 CSI） |

权限沿用三层角色：**读**=该 ns 有任意角色（`view`+），**运维与改配置**=该 ns 可写（`edit`/`admin`）；备份仓库这类集群级资源按**集群层写权限**收口。
破坏性动作（删集群、删备份、删索引、删 topic、删仓库）一律走**输入名称确认**，不是点一下"确定"就能删。
某引擎的 `ClusterDefinition` 未安装时，该子菜单给出"未装 addon"提示而不是空白页。
addon 的 `topologies[].components[].compDef` 往往写成正则（如 `^mysql-\d+\.\d+.*$`），而 `Cluster.spec.componentSpecs[].componentDef` 必须是具体模板名——目录与创建都会先按所选版本解析，避免 apiserver 直接拒绝。
不走拓扑的"独立组件"建法（`spec` 里只有 `componentSpecs`、没有 `clusterDef`）也照样列出：引擎按组件模板的 `app.kubernetes.io/name` 标签反推，拓扑列显示"独立组件"。

### CI / CD 与交付

| 模块 | 功能 |
| --- | --- |
| CI 流水线（内置 Tekton） | 原生模块，**集群隔离**（接口经 `X-Cluster` 选集群，Tekton 客户端按集群缓存）+ **命名空间隔离**（**项目 1:1 绑定一个已存在的 K8s ns**，Pipeline/Run/工作区 PVC/凭据 Secret 全落该 ns）：**执行中心**（统计卡 + WS 状态推送 + 5s 轮询兜底 + SSE 实时日志 + DAG 视图 + 人工审批 + 重跑 / 停止）、**流水线**（CRUD / 复制到项目 / 按分支或 Tag 运行）、**项目**、**凭据**（basic / token / dockerconfig / kubeconfig / aksk / raw 六类，Secret 只落平台 ns + 归属项目 ns，轮换时留空保持不变）、**制品**（build-image / upload-artifact 成功自动登记，血缘详情 + MinIO/Nexus 流式下载代理）、**发布记录**（k8s-deploy / helm-deploy 镜像快照 + **一键回滚**）、**全局变量**（`${global.KEY}` 编译期注入）；未装 Tekton 的集群优雅降级；SSE/WS 用 60s 短期流 token 鉴权 |
| CI 权限（按命名空间） | CI 不再有独立成员表，全部复用平台三层角色，按**资源所属项目的命名空间**判定：**浏览**项目/流水线/运行/日志/制品 = 该 ns 有任意角色（`view` 及以上）；**运行 / 重跑 / 停止 / 审批** = 同上（`view` 也能跑）；**建改删流水线、项目凭据、定时与 Webhook、回滚、编辑/删除项目** = 该 ns 可写（`edit`/`admin`）；**平台级凭据与全局变量** = 平台管理员。无权限的 ns 其项目在列表与下拉中直接不出现；Webhook token 只对能写该 ns 的人明文展示；跨项目引用凭据在编译期被拒 |
| 镜像构建器（CI 页新 tab） | **不写 Dockerfile、不配 Git 仓库**的出镜像方式：上传已在本地编译好的产物（Java fat jar / Go 二进制 / 前端静态包 tar.gz 或 zip——zip 由平台归一成 tar.gz，防 zip-slip 与解压炸弹），平台按应用类型生成约定 Dockerfile（jar→temurin-jre `java -jar`+JAVA_OPTS、二进制→alpine、静态包→nginx 站点），经**托管流水线**执行：任务 Pod 凭随机令牌回平台拉取制品（artifact-fetch 节点，sha256 校验）→ buildkitd 构建推送（build-image，SBOM 照常）。构建器 = 可复用的配置表单（目标仓库/推送凭证/基础镜像/端口/启动参数/默认 Tag——启动参数生成镜像 CMD，追加在 ENTRYPOINT 后、exec 数组不经 shell），每次上传即生成新托管版本、每次构建产生一条普通流水线运行——**执行历史 / SSE 日志 / 制品登记 / 排队串行**全部复用既有链路，构建记录跳「执行中心」直接看。配置需 `ci.serverUrl`（任务 Pod 集群内回调平台地址）；上传大小受 `ci.builderUploadMaxMB`（默认 512）限制；约定基础镜像见 docs/images.md |
| 可视化设计器 | vue-flow 画布 + **20 类节点插件**（随二进制 go:embed 分发）：git-clone、artifact-fetch、go-build、maven-build、gradle-build、npm-build、python-build、build-image、push-registry、image-verify、harbor-scan、trivy-scan、sonar-scan、k8s-deploy、helm-deploy、gitops-bump、argocd-sync、upload-artifact、approval、condition；属性面板 schema 驱动 + Git 分支/Tag/K8s 目标/凭据动态下拉、**节点开关**（关闭即跳过：编译不出该任务、前后依赖自动桥接，画布置灰标"已跳过"；下游引用其结果时编译/校验给出"已关闭"指明错误）、Dagre 自动布局、**JSON 导入导出 / 复制流水线 / 跨项目复制**、未保存离开拦截、**定时任务与 Webhook 管理**（Designer 设置内） |
| 节点间数据传递 | 节点参数可引用上游结果 `$(tasks.<任务名>.results.<结果名>)`（如 gitops-bump 的 images 填 `$(tasks.build-image-xxx.results.imageRef)`）；编译期校验连线依赖与结果声明，引用落在参数默认值时由编译器提升为 PipelineTask 级参数（Tekton 不替换内嵌 taskSpec 的默认值） |
| 构建依赖缓存 | go / maven / gradle / python / **npm(yarn/pnpm)** 五类构建节点各有「使用依赖缓存」开关：勾了就把工具缓存目录指到 **运行工作区同一块盘**里的 `kc-cache/` 子目录（`GOMODCACHE`/`GOCACHE`、`maven.repo.local`、`GRADLE_USER_HOME`、`PIP_CACHE_DIR`、`npm_config_cache`+`YARN_CACHE_FOLDER`+`pnpm store-dir`），依赖下载跨运行复用。同盘是因为独立第二块 RWO 盘会让 TaskRun 找不到能同时挂载两块盘的节点（Pod 永久 Pending，原因只在事件里）。平台开关关着时**照常发布**（缓存只是加速手段，不值得为它停一次发布），但执行详情会写明「未启用：<原因>」——原来这句话只在服务日志里。运维入口在项目行的「缓存」：承载盘状态/容量、哪些流水线在用、**清空依赖缓存**（删盘重建空盘，输入项目名确认；有执行在跑时拒绝）。缓存只覆盖下载与构建缓存，不复用 node_modules/target 这类安装结果。 |
| 执行工作区与排队 | workspace PVC **按项目一块**（`ci-ws-p<项目>`，幂等 ensure、长期复用），**盘内按流水线分工作子目录**（`pl-<流水线>`，首次运行自动创建）：**同项目的不同流水线可并行执行**，互不踩工作目录；**同一条流水线的执行串行**——后到的触发落 `queued`（只登记不建 Tekton 对象），由后台轮询按入队顺序逐个放行，排队中也能"停止"（= 出队，不需要集群可达）。盘只在**项目删除**时回收；`git-clone` 每次先清空自己流水线的工作目录（含 `.git`）保证干净起点（改为子目录之前的盘根残留需手动清理）。集群不可达时排队记录保持 `queued` 重试，不会被误判失败 |
| 触发方式 | 界面运行、**cron 定时**（瞬时失败快速重试）、**GitLab Webhook**（push/tag/MR，token 走 URL，事件去重） |
| ArgoCD 应用 | dynamic client 读 `applications.argoproj.io` / `appprojects.argoproj.io`。**贴近原生**：状态筛选 chips（Synced/OutOfSync/Healthy/Degraded/Progressing/Unknown/已暂停 + 计数）+ Project 下拉 + 名称搜索；动作组 **SYNC**（CR 内嵌 operation，老版本 CRD 不支持时明确提示）/ **PAUSE–RESUME** / 软·硬刷新 / 开关自动同步（prune+selfHeal）/ **删除**（级联勾选 = 自动补 `resources-finalizer`，需输入名称确认）；**详情抽屉**：托管资源层级树（按 ownerRef 组装，健康色点 / OutOfSync / 待剪枝 / 集群缺失标记，常见 kind 一键跳列表）、operationState、Conditions、概览、同步历史；GitOps 动作按目标命名空间写权限放行；spec 非法时列表顶部直接展示原因（InvalidSpecError）；可视化编辑（表单 + YAML，**Project 下拉只列已存在 AppProject** 并校验仓库与目标 ns 白名单） |
| ArgoCD 仓库 | v3 以带标签 Secret 存储（`argocd.argoproj.io/secret-type=repository`）：增删改查（账号密码 / SSH / insecure / LFS）、**主机级 URL 作为凭据模板**（同域仓库自动继承）、凭据只返回有无标志 |
| Helm | **Releases**：跨命名空间列表、详情（manifest / values / notes / history）、**安装**（仓库 → chart → 版本 → values + wait / timeout / atomic）、**升级 / 回滚到修订 / 卸载**；**Chart 仓库**：源管理（增删改 / 私有认证）、index 刷新（内存缓存 TTL）、chart 多版本浏览 |

![CI 流水线](docs/images/09-ci.png)

### Nacos 微服务集成

OpenAPI 客户端**自动探测版本风格**：1.x/2.x 走 v1 API，3.x 走 `/v3/admin` + `/v3/auth`（3.x 移除了 v1 console/admin API），同一地址无需区分配置。

| 模块 | 功能 |
| --- | --- |
| 服务发现（登录即可用） | 跟随顶栏命名空间选择、多命名空间合并展示：服务列表含分组 / 集群数 / 实例数 / 健康比，实例抽屉含 IP:端口 / 健康 / 权重 / 集群 / 元数据，可删除服务注册记录 |
| 配置管理（登录即可用） | 命名空间联动，配置查看 / 编辑 / 发布 / 删除，多命名空间合并展示；**JSON 导出 / 导入**（按原命名空间或统一导入到指定 ns，适合跨环境迁移） |
| Nacos 管理（admin） | 按集群接入：**命名空间自动同步**（每个 K8s ns 自动建同名 Nacos ns + `ROLE_<ns>` rw 授权（resource 是 `ns#*#*` 三段式，裸 ns 在鉴权时永不命中），凭据落库可查看/重置，默认 5 分钟一轮、ns 创建即时联动）；**Pod 级自动注入**（见下）；Nacos 侧命名空间 / 用户浏览 |

**Pod 自动注入到底注入了什么**：往新建 Pod 的每个 `spec.containers[*].envFrom` 追加两个引用（幂等，已有同名引用就跳过；不动 initContainers，也不改已存在的 Pod）——

| 来源 | 键 | 值 |
| --- | --- | --- |
| ConfigMap `nacos-config`（同步器在每个 ns 维护） | `NACOS_SERVER_ADDR` | 接入配置里的地址，如 `http://nacos.mid-platform:8848` |
| | `NACOS_NAMESPACE` | K8s 命名空间名（与 Nacos ns 1:1） |
| Secret `nacos-credentials` | `NACOS_USERNAME` / `NACOS_PASSWORD` | 该 ns 的同名账号与平台生成/沿用的密码 |

应用侧直接引用即可（`spring.cloud.nacos.server-addr=${NACOS_SERVER_ADDR}`、`namespace=${NACOS_NAMESPACE}`、`username/password` 同理）；`dataId`/`group` 仍由应用自己给。生效前提，四条缺一就"看起来配好了但没注入"（Webhook 的 `failurePolicy=Ignore`，不会报错）：① 该 ns 带标签 `nacos-injection=enabled`（`autoLabel` 默认在同步时自动打，`skipNamespaces` 里的 ns 除外——**默认跳过 `kube-system,kube-public,kube-node-lease,kube-console`**）；② 该 ns 的映射已同步成功（`status=synced`，Webhook 会二次校验）；③ apiserver 连得到控制台：`service` 模式填控制台 Service 的 ns/名/端口（默认 9443，Service 与容器都要真的暴露它），`url` 模式必须是 `https://`；**自签证书的 SAN 按各集群的接入配置动态生成**（控制台不在 `kube-console` 命名空间时，写死域名的旧做法会 TLS 校验失败 → 静默不注入），配置改了下一轮同步自动重签并把新 caBundle 写回 Webhook 配置；④ 证书目录 `nacos.certDir` 要持久化，否则每次重启换一张 CA，重启到首轮同步之间新建的 Pod 不被注入（`deploy/02-deployment` 已改用 PVC）。

### 服务与网络 / 配置 / 存储

| 模块 | 功能 |
| --- | --- |
| 服务与网络 | 三级菜单：**服务与路由**（服务 / EndpointSlice / 路由 / Ingress 类）、**网关**（GatewayClass / Gateway / HTTP·TCP·TLS·UDP·GRPC Route / ReferenceGrant，discovery 自动探测集群提供的版本，未装优雅降级）、**网络策略**；均支持可视化编辑 |
| 服务地址 | 服务列表「服务地址」列：**LoadBalancer 优先外部 IP、ExternalName 用目标名、其余用集群内 DNS**；多端口 Service 每端口一条（同端口 UDP/TCP 去重），逐条一键复制 |
| 配置中心 | ConfigMap / Secret / ServiceAccount 列表、编辑、YAML 视图 |
| 存储 | PVC / PV / StorageClass |
| 安全 (RBAC) | Role / RoleBinding / ClusterRole / ClusterRoleBinding（规则编辑器） |

### 导入导出

| 对象 | 功能 |
| --- | --- |
| K8s 资源 YAML | 通用资源列表页 **导出**（Kuboard 式逐层选择：命名空间 → 控制器 / 服务与路由 / 配置 / 其他资源四类勾选，类与类型级全选；多文档 `---` 分隔；深度清洗 uid/resourceVersion/managedFields/status/last-applied 注解，可直接再导入或入 Git；单项失败跳过并提示）；**导入**（上传 .yaml/.yml → 多文档预览（Kind/名称/命名空间）→ create-or-update 逐个应用，逐条成败反馈，失败不中断整批） |
| Nacos 配置 | 配置管理页 **导出**（所选命名空间全部配置含完整内容，JSON 文件，记录来源集群与时间）；**导入**（上传 JSON → 预览 → 按原命名空间或统一导入 → 逐条发布） |
| 报表与日志 | 用量报表、日志检索结果 **导出 CSV** |

### 镜像仓库（Harbor + Trivy）

| 模块 | 功能 |
| --- | --- |
| 接入与浏览 | Harbor API 接入（Basic 认证、可选跳过 TLS 校验、连通性测试）；**三步浏览**：项目 → 镜像仓库 → Tag（制品） |
| 漏洞扫描 | 对任意 Tag **触发扫描**（Harbor 内置 Trivy）并轮询状态；扫描概览（状态 / 最高级别 / C·H·M·L 计数） |
| CVE 报告 | 漏洞明细表格（级别筛选、CVE 链接、受影响包、安装 → 修复版本、**CVSS 分数着色**、修复状态）；双格式解析（Harbor 3.x 1.1 报告 + 2.x 旧格式） |
| Pod 维度 CVE | Pod 详情容器表「镜像 CVE」徽标（`CVE C·H·M`，点击弹明细）；镜像无仓库前缀或不在已配置仓库时优雅降级（灰标签 + 原因提示，不报错） |

![镜像仓库](docs/images/10-registry.png)

### 权限管理（KubeSphere 式层级 RBAC）

借鉴 KubeSphere 的多租户 RBAC 模型：以「角色」为权限基础，按**层级**把角色绑定给用户 / 用户组，前端据登录用户的角色快照动态渲染菜单与写操作按钮（真正的判定在后端逐接口强制）。

| 层级 | 定位 | 内置角色 | 说明 |
| --- | --- | --- | --- |
| 平台 (platform) | 控制台自身管理权限 | `platform-admin` / `platform-viewer` | admin=用户/授权/角色/集群注册/平台设置全量；viewer=授权、角色、审计、用量报表、集群管理**只读** |
| 集群 (cluster) | 特定集群的集群级操作 | `cluster-admin` / `cluster-viewer` | admin=节点运维、命名空间增删、告警静默、节点终端，且可写全 ns；viewer=只读 |
| 项目 (project) | 命名空间内资源读写 | `view` / `edit` / `admin` | 与 K8s 内置 ClusterRole 对齐（view 只读、edit 可改业务资源与 Pod 终端/文件、admin 含角色/配额） |

* **三层而非四层**：不提供「企业空间 (Workspace)」隔离单元（当前系统无该概念）；如后续引入，在 `service.BuiltinRoles` 扩一层并在授权向导加一步选择即可。
* **角色 = 内置 + 自定义**：内置角色启动时 seed、不可改删；平台管理员可在「角色管理」按 **API 授权项**（资源组 × 读/写/全部）勾选创建自定义角色，实现最小权限。
* **绑定以控制台记录为准**：授权记录落库（判定主数据源），并**同事务物化**为 K8s RBAC（`kc-rbac-<id>` 的 RoleBinding/ClusterRoleBinding，自定义角色生成 `kc-role-<name>` ClusterRole），kubectl 侧同样生效。
* **兼容旧数据**：旧「授权」页生成的 `kc-grant-*` 绑定继续被识别（按项目层授权展示），不做破坏性迁移；运维手工创建的 K8s RBAC 也仍参与判定（并集）。
* **敏感能力按层级收口**：Pod 终端 / 容器文件浏览需**项目层写权限**；节点终端（宿主机 root shell）需**集群层写权限**；集群管理页与 `/clusters` 完整信息仅**平台角色**可见；**CI 全模块按项目绑定的命名空间判定**（读/运行 = 该 ns 有任意角色，建改删 = 该 ns 可写，平台级凭据与全局变量 = 平台管理员）。
* **用户与组**：先建用户再按层级绑定角色；未绑定任何角色的普通用户无写权限（读取侧默认仍"登录即可浏览"，要收紧见下面开关）。用户组作为绑定主体时组内成员继承权限。
* **审计**：认证后的非 GET 写操作自动落库（操作人 / 方法 / 路径 / 结果 / 时间），支持过滤分页与按保留期自动清理。

![授权管理](docs/images/11-authz.png)

#### 菜单按能力显隐与接口收口

菜单层级不是一个独立维护的权限维度，而是**「这个用户在当前集群能读哪些资源」的结论**：`service.Access` 的 `readRes`（生效角色规则里可读的 `apiGroup/resource` 集合）经 `service.menu.go` 的能力表推导出可见菜单键，随 `/rbac/my-permissions` 的 `menus` 字段发给前端。

| 菜单 | 需要什么能力 | 效果举例 |
| --- | --- | --- |
| 集群总览 / 事件 / CI / Helm / 数据库 / 可观测性 | 该集群**任一命名空间有角色**（含只读 view），或平台角色 | 没授权的人看不到集群数据入口；view 用户能看 |
| 各资源页（工作负载 / 服务 / 配置 / 存储 / RBAC / 配额） | **该资源本身可读**（按 URL 里的资源名现推，KindMap 是单一事实来源） | view 能读 configmaps → 给；view 读不到 secrets → 「保密字典」不出现 |
| 节点 / 自定义资源(CRD) / PV / StorageClass | 集群级资源读权限 | 只有项目角色的用户看不到 |
| 命名空间 | `namespaces` 可读，**或**该集群有任意角色 | 上游 `admin` 反而不含 namespaces 读权限，而这是导航入口，所以叠了兜底 |
| 授权 / 角色 / 审计 / 用量报表 / 集群管理 | 平台角色（admin 或 viewer） | viewer 只读可见 |
| 用户 / 通知 / 镜像仓库 / Nacos 管理 / 备份概览 | 仅 platform-admin | viewer 看不到 |
| 微服务（Nacos）/ Chart 仓库源 | 登录即可（没有对应的 K8s 资源） | 口径不变 |

* **一份真相两处用**：侧边栏显隐、路由守卫（直接敲 URL 也挡）、以及**接口收口**都取自这张能力表，避免"菜单藏起来了但接口还敞开"或"入口给得堂堂正正、点进去 403"。
* **开关 `rbac.enforceMenus`（默认关）**：关掉时只藏菜单，接口维持既有口径（写权限一直逐接口判）；打开后 `middleware.MenuGuard` 对能力不匹配的请求直接 403，消息里说明该授什么角色。灰度建议：先在「授权」页把该给的 view/edit 补齐 → 打开开关 → 观察有没有人被打回。
* **能力来源包含手工建的 K8s RBAC**：DB 授权记录与集群侧 CRB/RB 取**并集**后再算菜单，所以只在 kubectl 里授过权的用户不会因为"没落库"而看不见页面。
* **前端兜底**：快照里没有 `menus`（老后端）时按"全部可见"处理，不会把侧边栏清空。

### 平台管理

| 模块 | 功能 |
| --- | --- |
| 用户管理 | 用户 CRUD、重置密码、角色（admin / user）、**用户组**（组可直接作为 K8s RoleBinding 的 Group 主体，按组批量授权） |
| 授权管理 | 三层授权向导：选层级（平台 / 集群 / 项目）→ 选主体（用户 / 组）→ 选角色（内置或自定义）→ 选范围；集群/项目层同步物化 K8s RBAC；列表合并展示旧版授权（可回收）；**权限查询**：控制台角色 + K8s 生效 RBAC 双视角 |
| 角色管理 | 内置角色只读展示；自定义角色按授权项目录勾选生成 |
| 审计日志 | 写操作审计，过滤与分页，按保留期自动清理 |
| 日志源 | 按集群配置 Elasticsearch：连接信息（ns/svc/port 或直连地址、basic auth）、**行日志索引前缀**（标准输出 + 单行文本采集，支持 `{namespace}` 占位符）、**JSON 采集索引前缀**（默认 `logstash-`）、事件归档开关与前缀 |
| 通知 / 镜像仓库 / Nacos / 备份 | 分别见对应章节；备份概览汇聚 **Velero** 备份任务 / 计划 / 恢复（进度条、快照数、耗时、失败信息与关联事件、计划下次执行时间；**平台管理员可视化新建备份、新建计划、发起恢复、启停计划、删备份**，未安装 / 未配置存储位置时分别提示引导） |
| 用量报表 | 基于 Prometheus 历史指标（container_cpu/memory + kube-state-metrics）的命名空间用量区间报表（平均/峰值），**导出 CSV** |

## 技术栈

- **后端**：Go 1.25 / Gin / k8s.io/client-go v0.34（typed + dynamic + discovery）/ GORM / JWT + 个人 API Token / Helm v3 SDK / gorilla-websocket + SSE
- **前端**：Vue 3 + TypeScript + Vite + Element Plus + Pinia + CodeMirror 6 + js-yaml（表单 ↔ YAML 互转）+ ECharts 6 + xterm + vue-flow
- **存储**：SQLite（开发，纯 Go 驱动）/ MySQL / PostgreSQL（GORM 多驱动，config.yaml 切换）
- **CI 引擎**：Tekton（PipelineRun 由控制台编译下发），制品存储 Nexus / MinIO
- **外部集成**：Prometheus · Alertmanager · Grafana · Elasticsearch · Harbor(+Trivy) · ArgoCD · Velero · Nacos

## 架构

```
┌──────────────────────┐        ┌──────────────────────────────┐        ┌──────────────────┐
│  Vue 3 + Element Plus │  HTTP │      Go + Gin + client-go     │  HTTP   │   Kubernetes     │
│  可视化表单 + YAML 双视图│ ─────▶ │  集群注册表 + 连接缓存 + 熔断    │ ───────▶ │  kube-apiserver  │
│  ECharts + xterm      │        │  discovery 资源注册表          │         │  （含 CRD/Gateway/ │
│  Pinia 全局状态        │        │  Helm SDK + ws exec + 审计    │         │   ArgoCD）       │
└──────────────────────┘        └──────────┬───────────────────┘         └──────────────────┘
                                           │ GORM
                                           ▼
                              ┌──────────────────────────────┐
                              │ SQLite / MySQL / PostgreSQL   │
                              │ （用户/集群/HelmRepo/审计/渠道） │
                              └──────────────────────────────┘
  外部依赖（按集群或全局配置）：Prometheus · Elasticsearch · Tekton · argocd · Harbor · Nexus/MinIO · Velero
  监控/日志/告警一律访问集群内服务，无需对外暴露端口
```

## 快速开始

### 开发

```bash
# 后端（:8080，SQLite 默认 ./data/kube-console.db）
cd server && go run ./cmd/server

# 前端（:5173，vite 代理 /api → :8080）
cd web && npm install && npm run dev
```

首次登录：`admin / admin123`（生产请改 `configs/config.yaml` 并登录后改密）。

测试与构建：`cd server && go test ./...`、`cd web && npm test && npm run build`。

### 配置（configs/config.yaml）

所有字段均可用 `KC_` 前缀环境变量覆盖（如 `KC_DB_DSN`、`KC_JWT_SECRET`、`KC_ADMIN_PASSWORD`、`KC_CI_IMAGE_REGISTRY`）。

```yaml
server:   { port: 8080, logLevel: info }
database:
  driver: sqlite          # sqlite | mysql | postgres
  dsn: ./data/kube-console.db
jwt:    { secret: change-me-to-a-long-random-string, expireIn: 24 }   # 小时
admin:  { username: admin, password: admin123 }      # 首个管理员（启动 seed）
k8s:    { debugImage: busybox:1.36 }                 # 无 shell 容器注入的调试镜像

# 内置 CI（不接 Tekton 可整段省略，页面自动降级）
ci:
  enabled: true
  runTimeout: 2h
  pvcSize: 10Gi
  pvcStorageClass: ""            # 空 = 集群默认 StorageClass
  cacheEnabled: true            # 依赖缓存开关（缓存与运行工作区同盘，容量就是上面的 pvcSize）
  serviceAccount: ci-bot
  imagePullSecret: harbor
  platformNamespace: ci-platform
  imageRegistry: harbor.example.com/library   # 一处切换全部 CI 节点镜像仓库
  serverUrl: http://kube-console-server.kube-console:8080  # 镜像构建器必需：任务 Pod 回平台下载制品的集群内后端 Service 地址（非浏览器地址）
  builderUploadDir: ./data/ci-artifacts       # 构建器上传制品存放目录
  builderUploadMaxMB: 512                     # 单个制品大小上限
  buildkitAddr: ""
  nexusUrl: ""                   # 制品下载代理（或 minioEndpoint）
  minioEndpoint: ""
  minioBucket: ""

# 归档轮询与保留
obs:
  alertIntervalSec: 60
  alertRetentionDays: 90
  eventIntervalSec: 60
  eventRetentionDays: 30

# Nacos 同步与 Pod 注入 Webhook
nacos:
  syncIntervalSec: 300
  webhookPort: 9443
  skipNamespaces: ""
  certDir: ./data

# 权限模型行为开关
rbac:
  enforceMenus: false       # 接口按菜单能力收口（env: KC_RBAC_ENFORCE_MENUS=1 打开）
```

集群级外部依赖（Prometheus / Grafana / ES / Alertmanager / Harbor / Nacos / Helm 仓库）不写在配置文件里，登录后在界面上按集群配置，落库保存。

**用 MySQL 做元数据库**：`database.driver: mysql` + `dsn: user:pw@tcp(host:3306)/kube_console?charset=utf8mb4&parseTime=True&loc=Local`（库需先建好，字符集 `utf8mb4`）。DSN 里漏写 `charset` / `parseTime` 时启动会自动补齐——缺前者中文按 latin1 存取会乱码，缺后者 `DATETIME` 列扫描直接报错。兼容性可用一条命令自证（不设置该变量时测试自动跳过）：

```bash
cd server && KC_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3306)/kube_console_test' go test ./cmd/server -run MySQL
```

它会跑全量建表 + 二次迁移幂等 + 保留字列（`ci_global_vars.key` 在 MySQL 是保留字）排序读取 + 中文往返 + 唯一冲突识别。

### 部署

前后端分开部署（前端 `web/dist` 静态文件 + 后端 Go 单二进制，经 Nginx 同源反代 `/api`）的完整步骤、Nginx 配置（WS/SSE 关键项）、systemd / Docker / K8s 三种形态与验证清单见 **[docs/deployment.md](docs/deployment.md)**。K8s manifest 在 `deploy/`（后端 Deployment + 前端 Deployment + Gateway API 路由，PostgreSQL）。

## 使用指引（常用路径）

1. **接入集群**：集群 → 集群管理 → 添加（粘贴 kubeconfig，自动校验连通性）→ 顶栏切换；顺手配 Prometheus / Grafana 地址解锁监控
2. **看应用**：工作负载 → Deployment 列表 → 点名称进详情（左树右面板）→ 进 Pod → 日志 / 终端 / 文件 / CVE
3. **发版**：CI / CD → CI 流水线 → 「设计器」可视化编排 → 运行（选分支/Tag）；或 Helm → Releases → 安装/升级；或 ArgoCD → 应用 → 新建（Git 目录 / Helm Chart，Project 必须是集群已存在的 AppProject；私有仓库先在 ArgoCD → 仓库 注册凭据）
4. **排障**：可观测性 → 监控（总览 → 命名空间 / 节点 / 工作负载 / Pod 五级下钻）/ 告警 / 事件中心 / 日志检索（「来源」区分标准输出 / 文本文件 / JSON 采集）；应用写文件日志的容器 → 工作负载详情「日志采集」开启 Sidecar；节点问题 → 节点详情 → 终端 / 排水 / 封锁
5. **数据库**：数据库 → 引擎子菜单 → 新建向导（引擎 / 拓扑 / 组件模板 / 分片数全部由 CRD 元数据驱动）→ 详情抽屉按页签看 Pod、进查询终端、监控曲线、Kafka topic、ES 索引、运维工单与备份恢复；没接 exporter 时监控页签有「一键接入抓取」
6. **授权**：平台管理 → 用户管理建用户/组 → 授权管理按平台·集群·项目三层绑角色 → 角色管理按需自定义最小权限；敏感操作（终端、排水、平台设置）按层级收口
7. **迁移与盘点**：各列表页「导出 / 导入」批量搬 YAML；平台管理 → 用量报表（导出 CSV）；镜像仓库触发扫描看 CVE 报告

## 文档

- [docs/api.md](docs/api.md) — 后端 API 参考（含各端点权限与入参）
- [docs/deployment.md](docs/deployment.md) — 前后端分开部署说明（Nginx 同源反代 / Docker / K8s）
- [docs/images.md](docs/images.md) — 镜像依赖清单与全局变量（`ci.imageRegistry` 一处切换全部 CI 节点镜像仓库）
- [docs/images/](docs/images/) — 本文件引用的功能截图
