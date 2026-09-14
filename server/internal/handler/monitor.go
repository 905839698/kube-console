package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// MonitorHandler Prometheus 监控接口
type MonitorHandler struct {
	clusters *service.ClusterManager
	monitor  *service.MonitorService
}

func NewMonitorHandler(clusters *service.ClusterManager) *MonitorHandler {
	return &MonitorHandler{clusters: clusters, monitor: service.NewMonitorService(clusters)}
}

// clusterAndRange 解析当前集群 + 时间范围（默认 6h）
func (h *MonitorHandler) clusterAndRange(c *gin.Context) (cluster *model.Cluster, r string, ok bool) {
	name := middleware.ClusterName(c)
	if name == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return nil, "", false
	}
	cl, err := h.clusters.GetRaw(name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil, "", false
	}
	r = c.Query("range")
	if r != "1h" && r != "6h" && r != "24h" {
		r = "6h"
	}
	return cl, r, true
}

func (h *MonitorHandler) clientOf(c *gin.Context, cl *model.Cluster) (client *kube.Client, ok bool) {
	client, err := h.clusters.ClientChecked(cl.Name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil, false
	}
	return client, true
}

// Overview 集群级监控
func (h *MonitorHandler) Overview(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.Overview(c.Request.Context(), client, cl, r)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}

// Query 通用 PromQL 查询（调试/排查用）?q=
func (h *MonitorHandler) Query(c *gin.Context) {
	cl, _, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	q := c.Query("q")
	if q == "" {
		response.Fail(c, 400, 400, "缺少 q 参数")
		return
	}
	res, err := h.monitor.Query(c.Request.Context(), client, service.PromConfigOf(cl), q)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, res)
}

// 时间范围 → 时长 / 查询步长（控制点数在数百个量级，兼顾精度与响应体大小）
var (
	rangeDurations = map[string]time.Duration{
		"1h":  1 * time.Hour,
		"6h":  6 * time.Hour,
		"24h": 24 * time.Hour,
	}
	rangeSteps = map[string]time.Duration{
		"1h":  15 * time.Second,
		"6h":  60 * time.Second,
		"24h": 300 * time.Second,
	}
)

// QueryRange 范围查询（PromQL 图形化展示）?q=&range=1h|6h|24h
func (h *MonitorHandler) QueryRange(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	q := c.Query("q")
	if q == "" {
		response.Fail(c, 400, 400, "缺少 q 参数")
		return
	}
	end := time.Now()
	res, err := h.monitor.QueryRange(c.Request.Context(), client, service.PromConfigOf(cl), q, end.Add(-rangeDurations[r]), end, rangeSteps[r])
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, res)
}

// Node 节点监控
func (h *MonitorHandler) Node(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.Node(c.Request.Context(), client, cl, c.Param("name"), r)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}

// Namespace 命名空间监控
func (h *MonitorHandler) Namespace(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.Namespace(c.Request.Context(), client, cl, c.Param("name"), r)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}

// Workload 工作负载监控
func (h *MonitorHandler) Workload(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.Workload(c.Request.Context(), client, cl, c.Query("kind"), c.Query("namespace"), c.Query("name"), r)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}

// Pod Pod 监控
func (h *MonitorHandler) Pod(c *gin.Context) {
	cl, r, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.Pod(c.Request.Context(), client, cl, c.Query("namespace"), c.Query("name"), r)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}

// PrometheusCheck 测试 Prometheus 连通性
func (h *MonitorHandler) PrometheusCheck(c *gin.Context) {
	cl, _, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	if err := h.monitor.TestConnectivity(c.Request.Context(), client, service.PromConfigOf(cl)); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"status": "ok", "prometheus": cl.PrometheusService})
}

// Alerts 告警列表（Prometheus 告警规则 + 活动告警实例）
func (h *MonitorHandler) Alerts(c *gin.Context) {
	cl, _, ok := h.clusterAndRange(c)
	if !ok {
		return
	}
	client, ok := h.clientOf(c, cl)
	if !ok {
		return
	}
	data, err := h.monitor.AlertRules(c.Request.Context(), client, cl)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, data)
}
