// 可观测性扩展：Alertmanager 接入（配置/实时告警/静默/主配置 YAML）+ 告警历史归档查询
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// ObsHandler 可观测性扩展接口
type ObsHandler struct {
	db       *gorm.DB
	clusters *service.ClusterManager
	archive  *service.AlertArchiveService
	events   *service.EventArchiveService
}

func NewObsHandler(db *gorm.DB, clusters *service.ClusterManager, archive *service.AlertArchiveService, events *service.EventArchiveService) *ObsHandler {
	return &ObsHandler{db: db, clusters: clusters, archive: archive, events: events}
}

// clusterOf 解析 X-Cluster 并返回集群记录
func (h *ObsHandler) clusterOf(c *gin.Context) (*model.Cluster, bool) {
	name := middleware.ClusterName(c)
	if name == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return nil, false
	}
	cl, err := h.clusters.GetRaw(name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil, false
	}
	return cl, true
}

// clusterClient 解析 X-Cluster 并返回集群记录 + 客户端
func (h *ObsHandler) clusterClient(c *gin.Context) (*model.Cluster, *kube.Client, bool) {
	cl, ok := h.clusterOf(c)
	if !ok {
		return nil, nil, false
	}
	client, err := h.clusters.ClientChecked(cl.Name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil, nil, false
	}
	return cl, client, true
}

// amConfigOf 当前集群的 AM 配置（未配置返回 nil）
func (h *ObsHandler) amConfigOf(clusterName string) *model.AlertmanagerConfig {
	var cfg model.AlertmanagerConfig
	if err := h.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		return nil
	}
	return &cfg
}

// ------------------- AM 连接配置（admin） -------------------

// ListAMConfigs GET /alertmanager
func (h *ObsHandler) ListAMConfigs(c *gin.Context) {
	var items []model.AlertmanagerConfig
	h.db.Find(&items)
	response.OK(c, items)
}

// SaveAMConfig POST /alertmanager（按 clusterName upsert）
func (h *ObsHandler) SaveAMConfig(c *gin.Context) {
	var cfg model.AlertmanagerConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if cfg.ClusterName == "" {
		response.Fail(c, 400, 400, "集群名必填")
		return
	}
	if cfg.DirectURL == "" && (cfg.Namespace == "" || cfg.Service == "" || cfg.Port == 0) {
		response.Fail(c, 400, 400, "命名空间/服务名/端口必填（或填直连地址）")
		return
	}
	var existing model.AlertmanagerConfig
	if err := h.db.Where("cluster_name = ?", cfg.ClusterName).First(&existing).Error; err == nil {
		cfg.ID = existing.ID
		cfg.CreatedAt = existing.CreatedAt
		h.db.Save(&cfg)
	} else {
		h.db.Create(&cfg)
	}
	response.OK(c, cfg)
}

// DeleteAMConfig DELETE /alertmanager/:cluster
func (h *ObsHandler) DeleteAMConfig(c *gin.Context) {
	h.db.Where("cluster_name = ?", c.Param("cluster")).Delete(&model.AlertmanagerConfig{})
	response.OK(c, nil)
}

