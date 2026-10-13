// kbrepo.go 备份仓库（BackupRepo）管理接口。仓库是集群级资产、不属于任何命名空间，
// 所以权限不按 ns 收口：读 = 登录用户（与只读的 /kb/repos 同口径），
// 写 = 集群层可写（cluster-admin / platform-admin）。
// 存储凭证（AK/SK）只在集群里以 Secret 形式被引用，控制台既不落库也不回显。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// denyClusterWrite 集群级写权限闸门（BackupRepo 是集群级资源）
func (h *KBHandler) denyClusterWrite(c *gin.Context) bool {
	if !h.access(c, middleware.ClusterName(c)).CanWriteCluster() {
		response.Fail(c, http.StatusForbidden, 403, "备份仓库是集群级资源，需要集群层写权限（cluster-admin / platform-admin）")
		return true
	}
	return false
}

// StorageProviders GET /kb/storage-providers —— provider 与其参数/凭证字段清单（仓库表单数据源）
func (h *KBHandler) StorageProviders(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	providers, err := service.KBStorageProviders(c.Request.Context(), cli)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, providers)
}

// CredentialSecrets GET /kb/repo-secrets?namespace=&provider= —— 键齐全的凭证 Secret（只回名字）
func (h *KBHandler) CredentialSecrets(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	if ns == "" {
		response.Fail(c, http.StatusBadRequest, 400, "需要 namespace 参数")
		return
	}
	if h.denyRead(c, ns) {
		return
	}
	secrets, err := service.KBCredentialSecrets(c.Request.Context(), cli, ns, c.Query("provider"))
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, secrets)
}

// BackupRepoDetail GET /kb/repos/:name —— 仓库详情（编辑回填 + 状态条件）
func (h *KBHandler) BackupRepoDetail(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	d, err := service.KBGetBackupRepo(c.Request.Context(), cli, c.Param("name"))
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, d)
}

// BackupRepoCreate POST /kb/repos —— 建仓库（S3 等对象存储）
func (h *KBHandler) BackupRepoCreate(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	if h.denyClusterWrite(c) {
		return
	}
	var in service.KBRepoInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := service.KBCreateBackupRepo(c.Request.Context(), cli, in); err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"name": in.Name})
}

// BackupRepoUpdate PUT /kb/repos/:name —— 改仓库（provider 不可改）
func (h *KBHandler) BackupRepoUpdate(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	if h.denyClusterWrite(c) {
		return
	}
	var in service.KBRepoInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "请求体不是合法 JSON: "+err.Error())
		return
	}
	// 路径里的名字是权威，避免表单被改成另一个仓库名去覆盖别人的配置
	in.Name = c.Param("name")
	if err := service.KBUpdateBackupRepo(c.Request.Context(), cli, in); err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"name": in.Name})
}

// BackupRepoDelete DELETE /kb/repos/:name —— 删仓库
func (h *KBHandler) BackupRepoDelete(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	if h.denyClusterWrite(c) {
		return
	}
	if err := service.KBDeleteBackupRepo(c.Request.Context(), cli, c.Param("name")); err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"deleted": c.Param("name")})
}
