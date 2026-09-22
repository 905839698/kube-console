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
	"slices"
	"sort"
	"strconv"
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
	// Source 日志来源：空=全部，stdout=标准输出，file=单行文本文件采集，json=JSON 结构化采集
	Source  string `json:"source"`
	Keyword string `json:"keyword"`
	Level   string `json:"level"` // error/warn/info，空=全部
	Minutes int    `json:"minutes"`
	// From/To 绝对时间范围（RFC3339 字符串或 epoch 毫秒）。任一非空即生效，
	// 缺省的一端不限制；都为空时按 Minutes 相对范围
	From string `json:"from"`
	To   string `json:"to"`
	Page int    `json:"page"`
	Size int    `json:"size"`
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
	autoDirect := false
	switch {
	case src.DirectURL != "":
		// 显式直连：apiserver service proxy 会剥离 Authorization 头，开启安全认证的 ES 走代理永远 401
		u = strings.TrimRight(src.DirectURL, "/") + "/" + path
		httpClient = directHTTPClient()
	case src.Username != "":
		// 安全 ES（配置了账号）但未填直连地址：代理会剥离 Authorization 头（永远 401），
		// 自动改走集群内 Service DNS 直连（控制台与 ES 同集群部署时可用；跨集群请显式填直连地址）
		autoDirect = true
		u = fmt.Sprintf("http://%s.%s.svc:%d/%s", src.Service, src.Namespace, src.Port, path)
		httpClient = directHTTPClient()
	default:
		// 匿名 ES：经 apiserver 代理访问，无需凭证
		base := strings.TrimRight(c.Config.Host, "/")
		u = fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%d/proxy/%s",
			base, src.Namespace, src.Service, src.Port, path)
		httpClient, err = rest.HTTPClientFor(c.Config)
		if err != nil {
			return nil, err
		}
		// rest 客户端默认无 Timeout：ES 挂起时流式读取无上界
		httpClient.Timeout = 45 * time.Second
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
	} else if src.DirectURL == "" && !autoDirect {
		if bearer := c.Config.BearerToken; bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if autoDirect {
			return nil, fmt.Errorf("ES 集群内直连失败（%s.%s.svc:%d 不可达，控制台可能未与 ES 同集群）：请在「直连地址」填写 ES 实际可达地址后重试: %w",
				src.Service, src.Namespace, src.Port, err)
		}
		return nil, fmt.Errorf("ES 访问失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("ES 返回 401：认证被拒绝，请检查用户名密码是否正确（安全 ES 不能经 apiserver 代理访问——代理会剥离认证头，请使用直连地址）: %s",
			truncateStr(string(raw), 200))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ES 返回 %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
	}
	return raw, nil
}

// directHTTPClient 直连 HTTP 客户端：不限命名空间时 k8s-* 全量索引检索较慢，
// 超时对齐 handler 的 45s 上下文；自签证书跳过校验
func directHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// SearchLogs 检索历史日志（fluentd 字段约定：kubernetes.pod_name 等）
// parseLogTime 解析日志检索的绝对时间：epoch 毫秒或 RFC3339；空串返回 nil（该端不限制）
func parseLogTime(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ms, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return t.UnixMilli(), nil
}

// logIndexPatterns 按来源选出检索索引模式（行日志与 JSON 采集分索引）
// source: 空=全部；stdout/file=行日志前缀；json=JSON 前缀
func logIndexPatterns(src *model.LogSource, source, ns string) []string {
	var prefixes []string
	switch source {
	case "json":
		prefixes = []string{jsonIndexPrefix(src)}
	case "stdout", "file":
		prefixes = []string{lineIndexPrefix(src)}
	default:
		prefixes = []string{lineIndexPrefix(src), jsonIndexPrefix(src)}
	}
	out := []string{}
	for _, p := range prefixes {
		pat := indexPattern(p, ns)
		if !slices.Contains(out, pat) {
			out = append(out, pat)
		}
	}
	return out
}

// indexPattern 前缀 → 通配模式。与写入侧 collectIndex 的分名规则一致（裸前缀用 "-" 拼命名空间），
// 含 {namespace} 占位符时按命名空间展开，不限命名空间展开为 *
func indexPattern(prefix, ns string) string {
	base := strings.ToLower(strings.TrimRight(prefix, "-*"))
	if strings.Contains(base, "{namespace}") {
		wild := strings.ToLower(ns) // 空命名空间 → 占位符替换为空，后面拼的 * 即通配
		return strings.ReplaceAll(base, "{namespace}", wild) + "*"
	}
	if base == "" {
		base = strings.TrimRight(DefaultIndexPrefix, "-*")
	}
	return base + "-*"
}

// sourceFilters 来源筛选：Sidecar 采集文档带 kube-console.log-* 标记，fluentd 标准输出没有
func sourceFilters(source string) (must, mustNot []map[string]any) {
	exists := map[string]any{"exists": map[string]any{"field": LogCollectionField}}
	switch source {
	case "stdout":
		return nil, []map[string]any{exists}
	case "file":
		// 旧版本采集文档无 log-format 字段，只排除 json
		return []map[string]any{exists}, []map[string]any{
			{"term": map[string]any{LogFormatField + ".keyword": "json"}},
		}
	case "json":
		return []map[string]any{{"term": map[string]any{LogFormatField + ".keyword": "json"}}}, nil
	}
	return nil, nil
}

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
	srcMust, srcMustNot := sourceFilters(q.Source)
	must = append(must, srcMust...)
	var mustNot []map[string]any
	mustNot = append(mustNot, srcMustNot...)
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
	if q.From != "" || q.To != "" {
		rg := map[string]any{"format": "epoch_millis"}
		if v, err := parseLogTime(q.From); err != nil {
			return nil, fmt.Errorf("起始时间非法（%s）: %w", q.From, err)
		} else if v != nil {
			rg["gte"] = v
		}
		if v, err := parseLogTime(q.To); err != nil {
			return nil, fmt.Errorf("结束时间非法（%s）: %w", q.To, err)
		} else if v != nil {
			rg["lte"] = v
		}
		filter = append(filter, map[string]any{"range": map[string]any{"@timestamp": rg}})
	} else {
		filter = append(filter, map[string]any{"range": map[string]any{
			"@timestamp": map[string]any{"gte": now - int64(q.Minutes)*60*1000, "lte": now, "format": "epoch_millis"},
		}})
	}

	// 索引模式：行日志与 JSON 采集两条前缀（各自支持 {namespace} 占位符），
	// 按来源筛选只查命中的那条，避免无谓的全索引扫描
	index := strings.Join(logIndexPatterns(src, q.Source, q.Namespace), ",")
	boolQ := map[string]any{"filter": filter}
	if len(must) > 0 {
		boolQ["must"] = must
	}
	if len(mustNot) > 0 {
		boolQ["must_not"] = mustNot
	}
	body := map[string]any{
		"from":  (q.Page - 1) * q.Size,
		"size":  q.Size,
		"query": map[string]any{"bool": boolQ},
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
	} else if _, ok := src["kubernetes.namespace_name"]; ok {
		// 控制台 Fluent Bit Sidecar 采集文档：modify 过滤器写的是字面带点 key，_source 原样保留
		hit.Namespace, _ = src["kubernetes.namespace_name"].(string)
		hit.Pod, _ = src["kubernetes.pod_name"].(string)
		hit.Container, _ = src["kubernetes.container_name"].(string)
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
