// 可观测性扩展：ES 历史日志检索（apiserver service proxy）+ 命名空间用量报表 + 证书巡检
package service

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// ------------------- ES 历史日志 -------------------

// LogQuery 日志检索条件
type LogQuery struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Keyword   string `json:"keyword"`
	Level     string `json:"level"` // error/warn/info，空=全部
	Minutes   int    `json:"minutes"`
	Page      int    `json:"page"`
	Size      int    `json:"size"`
}

// LogHit 一条日志
type LogHit struct {
	Timestamp string `json:"timestamp"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// LogSearchResult 检索结果
type LogSearchResult struct {
	Total    int64      `json:"total"`
	Items    []LogHit   `json:"items"`
	Took     int        `json:"took"`
	PodFacet []PodFacet `json:"podFacet"`
}

// PodFacet 按 Pod 聚合计数
type PodFacet struct {
	Pod   string `json:"pod"`
	Count int64  `json:"count"`
}

func esRequest(ctx context.Context, c *kube.Client, src *model.LogSource, method, path string, body []byte) ([]byte, error) {
	path = strings.TrimLeft(path, "/")
	var u string
	var httpClient *http.Client
	var err error
	if src.DirectURL != "" {
		// 直连：apiserver service proxy 会剥离 Authorization 头，开启安全认证的 ES 走代理永远 401
		u = strings.TrimRight(src.DirectURL, "/") + "/" + path
		httpClient = &http.Client{
			// 不限命名空间时 k8s-* 全量索引检索较慢，超时对齐 handler 的 45s 上下文
			Timeout:   60 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}
	} else {
		base := strings.TrimRight(c.Config.Host, "/")
		u = fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%d/proxy/%s",
			base, src.Namespace, src.Service, src.Port, path)
		httpClient, err = rest.HTTPClientFor(c.Config)
		if err != nil {
			return nil, err
		}
	}
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// ES basic auth 优先；代理模式下无账号时透传 apiserver 凭证
	if src.Username != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(src.Username+":"+src.Password)))
	} else if src.DirectURL == "" {
		if bearer := c.Config.BearerToken; bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ES 访问失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ES 返回 %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
	}
	return raw, nil
}

// SearchLogs 检索历史日志（fluentd 字段约定：kubernetes.pod_name 等）
func SearchLogs(ctx context.Context, c *kube.Client, src *model.LogSource, q LogQuery) (*LogSearchResult, error) {
	if src == nil || !src.Enabled {
		return nil, fmt.Errorf("该集群未配置日志源，请到「平台管理-日志源配置」填写 Elasticsearch 地址")
	}
	if q.Size <= 0 || q.Size > 200 {
		q.Size = 50
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Minutes <= 0 {
		q.Minutes = 60
	}

	var must []map[string]any
	// 这些字段映射为 text（连字符会被分词），term 精确匹配必须用 .keyword 子字段
	terms := map[string]string{}
	if q.Namespace != "" {
		terms["kubernetes.namespace_name.keyword"] = q.Namespace
	}
	if q.Pod != "" {
		terms["kubernetes.pod_name.keyword"] = q.Pod
	}
	if q.Container != "" {
		terms["kubernetes.container_name.keyword"] = q.Container
	}
	for k, v := range terms {
		must = append(must, map[string]any{"term": map[string]any{k: v}})
	}
	if q.Keyword != "" {
		must = append(must, map[string]any{"query_string": map[string]any{
			"query": q.Keyword, "fields": []string{"log", "message"},
		}})
	}
	var filter []map[string]any
	if q.Level != "" {
		lvMap := map[string][]string{
			"error": {"error", "err", "ERROR", "E"},
			"warn":  {"warn", "warning", "WARN", "W"},
			"info":  {"info", "INFO", "I"},
		}
		should := []map[string]any{}
		for _, l := range lvMap[q.Level] {
			should = append(should, map[string]any{"term": map[string]any{"kubernetes.labels.level": l}})
			should = append(should, map[string]any{"term": map[string]any{"stream": l}})
		}
		filter = append(filter, map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}})
	}
	now := time.Now().UnixNano() / int64(time.Millisecond)
	filter = append(filter, map[string]any{"range": map[string]any{
		"@timestamp": map[string]any{"gte": now - int64(q.Minutes)*60*1000, "lte": now, "format": "epoch_millis"},
	}})

	// 索引模式：前缀支持 {namespace} 占位符（索引按命名空间拆分时，
	// 如 logstash-{namespace}- → 查 dev 展开为 logstash-dev*，不限命名空间展开为 logstash-*）
	index := "logstash-*"
	if src.IndexPrefix != "" {
		index = strings.TrimRight(src.IndexPrefix, "-*")
		if strings.Contains(index, "{namespace}") {
			ns := q.Namespace
			if ns == "" {
				ns = "*"
			}
			index = strings.ReplaceAll(index, "{namespace}", ns)
		}
		if !strings.HasSuffix(index, "*") {
			index += "*"
		}
	}
	body := map[string]any{
		"from":  (q.Page - 1) * q.Size,
		"size":  q.Size,
		"query": map[string]any{"bool": map[string]any{"must": must, "filter": filter}},
		"aggs":  map[string]any{"pods": map[string]any{"terms": map[string]any{"field": "kubernetes.pod_name.keyword", "size": 30}}},
		"sort":  []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}},
	}
	payload, _ := json.Marshal(body)

	start := time.Now()
	raw, err := esRequest(ctx, c, src, http.MethodPost, index+"/_search", payload)
	if err != nil {
		return nil, err
	}
	out := &LogSearchResult{Took: int(time.Since(start).Milliseconds()), Items: []LogHit{}, PodFacet: []PodFacet{}}
	var esResp struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
		Aggregations struct {
			Pods struct {
				Buckets []struct {
					Key   string `json:"key"`
					Count int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"pods"`
		} `json:"aggregations"`
	}
	if err := json.Unmarshal(raw, &esResp); err != nil {
		return nil, fmt.Errorf("ES 响应解析失败: %w", err)
	}
	out.Total = esResp.Hits.Total.Value
	for _, h := range esResp.Hits.Hits {
		out.Items = append(out.Items, parseLogHit(h.Source))
	}
	for _, b := range esResp.Aggregations.Pods.Buckets {
		out.PodFacet = append(out.PodFacet, PodFacet{Pod: b.Key, Count: b.Count})
	}
	sort.Slice(out.PodFacet, func(i, j int) bool { return out.PodFacet[i].Count > out.PodFacet[j].Count })
	return out, nil
}

