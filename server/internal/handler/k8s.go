package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// K8sHandler Kubernetes 资源接口
type K8sHandler struct {
	clusters  *service.ClusterManager
	k8s       *service.K8sService
	jwtSecret string
}

func NewK8sHandler(clusters *service.ClusterManager, k8s *service.K8sService, jwtSecret string) *K8sHandler {
	return &K8sHandler{clusters: clusters, k8s: k8s, jwtSecret: jwtSecret}
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
	if err := h.k8s.ExecPod(c.Request.Context(), client, ws, queryNamespace(c), c.Param("name"), container, shell, debug); err != nil {
		// 升级后无法返回 HTTP 错误，经 WS 发送错误消息
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"exit","message":"`+err.Error()+`"}`))
		_ = ws.Close()
	}
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
		Replicas int32 `json:"replicas" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "replicas 不能为空")
		return
	}
	if req.Replicas < 0 {
		response.Fail(c, 400, 400, "replicas 不能为负数")
		return
	}
	if err := h.k8s.ScaleWorkload(c.Request.Context(), client, c.Param("kind"), queryNamespace(c), c.Param("name"), req.Replicas); err != nil {
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
	created, err := h.k8s.ApplyYAML(c.Request.Context(), client, req.YAML)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	// Service 接入监控：根据 annotation 自动创建/更新/删除 ServiceMonitor（静默失败，不影响保存）
	_ = h.k8s.SyncServiceMonitor(c.Request.Context(), client, req.YAML)
	// 工作负载接入监控：根据 annotation 自动创建/更新/删除 PodMonitor
	_ = h.k8s.SyncPodMonitor(c.Request.Context(), client, req.YAML)
	response.OK(c, gin.H{"created": created})
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
