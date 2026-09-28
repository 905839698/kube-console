// kbbackup.go 数据库备份恢复的 HTTP 接口。权限口径与其它 KB 接口一致：
// 读 = 集群所在 ns 可读，写（建/删备份、改计划、恢复）= 该 ns 可写。
// BackupRepo 是集群级资产，只读透出（创建仓库涉及存储供应商凭证，不在控制台范围内）。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// BackupRepos GET /kb/repos —— 备份仓库列表（集群级；空列表 = 未配置备份目的地）
func (h *KBHandler) BackupRepos(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	repos, err := service.KBListBackupRepos(c.Request.Context(), cli)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, repos)
}

// BackupPolicies GET /kb/clusters/:namespace/:name/backup/policies —— 各组件备份策略与方法
func (h *KBHandler) BackupPolicies(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	out, err := service.KBListBackupPolicies(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// BackupSchedule GET /kb/clusters/:namespace/:name/backup/schedule —— 备份计划
func (h *KBHandler) BackupScheduleGet(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	out, err := service.KBGetBackupSchedule(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// BackupSchedulePut PUT /kb/clusters/:namespace/:name/backup/schedule —— 启停/改 cron/改保留期
func (h *KBHandler) BackupSchedulePut(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	var req struct {
		Component string                      `json:"component"`
		Items     []service.KBScheduleItem    `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Component == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: component 必填")
		return
	}
	if err := service.KBUpdateBackupSchedule(c.Request.Context(), cli, ns, name, req.Component, req.Items); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// Backups GET /kb/clusters/:namespace/:name/backups —— 备份记录列表
func (h *KBHandler) Backups(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	out, err := service.KBListBackups(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// BackupCreate POST /kb/clusters/:namespace/:name/backups —— 手动触发一次备份
func (h *KBHandler) BackupCreate(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	var in service.KBBackupCreateInput
	if err := c.ShouldBindJSON(&in); err != nil || in.Component == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: component 必填")
		return
	}
	backupName, err := service.KBCreateBackup(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": backupName})
}

// BackupDetail GET /kb/clusters/:namespace/:name/backups/:backup —— 备份详情（进度/各 action）
func (h *KBHandler) BackupDetail(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, backup := c.Param("namespace"), c.Param("backup")
	if h.denyRead(c, ns) {
		return
	}
	d, err := service.KBGetBackup(c.Request.Context(), cli, ns, backup)
	if err != nil {
		kbFail(c, err, "备份不存在")
		return
	}
	response.OK(c, d)
}

// BackupDelete DELETE /kb/clusters/:namespace/:name/backups/:backup
func (h *KBHandler) BackupDelete(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, backup := c.Param("namespace"), c.Param("backup")
	if h.denyWrite(c, ns) {
		return
	}
	if err := service.KBDeleteBackup(c.Request.Context(), cli, ns, backup); err != nil {
		kbFail(c, err, "备份不存在")
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// RestoreCreate POST /kb/clusters/:namespace/:name/backup/restore —— 用备份恢复出新集群
func (h *KBHandler) RestoreCreate(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	var in service.KBRestoreInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	created, err := service.KBRestoreToNewCluster(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"cluster": created})
}

// Restores GET /kb/clusters/:namespace/:name/backup/restores —— 恢复任务记录
func (h *KBHandler) Restores(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	out, err := service.KBListRestores(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// Rebuild POST /kb/clusters/:namespace/:name/backup/rebuild —— 用备份重建组件实例（RebuildInstance 工单）
func (h *KBHandler) Rebuild(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyWrite(c, ns) {
		return
	}
	var req struct {
		Component  string `json:"component"`
		BackupName string `json:"backupName"`
		InPlace    bool   `json:"inPlace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Component == "" || req.BackupName == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: component 与 backupName 必填")
		return
	}
	taskName, err := service.KBSubmitOps(c.Request.Context(), cli, ns, name, &service.KBOpsInput{
		Type: "RebuildInstance", Component: req.Component, BackupName: req.BackupName, InPlace: req.InPlace,
	})
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": taskName})
}
