package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// HelmHandler Helm release 与 chart 仓库接口
type HelmHandler struct {
	clusters *service.ClusterManager
	helm     *service.HelmService
}

func NewHelmHandler(clusters *service.ClusterManager, helm *service.HelmService) *HelmHandler {
	return &HelmHandler{clusters: clusters, helm: helm}
}

// client 解析 X-Cluster 头并返回集群客户端，失败时写入响应并返回 nil
func (h *HelmHandler) client(c *gin.Context) *kube.Client {
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

// nsQuery 读取 namespace 查询参数（空值透传给 service，由 splitNamespaces 处理）
func nsQuery(c *gin.Context) string {
	return c.Query("namespace")
}

// ------------------- Release 查询 -------------------

// ListReleases GET /helm/releases?namespace=&search=
func (h *HelmHandler) ListReleases(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.helm.ListReleases(c.Request.Context(), client, nsQuery(c), c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// ReleaseInfo GET /helm/releases/:name/info?namespace=
func (h *HelmHandler) ReleaseInfo(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	name, ns := c.Param("name"), nsQuery(c)
	info, err := h.helm.ReleaseInfo(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, info)
}

// ReleaseHistory GET /helm/releases/:name/history?namespace=
func (h *HelmHandler) ReleaseHistory(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	name, ns := c.Param("name"), nsQuery(c)
	items, err := h.helm.ReleaseHistory(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// ------------------- Release 操作 -------------------

// UninstallRelease DELETE /helm/releases/:name?namespace=&keepHistory=
func (h *HelmHandler) UninstallRelease(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	name, ns := c.Param("name"), nsQuery(c)
	keep := c.Query("keepHistory") == "true"
	if err := h.helm.UninstallRelease(c.Request.Context(), client, ns, name, keep); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// RollbackRelease PUT /helm/releases/:name/rollback?namespace=&revision=&wait=
func (h *HelmHandler) RollbackRelease(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	name, ns := c.Param("name"), nsQuery(c)
	revision := 0
	if v := c.Query("revision"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			revision = n
		}
	}
	wait := c.Query("wait") == "true"
	if err := h.helm.RollbackRelease(c.Request.Context(), client, ns, name, revision, wait); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// UpgradeRelease PUT /helm/releases/:name/upgrade?namespace=
func (h *HelmHandler) UpgradeRelease(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	name, ns := c.Param("name"), nsQuery(c)
	var p service.UpgradeParams
	if err := c.ShouldBindJSON(&p); err != nil {
		response.Fail(c, 400, 400, "请求体解析失败: "+err.Error())
		return
	}
	info, err := h.helm.UpgradeRelease(c.Request.Context(), client, ns, name, p)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, info)
}

// InstallRelease POST /helm/releases
func (h *HelmHandler) InstallRelease(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var p service.InstallParams
	if err := c.ShouldBindJSON(&p); err != nil {
		response.Fail(c, 400, 400, "请求体解析失败: "+err.Error())
		return
	}
	if p.RepoName == "" || p.Chart == "" {
		response.Fail(c, 400, 400, "缺少仓库名或 chart 名称")
		return
	}
	info, err := h.helm.InstallRelease(c.Request.Context(), client, p)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, info)
}

// ------------------- Chart 仓库管理（集群无关，存 db 全局） -------------------

type repoCreateReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type repoUpdateReq struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ListRepos GET /helm/repos
func (h *HelmHandler) ListRepos(c *gin.Context) {
	items, err := h.helm.ListRepos()
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, items)
}

// AddRepo POST /helm/repos
func (h *HelmHandler) AddRepo(c *gin.Context) {
	var req repoCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "请求体解析失败: "+err.Error())
		return
	}
	if req.Name == "" || req.URL == "" {
		response.Fail(c, 400, 400, "仓库名称和 URL 不能为空")
		return
	}
	repo, err := h.helm.AddRepo(req.Name, req.URL, req.Username, req.Password)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, repo)
}

// UpdateRepo PUT /helm/repos/:id
func (h *HelmHandler) UpdateRepo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 400, 400, "无效的仓库 ID")
		return
	}
	var req repoUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "请求体解析失败: "+err.Error())
		return
	}
	repo, err := h.helm.UpdateRepo(uint(id), req.URL, req.Username, req.Password)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, repo)
}

// RemoveRepo DELETE /helm/repos/:id
func (h *HelmHandler) RemoveRepo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 400, 400, "无效的仓库 ID")
		return
	}
	if err := h.helm.RemoveRepo(uint(id)); err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, nil)
}

// RefreshRepo PUT /helm/repos/:id/refresh
func (h *HelmHandler) RefreshRepo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 400, 400, "无效的仓库 ID")
		return
	}
	count, err := h.helm.RefreshRepo(uint(id))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"chartCount": count})
}

// RepoCharts GET /helm/repos/:id/charts?search=
// ReleaseUpgradeValues GET /helm/releases/:name/upgrade-values?namespace= —— 升级弹窗预填
// （用户 values 优先；为空回退 release 内 chart 默认值；不依赖 chart 源仓库注册）
func (h *HelmHandler) ReleaseUpgradeValues(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	config, chartValues, err := h.helm.ReleaseUpgradeValues(c.Request.Context(), client, nsQuery(c), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"config": config, "chartValues": chartValues})
}

// ChartValues GET /helm/charts/values?repo=&chart=&version= —— chart 默认 values（安装/升级弹窗预填）
func (h *HelmHandler) ChartValues(c *gin.Context) {
	repoName := c.Query("repo")
	chartName := c.Query("chart")
	if repoName == "" || chartName == "" {
		response.Fail(c, 400, 400, "repo/chart 必填")
		return
	}
	v, err := h.helm.ChartValues(repoName, chartName, c.Query("version"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"values": v})
}

func (h *HelmHandler) RepoCharts(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 400, 400, "无效的仓库 ID")
		return
	}
	items, err := h.helm.RepoCharts(uint(id), c.Query("search"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}
