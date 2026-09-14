// 告警历史归档：后台轮询各集群 Alertmanager /api/v2/alerts，按指纹做
// firing→resolved 状态机落库（一次触发→恢复为一条 AlertEvent 记录）
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"kube-console/server/internal/model"
)

// AlertArchiveService 告警历史归档服务
type AlertArchiveService struct {
	db        *gorm.DB
	clusters  *ClusterManager
	interval  time.Duration
	retention time.Duration

	mu        sync.Mutex
	lastErr   map[string]string // cluster -> 最近一轮轮询错误（空串 = 正常）
	lastClean time.Time
}

// NewAlertArchiveService 创建告警归档服务（不启动，由 main 调 Start）
func NewAlertArchiveService(db *gorm.DB, clusters *ClusterManager, intervalSec, retentionDays int) *AlertArchiveService {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	if retentionDays <= 0 {
		retentionDays = 90
	}
	return &AlertArchiveService{
		db:        db,
		clusters:  clusters,
		interval:  time.Duration(intervalSec) * time.Second,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		lastErr:   map[string]string{},
		lastClean: time.Now(),
	}
}

// Start 启动后台轮询（立即执行一轮）
func (s *AlertArchiveService) Start() {
	go func() {
		s.pollAll()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for range ticker.C {
			s.pollAll()
		}
	}()
}

// StatusOf 最近一轮轮询状态（cluster -> 错误信息，空 = 正常），供前端展示可用性
func (s *AlertArchiveService) StatusOf() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.lastErr))
	for k, v := range s.lastErr {
		out[k] = v
	}
	return out
}

func (s *AlertArchiveService) pollAll() {
	var cls []model.Cluster
	s.db.Find(&cls)
	for _, cl := range cls {
		s.pollCluster(cl.Name)
	}
	// 每天清理一次过期历史
	if time.Since(s.lastClean) >= 24*time.Hour {
		s.cleanup()
	}
}

// pollCluster 单集群轮询：未配置 AM 的集群静默跳过；失败记录到 lastErr
func (s *AlertArchiveService) pollCluster(clusterName string) {
	setErr := func(err error) {
		s.mu.Lock()
		if err != nil {
			s.lastErr[clusterName] = truncateStr(err.Error(), 300)
		} else {
			delete(s.lastErr, clusterName)
		}
		s.mu.Unlock()
	}
	var cfg model.AlertmanagerConfig
	if err := s.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		setErr(nil) // 未配置 = 该集群不启用 AM，不算错误
		return
	}
	client, err := s.clusters.Client(clusterName)
	if err != nil {
		setErr(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alerts, err := AMAlerts(ctx, client, &cfg)
	if err != nil {
		setErr(err)
		return
	}
	if err := syncAlertEvents(s.db, clusterName, alerts, time.Now()); err != nil {
		setErr(err)
		return
	}
	setErr(nil)
}

// syncAlertEvents 状态机核心：
//  1. AM 在列表中的指纹 → 无记录则记触发，有记录则刷新 LastSeenAt
//  2. 本地仍为 firing 但已不在 AM 列表 → 判定恢复
//  3. 恢复后同指纹再次出现 → 新开一条记录（历史保留每次故障）
func syncAlertEvents(db *gorm.DB, cluster string, alerts []AMAlert, now time.Time) error {
	var open []model.AlertEvent
	if err := db.Where("cluster = ? AND state = ?", cluster, model.AlertStateFiring).Find(&open).Error; err != nil {
		return err
	}
	openByFp := make(map[string]model.AlertEvent, len(open))
	for _, ev := range open {
		openByFp[ev.Fingerprint] = ev
	}
	seen := make(map[string]bool, len(alerts))
	for _, a := range alerts {
		if a.Fingerprint == "" {
			continue
		}
		seen[a.Fingerprint] = true
		if ev, ok := openByFp[a.Fingerprint]; ok {
			db.Model(&ev).Update("last_seen_at", now)
			continue
		}
		started := now
		if t, err := time.Parse(time.RFC3339, a.StartsAt); err == nil && !t.IsZero() {
			started = t
		}
		labels, _ := json.Marshal(a.Labels)
		ev := model.AlertEvent{
			Cluster:     cluster,
			Fingerprint: a.Fingerprint,
			AlertName:   a.Labels["alertname"],
			Severity:    a.Labels["severity"],
			Namespace:   a.Labels["namespace"],
			Labels:      string(labels),
			State:       model.AlertStateFiring,
			StartedAt:   started,
			LastSeenAt:  now,
		}
		if err := db.Create(&ev).Error; err != nil {
			return fmt.Errorf("写入告警事件失败: %w", err)
		}
	}
	// 不在 AM 列表中的开放记录 → 恢复
	for fp, ev := range openByFp {
		if seen[fp] {
			continue
		}
		if err := db.Model(&ev).Updates(map[string]interface{}{
			"state":       model.AlertStateResolved,
			"resolved_at": now,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// cleanup 清理超过保留期（按触发时间）的历史
func (s *AlertArchiveService) cleanup() {
	s.lastClean = time.Now()
	cut := time.Now().Add(-s.retention)
	if err := s.db.Where("started_at < ?", cut).Delete(&model.AlertEvent{}).Error; err != nil {
		log.Printf("告警历史清理失败: %v", err)
		return
	}
	log.Printf("告警历史清理完成（保留 %d 天）", int(s.retention.Hours()/24))
}
