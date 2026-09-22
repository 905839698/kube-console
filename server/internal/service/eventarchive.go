// 事件归档：轮询各集群 K8s Events 持久化到 Elasticsearch（复用日志源的连接配置），
// 以 uid 为文档 ID 增量 upsert（count/lastTimestamp 变化才写入），支持按天滚动索引与过期清理
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"gorm.io/gorm"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// EventArchiveService 事件归档服务
type EventArchiveService struct {
	db         *gorm.DB
	clusters   *ClusterManager
	interval   time.Duration
	retention  time.Duration // 0 = 永不清理
	defaultPfx string

	mu      sync.Mutex
	seen    map[string]map[string]string // cluster -> uid -> 指纹（count|lastTs）
	lastErr map[string]string            // cluster -> 最近一轮错误
}

// NewEventArchiveService 创建事件归档服务（不启动，由 main 调 Start）
func NewEventArchiveService(db *gorm.DB, clusters *ClusterManager, intervalSec, retentionDays int) *EventArchiveService {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	var retention time.Duration
	if retentionDays > 0 {
		retention = time.Duration(retentionDays) * 24 * time.Hour
	}
	return &EventArchiveService{
		db: db, clusters: clusters,
		interval:   time.Duration(intervalSec) * time.Second,
		retention:  retention,
		defaultPfx: "kc-events-",
		seen:       map[string]map[string]string{},
		lastErr:    map[string]string{},
	}
}

// Start 启动后台轮询
func (s *EventArchiveService) Start() {
	go func() {
		// 启动稍等片刻，等集群连接就绪
		time.Sleep(10 * time.Second)
		s.pollAll()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for range ticker.C {
			s.pollAll()
		}
	}()
	// 每日清理过期索引
	if s.retention > 0 {
		go func() {
			for {
				now := time.Now()
				next := time.Date(now.Year(), now.Month(), now.Day(), 2, 30, 0, 0, now.Location())
				if !next.After(now) {
					next = next.Add(24 * time.Hour)
				}
				time.Sleep(next.Sub(now))
				s.cleanupAll()
			}
		}()
	}
}

// StatusOf 最近一轮归档状态（cluster -> 错误，空 = 正常）
func (s *EventArchiveService) StatusOf() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.lastErr))
	for k, v := range s.lastErr {
		out[k] = v
	}
	return out
}

func (s *EventArchiveService) setErr(cluster, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg == "" {
		delete(s.lastErr, cluster)
	} else {
		s.lastErr[cluster] = truncateStr(msg, 300)
	}
}

func (s *EventArchiveService) pollAll() {
	var sources []model.LogSource
	s.db.Where("enabled = ? AND event_enabled = ?", true, true).Find(&sources)
	for _, src := range sources {
		s.pollCluster(src)
	}
}