// TestAMConfig POST /alertmanager/test  body: {clusterName}
func (h *ObsHandler) TestAMConfig(c *gin.Context) {
	var req struct {
		ClusterName string `json:"clusterName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ClusterName == "" {
		response.Fail(c, 400, 400, "集群名必填")
		return
	}
	cfg := h.amConfigOf(req.ClusterName)
	if cfg == nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Alertmanager")
		return
	}
	client, err := h.clusters.ClientChecked(req.ClusterName)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := service.AMStatus(ctx, client, cfg); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// ------------------- 实时告警 / 静默（authed） -------------------

// AMStatus 当前集群 AM 可用性（配置状态 + 连通性 + 归档轮询最近错误）
func (h *ObsHandler) AMStatus(c *gin.Context) {
	cl, ok := h.clusterOf(c)
	if !ok {
		return
	}
	cfg := h.amConfigOf(cl.Name)
	out := gin.H{"configured": cfg != nil, "ok": false}
	if cfg == nil {
		response.OK(c, out)
		return
	}
	out["config"] = cfg
	if client, err := h.clusters.ClientChecked(cl.Name); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out["ok"] = service.AMStatus(ctx, client, cfg) == nil
		cancel()
	}
	if lastErr, ok := h.archive.StatusOf()[cl.Name]; ok && lastErr != "" {
		out["error"] = lastErr
	}
	response.OK(c, out)
}

// AMAlerts GET /monitor/am/alerts 当前集群实时告警（含静默/抑制状态）
func (h *ObsHandler) AMAlerts(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	cfg := h.amConfigOf(cl.Name)
	if cfg == nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Alertmanager，请先在告警页完成接入配置")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alerts, err := service.AMAlerts(ctx, client, cfg)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, alerts)
}

// AMSilenceList GET /monitor/am/silences
func (h *ObsHandler) AMSilenceList(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	cfg := h.amConfigOf(cl.Name)
	if cfg == nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Alertmanager")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := service.AMSilences(ctx, client, cfg)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, items)
}

// AMSilenceCreate POST /monitor/am/silences
func (h *ObsHandler) AMSilenceCreate(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	cfg := h.amConfigOf(cl.Name)
	if cfg == nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Alertmanager")
		return
	}
	var req struct {
		Matchers []service.AMMatcher `json:"matchers"`
		StartsAt string              `json:"startsAt"`
		EndsAt   string              `json:"endsAt"`
		Comment  string              `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if len(req.Matchers) == 0 {
		response.Fail(c, 400, 400, "至少需要一个匹配器")
		return
	}
	if req.Comment == "" {
		req.Comment = "kube-console 静默"
	}
	body, _ := json.Marshal(gin.H{
		"matchers":  req.Matchers,
		"startsAt":  req.StartsAt,
		"endsAt":    req.EndsAt,
		"createdBy": middleware.CurrentUser(c),
		"comment":   req.Comment,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.AMCreateSilence(ctx, client, cfg, body); err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, nil)
}

// AMSilenceDelete DELETE /monitor/am/silences/:id
func (h *ObsHandler) AMSilenceDelete(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	cfg := h.amConfigOf(cl.Name)
	if cfg == nil {
		response.Fail(c, 400, 400, "该集群尚未配置 Alertmanager")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.AMDeleteSilence(ctx, client, cfg, c.Param("id")); err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, nil)
}

// ------------------- AM 主配置 YAML（admin） -------------------

// AMConfigYAMLGet GET /monitor/am/config-yaml?secret=ns/name（缺省用集群配置的 ConfigSecret）
func (h *ObsHandler) AMConfigYAMLGet(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	ref := c.Query("secret")
	if ref == "" {
		cfg := h.amConfigOf(cl.Name)
		if cfg == nil || cfg.ConfigSecret == "" {
			response.Fail(c, 400, 400, "未配置 Alertmanager 主配置 Secret（ns/name），可在连接配置中填写")
			return
		}
		ref = cfg.ConfigSecret
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := service.LoadAMConfigYAML(ctx, client, ref)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, out)
}

// AMConfigYAMLSave PUT /monitor/am/config-yaml  body: {secret, yaml}
func (h *ObsHandler) AMConfigYAMLSave(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	var req struct {
		Secret string `json:"secret"`
		YAML   string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if req.Secret == "" {
		cfg := h.amConfigOf(cl.Name)
		if cfg == nil || cfg.ConfigSecret == "" {
			response.Fail(c, 400, 400, "未指定主配置 Secret")
			return
		}
		req.Secret = cfg.ConfigSecret
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.SaveAMConfigYAML(ctx, client, req.Secret, req.YAML); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// ------------------- 告警历史（authed，跨集群） -------------------

// AlertEventList GET /alertevents?cluster=&state=&name=&namespace=&days=&page=&size=
func (h *ObsHandler) AlertEventList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := h.db.Model(&model.AlertEvent{})
	if v := c.Query("cluster"); v != "" {
		q = q.Where("cluster = ?", v)
	}
	if v := c.Query("state"); v == model.AlertStateFiring || v == model.AlertStateResolved {
		q = q.Where("state = ?", v)
	}
	if v := c.Query("name"); v != "" {
		q = q.Where("alert_name LIKE ?", "%"+v+"%")
	}
	if v := c.Query("namespace"); v != "" {
		q = q.Where("namespace = ?", v)
	}
	if days, err := strconv.Atoi(c.Query("days")); err == nil && days > 0 {
		q = q.Where("started_at > ?", time.Now().AddDate(0, 0, -days))
	}
	var total int64
	q.Count(&total)
	var items []model.AlertEvent
	q.Order("started_at DESC").Offset((page - 1) * size).Limit(size).Find(&items)
	response.OK(c, gin.H{"total": total, "items": items, "page": page, "size": size})
}

// AlertEventStats GET /alertevents/stats?days=7
func (h *ObsHandler) AlertEventStats(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days < 1 || days > 365 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days)
	var fired, resolvedCount, active int64
	h.db.Model(&model.AlertEvent{}).Where("started_at > ?", since).Count(&fired)
	h.db.Model(&model.AlertEvent{}).Where("resolved_at > ?", since).Count(&resolvedCount)
	h.db.Model(&model.AlertEvent{}).Where("state = ?", model.AlertStateFiring).Count(&active)
	out := gin.H{"days": days, "fired": fired, "resolved": resolvedCount, "active": active, "avgDurationHours": 0}
	// 平均持续时长（仅统计窗口内已恢复的，取最近 5000 条避免全表计算）
	var rows []model.AlertEvent
	h.db.Where("state = ? AND resolved_at IS NOT NULL AND started_at > ?", model.AlertStateResolved, since).
		Limit(5000).Order("started_at DESC").Find(&rows)
	if len(rows) > 0 {
		var total time.Duration
		for _, r := range rows {
			total += r.ResolvedAt.Sub(r.StartedAt)
		}
		out["avgDurationHours"] = math.Round(total.Hours()/float64(len(rows))*10) / 10
	}
	// Top 告警名
	var tops []struct {
		AlertName string `json:"alertName"`
		Count     int64  `json:"count"`
	}
	h.db.Model(&model.AlertEvent{}).Select("alert_name, COUNT(*) as count").
		Where("started_at > ?", since).Group("alert_name").Order("count DESC").Limit(5).Scan(&tops)
	out["top"] = tops
	response.OK(c, out)
}

// ------------------- 事件归档检索（authed） -------------------

// EventsArchiveSearch POST /events/archive/search
func (h *ObsHandler) EventsArchiveSearch(c *gin.Context) {
	cl, client, ok := h.clusterClient(c)
	if !ok {
		return
	}
	var src model.LogSource
	if err := h.db.Where("cluster_name = ?", cl.Name).First(&src).Error; err != nil {
		response.Fail(c, 400, 400, "当前集群未配置 ES 日志源（事件归档复用日志源连接），请先在日志检索/事件页配置")
		return
	}
	if !src.Enabled || !src.EventEnabled {
		response.Fail(c, 400, 400, "当前集群未启用事件归档，请在日志源配置中开启")
		return
	}
	var q struct {
		Namespace string `json:"namespace"`
		Type      string `json:"type"`
		Reason    string `json:"reason"`
		Keyword   string `json:"keyword"`
		Object    string `json:"object"`
		Start     string `json:"start"` // RFC3339 或毫秒时间戳
		End       string `json:"end"`
		Page      int    `json:"page"`
		Size      int    `json:"size"`
	}
	if err := c.ShouldBindJSON(&q); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	eq := service.EventArchiveQuery{
		Namespace: q.Namespace, Type: q.Type, Reason: q.Reason,
		Keyword: q.Keyword, Object: q.Object, Page: q.Page, Size: q.Size,
	}
	parseTs := func(s string) time.Time {
		if s == "" {
			return time.Time{}
		}
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMilli(ms)
		}
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}
	eq.Start, eq.End = parseTs(q.Start), parseTs(q.End)
	if eq.End.IsZero() {
		eq.End = time.Now()
	}
	if eq.Start.IsZero() {
		eq.Start = eq.End.Add(-24 * time.Hour)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := service.SearchArchivedEvents(ctx, client, &src, eq)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, out)
}

// ------------------- Grafana 内嵌（authed） -------------------

// GrafanaCheck GET /monitor/grafana-check?url= 服务端探测 Grafana 可达性（尽力而为：服务端不可达不代表浏览器端不可达）
func (h *ObsHandler) GrafanaCheck(c *gin.Context) {
	u := strings.TrimSpace(c.Query("url"))
	if u == "" {
		response.Fail(c, 400, 400, "缺少 url 参数")
		return
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		response.Fail(c, 400, 400, "Grafana 地址需以 http:// 或 https:// 开头")
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(strings.TrimRight(u, "/") + "/api/health")
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		response.OK(c, gin.H{"ok": false, "error": "服务端无法访问该地址（若仅浏览器可达可忽略）: " + msg})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		response.OK(c, gin.H{"ok": false, "error": fmt.Sprintf("Grafana 返回 %d", resp.StatusCode)})
		return
	}
	response.OK(c, gin.H{"ok": true})
}
