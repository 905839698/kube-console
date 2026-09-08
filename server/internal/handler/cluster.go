package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

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

// List 集群列表
func (h *ClusterHandler) List(c *gin.Context) {
	clusters, err := h.clusters.List()
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, clusters)
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