func (s *EventArchiveService) pollCluster(src model.LogSource) {
	client, err := s.clusters.Client(src.ClusterName)
	if err != nil {
		s.setErr(src.ClusterName, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	events, err := client.Clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if err != nil {
		s.setErr(src.ClusterName, err.Error())
		return
	}
	if err := s.archiveEvents(ctx, client, &src, events.Items); err != nil {
		s.setErr(src.ClusterName, err.Error())
		return
	}
	s.setErr(src.ClusterName, "")
}

// eventHash 事件内容指纹：count 或最后发生时间变化才重新写 ES
func eventHash(ev corev1.Event) string {
	return fmt.Sprintf("%d|%d", ev.Count, ev.LastTimestamp.Unix())
}

// eventIndexPrefix 归档索引前缀（未配置用默认）
func eventIndexPrefix(src *model.LogSource) string {
	if src.EventIndexPrefix != "" {
		return strings.TrimRight(src.EventIndexPrefix, "-") + "-"
	}
	return "kc-events-"
}

// buildEventBulk 构造 _bulk NDJSON 请求体（_id = cluster-uid，按 lastTs 落到按天索引）。
// seen 为上次轮询的指纹表，返回更新后的表、请求体与变更数（纯函数，便于单测）
func buildEventBulk(cluster, prefix string, events []corev1.Event, seen map[string]string) (string, int, map[string]string) {
	next := make(map[string]string, len(events)+len(seen))
	for k, v := range seen {
		next[k] = v
	}
	var bulk strings.Builder
	changed := 0
	for _, ev := range events {
		if ev.UID == "" {
			continue
		}
		hash := eventHash(ev)
		if prev, ok := next[string(ev.UID)]; ok && prev == hash {
			continue
		}
		lastTs := ev.LastTimestamp.Time
		if lastTs.IsZero() {
			lastTs = ev.FirstTimestamp.Time
		}
		if lastTs.IsZero() {
			lastTs = ev.CreationTimestamp.Time
		}
		firstTs := ev.FirstTimestamp.Time
		if firstTs.IsZero() {
			firstTs = lastTs
		}
		doc := map[string]interface{}{
			"@timestamp":      lastTs.UTC().Format(time.RFC3339Nano),
			"cluster":         cluster,
			"uid":             string(ev.UID),
			"namespace":       ev.Namespace,
			"type":            ev.Type,
			"reason":          ev.Reason,
			"message":         ev.Message,
			"object_kind":     ev.InvolvedObject.Kind,
			"object_name":     ev.InvolvedObject.Name,
			"count":           ev.Count,
			"first_timestamp": firstTs.UTC().Format(time.RFC3339Nano),
			"last_timestamp":  lastTs.UTC().Format(time.RFC3339Nano),
		}
		body, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		// 索引日期与 @timestamp 一致用 UTC：本地时区会让跨零点事件落错天分区
		index := prefix + lastTs.UTC().Format("2006.01.02")
		bulk.WriteString(fmt.Sprintf(`{"index":{"_index":%q,"_id":%q}}`+"\n", index, cluster+"-"+string(ev.UID)))
		bulk.Write(body)
		bulk.WriteString("\n")
		next[string(ev.UID)] = hash
		changed++
	}
	return bulk.String(), changed, next
}

// archiveEvents 增量 upsert 事件到 ES
func (s *EventArchiveService) archiveEvents(ctx context.Context, c *kube.Client, src *model.LogSource, events []corev1.Event) error {
	cluster := src.ClusterName
	s.mu.Lock()
	seen := s.seen[cluster]
	if seen == nil {
		seen = map[string]string{}
	}
	s.mu.Unlock()

	body, changed, next := buildEventBulk(cluster, eventIndexPrefix(src), events, seen)

	if changed == 0 {
		return nil
	}
	// seen 指纹在 ES 写成功之后才提交：先提交会让 ES 短暂故障期间的变更
	// 被标记「已见」，后续轮次判「未变化」跳过——该窗口的事件变更永不落 ES
	// （_id=cluster-uid 幂等 upsert，重试无副作用）
	if _, err := esRequest(ctx, c, src, http.MethodPost, "/_bulk", []byte(body)); err != nil {
		return fmt.Errorf("事件写入 ES 失败: %w", err)
	}
	s.mu.Lock()
	s.seen[cluster] = next
	s.mu.Unlock()
	return nil
}

// cleanupAll 清理超过保留期的归档索引（按索引名日期解析）
func (s *EventArchiveService) cleanupAll() {
	var sources []model.LogSource
	s.db.Where("enabled = ? AND event_enabled = ?", true, true).Find(&sources)
	for _, src := range sources {
		client, err := s.clusters.Client(src.ClusterName)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		raw, err := esRequest(ctx, client, &src, http.MethodGet, "/_cat/indices/"+eventIndexPrefix(&src)+"*?h=index&format=json", nil)
		cancel()
		if err != nil {
			continue
		}
		var idxs []map[string]string
		if json.Unmarshal(raw, &idxs) != nil {
			continue
		}
		cut := time.Now().Add(-s.retention)
		pfx := eventIndexPrefix(&src)
		dateRe := regexp.MustCompile(`^` + regexp.QuoteMeta(pfx) + `(\d{4})\.(\d{2})\.(\d{2})$`)
		for _, it := range idxs {
			name := it["index"]
			m := dateRe.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			t, err := time.ParseInLocation("2006.01.02", m[1]+"."+m[2]+"."+m[3], time.Local)
			if err != nil || !t.Before(cut) {
				continue
			}
			ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
			_, err = esRequest(ctx2, client, &src, http.MethodDelete, "/"+name, nil)
			cancel2()
			if err != nil {
				log.Printf("删除过期事件索引 %s 失败: %v", name, err)
			} else {
				log.Printf("已删除过期事件索引 %s（保留 %d 天）", name, int(s.retention.Hours()/24))
			}
		}
	}
}

// ------------------- 归档事件检索 -------------------

// EventArchiveQuery 归档事件检索条件
type EventArchiveQuery struct {
	Namespace string // 逗号分隔多个，空 = 全部
	Type      string // Warning | Normal，空 = 全部
	Reason    string
	Keyword   string
	Object    string // 对象名子串/通配
	Start     time.Time
	End       time.Time
	Page      int
	Size      int
}

// EventArchiveHit 对齐前端 EventItemEx 结构
type EventArchiveHit struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Count     int64  `json:"count"`
	LastAt    string `json:"lastAt"`
}

// SearchArchivedEvents 在 ES 归档索引中检索历史事件
func SearchArchivedEvents(ctx context.Context, c *kube.Client, src *model.LogSource, q EventArchiveQuery) (map[string]interface{}, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Size < 1 || q.Size > 200 {
		q.Size = 50
	}
	filter := []map[string]interface{}{
		{"term": map[string]interface{}{"cluster.keyword": src.ClusterName}},
	}
	if q.Namespace != "" {
		filter = append(filter, map[string]interface{}{"terms": map[string]interface{}{
			"namespace.keyword": strings.Split(q.Namespace, ","),
		}})
	}
	if q.Type != "" {
		filter = append(filter, map[string]interface{}{"term": map[string]interface{}{"type.keyword": q.Type}})
	}
	if q.Reason != "" {
		filter = append(filter, map[string]interface{}{"term": map[string]interface{}{"reason.keyword": q.Reason}})
	}
	var must []map[string]interface{}
	if q.Keyword != "" {
		must = append(must, map[string]interface{}{"query_string": map[string]interface{}{
			"fields": []string{"message", "reason", "object_name"}, "query": q.Keyword,
		}})
	}
	if q.Object != "" {
		must = append(must, map[string]interface{}{"wildcard": map[string]interface{}{
			"object_name.keyword": "*" + q.Object + "*",
		}})
	}
	if !q.Start.IsZero() || !q.End.IsZero() {
		r := map[string]interface{}{"format": "epoch_millis"}
		if !q.Start.IsZero() {
			r["gte"] = q.Start.UnixMilli()
		}
		if !q.End.IsZero() {
			r["lte"] = q.End.UnixMilli()
		}
		filter = append(filter, map[string]interface{}{"range": map[string]interface{}{"@timestamp": r}})
	}
	boolQ := map[string]interface{}{"filter": filter}
	if len(must) > 0 {
		boolQ["must"] = must
	}
	dsl := map[string]interface{}{
		"from":  (q.Page - 1) * q.Size,
		"size":  q.Size,
		"query": map[string]interface{}{"bool": boolQ},
		"sort":  []map[string]interface{}{{"@timestamp": map[string]string{"order": "desc"}}},
	}
	body, _ := json.Marshal(dsl)
	index := eventIndexPrefix(src) + "*"
	raw, err := esRequest(ctx, c, src, http.MethodPost, "/"+index+"/_search", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Took  int `json:"took"`
		Hits  struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Source map[string]interface{} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析 ES 响应失败: %w", err)
	}
	items := []EventArchiveHit{}
	for _, h := range resp.Hits.Hits {
		items = append(items, mapHit(h.Source))
	}
	return map[string]interface{}{"total": resp.Hits.Total.Value, "items": items, "took": resp.Took}, nil
}

func mapHit(src map[string]interface{}) EventArchiveHit {
	get := func(k string) string {
		if v, ok := src[k].(string); ok {
			return v
		}
		return ""
	}
	cnt, _ := src["count"].(float64)
	return EventArchiveHit{
		Name:      get("uid"),
		Namespace: get("namespace"),
		Type:      get("type"),
		Reason:    get("reason"),
		Message:   get("message"),
		Object:    strings.TrimSpace(get("object_kind") + "/" + get("object_name")),
		Count:     int64(cnt),
		LastAt:    get("last_timestamp"),
	}
}
