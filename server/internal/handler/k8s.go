package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// K8sHandler Kubernetes 资源接口
type K8sHandler struct {
	clusters  *service.ClusterManager
	k8s       *service.K8sService
	jwtSecret string
	// perm 写权限判定（对齐授权页的命名空间粒度授权）。yaml/apply 是多文档提交，
	// 目标 ns 取自文档内容而不是 query，路由中间件判不了，故在 handler 内逐文档查。
	perm *service.PermissionService
	// OnNamespaceCreated 命名空间创建后的联动钩子（Nacos 自动同步），由 router 装配注入；可为 nil
	OnNamespaceCreated func(cluster, namespace string)
	// LogSourceFor 取集群 ES 日志源（日志采集 Sidecar 生成输出配置用），由 router 装配注入；可为 nil
	LogSourceFor func(clusterName string) (*model.LogSource, error)
}

func NewK8sHandler(clusters *service.ClusterManager, k8s *service.K8sService, jwtSecret string, perm *service.PermissionService) *K8sHandler {
	return &K8sHandler{clusters: clusters, k8s: k8s, jwtSecret: jwtSecret, perm: perm}
}

// client 解析 X-Cluster 头并返回集群客户端，失败时写入响应并返回 nil
func (h *K8sHandler) client(c *gin.Context) *kube.Client {
	name := middleware.ClusterName(c)
	if name == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return nil
	}
	client, err := h.clusters.ClientChecked(name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil
	}
	return client
}

// clusterName 返回当前集群名（供校验默认命名空间等场景）
func (h *K8sHandler) clusterName(c *gin.Context) string {
	return middleware.ClusterName(c)
}

func queryNamespace(c *gin.Context) string {
	ns := c.Query("namespace")
	if ns == "" {
		return "default"
	}
	return ns // "*" 或 "a,b" 原样透传，由 service 层 splitNamespaces 处理
}

// ------------------- 全局搜索 / 工作负载矩阵 -------------------

// Search 全局搜索（?q=&limit=）
func (h *K8sHandler) Search(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	limit := 10
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items := h.k8s.Search(c.Request.Context(), client, c.Query("q"), limit)
	response.OK(c, items)
}

// WorkloadMatrix 工作负载矩阵数据
func (h *K8sHandler) WorkloadMatrix(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	matrix, err := h.k8s.WorkloadMatrixData(c.Request.Context(), client)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, matrix)
}

// QuotaOverview 命名空间配额总览
func (h *K8sHandler) QuotaOverview(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.QuotaOverview(c.Request.Context(), client)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// ------------------- 资源注册表 -------------------

// ResourceDefs 列出集群全部 API 资源（?force=1 强制刷新缓存）
func (h *K8sHandler) ResourceDefs(c *gin.Context) {
	name := middleware.ClusterName(c)
	if name == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return
	}
	groups, err := h.clusters.ResourceDefs(name, c.Query("force") == "1")
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, groups)
}

// GatewayAvailability Gateway API 资源在当前集群实际可用的版本（discovery 解析）
func (h *K8sHandler) GatewayAvailability(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	response.OK(c, kube.ListGatewayAvailability(c.Request.Context(), client))
}

// ------------------- 集群总览 -------------------

// Overview 集群总览统计
func (h *K8sHandler) Overview(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	stats, err := h.k8s.Overview(c.Request.Context(), client)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, stats)
}

// ------------------- 命名空间 -------------------

// ListNamespaces 命名空间列表
func (h *K8sHandler) ListNamespaces(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListNamespaces(c.Request.Context(), client, c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// CreateNamespace 创建命名空间
func (h *K8sHandler) CreateNamespace(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "命名空间名称不能为空")
		return
	}
	if err := h.k8s.CreateNamespace(c.Request.Context(), client, req.Name); err != nil {
		response.K8sError(c, err)
		return
	}
	// Nacos 联动：异步同步该命名空间（未配置 Nacos 的集群静默跳过）
	if h.OnNamespaceCreated != nil {
		go h.OnNamespaceCreated(h.clusterName(c), req.Name)
	}
	response.OK(c, nil)
}

