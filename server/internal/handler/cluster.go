package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// ClusterHandler 集群管理接口
type ClusterHandler struct {
	clusters *service.ClusterManager
}

func NewClusterHandler(clusters *service.ClusterManager) *ClusterHandler {
	return &ClusterHandler{clusters: clusters}
}

type clusterReq struct {
	Name       string `json:"name" binding:"required"`
	Kubeconfig string `json:"kubeconfig" binding:"required"`
}

// List 集群列表（完整信息：apiserver 地址、监控/面板配置等——仅平台角色，见 router）
func (h *ClusterHandler) List(c *gin.Context) {
	clusters, err := h.clusters.List()
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, clusters)
}

// ClusterBrief 普通用户可见的最小集群信息：顶栏切换器与就绪判断用，
// 不暴露 kubeconfig、apiserver 地址与监控配置
type ClusterBrief struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage"`
	GrafanaURL   string `json:"grafanaURL"` // Grafana 面板页 iframe 需要
}

func toBrief(cl *model.Cluster) ClusterBrief {
	return ClusterBrief{Name: cl.Name, Status: cl.Status, ErrorMessage: cl.ErrorMessage, GrafanaURL: cl.GrafanaURL}
}

// ListMine GET /my-clusters —— 所有登录用户：集群切换器所需最小字段
func (h *ClusterHandler) ListMine(c *gin.Context) {
	clusters, err := h.clusters.List()
	if err != nil {
		response.ServerError(c, err)
		return
	}
	out := make([]ClusterBrief, 0, len(clusters))
	for i := range clusters {
		out = append(out, toBrief(&clusters[i]))
	}
	response.OK(c, out)
}

// ConnectivityMine GET /my-clusters/connectivity?name= —— 「集群不可达」面板重新检测
func (h *ClusterHandler) ConnectivityMine(c *gin.Context) {
	cluster, err := h.clusters.TestConnectivity(c.Query("name"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, toBrief(cluster))
}

// Create 添加集群
func (h *ClusterHandler) Create(c *gin.Context) {
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "集群名称和 kubeconfig 不能为空")
		return
	}
	cluster, err := h.clusters.Create(strings.TrimSpace(req.Name), req.Kubeconfig)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, cluster)
}

// Update 更新集群 kubeconfig
func (h *ClusterHandler) Update(c *gin.Context) {
	name := c.Param("name")
	var req struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "请求体无效")
		return
	}
	if strings.TrimSpace(req.Kubeconfig) == "" {
		response.Fail(c, 400, 400, "kubeconfig 不能为空")
		return
	}
	cluster, err := h.clusters.Update(name, req.Kubeconfig)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, cluster)
}

// Delete 删除集群
func (h *ClusterHandler) Delete(c *gin.Context) {
	if err := h.clusters.Delete(c.Param("name")); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// UpdatePrometheus 更新集群 Prometheus 配置
func (h *ClusterHandler) UpdatePrometheus(c *gin.Context) {
	var req struct {
		PrometheusNamespace string `json:"prometheusNamespace"`
		PrometheusService   string `json:"prometheusService"`
		PrometheusPort      int    `json:"prometheusPort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "请求体无效")
		return
	}
	cluster, err := h.clusters.UpdatePrometheus(c.Param("name"), strings.TrimSpace(req.PrometheusNamespace), strings.TrimSpace(req.PrometheusService), req.PrometheusPort)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, cluster)
}

// Connectivity 测试连通性
func (h *ClusterHandler) Connectivity(c *gin.Context) {
	cluster, err := h.clusters.TestConnectivity(c.Param("name"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, cluster)
}

// UpdateGrafana 更新集群 Grafana 地址（iframe 直连内嵌用）
func (h *ClusterHandler) UpdateGrafana(c *gin.Context) {
	var req struct {
		GrafanaURL string `json:"grafanaURL"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "请求体无效")
		return
	}
	cluster, err := h.clusters.UpdateGrafana(c.Param("name"), req.GrafanaURL)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, cluster)
}
