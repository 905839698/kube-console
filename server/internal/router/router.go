// Package router 注册全部 HTTP 路由
package router

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kube-console/server/internal/ci"
	"kube-console/server/internal/config"
	"kube-console/server/internal/handler"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
)

// Setup 构建并返回 gin.Engine；archive/eventArchive 为 main 已启动的归档服务（状态查询用）
func Setup(db *gorm.DB, cfg *config.Config, clusters *service.ClusterManager, ciDeps *ci.Deps, archive *service.AlertArchiveService, eventArchive *service.EventArchiveService, nacosSvc *service.NacosService) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger(middleware.ParseLogLevel(cfg.Server.LogLevel)))

	api := r.Group("/api")

	authHandler := handler.NewAuthHandler(db, cfg)
	clusterHandler := handler.NewClusterHandler(clusters)
	k8sHandler := handler.NewK8sHandler(clusters, service.NewK8sService(cfg.K8s.DebugImage), cfg.JWT.Secret)
	monitorHandler := handler.NewMonitorHandler(clusters)
	helmHandler := handler.NewHelmHandler(clusters, service.NewHelmService(db))
	userHandler := handler.NewUserHandler(db, cfg)
	authzHandler := handler.NewAuthzHandler(clusters)
	platformHandler := handler.NewPlatformHandler(db, clusters, cfg.K8s.DebugImage)
	ciHandler := handler.NewCIHandler(db, clusters)
	cicdHandler := handler.NewCICDHandler(ciDeps, db, cfg.JWT.Secret, cfg.Admin.Username)
	// 可观测性扩展（Alertmanager 接入 / 告警历史 / 事件归档）
	obsHandler := handler.NewObsHandler(db, clusters, archive, eventArchive)
	// Nacos 微服务集成（配置/同步/注入/资源浏览）+ 命名空间创建联动钩子
	nacosHandler := handler.NewNacosHandler(db, clusters, nacosSvc)
	k8sHandler.OnNamespaceCreated = nacosSvc.SyncNamespaceAsync

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

		// 节点运维
		authed.POST("/nodes/:name/cordon", k8sHandler.CordonNode)
		authed.POST("/nodes/:name/drain", k8sHandler.DrainNode)
		authed.PUT("/nodes/:name/taints", k8sHandler.UpdateNodeTaints)
		authed.PUT("/nodes/:name/labels", k8sHandler.UpdateNodeLabels)

		// 驱逐 Pod（Eviction API，尊重 PDB）
		authed.POST("/pods/:name/evict", k8sHandler.EvictPod)

		// 容器文件浏览器
		authed.GET("/pods/:name/files", k8sHandler.ListFiles)
		authed.GET("/pods/:name/files/download", k8sHandler.DownloadFile)
		authed.POST("/pods/:name/files/upload", k8sHandler.UploadFile)
		authed.POST("/pods/:name/files/action", k8sHandler.FileAction)

		// 授权管理（向导式创建/回收 RBAC 绑定）
		authed.POST("/authz/grant", authzHandler.Grant)
		authed.GET("/authz/bindings", authzHandler.Grants)
		authed.DELETE("/authz/bindings/:kind/:name", authzHandler.Revoke)
		authed.GET("/authz/user-permissions", platformHandler.UserPermissions)

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
		authed.GET("/argocd/apps", ciHandler.ArgoApps)
		authed.GET("/argocd/apps/:namespace/:name", ciHandler.ArgoAppGet)
		authed.GET("/argocd/repos", ciHandler.ArgoRepos)
		// 镜像 Tag 下拉与 Pod 维度 CVE（只读，登录即可用）
		authed.GET("/registry/image-tags", platformHandler.ImageTags)
		authed.GET("/registry/image-vulns", platformHandler.ImageVulns)

			// Nacos 微服务：服务发现 / 配置管理（登录即可用，跟随全局命名空间选择；
			// 连接配置 / 同步 / 密码轮换 / Nacos 侧命名空间与用户管理仍走 admin 组）
			authed.GET("/nacos/ready", nacosHandler.NacosReady)
			authed.GET("/nacos/services", nacosHandler.NacosServices)
			authed.GET("/nacos/instances", nacosHandler.NacosInstances)
			authed.DELETE("/nacos/service", nacosHandler.NacosServiceDelete)
			authed.GET("/nacos/configs", nacosHandler.NacosConfigList)
			authed.GET("/nacos/config-export", nacosHandler.NacosConfigExport)
			authed.GET("/nacos/config-content", nacosHandler.NacosConfigContent)
			authed.POST("/nacos/config-publish", nacosHandler.NacosConfigPublish)
			authed.DELETE("/nacos/config-content", nacosHandler.NacosConfigDelete)

			// 平台管理（仅管理员）
			admin := authed.Group("")
			admin.Use(middleware.AdminRequired(db, cfg.Admin.Username))
			{
				admin.GET("/users", userHandler.List)
				admin.POST("/users", userHandler.Create)
				admin.PUT("/users/:id/password", userHandler.ResetPassword)
				admin.PUT("/users/:id/role", userHandler.UpdateRole)
				admin.DELETE("/users/:id", userHandler.Delete)
				admin.GET("/audit", userHandler.AuditList)

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
				admin.GET("/monitor/am/config-yaml", obsHandler.AMConfigYAMLGet)
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

			admin.POST("/argocd/apps/:namespace/:name/refresh", ciHandler.ArgoRefresh)
			// ArgoCD 仓库（含账号密码）管理
			admin.POST("/argocd/repos", ciHandler.ArgoRepoCreate)
			admin.PUT("/argocd/repos/:name", ciHandler.ArgoRepoUpdate)
			admin.DELETE("/argocd/repos/:name", ciHandler.ArgoRepoDelete)
		}

		// 集群管理
		authed.GET("/clusters", clusterHandler.List)
		authed.POST("/clusters", clusterHandler.Create)
		authed.PUT("/clusters/:name", clusterHandler.Update)
		authed.DELETE("/clusters/:name", clusterHandler.Delete)
		authed.GET("/clusters/:name/connectivity", clusterHandler.Connectivity)
		authed.PUT("/clusters/:name/prometheus", clusterHandler.UpdatePrometheus)
		authed.PUT("/clusters/:name/grafana", clusterHandler.UpdateGrafana)
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
		authed.POST("/monitor/am/silences", obsHandler.AMSilenceCreate)
		authed.DELETE("/monitor/am/silences/:id", obsHandler.AMSilenceDelete)
		authed.GET("/alertevents", obsHandler.AlertEventList)
		authed.GET("/alertevents/stats", obsHandler.AlertEventStats)

		// 集群资源（通过 X-Cluster 请求头选择集群）
		authed.GET("/overview", k8sHandler.Overview)
		authed.GET("/resource-defs", k8sHandler.ResourceDefs)
		authed.GET("/gateway-availability", k8sHandler.GatewayAvailability)

		// 全局搜索与工作负载矩阵
		authed.GET("/search", k8sHandler.Search)
		authed.GET("/workloads/matrix", k8sHandler.WorkloadMatrix)
		authed.GET("/quotas/overview", k8sHandler.QuotaOverview)

		authed.GET("/namespaces", k8sHandler.ListNamespaces)
		authed.POST("/namespaces", k8sHandler.CreateNamespace)
		authed.DELETE("/namespaces/:name", k8sHandler.DeleteNamespace)

		authed.GET("/nodes", k8sHandler.ListNodeDetails)
		authed.GET("/nodes/:name", k8sHandler.GetNodeDetail)

		authed.GET("/pods", k8sHandler.ListPods)
		authed.GET("/pods/:name", k8sHandler.GetPodDetail)
		authed.GET("/pods/:name/logs", k8sHandler.StreamPodLogs)
		authed.DELETE("/pods/:name", k8sHandler.DeletePod)

		authed.GET("/workloads/:kind", k8sHandler.ListWorkloads)
		authed.GET("/workloads/:kind/:name", k8sHandler.GetWorkloadDetail)
		authed.GET("/workloads/:kind/:name/rollouts", k8sHandler.Rollouts)
		authed.POST("/workloads/:kind/:name/rollback", k8sHandler.Rollback)
		authed.PUT("/workloads/:kind/:name/scale", k8sHandler.ScaleWorkload)
		authed.PUT("/workloads/:kind/:name/restart", k8sHandler.RestartWorkload)
		authed.DELETE("/workloads/:kind/:name", k8sHandler.DeleteWorkload)

		// 通用资源（kind: services|ingresses|configmaps|secrets|pvc|pv|storageclasses 等 KindMap 资源）
		authed.GET("/resources/:kind", k8sHandler.ListGeneric)
		authed.GET("/resources/:kind/:name/yaml", k8sHandler.GetGenericYAML)
		authed.DELETE("/resources/:kind/:name", k8sHandler.DeleteGeneric)

		// 任意 GVR（CRD 浏览）：group 为空用 core
		authed.GET("/generic/:group/:version/:resource", k8sHandler.ListGenericGVR)
		authed.GET("/generic/:group/:version/:resource/:name/yaml", k8sHandler.GetGenericGVRYAML)
		authed.DELETE("/generic/:group/:version/:resource/:name", k8sHandler.DeleteGenericGVR)

			// YAML 应用与读取（export：按勾选的资源导出多文档 YAML，Kuboard 式逐层选择）
			authed.POST("/yaml/apply", k8sHandler.ApplyYAML)
			authed.GET("/yaml", k8sHandler.GetYAML)
			authed.POST("/yaml/export", k8sHandler.ExportYAML)

		// Helm 应用管理（release 操作通过 X-Cluster 选择集群）
		authed.GET("/helm/releases", helmHandler.ListReleases)
		authed.GET("/helm/releases/:name/info", helmHandler.ReleaseInfo)
		authed.GET("/helm/releases/:name/history", helmHandler.ReleaseHistory)
		authed.GET("/helm/releases/:name/upgrade-values", helmHandler.ReleaseUpgradeValues)
		authed.PUT("/helm/releases/:name/rollback", helmHandler.RollbackRelease)
		authed.PUT("/helm/releases/:name/upgrade", helmHandler.UpgradeRelease)
		authed.DELETE("/helm/releases/:name", helmHandler.UninstallRelease)
		authed.POST("/helm/releases", helmHandler.InstallRelease)

		// Chart 仓库源（全局配置，不依赖 X-Cluster）
		authed.GET("/helm/repos", helmHandler.ListRepos)
		authed.POST("/helm/repos", helmHandler.AddRepo)
		authed.PUT("/helm/repos/:id", helmHandler.UpdateRepo)
		authed.DELETE("/helm/repos/:id", helmHandler.RemoveRepo)
		authed.PUT("/helm/repos/:id/refresh", helmHandler.RefreshRepo)
		authed.GET("/helm/charts/values", helmHandler.ChartValues)
		authed.GET("/helm/repos/:id/charts", helmHandler.RepoCharts)
	}

	return r
}