// DeleteNamespace 删除命名空间
func (h *K8sHandler) DeleteNamespace(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.DeleteNamespace(c.Request.Context(), client, c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// ------------------- 节点 -------------------

// ListNodeDetails 节点列表
func (h *K8sHandler) ListNodeDetails(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListNodeDetails(c.Request.Context(), client)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// GetNodeDetail 节点详情
func (h *K8sHandler) GetNodeDetail(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	detail, err := h.k8s.GetNodeDetail(c.Request.Context(), client, c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, detail)
}

// ------------------- Pod -------------------

// ListPods Pod 列表
func (h *K8sHandler) ListPods(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListPods(c.Request.Context(), client, queryNamespace(c), c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// GetPodDetail Pod 详情
func (h *K8sHandler) GetPodDetail(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	detail, err := h.k8s.GetPodDetail(c.Request.Context(), client, queryNamespace(c), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, detail)
}

// DeletePod 删除 Pod
func (h *K8sHandler) DeletePod(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.DeletePod(c.Request.Context(), client, queryNamespace(c), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// StreamPodLogs 流式输出 Pod 日志（text/plain）
// 参数: container, tailLines(默认500), follow(0/1, 默认1)
func (h *K8sHandler) StreamPodLogs(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	tailLines := int64(500)
	if v := c.Query("tailLines"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			tailLines = n
		}
	}
	follow := c.Query("follow") != "0"
	container := c.Query("container")

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Status(200)
	c.Writer.Flush()

	err := h.k8s.StreamPodLogs(c.Request.Context(), client, queryNamespace(c), c.Param("name"), container, tailLines, follow, c.Writer)
	if err != nil {
		// 流已开始，只能追加错误行
		_, _ = c.Writer.WriteString("\n[error] " + err.Error() + "\n")
	}
	c.Writer.Flush()
}

// ExecPod 通过 WebSocket 连接 Pod 内 Shell
// 浏览器 WebSocket 无法自定义请求头，token/cluster 走 query：?token=&cluster=&namespace=&container=
// 路由挂在认证组外，此处自行校验 token
func (h *K8sHandler) ExecPod(c *gin.Context) {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		response.Fail(c, http.StatusUnauthorized, 401, "缺少 token 参数")
		return
	}
	claims, err := middleware.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		response.Fail(c, http.StatusUnauthorized, 401, "令牌无效或已过期")
		return
	}
	cluster := c.Query("cluster")
	if cluster == "" {
		response.Fail(c, http.StatusBadRequest, 400, "缺少 cluster 参数")
		return
	}
	// Pod 终端 = pods/exec，按 KubeSphere 口径归入命名空间写权限（edit/admin 或平台管理员）。
	// 权限关放在连通性检查之前：越权一律 403，不因集群恰好不可达而降级成 400
	if h.perm != nil && !h.perm.Access(c.Request.Context(), cluster, claims.Username).CanWriteNS(queryNamespace(c)) {
		response.Fail(c, http.StatusForbidden, 403, "Pod 终端需要该命名空间的写权限（edit/admin 角色或平台管理员）")
		return
	}
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true }, // 前端经 vite proxy/同源部署，放开 origin 校验
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	// 升级成功后不再走 HTTP 响应，记录审计信息
	c.Set(middleware.UsernameKey, claims.Username)

	container := c.Query("container")
	shell := c.Query("shell")
	debug := c.Query("debug") == "1"
	// ExecPod 内部已负责发送 exit 消息并关闭 WS（含错误分支），
	// 这里不再重复写——连接已关，且错误串拼接进 JSON 含引号/换行时是非法 JSON
	_ = h.k8s.ExecPod(c.Request.Context(), client, ws, queryNamespace(c), c.Param("name"), container, shell, debug)
}

// ------------------- 工作负载 -------------------

// ListWorkloads 工作负载列表（kind: deployments|statefulsets|daemonsets）
func (h *K8sHandler) ListWorkloads(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListWorkloads(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// GetWorkloadDetail 工作负载详情
func (h *K8sHandler) GetWorkloadDetail(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	detail, err := h.k8s.GetWorkloadDetail(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, detail)
}

// ScaleWorkload 调整副本数
func (h *K8sHandler) ScaleWorkload(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		// 指针：binding:"required" 对数值按「非零值」校验，{"replicas":0} 会被拒，
		// 而缩容到 0（停工作负载）是合法操作
		Replicas *int32 `json:"replicas"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "replicas 不能为空")
		return
	}
	if req.Replicas == nil {
		response.Fail(c, 400, 400, "replicas 不能为空")
		return
	}
	if *req.Replicas < 0 {
		response.Fail(c, 400, 400, "replicas 不能为负数")
		return
	}
	if err := h.k8s.ScaleWorkload(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"), *req.Replicas); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// RestartWorkload 滚动重启
func (h *K8sHandler) RestartWorkload(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.RestartWorkload(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// DeleteWorkload 删除工作负载
func (h *K8sHandler) DeleteWorkload(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.DeleteWorkload(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// ------------------- 容器内日志采集（Fluent Bit Sidecar） -------------------

// GetLogCollection 查询日志采集状态
func (h *K8sHandler) GetLogCollection(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	st, err := h.k8s.GetLogCollection(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, st)
}

// SetLogCollection 开启/更新日志采集（注入 Sidecar，触发滚动更新）
func (h *K8sHandler) SetLogCollection(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var in service.LogCollectionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if h.LogSourceFor == nil {
		response.Fail(c, 500, 500, "日志源查询未启用")
		return
	}
	src, err := h.LogSourceFor(h.clusterName(c))
	if err != nil || src == nil || !src.Enabled {
		response.Fail(c, 400, 400, "该集群未启用日志源（平台管理-日志源配置），无法生成 ES 采集配置")
		return
	}
	if err := h.k8s.EnableLogCollection(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"), &in, src); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// RemoveLogCollection 关闭日志采集（摘除 Sidecar 与配置资源）
func (h *K8sHandler) RemoveLogCollection(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.RemoveLogCollection(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// ------------------- 通用资源（Service/Ingress/ConfigMap/Secret） -------------------

// ListGeneric 通用资源列表（kind: services|ingresses|configmaps|secrets）
func (h *K8sHandler) ListGeneric(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListGeneric(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// GetGenericYAML 通用资源 YAML
func (h *K8sHandler) GetGenericYAML(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	yamlStr, err := h.k8s.GetGenericYAML(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlStr})
}

// DeleteGeneric 删除通用资源
func (h *K8sHandler) DeleteGeneric(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.DeleteGeneric(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// ------------------- 任意 GVR（CRD 浏览） -------------------

// GVR 参数: /api/generic/:group/:version/:resource
// group 为空时传 "core"（表示核心组）
func (h *K8sHandler) genericGVRParams(c *gin.Context) schema.GroupVersionResource {
	group := c.Param("group")
	if group == "core" {
		group = ""
	}
	return schema.GroupVersionResource{Group: group, Version: c.Param("version"), Resource: c.Param("resource")}
}

// ListGenericGVR 任意资源列表
func (h *K8sHandler) ListGenericGVR(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	namespace := ""
	if c.Query("namespace") != "" {
		namespace = c.Query("namespace")
	}
	items, err := h.k8s.ListGenericGVR(c.Request.Context(), client, h.genericGVRParams(c), namespace, c.Query("search"), c.Query("labelSelector"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// GetGenericGVRYAML 任意资源 YAML
func (h *K8sHandler) GetGenericGVRYAML(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	yamlStr, err := h.k8s.GetGenericYAMLByGVR(c.Request.Context(), client, h.genericGVRParams(c), c.Query("namespace"), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlStr})
}

// DeleteGenericGVR 任意资源删除
func (h *K8sHandler) DeleteGenericGVR(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	if err := h.k8s.DeleteGenericByGVR(c.Request.Context(), client, h.genericGVRParams(c), c.Query("namespace"), c.Param("name")); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// ------------------- YAML 应用 -------------------

type yamlReq struct {
	YAML string `json:"yaml" binding:"required"`
}

// ApplyYAML 应用 YAML（create-or-update）
func (h *K8sHandler) ApplyYAML(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req yamlReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "yaml 不能为空")
		return
	}
	// 多文档提交：先逐文档判定写权限再提交（避免只写成功一半）。
	// 命名空间级文档需该 ns 的 edit/admin；集群级文档（Namespace/ClusterRole/CRD 等）
	// 需集群级权限或平台管理员——否则只读用户能借 apply 绕过命名空间粒度授权。
	if msg := h.checkApplyPermission(c, req.YAML); msg != "" {
		response.Fail(c, http.StatusForbidden, http.StatusForbidden, msg)
		return
	}
	created, err := h.k8s.ApplyYAML(c.Request.Context(), client, req.YAML)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	// Service 接入监控：根据 annotation 自动创建/更新/删除 ServiceMonitor（静默失败，不影响保存）
	_ = h.k8s.SyncServiceMonitor(c.Request.Context(), client, req.YAML)
	// 工作负载接入监控：根据 annotation 自动创建/更新/删除 PodMonitor
	_ = h.k8s.SyncPodMonitor(c.Request.Context(), client, req.YAML)
	// Route 跨命名空间引用：自动创建 ReferenceGrant 授权
	_ = h.k8s.SyncReferenceGrant(c.Request.Context(), client, req.YAML)
	response.OK(c, gin.H{"created": created})
}

// checkApplyPermission 逐文档判定写权限，返回空串表示放行，否则返回拒绝原因。
// 作用域判定优先用集群 discovery（ResourceDefs，含 CRD 的 namespaced 标志），
// 取不到时退回平台的静态表 kube.IsClusterScoped（与 /resources/:kind 的口径一致）。
func (h *K8sHandler) checkApplyPermission(c *gin.Context, yamlStr string) string {
	docs, err := kube.ParseYAMLDocs(yamlStr)
	if err != nil {
		return "" // 解析失败交给 ApplyYAML 报错，这里不重复报
	}
	if h.perm == nil {
		return ""
	}
	cluster := middleware.ClusterName(c)
	acc := h.perm.Access(c.Request.Context(), cluster, middleware.CurrentUser(c))
	if acc.PlatformAdmin {
		return ""
	}
	namespaced := map[string]bool{}
	if defs, derr := h.clusters.ResourceDefs(cluster, false); derr == nil {
		for _, g := range defs {
			for _, r := range g.Resources {
				if r.Kind != "" {
					namespaced[strings.ToLower(r.Kind)] = r.Namespaced
				}
			}
		}
	}
	for i, obj := range docs {
		kind, name := obj.GetKind(), obj.GetName()
		if kind == "" {
			continue
		}
		isNS, known := namespaced[strings.ToLower(kind)]
		if !known {
			// discovery 不可用时的兜底：按平台静态表判断（KindMap 推出复数名）
			plural := strings.ToLower(kind)
			if gvr, ok := kube.KindMap[kind]; ok {
				plural = gvr.Resource
			}
			isNS = !kube.IsClusterScoped(plural)
		}
		if !isNS {
			if !acc.CanWriteCluster() {
				return fmt.Sprintf("第 %d 个文档 %s/%s 是集群级资源，需要集群级权限或平台管理员", i+1, kind, name)
			}
			continue
		}
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		if !acc.CanWriteNS(ns) {
			return fmt.Sprintf("没有命名空间 %s 的写权限（第 %d 个文档 %s/%s）：请在「授权」页授予该命名空间的 edit 或 admin 角色", ns, i+1, kind, name)
		}
	}
	return ""
}

// ExportYAML POST /yaml/export  {"resources":[{"kind","namespace","name"}]} → 多文档 YAML + 跳过项
func (h *K8sHandler) ExportYAML(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Resources []service.ExportRef `json:"resources"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Resources) == 0 {
		response.Fail(c, 400, 400, "resources 不能为空")
		return
	}
	yamlStr, skipped, err := h.k8s.ExportResources(c.Request.Context(), client, req.Resources)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlStr, "skipped": skipped})
}

// GetYAML 获取任意受支持资源的 YAML（参数: resource, namespace, name）
func (h *K8sHandler) GetYAML(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	resource := c.Query("resource")
	name := c.Query("name")
	if resource == "" || name == "" {
		response.Fail(c, 400, 400, "缺少 resource/name 参数")
		return
	}
	yamlStr, err := h.k8s.GetResourceYAML(c.Request.Context(), client, resource, queryNamespace(c), name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlStr})
}

// ExecNode 节点终端 WebSocket 入口：GET /nodes/:name/exec?token=&cluster=
func (h *K8sHandler) ExecNode(c *gin.Context) {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		response.Fail(c, http.StatusUnauthorized, 401, "缺少 token 参数")
		return
	}
	claims, err := middleware.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		response.Fail(c, http.StatusUnauthorized, 401, "令牌无效或已过期")
		return
	}
	cluster := c.Query("cluster")
	if cluster == "" {
		response.Fail(c, http.StatusBadRequest, 400, "缺少 cluster 参数")
		return
	}
	// 节点终端 = 宿主机 root shell + 创建调试 Pod，属集群级操作（cluster-admin 或平台管理员）；
	// 权限关先于连通性检查（越权一律 403）
	if h.perm != nil && !h.perm.Access(c.Request.Context(), cluster, claims.Username).CanWriteCluster() {
		response.Fail(c, http.StatusForbidden, 403, "节点终端需要集群级权限（cluster-admin 角色或平台管理员）")
		return
	}
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	c.Set(middleware.UsernameKey, claims.Username)
	_ = h.k8s.ExecNode(c.Request.Context(), client, ws, c.Param("name"))
}
