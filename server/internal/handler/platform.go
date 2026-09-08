// 平台扩展 handler：日志检索/日志源配置、通知渠道与记录、镜像仓库、用量报表、证书巡检、备份概览、权限逆查、用户组、API Token
package handler

import (
	"context"
	"crypto/sha256"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"net/http"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// PlatformHandler 平台扩展功能
type PlatformHandler struct {
	db         *gorm.DB
	clusters   *service.ClusterManager
	monitor    *service.MonitorService
	debugImage string
}

// NewPlatformHandler 创建
func NewPlatformHandler(db *gorm.DB, clusters *service.ClusterManager, debugImage string) *PlatformHandler {
	return &PlatformHandler{db: db, clusters: clusters, monitor: service.NewMonitorService(clusters), debugImage: debugImage}
}

// kubeClient 当前集群的 kube.Client（X-Cluster 头）
func (h *PlatformHandler) kubeClient(c *gin.Context) *kube.Client {
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

// ------------------- 日志检索 -------------------

// LogSearch POST /logs/search {namespace,pod,container,keyword,level,minutes,page,size}
func (h *PlatformHandler) LogSearch(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	if cluster == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return
	}
	var q service.LogQuery
	if err := c.ShouldBindJSON(&q); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	var src model.LogSource
	if err := h.db.Where("cluster_name = ?", cluster).First(&src).Error; err != nil {
		response.Fail(c, 400, 400, "该集群未配置日志源（平台管理-日志源配置）")
		return
	}
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	result, err := service.SearchLogs(ctx, client, &src, q)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, result)
}

// ListLogSources GET /logsources
func (h *PlatformHandler) ListLogSources(c *gin.Context) {
	var items []model.LogSource
	h.db.Find(&items)
	response.OK(c, items)
}

// SaveLogSource POST/PUT /logsources
func (h *PlatformHandler) SaveLogSource(c *gin.Context) {
	var src model.LogSource
	if err := c.ShouldBindJSON(&src); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if src.Namespace == "" || src.Service == "" || src.Port == 0 {
		response.Fail(c, 400, 400, "命名空间/服务名/端口必填")
		return
	}
	var existing model.LogSource
	if err := h.db.Where("cluster_name = ?", src.ClusterName).First(&existing).Error; err == nil {
		src.ID = existing.ID
		src.CreatedAt = existing.CreatedAt
		// 密码留空表示保持不变
		if src.Password == "" {
			src.Password = existing.Password
		}
		h.db.Save(&src)
	} else {
		h.db.Create(&src)
	}
	response.OK(c, src)
}

// DeleteLogSource DELETE /logsources/:cluster
func (h *PlatformHandler) DeleteLogSource(c *gin.Context) {
	h.db.Where("cluster_name = ?", c.Param("cluster")).Delete(&model.LogSource{})
	response.OK(c, nil)
}

