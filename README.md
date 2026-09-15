# Kube Console — Kubernetes 管理平台

参考 [Kuboard](https://www.kuboard.cn/) 设计的一体化 Kubernetes 管理平台（云厂商控制台风格，支持浅色/深色主题），覆盖：多集群管理（kubeconfig 注册制）、全资源浏览与编辑、工作负载可视化编辑（表单 + YAML 双视图）、Pod 生命周期运维、五级监控与告警、Helm 应用管理、内置 Tekton CI/CD 流水线与 ArgoCD 交付视图、镜像仓库漏洞管理（Harbor + Trivy）、Nacos 微服务集成（服务发现 / 配置管理 / Pod 注入）、资源与配置导入导出、通知与审计。

## 界面与交互

- **主题**：KubeSphere 4.2 风格——品牌绿主色（`#00AA55`）+ 深色侧边栏（菜单项全量图标、活动项左侧绿色指示条）+ 浅灰内容区，支持深色模式（用户菜单切换）；主题色集中定义于 `web/src/styles/theme.css`
- **全局搜索**：顶栏搜索框，输入即搜（Pod / 工作负载 / Service / ConfigMap / 命名空间 / 节点），结果按类型分组，点击直达详情
- **全局命名空间选择**：顶栏命名空间多选（含「全选」），所有命名空间相关页面联动跟随；筛选状态与 URL 同步（刷新/分享可复现）
- **全局集群切换**：顶栏下拉切换已注册集群（`X-Cluster` 请求头），资源数据不缓存、每次直连 kube-apiserver
- **工作负载矩阵**：命名空间 × 工作负载类型二维矩阵（ready/total 着色），点击格子直达对应列表
- **左树右面板**：工作负载 / Pod 详情页左侧资源树（工作负载 → Pod → 容器，状态色点），右侧面板联动
- **行点击进详情**：所有资源列表支持行点击直达详情（操作列按钮不受影响）；Service 列表例外——点击名称展示 **selector 匹配的后端 Pod 列表**（状态/就绪/重启/IP/节点），YAML 详情走操作列
- **操作审计**：认证后的写操作自动落库审计（平台管理-审计日志）

## 菜单结构

一级菜单 12 项（仪表盘 + 11 个分组）：

```
集群总览
集群          集群管理 / 命名空间 / 节点 / 自定义资源
工作负载       矩阵视图 / Deployment / StatefulSet / DaemonSet / CronJob / Job / Pod / HPA
可观测性       监控 / 告警 / Grafana 面板 / 事件中心 / 日志检索
CI / CD       CI 流水线 / ArgoCD 应用 / Helm Releases / Chart 仓库
微服务         服务发现 / 配置管理（Nacos，登录即可用，跟随顶栏命名空间选择）
服务与网络     服务与路由（服务/路由/Ingress 类）· 网关（Gateway API 全量资源）· 网络策略
配置中心       ConfigMap / Secret / ServiceAccount
存储           PVC / PV / StorageClass
安全 (RBAC)    Role / RoleBinding / ClusterRole / ClusterRoleBinding
配额           ResourceQuota / LimitRange
平台管理       用户管理 / 授权管理 / 审计日志 / 通知管理 / Nacos 管理 / 镜像仓库 / 用量报表 / 备份概览（仅管理员）
```

## 功能一览

### 集群

| 模块 | 功能 |
| --- | --- |
| 集群管理 | kubeconfig 粘贴注册 / 编辑 / 删除 / 连通性测试，多集群界面切换；Prometheus 地址按集群配置 + 连通性测试；**Grafana 地址按集群配置**（iframe 内嵌面板）；**熔断**：连接异常的集群自动标记，避免请求长时间挂起；**证书巡检**：各集群证书到期时间盘点（提前 30/7 天告警色） |
| 集群总览 | 节点 / Pod / 工作负载 / 命名空间统计卡片，节点状态表，资源容量 |
| 命名空间 | 列表 / 创建 / 删除 / 独立监控页 |
| 节点 | 列表与详情（容量、Conditions、污点、节点上 Pod、监控）；**运维操作**：封锁/解除封锁（cordon）、排水（drain）、污点与标签编辑；**节点终端**：自动创建 nsenter 调试 Pod 进入宿主机 shell |
| 自定义资源 | discovery 驱动的资源浏览器：按 API group 展示全部官方资源与 CRD（KubeBlocks / Elastic / Tekton / Gateway API 等），浏览 / 搜索 / 删除 / YAML 编辑 |

### 工作负载（含 Pod 融合）

| 模块 | 功能 |
| --- | --- |
| 控制器 | Deployment / StatefulSet / DaemonSet / CronJob / Job / HPA 列表与详情（关联 Pod、事件）；**缩放 / 滚动重启 / 删除 / 批量操作**；**发布历史与一键回滚**（rollouts 时间线）；行点击进详情 |
| Pod（融入工作负载列表） | 与控制器同列表框架：状态 / 就绪 / 重启 / IP / 节点 / 镜像列，操作下拉 **详情 / 日志 / 终端 / 驱除（Eviction API，尊重 PDB）/ 删除**；批量删除 |
| Pod 详情 | 容器状态（**镜像 + 镜像 CVE 徽标**，点击弹出 CVE 明细）、Conditions、事件、监控、**Web 终端**、**容器文件浏览器**（浏览 / 下载 / 上传 / 新建 / 重命名 / 删除） |
| Web 终端 | Pod 内 Shell 终端（WebSocket exec）：容器无 shell 时自动回退 `/bin/bash`/`/bin/ash`；distroless 镜像可注入临时调试容器（`kubectl debug` 风格，镜像 `k8s.debugImage` 可配，默认 busybox）；终端尺寸自适应 |
| 可视化编辑 | 表单 + YAML 双视图：工作负载（容器 / 镜像 / 环境变量 / 端口 / 资源限制 / 探针 / 挂载 / 卷 / 容忍 / 调度 / 更新策略）、Service（**标签选择器级联下拉**：key/value 选项来自命名空间内 Pod 现有标签，可输入自定义值）、Ingress、ConfigMap、Secret、PVC、PV、StorageClass、NetworkPolicy、HPA、ServiceAccount、Role / ClusterRole（规则编辑器）、RoleBinding / ClusterRoleBinding、ResourceQuota、LimitRange、Gateway API 全量资源（**路由后端服务三级级联下拉**：命名空间 → Service → 端口名称，端口选项取自所选 Service 的 spec.ports）；其余资源 YAML 编辑兜底 |
| 镜像 Tag 自动补全 | 可视化编辑输入镜像后自动拉取该仓库可选 Tag 下拉（Harbor，700ms 防抖） |

### 可观测性

| 模块 | 功能 |
| --- | --- |
| 监控 | 基于 Prometheus 的五级监控（经 kube-apiserver proxy，无需对外暴露 Prometheus），ECharts 图表 + `?range=1h\|6h\|24h`：**集群**（CPU/内存/磁盘/网络卡片 + 趋势 + 节点/命名空间排行 + 控制面组件）、**节点**、**命名空间**、**工作负载**（按容器聚合）、**Pod** |
| 告警 | 基于 Prometheus `/api/v1/rules`，三视图：**告警规则**（firing/pending/inactive、严重度统计、过滤、规则详情与活动实例；每条规则标注来源 PrometheusRule CR，支持**在线查看 / 编辑 / 新建 / 删除**——单条规则粒度写回 CR，prometheus-operator 自动同步生效，通常数秒内重载）、**告警分组**（按 rule group 汇总、一键定位）、**规则检查**（扫描缺 severity / namespace / summary / runbook 等配置缺陷） |
| Alertmanager | 接入集群 **Alertmanager**（经 apiserver proxy / 直连，未配置优雅降级）：**实时告警**（含 silenced/inhibited 状态、30s 自动刷新）、**静默管理**（告警行一键静默 + 自定义匹配器/时长）、**主配置 YAML 在线编辑**（Secret 内 alertmanager.yaml[.gz] 自动识别，内置钉钉/邮件/企微/Slack receiver 模板片段；通知发送由 AM 完成，恢复通知 = `send_resolved: true`）、**告警历史归档**（服务端轮询 AM 按指纹状态机落库，触发/恢复/持续时长 + 统计卡片，默认保留 90 天） |
| 事件中心 | 实时：全集群 K8s 事件聚合（30s 自动刷新，页面隐藏自动暂停），按类型 / 对象 / 命名空间过滤；**归档检索**：服务端轮询 Events 增量写入 ES（`kc-events-*` 按天滚动索引，复用日志源连接，默认保留 30 天），任意历史时段回查 |
| 日志检索 | 对接 **Elasticsearch**（按集群配置日志源，支持连通性测试）：全文 + 字段查询、Pod 分布 Top 侧栏、时间范围、高亮 |
| Grafana 面板 | 按集群配置 Grafana 地址后 iframe 内嵌（kiosk 模式，支持粘贴任意 dashboard 链接 + 常用面板收藏）；需 Grafana 开启 `allow_embedding` 与匿名只读 |
| 通知管理 | CI 流水线执行事件（成功/失败/待审批）推送到启用渠道；渠道支持**钉钉**（加签）等，可测连通；推送历史可查（K8s 告警通知走上方 Alertmanager） |

### CI / CD 与交付

| 模块 | 功能 |
| --- | --- |
| CI 流水线（内置 Tekton） | 由 [ci-platform](http://gitlab.cqyxpt.site/cec/ci-platform) 融合而来的原生模块，**集群隔离**（全部接口经 X-Cluster 选择集群，Tekton 客户端按集群缓存）+ **命名空间隔离**（项目 = 一个 K8s 命名空间，Pipeline/Run/工作区 PVC/凭证 Secret 全部落在项目 ns）：**执行中心**（统计卡片 + WS 状态推送 + 5s 轮询兜底 + SSE 实时日志 + DAG 视图 + 人工审批 + 重跑 / 停止）、**流水线**（CRUD / 复制到项目 / 运行——分支或 Tag 下拉 / **vue-flow 可视化设计器**：19 类节点插件（go:embed 随二进制分发）、属性面板 schema 驱动 + Git 分支/Tag/K8s 目标/凭据动态下拉、Dagre 自动布局、JSON 导入导出、未保存离开拦截）、**项目**（CRUD，命名空间必填）、**制品**（build-image / upload-artifact 任务成功自动登记，血缘详情 + MinIO/Nexus 流式下载代理）、**发布记录**（k8s-deploy / helm-deploy 镜像快照 + 一键回滚）、**定时任务**（cron 调度 + 瞬时失败快速重试）、**Webhook**（GitLab push/tag/MR，token 走 URL，事件去重）、**全局变量**（`\${global.KEY}` 编译期注入）、**凭据**（basic / token / dockerconfig / kubeconfig / aksk / raw 六类，Secret 扇出到平台 ns + 各项目 ns，运行流水线时按需拉齐到 run 所在 ns，轮换语义：留空保持不变）；执行事件（成功/失败/待审批）推送通知渠道；未安装 Tekton 的集群优雅降级提示；写操作限管理员，SSE/WS 用 60s 短期流 token 查询鉴权 |
| ArgoCD 应用 | 通过 dynamic client 读取集群 `applications.argoproj.io` CRD：同步 / 健康状态列表、autoSync 标识、**手动触发刷新**（`refresh=normal` annotation）；未安装 ArgoCD 时优雅降级提示 |
| Helm 应用 | **Releases**：跨命名空间列表、详情（manifest / values / notes 三 tab）、版本历史、**安装**（仓库 → chart → 版本 → values + wait / timeout / atomic）、**升级 / 回滚 / 卸载**；**Chart 仓库**：源管理（增删改 / 私有认证）、index 刷新（内存缓存 TTL）、chart 浏览多版本 |

### Nacos 微服务集成

OpenAPI 客户端**自动探测版本风格**：Nacos 1.x/2.x 走 v1 API，3.x 走 `/v3/admin` + `/v3/auth`（3.x 移除了 v1 console/admin API），同一地址无需配置区别。

| 模块 | 功能 |
| --- | --- |
| 微服务（登录即可用） | **服务发现**（跟随顶栏命名空间选择，多命名空间合并展示：服务列表含分组/集群数/实例数/健康比，实例抽屉含 IP:端口/健康/权重/集群/元数据，可删除服务注册记录）、**配置管理**（命名空间联动，配置查看/编辑/发布/删除，多命名空间合并展示） |
| Nacos 管理（admin） | 按集群接入 Nacos：**命名空间自动同步**（每个 K8s ns 自动创建同名 Nacos ns + 用户 `ROLE_<ns>` rw 授权，凭据落库可查看/重置，默认 5 分钟一轮、ns 创建即时联动）；**Pod 级自动注入**（Admission Webhook + 自签 TLS：注入的 ns 创建 Pod 自动追加 `envFrom` 的 `nacos-config` ConfigMap 与 `nacos-credentials` Secret，kubectl 直建同样生效，failurePolicy=Ignore 不阻塞业务）；Nacos 侧命名空间/用户浏览 |

### 服务与网络 / 配置 / 存储 / 安全 / 配额

| 模块 | 功能 |
| --- | --- |
| 服务与网络 | 三级菜单：**服务与路由**（服务 / 路由 / Ingress 类）、**网关**（GatewayClass / Gateway / HTTP·TCP·TLS·UDP·GRPC Route / ReferenceGrant，discovery 自动探测当前集群提供的版本，未安装优雅降级）、**网络策略**；均支持可视化编辑 |
| 服务地址 | 服务列表「服务地址」列：**LoadBalancer 优先外部 IP、ExternalName 用目标名、其余用集群内 DNS**；多端口 Service 每端口一条（同端口 UDP/TCP 去重），逐条一键复制 |
| 配置中心 | ConfigMap / Secret / ServiceAccount |
| 存储 | PVC / PV / StorageClass |
| 安全 (RBAC) | Role / RoleBinding / ClusterRole / ClusterRoleBinding |
| 配额 | ResourceQuota 总览（按命名空间汇总 used/hard）+ LimitRange |

### 导入导出

| 对象 | 功能 |
| --- | --- |
| K8s 资源 YAML | 通用资源列表页：**导出**（Kuboard 式逐层选择：命名空间 → 控制器 / 服务与路由 / 配置 / 其他资源四类勾选，类与类型级全选；多文档 `---` 分隔；深度清洗 uid/resourceVersion/managedFields/status/last-applied 注解，可直接再导入或入 Git；单项失败跳过并提示）；**导入**（上传 .yaml/.yml → 多文档预览（Kind/名称/命名空间）→ create-or-update 逐个应用，逐条成败反馈，失败不中断整批） |
| Nacos 配置 | 微服务 → 配置管理页：**导出**（所选命名空间全部配置含完整内容，JSON 文件，记录来源集群与时间）；**导入**（上传 JSON → 预览 → 按原命名空间或统一导入到指定命名空间 → 逐条发布，适合跨环境/集群迁移） |

### 镜像仓库（Harbor）

| 模块 | 功能 |
| --- | --- |
| 平台管理-镜像仓库 | Harbor API 接入（Basic 认证、可选跳过 TLS 校验、连通性测试）；**三步浏览**：项目 → 镜像仓库 → Tag（制品） |
| 漏洞扫描 | 对任意 Tag **触发扫描**（Harbor 内置 Trivy）并轮询状态；扫描概览（状态 / 最高级别 / C·H·M·L 计数） |
| CVE 报告 | 漏洞明细表格（级别筛选、CVE 链接、受影响包、安装 → 修复版本、**CVSS 分数着色**、修复状态）；双格式解析（Harbor 3.x 1.1 报告 + 2.x 旧格式） |
| Pod 维度 CVE | Pod 详情容器表「镜像 CVE」列徽标（`CVE C·H·M`，点击弹 CVE 明细）；镜像无仓库前缀或不在已配置仓库时优雅降级（灰标签 + 原因提示，不报错） |

### 平台管理（管理员）

| 模块 | 功能 |
| --- | --- |
| 用户管理 | 用户 CRUD、重置密码、角色（admin / user）、**用户组**（组可直接作为 K8s RoleBinding 的 Group 主体，按组批量授权） |
| 授权管理 | **向导式授权**（选用户/组 → 命名空间 → 角色模板 → 生成 RoleBinding）与回收；**权限逆查**：某用户在哪些命名空间有什么权限 |
| 审计日志 | 认证后写操作审计（操作人 / 方法 / 路径 / 结果 / 时间），过滤与分页 |
| 通知管理 | 见「可观测性-通知管理」 |
| 镜像仓库 | 见「镜像仓库（Harbor）」 |
| 用量报表 | 基于 Prometheus 历史指标（container_cpu/memory + kube-state-metrics）的命名空间用量区间报表 |
| 备份概览 | 已安装 **Velero** 的集群：备份 / 恢复任务列表（未安装时提示部署） |
| 证书巡检 | 各集群 TLS 证书到期盘点 |
| 日志源 | 按集群配置 Elasticsearch 日志源（连通性测试） |
| 个人 | 修改密码、**个人 API Token**（创建 / 吊销，用于脚本化调用）、深色模式 |

## 技术栈

- **后端**：Go 1.25 / Gin / k8s.io/client-go v0.34（typed + dynamic + discovery）/ GORM / JWT / Helm v3 SDK / gorilla-websocket
- **前端**：Vue 3 + TypeScript + Vite + Element Plus + Pinia + CodeMirror 6 + js-yaml（表单 ↔ YAML 互转）+ ECharts 6 + xterm
- **存储**：SQLite（开发，纯 Go 驱动）/ MySQL / PostgreSQL（GORM 多驱动，config.yaml 切换）
- **外部集成**：Prometheus（监控/告警/用量）、Elasticsearch（日志检索）、Tekton（内置 CI）、ArgoCD（CD，CRD 读取）、Harbor（镜像仓库/Trivy 扫描 + CI 制品存储）、Nexus/MinIO（CI 制品存储）、Velero（备份概览）

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
  外部依赖（按集群或全局配置）：Prometheus · Elasticsearch · Tekton · Harbor · Nexus/MinIO · Velero
```

## 快速开始

### 开发

```bash
# 后端（:8080，SQLite 默认 ./data/kube-console.db）
cd server && go run ./cmd/server

# 前端（:5173，vite 代理 /api → :8080）
cd web && npm install && npm run dev
```

首次登录：`admin / admin123`（生产请改 `configs/config.yaml`）。

### 配置（configs/config.yaml）

```yaml
server:
  port: 8080
database:
  driver: sqlite        # sqlite | mysql | postgres
  dsn: ""               # 空 = ./data/kube-console.db
jwt:
  secret: "change-me"
  expireIn: 24          # 小时
admin:
  username: admin
  password: admin123    # 首个管理员，登录后请立即在界面改密
k8s:
  debugImage: "busybox" # 无 shell 容器注入调试用的镜像
```

### 部署

前后端分开部署（前端 `web/dist` 静态文件 + 后端 Go 单二进制，经 Nginx 同源反代 `/api`）的完整步骤、Nginx 配置（WS/SSE 关键项）、systemd/Docker/K8s 三种形态与验证清单见 **[docs/deployment.md](docs/deployment.md)**。K8s manifest 在 `deploy/`（后端 Deployment + 前端 Deployment + Gateway API 路由，PostgreSQL）。

## 使用指引（常用路径）

1. **接入集群**：集群 → 集群管理 → 添加（粘贴 kubeconfig，自动校验连通性）→ 顶栏切换
2. **看应用**：工作负载 → Deployment 列表 → 行点击进详情（左树右面板）→ 进 Pod → 日志 / 终端 / 文件 / CVE
3. **发版**：CI / CD → CI 流水线 → 「设计器」可视化编排 → 运行（选分支/Tag）；或 Helm → Releases → 安装/升级
4. **排障**：可观测性 → 监控（五级下钻）/ 告警 / 事件中心 / 日志检索；节点问题 → 节点详情 → 终端/排水/封锁
5. **安全**：平台管理 → 授权管理（向导授权）/ 审计日志；镜像 → 平台管理 → 镜像仓库（扫描 + CVE 报告）
6. **微服务**：微服务 → 服务发现 / 配置管理（跟随顶栏命名空间选择）；批量迁移 → 各列表页「导出 / 导入」

## 文档

- [docs/api.md](docs/api.md) — 后端 API 参考
- [docs/deployment.md](docs/deployment.md) — 前后端分开部署说明（Nginx 同源反代 / Docker / K8s）
- [docs/images.md](docs/images.md) — 镜像依赖清单与全局变量（`ci.imageRegistry` 一处切换全部 CI 节点镜像仓库）
