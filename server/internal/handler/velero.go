// velero.go 备份概览接口（Velero）。读 = 登录即可（与旧 /backups 口径一致，
// 数据本身按用户 kubeconfig 的 RBAC 过滤，看不到的列表会带 partial 说明）；
// 写（发起恢复 / 启停计划 / 删备份）= 平台管理员——恢复是集群级重建动作，
// 删备份连带对象存储里的数据，不随命名空间授权下放。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// VeleroOverview GET /velero/overview —— 备份任务 / 计划 / 恢复三张列表一次拉全。
func (h *PlatformHandler) VeleroOverview(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	out, err := k8s.VeleroOverview(c.Request.Context(), client)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// VeleroEvents GET /velero/events?namespace=&kind=&name= —— 关联事件（失败信息）。
func (h *PlatformHandler) VeleroEvents(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, kind, name := c.Query("namespace"), c.Query("kind"), c.Query("name")
	if ns == "" || name == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: namespace/name 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	evs, err := k8s.VeleroEvents(c.Request.Context(), client, ns, kind, name)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	response.OK(c, evs)
}

// VeleroVolumes GET /velero/volumes?backup= —— 该备份的卷备份明细（PodVolumeBackup）。
func (h *PlatformHandler) VeleroVolumes(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	backup := c.Query("backup")
	if backup == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: backup 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	out, err := k8s.VeleroBackupVolumes(c.Request.Context(), client, backup)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	response.OK(c, out)
}

// VeleroBackupCreate POST /velero/backups —— 手动发起一次备份（admin）。
func (h *PlatformHandler) VeleroBackupCreate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var in service.VeleroBackupCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	name, err := k8s.VeleroCreateBackup(c.Request.Context(), client, &in)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": name})
}

// VeleroScheduleCreate POST /velero/schedules —— 新建备份计划（admin）。
func (h *PlatformHandler) VeleroScheduleCreate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var in service.VeleroScheduleCreateInput
	if err := c.ShouldBindJSON(&in); err != nil || in.Name == "" || in.Cron == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: name/cron 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	name, err := k8s.VeleroCreateSchedule(c.Request.Context(), client, &in)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": name})
}

// VeleroRestoreCreate POST /velero/restores —— 用备份发起恢复（admin）。
func (h *PlatformHandler) VeleroRestoreCreate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var in service.VeleroRestoreInput
	if err := c.ShouldBindJSON(&in); err != nil || in.BackupName == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: namespace/backupName 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	name, err := k8s.VeleroCreateRestore(c.Request.Context(), client, &in)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"name": name})
}

// VeleroSchedulePause PUT /velero/schedules/:namespace/:name/pause —— 启停计划（admin）。
func (h *PlatformHandler) VeleroSchedulePause(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: paused 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	if err := k8s.VeleroSetSchedulePaused(c.Request.Context(), client, c.Param("namespace"), c.Param("name"), req.Paused); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// VeleroScheduleUpdate PUT /velero/schedules/:namespace/:name —— 编辑计划（admin）。
// 整体替换 cron/paused/template（Velero 对已存在的 Schedule 用最新模板生成后续备份）。
func (h *PlatformHandler) VeleroScheduleUpdate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var in service.VeleroScheduleUpdateInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Cron) == "" {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: cron 必填")
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	if err := k8s.VeleroUpdateSchedule(c.Request.Context(), client, c.Param("namespace"), c.Param("name"), &in); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// VeleroScheduleDelete DELETE /velero/schedules/:namespace/:name —— 删计划（admin；不连带历史备份）。
func (h *PlatformHandler) VeleroScheduleDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	if err := k8s.VeleroDeleteSchedule(c.Request.Context(), client, c.Param("namespace"), c.Param("name")); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// VeleroBackupDelete DELETE /velero/backups/:namespace/:name —— 删备份（admin；连带远端备份数据）。
func (h *PlatformHandler) VeleroBackupDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	if err := k8s.VeleroDeleteBackup(c.Request.Context(), client, c.Param("namespace"), c.Param("name")); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// VeleroRestoreDelete DELETE /velero/restores/:namespace/:name —— 删恢复任务记录（admin）。
func (h *PlatformHandler) VeleroRestoreDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	if err := k8s.VeleroDeleteRestore(c.Request.Context(), client, c.Param("namespace"), c.Param("name")); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}
