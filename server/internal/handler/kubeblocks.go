// kubeblocks.go KubeBlocks 数据库集群的 HTTP 接口。
// 权限一律按集群所在命名空间判定（复用三层 RBAC）：读=该 ns 有任意角色，
// 写/运维/删除=该 ns 可写；未装 KubeBlocks 的集群各接口降级为空列表或明确错误。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

type KBHandler struct {
	clusters *service.ClusterManager
	perm     *service.PermissionService
}

func NewKBHandler(clusters *service.ClusterManager, perm *service.PermissionService) *KBHandler {
	return &KBHandler{clusters: clusters, perm: perm}
}

// access 当前用户在当前集群的权限快照
func (h *KBHandler) access(c *gin.Context, cluster string) *service.Access {
	return h.perm.Access(c.Request.Context(), cluster, middleware.CurrentUser(c))
}

func (h *KBHandler) denyRead(c *gin.Context, ns string) bool {
	if !h.access(c, middleware.ClusterName(c)).CanReadNS(ns) {
		response.Fail(c, http.StatusNotFound, 404, "资源不存在或无权访问")
		return true
	}
	return false
}

func (h *KBHandler) denyWrite(c *gin.Context, ns string) bool {
	if !h.access(c, middleware.ClusterName(c)).CanWriteNS(ns) {
		response.Fail(c, http.StatusForbidden, 403, "需要命名空间 "+ns+" 的写权限（项目层 edit/admin）")
		return true
	}
	return false
}

// Engines GET /kb/engines —— 可安装引擎与拓扑（向导数据源）
func (h *KBHandler) Engines(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	engines, err := service.KBEngines(c.Request.Context(), cli)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"installed": len(engines) > 0, "engines": engines})
}

// List GET /kb/clusters?namespace= —— 只返回该用户可读 ns 的集群
func (h *KBHandler) List(c *gin.Context) {
	cli, cl, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	items, err := service.KBListClusters(c.Request.Context(), cli, ns)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	acc := h.access(c, cl)
	visible := items[:0]
	for _, it := range items {
		if acc.CanReadNS(it.Namespace) {
			visible = append(visible, it)
		}
	}
	response.OK(c, visible)
}

// Detail GET /kb/clusters/:namespace/:name
func (h *KBHandler) Detail(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	v, err := service.KBGetCluster(c.Request.Context(), cli, ns, name)
	if err != nil {
		kbFail(c, err, "集群不存在")
		return
	}
	response.OK(c, v)
}

// Create POST /kb/clusters {namespace, name, clusterDef, topology, components[]}
func (h *KBHandler) Create(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	var in service.KBCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	ns := c.Query("namespace")
	if ns == "" {
		response.Fail(c, 400, 400, "namespace 必填（集群落在该命名空间）")
		return
	}
	if h.denyWrite(c, ns) {
		return
	}
	if err := service.KBCreateCluster(c.Request.Context(), cli, ns, &in); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true, "namespace": ns, "cluster": in.Name})
}