// TestLogSource POST /logsources/test
func (h *PlatformHandler) TestLogSource(c *gin.Context) {
	var src model.LogSource
	if err := c.ShouldBindJSON(&src); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if src.Password == "" {
		var existing model.LogSource
		if h.db.Where("cluster_name = ?", src.ClusterName).First(&existing).Error == nil {
			src.Password = existing.Password
		}
	}
	client, err := h.clusters.ClientChecked(src.ClusterName)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if err := service.TestLogSource(ctx, client, &src); err != nil {
		response.Fail(c, 400, 400, "连通失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ------------------- 通知渠道 -------------------

// ListChannels GET /notify/channels
func (h *PlatformHandler) ListChannels(c *gin.Context) {
	var items []model.NotifyChannel
	h.db.Find(&items)
	response.OK(c, items)
}

// SaveChannel POST/PUT /notify/channels
func (h *PlatformHandler) SaveChannel(c *gin.Context) {
	var ch model.NotifyChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if ch.Webhook == "" {
		response.Fail(c, 400, 400, "webhook 地址必填")
		return
	}
	var existing model.NotifyChannel
	if err := h.db.Where("name = ?", ch.Name).First(&existing).Error; err == nil {
		ch.ID = existing.ID
		ch.CreatedAt = existing.CreatedAt
		if ch.Secret == "" {
			ch.Secret = existing.Secret
		}
		h.db.Save(&ch)
	} else {
		h.db.Create(&ch)
	}
	response.OK(c, ch)
}

// DeleteChannel DELETE /notify/channels/:id
func (h *PlatformHandler) DeleteChannel(c *gin.Context) {
	h.db.Delete(&model.NotifyChannel{}, c.Param("id"))
	response.OK(c, nil)
}

// TestChannel POST /notify/channels/:id/test —— 发送测试消息
func (h *PlatformHandler) TestChannel(c *gin.Context) {
	var ch model.NotifyChannel
	if err := h.db.First(&ch, c.Param("id")).Error; err != nil {
		response.Fail(c, 404, 404, "渠道不存在")
		return
	}
	msg := fmt.Sprintf("### 测试通知\n\n- **时间**: %s\n- **内容**: 这是一条来自 kube-console 的测试消息，收到即表示渠道配置成功。", time.Now().Format("2006-01-02 15:04:05"))
	if err := service.SendNotification(ch, "kube-console 测试通知", msg); err != nil {
		response.Fail(c, 400, 400, "发送失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// NotifyLogs GET /notify/logs?page=&size=
func (h *PlatformHandler) NotifyLogs(c *gin.Context) {
	page, size := atoiDefault(c.Query("page"), 1), atoiDefault(c.Query("size"), 50)
	var total int64
	var items []model.NotifyLog
	h.db.Model(&model.NotifyLog{}).Count(&total)
	h.db.Order("sent_at DESC").Offset((page - 1) * size).Limit(size).Find(&items)
	response.OK(c, gin.H{"total": total, "items": items, "page": page, "size": size})
}

// ------------------- 镜像仓库（Harbor） -------------------

func (h *PlatformHandler) registryClient() (*service.RegistryClient, error) {
	var cfg model.RegistryConfig
	if err := h.db.First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("未配置镜像仓库（平台管理-镜像仓库设置）")
	}
	return service.NewRegistryClient(cfg.URL, cfg.Username, cfg.Password, cfg.Insecure)
}

// GetRegistryConfig GET /registry/config
func (h *PlatformHandler) GetRegistryConfig(c *gin.Context) {
	var cfg model.RegistryConfig
	if err := h.db.First(&cfg).Error; err != nil {
		response.OK(c, gin.H{})
		return
	}
	response.OK(c, gin.H{"url": cfg.URL, "username": cfg.Username, "insecure": cfg.Insecure})
}

// SaveRegistryConfig POST /registry/config
func (h *PlatformHandler) SaveRegistryConfig(c *gin.Context) {
	var in struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Insecure *bool  `json:"insecure"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.URL == "" {
		response.Fail(c, 400, 400, "参数错误：URL 必填")
		return
	}
	var cfg model.RegistryConfig
	err := h.db.First(&cfg).Error
	isNew := err != nil
	if isNew {
		cfg = model.RegistryConfig{URL: in.URL, Username: in.Username, Insecure: true}
	} else {
		cfg.URL = in.URL
		cfg.Username = in.Username
		if in.Password != "" {
			cfg.Password = in.Password
		}
		if in.Insecure != nil {
			cfg.Insecure = *in.Insecure
		}
	}
	if isNew || in.Password != "" {
		cfg.Password = in.Password
	}
	if isNew {
		h.db.Create(&cfg)
	} else {
		h.db.Save(&cfg)
	}
	response.OK(c, gin.H{"ok": true})
}

// TestRegistry POST /registry/test
func (h *PlatformHandler) TestRegistry(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	projects, err := client.ListProjects(ctx, "")
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true, "projects": len(projects)})
}

// RegistryProjects GET /registry/projects
func (h *PlatformHandler) RegistryProjects(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	projects, err := client.ListProjects(ctx, c.Query("search"))
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, projects)
}

// RegistryRepos GET /registry/projects/:project/repos
func (h *PlatformHandler) RegistryRepos(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	repos, err := client.ListRepositories(ctx, c.Param("project"), c.Query("search"))
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, repos)
}

// RegistryTags GET /registry/tags?project=&repo=
func (h *PlatformHandler) RegistryTags(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	project, repo := c.Query("project"), c.Query("repo")
	if project == "" || repo == "" {
		response.Fail(c, 400, 400, "缺少 project / repo 参数")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	artifacts, err := client.ListArtifacts(ctx, project, repo)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, artifacts)
}

// ------------------- 用量报表 -------------------

// UsageReport GET /usage?days=7
func (h *PlatformHandler) UsageReport(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	days := atoiDefault(c.Query("days"), 7)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	rows, err := service.NamespaceUsage(ctx, client, h.monitor, service.DefaultPromConfig(), days)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, rows)
}

// ------------------- 证书巡检 -------------------

// ClusterCerts GET /clusters/certs —— 全部集群 apiserver 证书剩余天数
func (h *PlatformHandler) ClusterCerts(c *gin.Context) {
	var list []model.Cluster
	h.db.Find(&list)
	out := []gin.H{}
	for _, cl := range list {
		info, err := service.InspectClusterCert(cl.KubeconfigServerHost())
		item := gin.H{"cluster": cl.Name, "status": cl.Status}
		if err != nil {
			item["error"] = err.Error()
		} else {
			item["cert"] = info
		}
		out = append(out, item)
	}
	response.OK(c, out)
}

// ------------------- 备份概览（Velero 只读） -------------------

// Backups GET /backups —— 读取 velero 备份/计划（未安装则返回空并提示）
func (h *PlatformHandler) Backups(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	k8s := service.NewK8sService(h.debugImage)
	gvrBackups := schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backups"}
	backups, err := k8s.ListGenericGVR(c.Request.Context(), client, gvrBackups, "", "", "")
	if err != nil {
		response.OK(c, gin.H{"installed": false, "items": []any{}})
		return
	}
	gvrSchedules := schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "schedules"}
	schedules, _ := k8s.ListGenericGVR(c.Request.Context(), client, gvrSchedules, "", "", "")
	response.OK(c, gin.H{"installed": true, "items": backups, "schedules": schedules})
}

func atoiDefault(s string, def int) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}

// ------------------- 用户组 -------------------

// ListGroups GET /groups
func (h *PlatformHandler) ListGroups(c *gin.Context) {
	var groups []model.UserGroup
	h.db.Find(&groups)
	// 附带各组用户数
	type row struct {
		model.UserGroup
		Users int64 `json:"users"`
	}
	out := []row{}
	for _, g := range groups {
		var cnt int64
		h.db.Model(&model.User{}).Where("group_id = ?", g.ID).Count(&cnt)
		out = append(out, row{UserGroup: g, Users: cnt})
	}
	response.OK(c, out)
}

// SaveGroup POST/PUT /groups
func (h *PlatformHandler) SaveGroup(c *gin.Context) {
	var g model.UserGroup
	if err := c.ShouldBindJSON(&g); err != nil || g.Name == "" {
		response.Fail(c, 400, 400, "组名必填")
		return
	}
	var existing model.UserGroup
	if err := h.db.Where("name = ?", g.Name).First(&existing).Error; err == nil {
		g.ID = existing.ID
		g.CreatedAt = existing.CreatedAt
		h.db.Save(&g)
	} else {
		h.db.Create(&g)
	}
	response.OK(c, g)
}

// DeleteGroup DELETE /groups/:id
func (h *PlatformHandler) DeleteGroup(c *gin.Context) {
	id := c.Param("id")
	h.db.Model(&model.User{}).Where("group_id = ?", id).Update("group_id", 0)
	h.db.Delete(&model.UserGroup{}, id)
	response.OK(c, nil)
}

// SetUserGroup PUT /users/:id/group {groupId}
func (h *PlatformHandler) SetUserGroup(c *gin.Context) {
	var in struct {
		GroupID uint `json:"groupId"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if err := h.db.Model(&model.User{}).Where("id = ?", c.Param("id")).Update("group_id", in.GroupID).Error; err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, nil)
}

// ------------------- API Token -------------------

// genToken 生成 kc_pat_ 前缀随机令牌
func genToken() (plain string, hash string) {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	plain = "kc_pat_" + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(sum[:])
}

// ListTokens GET /tokens —— 当前用户的 token 列表
func (h *PlatformHandler) ListTokens(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var items []model.ApiToken
	h.db.Where("user_id = ? AND revoked = ?", uid, false).Order("created_at DESC").Find(&items)
	response.OK(c, items)
}

// CreateToken POST /tokens {name, expiresInDays} —— 明文仅返回一次
func (h *PlatformHandler) CreateToken(c *gin.Context) {
	var in struct {
		Name          string `json:"name" binding:"required"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "名称必填")
		return
	}
	plain, hash := genToken()
	t := model.ApiToken{
		Name: in.Name, TokenHash: hash,
		UserID: middleware.CurrentUserID(c), Username: middleware.CurrentUser(c),
	}
	if in.ExpiresInDays > 0 {
		exp := time.Now().AddDate(0, 0, in.ExpiresInDays)
		t.ExpiresAt = &exp
	}
	if err := h.db.Create(&t).Error; err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"id": t.ID, "name": t.Name, "token": plain, "expiresAt": t.ExpiresAt})
}

// RevokeToken DELETE /tokens/:id
func (h *PlatformHandler) RevokeToken(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var t model.ApiToken
	if err := h.db.First(&t, c.Param("id")).Error; err == nil {
		if t.UserID == uid || middleware.CurrentUser(c) == "admin" || middleware.IsAdmin(h.db, "admin", uid, middleware.CurrentUser(c)) {
			h.db.Model(&t).Update("revoked", true)
		}
	}
	response.OK(c, nil)
}

// ------------------- 权限逆查 -------------------

// UserPermissions GET /authz/user-permissions?username=
func (h *PlatformHandler) UserPermissions(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	username := c.Query("username")
	if username == "" {
		response.Fail(c, 400, 400, "缺少 username 参数")
		return
	}
	// 用户的组
	var user model.User
	groups := []string{}
	if h.db.Where("username = ?", username).First(&user).Error == nil && user.GroupID > 0 {
		var g model.UserGroup
		if h.db.First(&g, user.GroupID).Error == nil {
			groups = append(groups, g.Name)
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	perms, err := service.CollectUserPermissions(ctx, client, username, groups)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	response.OK(c, gin.H{"username": username, "groups": groups, "permissions": perms})
}

// ------------------- 镜像扫描与漏洞报告 -------------------

// RegistryScan GET（查状态）/ POST（触发） /registry/scan?project=&repo=&reference=
func (h *PlatformHandler) RegistryScan(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	project, repo, ref := c.Query("project"), c.Query("repo"), c.Query("reference")
	if project == "" || repo == "" || ref == "" {
		response.Fail(c, 400, 400, "缺少 project / repo / reference 参数")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if c.Request.Method == http.MethodPost {
		if err := client.TriggerScan(ctx, project, repo, ref); err != nil {
			response.Fail(c, 502, 502, err.Error())
			return
		}
		response.OK(c, gin.H{"triggered": true})
		return
	}
	ov, err := client.GetArtifactScan(ctx, project, repo, ref)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, ov)
}

// RegistryVulns GET /registry/vulns?project=&repo=&reference= —— CVE 明细
func (h *PlatformHandler) RegistryVulns(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	project, repo, ref := c.Query("project"), c.Query("repo"), c.Query("reference")
	if project == "" || repo == "" || ref == "" {
		response.Fail(c, 400, 400, "缺少 project / repo / reference 参数")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	report, err := client.GetVulnerabilities(ctx, project, repo, ref)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, report)
}

// ImageTags GET /registry/image-tags?image= —— 可视化编辑工作负载时的可选 Tag（登录即可用）
func (h *PlatformHandler) ImageTags(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	tags, err := client.ImageTags(ctx, c.Query("image"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"tags": tags})
}

// ImageVulns GET /registry/image-vulns?image= —— Pod/容器维度的 CVE 信息
func (h *PlatformHandler) ImageVulns(c *gin.Context) {
	client, err := h.registryClient()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	ov, report, err := client.ImageVulns(ctx, c.Query("image"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"overview": ov, "report": report})
}