// parseLogHit 兼容 fluentd 字段布局
func parseLogHit(src map[string]any) LogHit {
	hit := LogHit{}
	if ts, ok := src["@timestamp"].(string); ok {
		hit.Timestamp = ts
	}
	if kubeObj, ok := src["kubernetes"].(map[string]any); ok {
		hit.Namespace, _ = kubeObj["namespace_name"].(string)
		hit.Pod, _ = kubeObj["pod_name"].(string)
		hit.Container, _ = kubeObj["container_name"].(string)
		if lvl, ok := kubeObj["labels"].(map[string]any); ok {
			hit.Level, _ = lvl["level"].(string)
		}
	}
	if m, ok := src["log"].(string); ok && m != "" {
		hit.Message = strings.TrimRight(m, "\n")
	} else if m, ok := src["message"].(string); ok {
		hit.Message = strings.TrimRight(m, "\n")
	} else if m, ok := src["msg"].(string); ok {
		hit.Message = m
	}
	if hit.Level == "" {
		if s, ok := src["stream"].(string); ok {
			hit.Level = s
		}
	}
	return hit
}

// TestLogSource 测试日志源连通性（经 proxy 查 ES 根信息）
func TestLogSource(ctx context.Context, c *kube.Client, src *model.LogSource) error {
	raw, err := esRequest(ctx, c, src, http.MethodGet, "/", nil)
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), "cluster_name") && !strings.Contains(string(raw), "version") {
		return fmt.Errorf("目标服务不是 Elasticsearch")
	}
	return nil
}

