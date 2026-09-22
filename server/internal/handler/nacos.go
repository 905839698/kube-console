// Nacos 微服务集成接口：按集群配置 CRUD、同步状态/手动同步、密码轮换、Nacos 资源浏览与配置管理
package handler

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

type NacosHandler struct {
	db       *gorm.DB
	clusters *service.ClusterManager
	nacos    *service.NacosService
}

func NewNacosHandler(db *gorm.DB, clusters *service.ClusterManager, nacos *service.NacosService) *NacosHandler {
	return &NacosHandler{db: db, clusters: clusters, nacos: nacos}
}

func (h *NacosHandler) cfgOf(clusterName string) (*model.NacosConfig, error) {
	var cfg model.NacosConfig
	if err := h.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ------------------- 连接配置（admin） -------------------

// ListNacosConfigs GET /nacos/config
func (h *NacosHandler) ListNacosConfigs(c *gin.Context) {
	var items []model.NacosConfig
	h.db.Find(&items)
	response.OK(c, items)
}

// SaveNacosConfig POST /nacos/config（按 clusterName upsert；密码留空保持不变）
func (h *NacosHandler) SaveNacosConfig(c *gin.Context) {
	// AdminPassword 模型上带 json:"-"（不回显，双向生效），直接绑模型会导致密码永远存不进库
	// （Nacos 登录恒报 403 User not found），须用独立入参结构体接收
	var in struct {
		ClusterName        string `json:"clusterName"`
		Addr               string `json:"addr"`
		AdminUsername      string `json:"adminUsername"`
		AdminPassword      string `json:"adminPassword"`
		Enabled            bool   `json:"enabled"`
		InjectEnabled      bool   `json:"injectEnabled"`
		AutoLabel          bool   `json:"autoLabel"`
		WebhookMode        string `json:"webhookMode"`
		WebhookURL         string `json:"webhookURL"`
		WebhookServiceNS   string `json:"webhookServiceNS"`
		WebhookServiceName string `json:"webhookServiceName"`
		WebhookServicePort int    `json:"webhookServicePort"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.ClusterName == "" || in.Addr == "" {
		response.Fail(c, 400, 400, "集群名与 Nacos 地址必填")
		return
	}
	if in.WebhookMode == "" {
		in.WebhookMode = "url"
	}
	cfg := model.NacosConfig{
		ClusterName:        in.ClusterName,
		Addr:               in.Addr,
		AdminUsername:      in.AdminUsername,
		AdminPassword:      in.AdminPassword,
		Enabled:            in.Enabled,
		InjectEnabled:      in.InjectEnabled,
		AutoLabel:          in.AutoLabel,
		WebhookMode:        in.WebhookMode,
		WebhookURL:         in.WebhookURL,
		WebhookServiceNS:   in.WebhookServiceNS,
		WebhookServiceName: in.WebhookServiceName,
		WebhookServicePort: in.WebhookServicePort,
	}
	var existing model.NacosConfig
	if err := h.db.Where("cluster_name = ?", cfg.ClusterName).First(&existing).Error; err == nil {
		cfg.ID = existing.ID
		cfg.CreatedAt = existing.CreatedAt
		if cfg.AdminPassword == "" {
			cfg.AdminPassword = existing.AdminPassword
		}
		if err := h.db.Save(&cfg).Error; err != nil {
			response.Fail(c, 500, 500, err.Error())
			return
		}
	} else {
		if err := h.db.Create(&cfg).Error; err != nil && errcode.IsUniqueViolation(err) {
			// 并发首建：对方已建，重读转更新
			if h.db.Where("cluster_name = ?", cfg.ClusterName).First(&existing).Error == nil {
				cfg.ID = existing.ID
				_ = h.db.Save(&cfg).Error
			}
		} else if err != nil {
			response.Fail(c, 500, 500, err.Error())
			return
		}
	}
	response.OK(c, cfg)
}

// DeleteNacosConfig DELETE /nacos/config/:cluster（清理映射表并移除集群的 MWC）
func (h *NacosHandler) DeleteNacosConfig(c *gin.Context) {
	cluster := c.Param("cluster")
	if err := h.db.Where("cluster_name = ?", cluster).Delete(&model.NacosConfig{}).Error; err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	if err := h.db.Where("cluster_name = ?", cluster).Delete(&model.NacosNamespace{}).Error; err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	h.nacos.RemoveWebhookConfig(cluster)
	response.OK(c, nil)
}

// TestNacosConfig POST /nacos/config/test  body: {clusterName}
func (h *NacosHandler) TestNacosConfig(c *gin.Context) {
	var req struct {
		ClusterName string `json:"clusterName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ClusterName == "" {
		response.Fail(c, 400, 400, "集群名必填")
		return
	}
	cfg, err := h.cfgOf(req.ClusterName)
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := service.NewNacosClient(cfg).Test(ctx); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// ------------------- 同步状态与操作（admin） -------------------

// NacosStatus GET /nacos/status
func (h *NacosHandler) NacosStatus(c *gin.Context) {
	var rows []model.NacosNamespace
	h.db.Order("cluster_name, k8s_namespace").Find(&rows)
	// 管理页需要展示注入凭据（模型上 json:"-" 防其他接口泄露，这里 admin-only 显式带出）
	type mappingView struct {
		model.NacosNamespace
		Password string `json:"password"`
	}
	items := make([]mappingView, 0, len(rows))
	for _, r := range rows {
		items = append(items, mappingView{NacosNamespace: r, Password: r.Password})
	}
	response.OK(c, gin.H{"sync": h.nacos.StatusOf(), "mappings": items})
}

// NacosSyncNow POST /nacos/sync/:cluster
func (h *NacosHandler) NacosSyncNow(c *gin.Context) {
	if err := h.nacos.SyncNow(c.Param("cluster")); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// NacosResetPassword POST /nacos/reset-password  body: {clusterName, namespace}
func (h *NacosHandler) NacosResetPassword(c *gin.Context) {
	var req struct {
		ClusterName string `json:"clusterName"`
		Namespace   string `json:"namespace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ClusterName == "" || req.Namespace == "" {
		response.Fail(c, 400, 400, "集群名与命名空间必填")
		return
	}
	username, err := h.nacos.ResetNamespacePassword(req.ClusterName, req.Namespace)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, gin.H{"username": username})
}

// ------------------- Nacos 资源浏览（admin） -------------------

// NacosNamespaces GET /nacos/namespaces?cluster=
func (h *NacosHandler) NacosNamespaces(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := service.NewNacosClient(cfg).ListNamespaces(ctx)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, items)
}

// NacosReady GET /nacos/ready?cluster=  当前集群是否已接入 Nacos（微服务页面用，登录即可）
func (h *NacosHandler) NacosReady(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.OK(c, gin.H{"configured": false})
		return
	}
	response.OK(c, gin.H{"configured": true, "enabled": cfg.Enabled})
}

// nsListFromQuery 解析 namespaces 请求参数：兼容多选（逗号分隔，* 展开为该集群全部命名空间）
func (h *NacosHandler) nsListFromQuery(c *gin.Context) ([]string, error) {
	raw := c.Query("namespaces")
	if raw == "" {
		raw = c.Query("namespace")
	}
	if raw == "" || raw == "*" {
		client, err := h.clusters.Client(c.Query("cluster"))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		nss, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(nss.Items))
		for _, ns := range nss.Items {
			out = append(out, ns.Name)
		}
		return out, nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// NacosServices GET /nacos/services?cluster=&namespaces=a,b,c（* = 全部）——服务发现列表
func (h *NacosHandler) NacosServices(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	nss, err := h.nsListFromQuery(c)
	if err != nil {
		response.Fail(c, 400, 400, "解析命名空间失败: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := service.NewNacosClient(cfg)
	type svcItem struct {
		service.NacosServiceInfo
		Namespace string `json:"namespace"`
	}
	out := make([]svcItem, 0, 32)
	var lastErr error
	for _, ns := range nss {
		items, err := nc.ListServices(ctx, ns)
		if err != nil {
			lastErr = err
			continue // 单个命名空间异常不阻塞整体（可能尚未同步到 Nacos）
		}
		for _, it := range items {
			out = append(out, svcItem{NacosServiceInfo: it, Namespace: ns})
		}
	}
	if len(out) == 0 && lastErr != nil && len(nss) > 0 {
		response.Fail(c, 502, 502, lastErr.Error())
		return
	}
	response.OK(c, out)
}

// NacosInstances GET /nacos/instances?cluster=&namespace=&service=&group=
func (h *NacosHandler) NacosInstances(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	if c.Query("service") == "" {
		response.Fail(c, 400, 400, "服务名必填")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := service.NewNacosClient(cfg).ListInstances(ctx, c.Query("namespace"), c.Query("group"), c.Query("service"))
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, items)
}

// NacosServiceDelete DELETE /nacos/service?cluster=&namespace=&service=&group=
func (h *NacosHandler) NacosServiceDelete(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	if c.Query("service") == "" {
		response.Fail(c, 400, 400, "服务名必填")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.NewNacosClient(cfg).DeleteService(ctx, c.Query("namespace"), c.Query("group"), c.Query("service")); err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, nil)
}

// NacosUsers GET /nacos/users?cluster=
func (h *NacosHandler) NacosUsers(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := service.NewNacosClient(cfg).ListUsers(ctx)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, items)
}

// NacosConfigList GET /nacos/configs?cluster=&namespaces=a,b,c（* = 全部）
func (h *NacosHandler) NacosConfigList(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	nss, err := h.nsListFromQuery(c)
	if err != nil {
		response.Fail(c, 400, 400, "解析命名空间失败: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := service.NewNacosClient(cfg)
	out := make([]service.NacosConfigItem, 0, 32)
	var lastErr error
	for _, ns := range nss {
		items, err := nc.ListConfigs(ctx, ns)
		if err != nil {
			lastErr = err
			continue // 单个命名空间异常不阻塞整体（可能尚未同步到 Nacos）
		}
		for _, it := range items {
			it.Namespace = ns
			out = append(out, it)
		}
	}
	if len(out) == 0 && lastErr != nil && len(nss) > 0 {
		response.Fail(c, 502, 502, lastErr.Error())
		return
	}
	response.OK(c, out)
}

// NacosConfigExport GET /nacos/config-export?cluster=&namespaces= —— 批量导出配置（含内容，JSON 文件）
func (h *NacosHandler) NacosConfigExport(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	nss, err := h.nsListFromQuery(c)
	if err != nil {
		response.Fail(c, 400, 400, "解析命名空间失败: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	nc := service.NewNacosClient(cfg)
	type expItem struct {
		Namespace string `json:"namespace"`
		DataId    string `json:"dataId"`
		Group     string `json:"group"`
		Type      string `json:"type"`
		Content   string `json:"content"`
	}
	out := make([]expItem, 0, 32)
	for _, ns := range nss {
		items, err := nc.ListConfigs(ctx, ns)
		if err != nil {
			continue // 单个命名空间异常不阻塞整体（可能尚未同步到 Nacos）
		}
		for _, it := range items {
			content, err := nc.GetConfig(ctx, ns, it.DataId, it.Group)
			if err != nil {
				continue
			}
			out = append(out, expItem{Namespace: ns, DataId: it.DataId, Group: it.Group, Type: it.Type, Content: content})
		}
	}
	response.OK(c, gin.H{"cluster": c.Query("cluster"), "exportedAt": time.Now().UTC().Format(time.RFC3339), "configs": out})
}

// NacosConfigContent GET /nacos/config-content?cluster=&namespace=&dataId=&group=
func (h *NacosHandler) NacosConfigContent(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content, err := service.NewNacosClient(cfg).GetConfig(ctx, c.Query("namespace"), c.Query("dataId"), c.Query("group"))
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, gin.H{"content": content})
}

// NacosConfigPublish POST /nacos/config-publish
func (h *NacosHandler) NacosConfigPublish(c *gin.Context) {
	var req struct {
		ClusterName string `json:"clusterName"`
		Namespace   string `json:"namespace"`
		DataId      string `json:"dataId"`
		Group       string `json:"group"`
		Content     string `json:"content"`
		Type        string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ClusterName == "" || req.Namespace == "" || req.DataId == "" {
		response.Fail(c, 400, 400, "集群/命名空间/dataId 必填")
		return
	}
	if req.Group == "" {
		req.Group = "DEFAULT_GROUP"
	}
	if req.Type == "" {
		req.Type = "YAML"
	}
	cfg, err := h.cfgOf(req.ClusterName)
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.NewNacosClient(cfg).PublishConfig(ctx, req.Namespace, req.DataId, req.Group, req.Content, req.Type); err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, nil)
}

// NacosConfigDelete DELETE /nacos/config-content?cluster=&namespace=&dataId=&group=
func (h *NacosHandler) NacosConfigDelete(c *gin.Context) {
	cfg, err := h.cfgOf(c.Query("cluster"))
	if err != nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Nacos")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.NewNacosClient(cfg).DeleteConfig(ctx, c.Query("namespace"), c.Query("dataId"), c.Query("group")); err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, nil)
}
