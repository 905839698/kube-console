// Package router 注册全部 HTTP 路由
package router

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kube-console/server/internal/ci"
	"kube-console/server/internal/config"
	"kube-console/server/internal/handler"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
)

// Setup 构建并返回 gin.Engine；archive/eventArchive 为 main 已启动的归档服务（状态查询用）
func Setup(db *gorm.DB, cfg *config.Config, clusters *service.ClusterManager, ciDeps *ci.Deps, archive *service.AlertArchiveService, eventArchive *service.EventArchiveService, nacosSvc *service.NacosService) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger(middleware.ParseLogLevel(cfg.Server.LogLevel)))

	api := r.Group("/api")

	// 写操作鉴权：按「授权」页写入集群的 RBAC 绑定判定（view=只读 / edit=该 ns 可写 /
	// admin=该 ns 可写；节点、命名空间增删、授权本身等集群级操作需集群级授权或平台管理员）。
	// 平台自身的写操作都用平台 kubeconfig 执行，K8s 不会替我们拦越权，必须显式判定。
	permSvc := service.NewPermissionService(db, clusters, cfg.Admin.Username)
	nsGuard := middleware.NamespaceWriteRequired(permSvc)
	clusterGuard := middleware.ClusterWriteRequired(permSvc)
	resourceGuard := middleware.ResourceWriteRequired(permSvc, clusters)

	authHandler := handler.NewAuthHandler(db, cfg)
	clusterHandler := handler.NewClusterHandler(clusters)
	k8sHandler := handler.NewK8sHandler(clusters, service.NewK8sService(cfg.K8s.DebugImage), cfg.JWT.Secret, permSvc)
	monitorHandler := handler.NewMonitorHandler(clusters)
	helmHandler := handler.NewHelmHandler(clusters, service.NewHelmService(db))
	userHandler := handler.NewUserHandler(db, cfg)
	authzHandler := handler.NewAuthzHandler(clusters, permSvc)
	rbacSvc := service.NewRbacService(db, clusters, permSvc)
	rbacHandler := handler.NewRbacHandler(rbacSvc, clusters, permSvc)
	platformHandler := handler.NewPlatformHandler(db, clusters, cfg.K8s.DebugImage, cfg.Admin.Username)
	ciHandler := handler.NewCIHandler(db, clusters, permSvc)
	cicdHandler := handler.NewCICDHandler(ciDeps, db, cfg.JWT.Secret, cfg.Admin.Username)
	// 可观测性扩展（Alertmanager 接入 / 告警历史 / 事件归档）
	obsHandler := handler.NewObsHandler(db, clusters, archive, eventArchive)
	// Nacos 微服务集成（配置/同步/注入/资源浏览）+ 命名空间创建联动钩子
	nacosHandler := handler.NewNacosHandler(db, clusters, nacosSvc)
	k8sHandler.OnNamespaceCreated = nacosSvc.SyncNamespaceAsync
	// 日志采集 Sidecar 生成 ES 输出配置时读取集群日志源
	k8sHandler.LogSourceFor = func(clusterName string) (*model.LogSource, error) {
		var src model.LogSource
		if err := db.Where("cluster_name = ?", clusterName).First(&src).Error; err != nil {
			return nil, err
		}
		return &src, nil
	}

	// 公开接口
	api.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	api.POST("/auth/login", authHandler.Login)

	// WebSocket 终端：浏览器无法自定义请求头，token/cluster 走 query，handler 内自行校验
	api.GET("/pods/:name/exec", k8sHandler.ExecPod)
	// CI：SSE 日志 / WS 状态推送（同样 query 鉴权）
	api.GET("/ci/runs/:id/tasks/:taskID/logs", cicdHandler.RunLogsSSE)
	api.GET("/ci/ws", cicdHandler.RunWS)
	// GitLab Webhook 回调（token 走路径，公开端点）
	api.POST("/ci/webhook/:token", cicdHandler.GitLabWebhook)
	// 节点终端：创建 nsenter 调试 Pod 后进入宿主机 shell
	api.GET("/nodes/:name/exec", k8sHandler.ExecNode)

	// 需要认证的接口
	authed := api.Group("")
	authed.Use(middleware.AuthRequired(cfg.JWT.Secret, db))
	// 操作审计：记录认证后的写操作
	authed.Use(middleware.AuditRequired(db))
	{
		authed.GET("/auth/me", authHandler.Me)
		authed.PUT("/auth/password", authHandler.ChangePassword)

		// 事件中心
		authed.GET("/events", k8sHandler.Events)
		authed.POST("/events/archive/search", obsHandler.EventsArchiveSearch)

		// 节点运维（集群级）
		authed.POST("/nodes/:name/cordon", clusterGuard, k8sHandler.CordonNode)
		authed.POST("/nodes/:name/drain", clusterGuard, k8sHandler.DrainNode)
		authed.PUT("/nodes/:name/taints", clusterGuard, k8sHandler.UpdateNodeTaints)
		authed.PUT("/nodes/:name/labels", clusterGuard, k8sHandler.UpdateNodeLabels)

		// 驱逐 Pod（Eviction API，尊重 PDB）：命名空间级写
		authed.POST("/pods/:name/evict", nsGuard, k8sHandler.EvictPod)

		// 容器文件浏览器（写操作按 ns 判定）
		authed.GET("/pods/:name/files", k8sHandler.ListFiles)
		authed.GET("/pods/:name/files/download", k8sHandler.DownloadFile)
		authed.POST("/pods/:name/files/upload", nsGuard, k8sHandler.UploadFile)
		authed.POST("/pods/:name/files/action", nsGuard, k8sHandler.FileAction)

		// 授权管理：读开放（向导需要选组/看授权），写仅平台管理员（见下方 admin 组）
		authed.GET("/authz/bindings", authzHandler.Grants)
		authed.GET("/authz/user-permissions", platformHandler.UserPermissions)
		authed.GET("/authz/my-permissions", authzHandler.MyPermissions)

		// 权限管理（KubeSphere 式三层角色）：角色 / 绑定 / 权限快照
		authed.GET("/rbac/roles", rbacHandler.ListRoles)
		authed.GET("/rbac/permission-items", rbacHandler.PermissionItems)
		authed.GET("/rbac/bindings", rbacHandler.ListBindings)
		authed.GET("/rbac/my-permissions", rbacHandler.MyPermissions)
		authed.GET("/rbac/user-roles", rbacHandler.UserRoles)

		// 平台扩展：日志检索 / 用量报表 / 备份概览 / 证书巡检
		authed.POST("/logs/search", platformHandler.LogSearch)
		authed.GET("/usage", platformHandler.UsageReport)
		authed.GET("/backups", platformHandler.Backups)
		authed.GET("/clusters/certs", platformHandler.ClusterCerts)

		// 个人 API Token
		authed.GET("/tokens", platformHandler.ListTokens)
		authed.POST("/tokens", platformHandler.CreateToken)
		authed.DELETE("/tokens/:id", platformHandler.RevokeToken)

		// 用户组（读：所有人可见组名以便授权向导选择；写：管理员）
		authed.GET("/groups", platformHandler.ListGroups)

		// 内置 CI（Tekton，多集群 + 项目 ns 隔离）与 CD（ArgoCD 视图）
		authed.GET("/ci/ready", cicdHandler.Ready)
		authed.GET("/ci/node-types", cicdHandler.NodeTypes)
		authed.GET("/ci/stream-token", cicdHandler.StreamToken)
		// 项目（= 命名空间隔离单元）
		authed.GET("/ci/projects", cicdHandler.ProjectList)
		authed.POST("/ci/projects", cicdHandler.ProjectCreate)
		authed.PUT("/ci/projects/:id", cicdHandler.ProjectUpdate)
		authed.DELETE("/ci/projects/:id", cicdHandler.ProjectDelete)
		// 流水线 + 版本
		authed.GET("/ci/pipelines", cicdHandler.PipelineList)
		authed.POST("/ci/pipelines", cicdHandler.PipelineCreate)
		authed.GET("/ci/pipelines/:id", cicdHandler.PipelineGet)
		authed.PUT("/ci/pipelines/:id", cicdHandler.PipelineUpdate)
		authed.DELETE("/ci/pipelines/:id", cicdHandler.PipelineDelete)
		authed.GET("/ci/pipelines/:id/versions", cicdHandler.PipelineVersions)
		authed.POST("/ci/pipelines/:id/versions", cicdHandler.PipelineSaveVersion)
		authed.POST("/ci/pipelines/:id/validate", cicdHandler.PipelineValidate)
		authed.POST("/ci/pipelines/:id/compile", cicdHandler.PipelineCompile)
		authed.POST("/ci/pipelines/:id/run", cicdHandler.PipelineRun)
		authed.POST("/ci/pipelines/:id/duplicate", cicdHandler.PipelineDuplicate)
		// 执行中心
		authed.GET("/ci/runs", cicdHandler.RunList)
		authed.GET("/ci/runs/:id", cicdHandler.RunDetail)
		authed.GET("/ci/runs/:id/tasks", cicdHandler.RunTasks)
		authed.POST("/ci/runs/:id/rerun", cicdHandler.RunRerun)
		authed.POST("/ci/runs/:id/cancel", cicdHandler.RunCancel)
		authed.GET("/ci/runs/:id/approvals", cicdHandler.RunApprovals)
		authed.POST("/ci/runs/:id/approvals", cicdHandler.RunApprovalDecide)
		// 凭据
		authed.GET("/ci/credentials", cicdHandler.CredentialList)
		authed.POST("/ci/credentials", cicdHandler.CredentialCreate)
		authed.PUT("/ci/credentials/:id", cicdHandler.CredentialUpdate)
		authed.DELETE("/ci/credentials/:id", cicdHandler.CredentialDelete)
		// 辅助：仓库 refs / k8s 部署目标
		authed.GET("/ci/schedules", cicdHandler.ScheduleList)
		authed.POST("/ci/schedules", cicdHandler.ScheduleCreate)
		authed.PUT("/ci/schedules/:id", cicdHandler.ScheduleUpdate)
		authed.DELETE("/ci/schedules/:id", cicdHandler.ScheduleDelete)
		authed.GET("/ci/webhooks", cicdHandler.WebhookList)
		authed.POST("/ci/webhooks", cicdHandler.WebhookCreate)
		authed.PUT("/ci/webhooks/:id", cicdHandler.WebhookUpdate)
		authed.DELETE("/ci/webhooks/:id", cicdHandler.WebhookDelete)

		authed.GET("/ci/artifacts", cicdHandler.ArtifactList)
		authed.GET("/ci/artifacts/:id", cicdHandler.ArtifactGet)
		authed.GET("/ci/artifacts/:id/download", cicdHandler.ArtifactDownload)

		authed.GET("/ci/deployments", cicdHandler.DeploymentList)
		authed.POST("/ci/deployments/:id/rollback", cicdHandler.DeploymentRollback)

		authed.GET("/ci/globals", cicdHandler.GlobalList)
		authed.POST("/ci/globals", cicdHandler.GlobalCreate)
		authed.PUT("/ci/globals/:id", cicdHandler.GlobalUpdate)
		authed.DELETE("/ci/globals/:id", cicdHandler.GlobalDelete)

		authed.GET("/ci/repo/refs", cicdHandler.RepoRefs)
		authed.GET("/ci/k8s/targets", cicdHandler.K8sTargets)
		// CD：ArgoCD 应用视图
		// CD：ArgoCD 应用视图（读开放；动作按目标命名空间写权限在 handler 内判定）
		authed.GET("/argocd/apps", ciHandler.ArgoApps)
		authed.GET("/argocd/apps/:namespace/:name", ciHandler.ArgoAppGet)
		authed.GET("/argocd/apps/:namespace/:name/detail", ciHandler.ArgoAppDetail)
		authed.GET("/argocd/apps/:namespace/:name/tree", ciHandler.ArgoAppTree)
		authed.POST("/argocd/apps/:namespace/:name/refresh", ciHandler.ArgoRefresh)
		authed.POST("/argocd/apps/:namespace/:name/sync", ciHandler.ArgoAppSync)
		authed.PUT("/argocd/apps/:namespace/:name/autosync", ciHandler.ArgoAppAutoSync)
		authed.PUT("/argocd/apps/:namespace/:name/pause", ciHandler.ArgoAppPause)
		authed.DELETE("/argocd/apps/:namespace/:name", ciHandler.ArgoAppDelete)
		// AppProject 列表（应用 spec.project 必须指向其中之一，表单据此选项目并校验仓库/目标）
		authed.GET("/argocd/projects", ciHandler.ArgoProjects)
		authed.GET("/argocd/repos", ciHandler.ArgoRepos)
		// 镜像 Tag 下拉与 Pod 维度 CVE（只读，登录即可用）
		authed.GET("/registry/image-tags", platformHandler.ImageTags)
		authed.GET("/registry/image-vulns", platformHandler.ImageVulns)

		// Nacos 微服务：服务发现 / 配置管理（登录即可用，跟随全局命名空间选择；
		// 连接配置 / 同步 / 密码轮换 / Nacos 侧命名空间与用户管理仍走 admin 组）
		authed.GET("/nacos/ready", nacosHandler.NacosReady)
		authed.GET("/nacos/services", nacosHandler.NacosServices)
		authed.GET("/nacos/instances", nacosHandler.NacosInstances)
		authed.DELETE("/nacos/service", nsGuard, nacosHandler.NacosServiceDelete)
		authed.GET("/nacos/configs", nacosHandler.NacosConfigList)
		authed.GET("/nacos/config-export", nacosHandler.NacosConfigExport)
		authed.GET("/nacos/config-content", nacosHandler.NacosConfigContent)
		// Nacos 命名空间与 K8s 命名空间同名（平台同步时 1:1 映射），写操作按该 ns 判定
		authed.POST("/nacos/config-publish", nsGuard, nacosHandler.NacosConfigPublish)
		authed.DELETE("/nacos/config-content", nsGuard, nacosHandler.NacosConfigDelete)

		// 平台管理（platform-admin 可写；platform-viewer 只读放开 GET——PlatformScoped）
		admin := authed.Group("")
		admin.Use(middleware.PlatformScoped(db, cfg.Admin.Username))
		{
			// 角色管理（自定义角色 CRUD 属写操作，实际仅 platform-admin 可通过）
			admin.POST("/rbac/roles", rbacHandler.CreateRole)
			admin.PUT("/rbac/roles/:id", rbacHandler.UpdateRole)
			admin.DELETE("/rbac/roles/:id", rbacHandler.DeleteRole)
			// 授权管理（写 DB + 物化/清理 K8s RBAC）
			admin.POST("/rbac/bindings", rbacHandler.CreateBinding)
			admin.DELETE("/rbac/bindings/:id", rbacHandler.DeleteBinding)

			admin.GET("/users", userHandler.List)
			admin.POST("/users", userHandler.Create)
			admin.PUT("/users/:id/password", userHandler.ResetPassword)
			admin.PUT("/users/:id/role", userHandler.UpdateRole)
			admin.DELETE("/users/:id", userHandler.Delete)
			admin.GET("/audit", userHandler.AuditList)

			// 授权管理：创建/回收 RBAC 绑定只允许平台管理员
			// （否则任何登录用户都能给自己授 cluster-admin —— 授权页的 role 允许自定义 ClusterRole 名）
			admin.POST("/authz/grant", authzHandler.Grant)
			admin.DELETE("/authz/bindings/:kind/:name", authzHandler.Revoke)

			// 集群管理：新增/修改/删除集群（含 kubeconfig）与监控连接配置；
			// 完整列表与连通性探测 GET 对 platform-viewer 只读放开（PlatformScoped）
			admin.GET("/clusters", clusterHandler.List)
			admin.GET("/clusters/:name/connectivity", clusterHandler.Connectivity)
			admin.POST("/clusters", clusterHandler.Create)
			admin.PUT("/clusters/:name", clusterHandler.Update)
			admin.DELETE("/clusters/:name", clusterHandler.Delete)
			admin.PUT("/clusters/:name/prometheus", clusterHandler.UpdatePrometheus)
			admin.PUT("/clusters/:name/grafana", clusterHandler.UpdateGrafana)

			// Chart 仓库源（全局配置，不依赖 X-Cluster）
			admin.GET("/helm/repos", helmHandler.ListRepos)
			admin.POST("/helm/repos", helmHandler.AddRepo)
			admin.PUT("/helm/repos/:id", helmHandler.UpdateRepo)
			admin.DELETE("/helm/repos/:id", helmHandler.RemoveRepo)
			admin.PUT("/helm/repos/:id/refresh", helmHandler.RefreshRepo)

			// 用户组管理
			admin.POST("/groups", platformHandler.SaveGroup)
			admin.DELETE("/groups/:id", platformHandler.DeleteGroup)
			admin.PUT("/users/:id/group", platformHandler.SetUserGroup)

			// 通知渠道与记录
			admin.GET("/notify/channels", platformHandler.ListChannels)
			admin.POST("/notify/channels", platformHandler.SaveChannel)
			admin.DELETE("/notify/channels/:id", platformHandler.DeleteChannel)
			admin.POST("/notify/channels/:id/test", platformHandler.TestChannel)
			admin.GET("/notify/logs", platformHandler.NotifyLogs)

			// Alertmanager 连接配置与主配置 YAML（读写 Secret，含 SMTP 密码，仅管理员）
			admin.GET("/alertmanager", obsHandler.ListAMConfigs)
			admin.POST("/alertmanager", obsHandler.SaveAMConfig)
			admin.DELETE("/alertmanager/:cluster", obsHandler.DeleteAMConfig)
			admin.POST("/alertmanager/test", obsHandler.TestAMConfig)
			admin.PUT("/monitor/am/config-yaml", obsHandler.AMConfigYAMLSave)

			// Nacos 集成管理：连接配置 / 同步 / 密码轮换 / Nacos 侧命名空间与用户
			admin.GET("/nacos/config", nacosHandler.ListNacosConfigs)
			admin.POST("/nacos/config", nacosHandler.SaveNacosConfig)
			admin.DELETE("/nacos/config/:cluster", nacosHandler.DeleteNacosConfig)
			admin.POST("/nacos/config/test", nacosHandler.TestNacosConfig)
			admin.GET("/nacos/status", nacosHandler.NacosStatus)
			admin.POST("/nacos/sync/:cluster", nacosHandler.NacosSyncNow)
			admin.POST("/nacos/reset-password", nacosHandler.NacosResetPassword)
			admin.GET("/nacos/namespaces", nacosHandler.NacosNamespaces)
			admin.GET("/nacos/users", nacosHandler.NacosUsers)

			// 镜像仓库
			admin.GET("/registry/config", platformHandler.GetRegistryConfig)
			admin.POST("/registry/config", platformHandler.SaveRegistryConfig)
			admin.POST("/registry/test", platformHandler.TestRegistry)
			admin.GET("/registry/projects", platformHandler.RegistryProjects)
			admin.GET("/registry/projects/:project/repos", platformHandler.RegistryRepos)
			admin.GET("/registry/tags", platformHandler.RegistryTags)
			admin.GET("/registry/scan", platformHandler.RegistryScan)
			admin.POST("/registry/scan", platformHandler.RegistryScan)
			admin.GET("/registry/vulns", platformHandler.RegistryVulns)

			// 日志源配置
			admin.GET("/logsources", platformHandler.ListLogSources)
			admin.POST("/logsources", platformHandler.SaveLogSource)
			admin.DELETE("/logsources/:cluster", platformHandler.DeleteLogSource)
			admin.POST("/logsources/test", platformHandler.TestLogSource)

			// ArgoCD 仓库（含账号密码）管理（应用刷新/同步/删除已按目标 ns 写权限收口）
			admin.POST("/argocd/repos", ciHandler.ArgoRepoCreate)
			admin.PUT("/argocd/repos/:name", ciHandler.ArgoRepoUpdate)
			admin.DELETE("/argocd/repos/:name", ciHandler.ArgoRepoDelete)
		}

		// 含敏感凭证明文的读取：即便 platform-viewer 也不放行（alertmanager.yaml 内嵌 SMTP 密码）
		strict := authed.Group("")
		strict.Use(middleware.AdminRequired(db, cfg.Admin.Username))
		{
			strict.GET("/monitor/am/config-yaml", obsHandler.AMConfigYAMLGet)
		}

		// 集群管理（平台基础设施）：完整列表（apiserver 地址、监控配置）仅平台角色；
		// /my-clusters 为普通用户的顶栏切换器最小信息源
		authed.GET("/my-clusters", clusterHandler.ListMine)
		authed.GET("/my-clusters/connectivity", clusterHandler.ConnectivityMine)
		authed.GET("/monitor/grafana-check", obsHandler.GrafanaCheck)

		// 监控（通过 X-Cluster 请求头选择集群，?range=1h|6h|24h）
		authed.GET("/monitor/overview", monitorHandler.Overview)
		authed.GET("/monitor/query", monitorHandler.Query)
		authed.GET("/monitor/query-range", monitorHandler.QueryRange)
		authed.GET("/monitor/node/:name", monitorHandler.Node)
		authed.GET("/monitor/namespace/:name", monitorHandler.Namespace)
		authed.GET("/monitor/workload", monitorHandler.Workload)
		authed.GET("/monitor/pod", monitorHandler.Pod)
		authed.GET("/monitor/prometheus-check", monitorHandler.PrometheusCheck)
		authed.GET("/monitor/alerts", monitorHandler.Alerts)

		// Alertmanager 接入：实时告警 / 静默 / 告警历史（主配置 YAML 读写走 admin 组）
		authed.GET("/monitor/am/status", obsHandler.AMStatus)
		authed.GET("/monitor/am/alerts", obsHandler.AMAlerts)
		authed.GET("/monitor/am/silences", obsHandler.AMSilenceList)
		// 静默是集群级操作（不属于任何命名空间）
		authed.POST("/monitor/am/silences", clusterGuard, obsHandler.AMSilenceCreate)
		authed.DELETE("/monitor/am/silences/:id", clusterGuard, obsHandler.AMSilenceDelete)
		authed.GET("/alertevents", obsHandler.AlertEventList)
		authed.GET("/alertevents/stats", obsHandler.AlertEventStats)
		authed.GET("/alertevents/sync-status", obsHandler.AlertEventSyncStatus)

		// 集群资源（通过 X-Cluster 请求头选择集群）
		authed.GET("/overview", k8sHandler.Overview)
		authed.GET("/resource-defs", k8sHandler.ResourceDefs)
		authed.GET("/gateway-availability", k8sHandler.GatewayAvailability)

		// 全局搜索与工作负载矩阵
		authed.GET("/search", k8sHandler.Search)
		authed.GET("/workloads/matrix", k8sHandler.WorkloadMatrix)
		authed.GET("/quotas/overview", k8sHandler.QuotaOverview)

		authed.GET("/namespaces", k8sHandler.ListNamespaces)
		authed.POST("/namespaces", clusterGuard, k8sHandler.CreateNamespace)
		authed.DELETE("/namespaces/:name", clusterGuard, k8sHandler.DeleteNamespace)

		authed.GET("/nodes", k8sHandler.ListNodeDetails)
		authed.GET("/nodes/:name", k8sHandler.GetNodeDetail)

		authed.GET("/pods", k8sHandler.ListPods)
		authed.GET("/pods/:name", k8sHandler.GetPodDetail)
		authed.GET("/pods/:name/logs", k8sHandler.StreamPodLogs)
		authed.DELETE("/pods/:name", nsGuard, k8sHandler.DeletePod)

		authed.GET("/workloads/:kind", k8sHandler.ListWorkloads)
		authed.GET("/workloads/:kind/:name", k8sHandler.GetWorkloadDetail)
		authed.GET("/workloads/:kind/:name/rollouts", k8sHandler.Rollouts)
		authed.POST("/workloads/:kind/:name/rollback", nsGuard, k8sHandler.Rollback)
		authed.PUT("/workloads/:kind/:name/scale", nsGuard, k8sHandler.ScaleWorkload)
		authed.PUT("/workloads/:kind/:name/restart", nsGuard, k8sHandler.RestartWorkload)
		authed.DELETE("/workloads/:kind/:name", nsGuard, k8sHandler.DeleteWorkload)

		// 容器内日志采集（Fluent Bit Sidecar + 共享 emptyDir -> ES）
		authed.GET("/workloads/:kind/:name/logcollection", k8sHandler.GetLogCollection)
		authed.PUT("/workloads/:kind/:name/logcollection", nsGuard, k8sHandler.SetLogCollection)
		authed.DELETE("/workloads/:kind/:name/logcollection", nsGuard, k8sHandler.RemoveLogCollection)

		// 通用资源（kind: services|ingresses|configmaps|secrets|pvc|pv|storageclasses 等 KindMap 资源）
		authed.GET("/resources/:kind", k8sHandler.ListGeneric)
		authed.GET("/resources/:kind/:name/yaml", k8sHandler.GetGenericYAML)
		authed.DELETE("/resources/:kind/:name", resourceGuard, k8sHandler.DeleteGeneric)

		// 任意 GVR（CRD 浏览）：group 为空用 core
		authed.GET("/generic/:group/:version/:resource", k8sHandler.ListGenericGVR)
		authed.GET("/generic/:group/:version/:resource/:name/yaml", k8sHandler.GetGenericGVRYAML)
		authed.DELETE("/generic/:group/:version/:resource/:name", resourceGuard, k8sHandler.DeleteGenericGVR)

		// YAML 应用与读取（export：按勾选的资源导出多文档 YAML，Kuboard 式逐层选择）
		// apply 是多文档提交：ns 级/集群级逐文档判定在 handler 内做（ns 取自文档内容）
		authed.POST("/yaml/apply", k8sHandler.ApplyYAML)
		authed.GET("/yaml", k8sHandler.GetYAML)
		authed.POST("/yaml/export", k8sHandler.ExportYAML)

		// Helm 应用管理（release 操作通过 X-Cluster 选择集群；仓库源是全局配置 → admin 组）
		authed.GET("/helm/releases", helmHandler.ListReleases)
		authed.GET("/helm/releases/:name/info", helmHandler.ReleaseInfo)
		authed.GET("/helm/releases/:name/history", helmHandler.ReleaseHistory)
		authed.GET("/helm/releases/:name/upgrade-values", helmHandler.ReleaseUpgradeValues)
		authed.PUT("/helm/releases/:name/rollback", nsGuard, helmHandler.RollbackRelease)
		authed.PUT("/helm/releases/:name/upgrade", nsGuard, helmHandler.UpgradeRelease)
		authed.DELETE("/helm/releases/:name", nsGuard, helmHandler.UninstallRelease)
		authed.POST("/helm/releases", nsGuard, helmHandler.InstallRelease)

		authed.GET("/helm/charts/values", helmHandler.ChartValues)
		authed.GET("/helm/repos/:id/charts", helmHandler.RepoCharts)
	}

	return r
}