// ------------------- 命名空间用量报表 -------------------

// NSUsageRow 一行用量数据（近 N 天平均/峰值）
type NSUsageRow struct {
	Namespace   string  `json:"namespace"`
	CPUAvgCores float64 `json:"cpuAvgCores"`
	CPUMaxCores float64 `json:"cpuMaxCores"`
	MemAvgGi    float64 `json:"memAvgGi"`
	MemMaxGi    float64 `json:"memMaxGi"`
	PodAvgCount float64 `json:"podAvgCount"`
}

// NamespaceUsage 经 Prometheus 聚合各命名空间用量
func NamespaceUsage(ctx context.Context, c *kube.Client, m *MonitorService, cfg PromConfig, days int) ([]NSUsageRow, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	step := time.Duration(days*24*3600/120) * time.Second // 约 120 个采样点
	end := time.Now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)

	queries := []struct {
		name string
		prom string
	}{
		{"cpu", `sum by (namespace) (rate(container_cpu_usage_seconds_total{container!="",image!=""}[5m]))`},
		{"mem", `sum by (namespace) (container_memory_working_set_bytes{container!="",image!=""})`},
		{"pod", `count by (namespace) (kube_pod_info)`},
	}
	rows := map[string]*NSUsageRow{}
	rowOf := func(ns string) *NSUsageRow {
		if rows[ns] == nil {
			rows[ns] = &NSUsageRow{Namespace: ns}
		}
		return rows[ns]
	}
	for _, qc := range queries {
		rs, err := m.QueryRange(ctx, c, cfg, qc.prom, start, end, step)
		if err != nil {
			continue
		}
		for _, r := range rs {
			ns := r.Metric["namespace"]
			if ns == "" {
				continue
			}
			row := rowOf(ns)
			var sum float64
			for _, pair := range r.Values {
				v := toFloat(pair[1])
				sum += v
				switch qc.name {
				case "cpu":
					if v > row.CPUMaxCores {
						row.CPUMaxCores = v
					}
				case "mem":
					if v > row.MemMaxGi {
						row.MemMaxGi = v
					}
				}
			}
			if n := len(r.Values); n > 0 {
				avg := sum / float64(n)
				switch qc.name {
				case "cpu":
					row.CPUAvgCores = avg
				case "mem":
					row.MemAvgGi = avg / (1 << 30)
					row.MemMaxGi = row.MemMaxGi / (1 << 30)
				case "pod":
					row.PodAvgCount = avg
				}
			}
		}
	}
	out := make([]NSUsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUAvgCores > out[j].CPUAvgCores })
	return out, nil
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%g", &f)
		return f
	}
	return 0
}

// ------------------- 证书巡检 -------------------

// CertInfo 证书信息
type CertInfo struct {
	Host     string `json:"host"`
	Subject  string `json:"subject"`
	Issuer   string `json:"issuer"`
	NotAfter string `json:"notAfter"`
	DaysLeft int    `json:"daysLeft"`
	Expired  bool   `json:"expired"`
}

// InspectClusterCert 与 apiserver 做 TLS 握手读取服务端证书有效期
func InspectClusterCert(host string) (*CertInfo, error) {
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.Index(host, "/"); i > 0 {
		host = host[:i]
	}
	if !strings.Contains(host, ":") {
		host += ":6443"
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("apiserver %s 连接失败: %w", host, err)
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil, fmt.Errorf("非 TLS 连接")
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("未获取到服务端证书")
	}
	leaf := state.PeerCertificates[0]
	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	return &CertInfo{
		Host:     host,
		Subject:  leaf.Subject.String(),
		Issuer:   leaf.Issuer.String(),
		NotAfter: leaf.NotAfter.Format("2006-01-02 15:04:05"),
		DaysLeft: days,
		Expired:  time.Now().After(leaf.NotAfter),
	}, nil
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