// Delete DELETE /kb/clusters/:namespace/:name
func (h *KBHandler) Delete(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	if err := service.KBDeleteCluster(c.Request.Context(), cli, ns, name); err != nil {
		kbFail(c, err, "集群不存在")
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ListOps GET /kb/clusters/:namespace/:name/ops
func (h *KBHandler) ListOps(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	ops, err := service.KBListOps(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, ops)
}

// SubmitOps POST /kb/clusters/:namespace/:name/ops {type,component,...}
func (h *KBHandler) SubmitOps(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	var in service.KBOpsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	taskName, err := service.KBSubmitOps(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	// 立刻回读一次状态：集群上已有工单在跑时新工单会排队（Pending），
	// 不把 phase 带回来的话前端只显示"已下发"，用户看到的就是"点了没反应"
	res := gin.H{"name": taskName}
	if d, derr := service.KBGetOps(c.Request.Context(), cli, ns, taskName); derr == nil && d != nil {
		res["phase"] = d.Phase
		res["message"] = d.Message
	}
	response.OK(c, res)
}

// OpsDetail GET /kb/clusters/:namespace/:name/ops/:ops —— 工单详情（条件时间线/进度/变更前旧值）
func (h *KBHandler) OpsDetail(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("ops")
	if h.denyRead(c, ns) {
		return
	}
	d, err := service.KBGetOps(c.Request.Context(), cli, ns, name)
	if err != nil {
		kbFail(c, err, "工单不存在")
		return
	}
	response.OK(c, d)
}

// OpsCancel POST /kb/clusters/:namespace/:name/ops/:ops/cancel —— 取消进行中的工单（patch spec.cancel）
func (h *KBHandler) OpsCancel(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("ops")
	if h.denyWrite(c, ns) {
		return
	}
	if err := service.KBCancelOps(c.Request.Context(), cli, ns, name); err != nil {
		kbFail(c, err, "工单不存在")
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// Versions GET /kb/clusters/:namespace/:name/versions —— 各组件可升级版本链（ComponentVersion）
func (h *KBHandler) Versions(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	out, err := service.KBUpgradeCandidates(c.Request.Context(), cli, ns, name)
	if err != nil {
		kbFail(c, err, "集群不存在")
		return
	}
	response.OK(c, out)
}

// Connection GET /kb/clusters/:namespace/:name/connection —— 连接信息（端点/端口/用户名）
func (h *KBHandler) Connection(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	v, err := service.KBConnectionInfo(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, v)
}

// Events GET /kb/clusters/:namespace/:name/events —— 该集群资源的 Warning 事件
func (h *KBHandler) Events(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	events, err := service.KBEvents(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, events)
}

// ConfigSchema GET /kb/clusters/:namespace/:name/config-schema/:component —— 参数表单数据源
// （ParametersDefinition 的 schema + ComponentParameter 当前值；1.0 的 parameters 组契约）
func (h *KBHandler) ConfigSchema(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name, comp := c.Param("namespace"), c.Param("name"), c.Param("component")
	if h.denyRead(c, ns) {
		return
	}
	v, err := service.KBGetComponentConfig(c.Request.Context(), cli, ns, name, comp)
	if err != nil {
		kbFail(c, err, "组件不存在")
		return
	}
	response.OK(c, v)
}

// SubmitReconfigure POST /kb/clusters/:namespace/:name/config-schema/:component ——
// 表单化改参：本地按 schema 校验（不可变/未知键/枚举）后下发 Reconfiguring 工单
func (h *KBHandler) SubmitReconfigure(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name, comp := c.Param("namespace"), c.Param("name"), c.Param("component")
	if h.denyWrite(c, ns) {
		return
	}
	var req struct {
		Parameters map[string]string `json:"parameters"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	schema, err := service.KBGetComponentConfig(c.Request.Context(), cli, ns, name, comp)
	if err != nil {
		kbFail(c, err, "组件不存在")
		return
	}
	if err := service.KBValidateParams(schema, req.Parameters); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	taskName, err := service.KBSubmitOps(c.Request.Context(), cli, ns, name, &service.KBOpsInput{
		Type: "Reconfiguring", Component: comp, Parameters: req.Parameters,
	})
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": taskName})
}

// MonitorAccess POST /kb/clusters/:namespace/:name/monitor-access —— 一键接入 exporter 抓取
func (h *KBHandler) MonitorAccess(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	created, err := service.KBEnsureServiceMonitor(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"created": created})
}

// ConfigMaps GET /kb/clusters/:namespace/:name/configmaps —— 该集群渲染出的配置载体
func (h *KBHandler) ConfigMaps(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	list, err := cli.Clientset.CoreV1().ConfigMaps(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	out := []gin.H{}
	for _, cm := range list.Items {
		if !strings.HasPrefix(cm.Name, name+"-") || !kbConfigCandidate(cm.Name) {
			continue
		}
		keys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		out = append(out, gin.H{"name": cm.Name, "keys": keys})
	}
	response.OK(c, out)
}

// GetConfigMap GET /kb/clusters/:namespace/:name/configmaps/:cm
func (h *KBHandler) GetConfigMap(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name, cmName := c.Param("namespace"), c.Param("name"), c.Param("cm")
	if h.denyRead(c, ns) || !kbOwned(name, cmName, c) {
		return
	}
	cm, err := cli.Clientset.CoreV1().ConfigMaps(ns).Get(c.Request.Context(), cmName, metav1.GetOptions{})
	if err != nil {
		kbFail(c, err, "配置不存在")
		return
	}
	response.OK(c, gin.H{"name": cm.Name, "data": cm.Data})
}

// PutConfigMap PUT /kb/clusters/:namespace/:name/configmaps/:cm {data:{k:v}}
// 改完需自行下发 OpsRequest(Restart) 或 reconfigure 才会生效——KubeBlocks 不保证热加载。
func (h *KBHandler) PutConfigMap(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name, cmName := c.Param("namespace"), c.Param("name"), c.Param("cm")
	if h.denyWrite(c, ns) || !kbOwned(name, cmName, c) {
		return
	}
	var req struct {
		Data map[string]string `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Data) == 0 {
		response.Fail(c, 400, 400, "参数错误: data 不能为空")
		return
	}
	cm, err := cli.Clientset.CoreV1().ConfigMaps(ns).Get(c.Request.Context(), cmName, metav1.GetOptions{})
	if err != nil {
		kbFail(c, err, "配置不存在")
		return
	}
	cm.Data = req.Data
	if _, err := cli.Clientset.CoreV1().ConfigMaps(ns).Update(c.Request.Context(), cm, metav1.UpdateOptions{}); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true, "note": "配置已写入 ConfigMap；需重启组件或下发 reconfigure 才会生效"})
}

// Accounts GET /kb/clusters/:namespace/:name/accounts —— 只回键名，绝不回明文值
func (h *KBHandler) Accounts(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	list, err := cli.Clientset.CoreV1().Secrets(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	out := []gin.H{}
	for _, s := range list.Items {
		if !strings.HasPrefix(s.Name, name+"-") {
			continue
		}
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		out = append(out, gin.H{"name": s.Name, "type": string(s.Type), "keys": keys})
	}
	response.OK(c, out)
}

// ---------- 辅助 ----------

// ctxClient 统一取集群与客户端
func (h *KBHandler) ctxClient(c *gin.Context) (*kube.Client, string, bool) {
	cluster := middleware.ClusterName(c)
	if cluster == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return nil, "", false
	}
	cli, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil, "", false
	}
	return cli, cluster, true
}

// kbConfigCandidate 排除非用户配置的载体：脚本与环境变量类 ConfigMap 不该在页面上改
func kbConfigCandidate(cmName string) bool {
	for _, suffix := range []string{"-scripts", "-env", "-history", "-leader"} {
		if strings.HasSuffix(cmName, suffix) {
			return false
		}
	}
	return true
}

// kbOwned 限定 ConfigMap 必须属于该集群，避免跨集群改到别人的配置
func kbOwned(cluster, cmName string, c *gin.Context) bool {
	if !strings.HasPrefix(cmName, cluster+"-") {
		response.Fail(c, 400, 400, "该配置不属于此集群")
		return false
	}
	return true
}

func kbFail(c *gin.Context, err error, notFoundMsg string) {
	if apierrors.IsNotFound(err) {
		response.Fail(c, http.StatusNotFound, 404, notFoundMsg)
		return
	}
	response.Fail(c, 500, 500, err.Error())
}
