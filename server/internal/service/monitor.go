package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// PromResult Prometheus 查询结果（简化）
type PromResult struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`    // [timestamp, "value"]
	Values [][]interface{}   `json:"values"`   // [[timestamp, "value"], ...]
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string       `json:"resultType"`
		Result     []PromResult `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// PromConfig Prometheus 访问配置
type PromConfig struct {
	Namespace string
	Service   string
	Port      int
}

// DefaultPromConfig 默认配置（Kuboard 部署的 Prometheus Operator）
func DefaultPromConfig() PromConfig {
	return PromConfig{Namespace: "kuboard", Service: "prometheus-k8s", Port: 9090}
}

// MonitorService Prometheus 监控查询服务
type MonitorService struct {
	// 依赖 k8s service 层的集群客户端
	clusters *ClusterManager
}

func NewMonitorService(clusters *ClusterManager) *MonitorService {
	return &MonitorService{clusters: clusters}
}

// promConfig 从集群记录读取 Prometheus 配置，未配置时用默认值
func PromConfigOf(c *model.Cluster) PromConfig {
	cfg := DefaultPromConfig()
	if c.PrometheusNamespace != "" {
		cfg.Namespace = c.PrometheusNamespace
	}
	if c.PrometheusService != "" {
		cfg.Service = c.PrometheusService
	}
	if c.PrometheusPort > 0 {
		cfg.Port = c.PrometheusPort
	}
	return cfg
}

// proxyURL 构建 kube-apiserver proxy 访问 Prometheus 的 URL
func proxyURL(c *kube.Client, cfg PromConfig, path string, params url.Values) (string, error) {
	base := strings.TrimRight(c.Config.Host, "/")
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%d/proxy/api/v1/%s",
		base, cfg.Namespace, cfg.Service, cfg.Port, path)
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return u, nil
}

func (m *MonitorService) doProm(ctx context.Context, c *kube.Client, cfg PromConfig, path string, params url.Values) (*promResponse, error) {
	u, err := proxyURL(c, cfg, path, params)
	if err != nil {
		return nil, err
	}
	// 复用 rest.Config 的认证（token/cert）
	httpClient, err := rest.HTTPClientFor(c.Config)
	if err != nil {
		return nil, fmt.Errorf("构建 HTTP 客户端失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// 透传 rest.Config 的认证头
	if bearer := c.Config.BearerToken; bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Prometheus 不可达（请检查集群的 Prometheus 配置）: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body))[:min(300, len(body))])
	}
	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("解析 Prometheus 响应失败: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("Prometheus 查询失败: %s", pr.Error)
	}
	return &pr, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Query 瞬时查询
func (m *MonitorService) Query(ctx context.Context, c *kube.Client, cfg PromConfig, promql string) ([]PromResult, error) {
	params := url.Values{"query": {promql}}
	pr, err := m.doProm(ctx, c, cfg, "query", params)
	if err != nil {
		return nil, err
	}
	return pr.Data.Result, nil
}

// QueryRange 范围查询
func (m *MonitorService) QueryRange(ctx context.Context, c *kube.Client, cfg PromConfig, promql string, start, end time.Time, step time.Duration) ([]PromResult, error) {
	params := url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	pr, err := m.doProm(ctx, c, cfg, "query_range", params)
	if err != nil {
		return nil, err
	}
	return pr.Data.Result, nil
}

// TestConnectivity 测试 Prometheus 连通性（查询 up 计数）
func (m *MonitorService) TestConnectivity(ctx context.Context, c *kube.Client, cfg PromConfig) error {
	_, err := m.Query(ctx, c, cfg, "count(up)")
	return err
}

// doPromRaw 访问 Prometheus 非 query 类接口（如 /api/v1/rules），返回解析后的 JSON。
// 用于 /rules 这类返回结构不同于 query 结果（data.groups[].rules[]）的端点。
func (m *MonitorService) doPromRaw(ctx context.Context, c *kube.Client, cfg PromConfig, path string) (map[string]interface{}, error) {
	u, err := proxyURL(c, cfg, path, nil)
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(c.Config)
	if err != nil {
		return nil, fmt.Errorf("构建 HTTP 客户端失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if bearer := c.Config.BearerToken; bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Prometheus 不可达: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body))[:min(300, len(body))])
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析 Prometheus 响应失败: %w", err)
	}
	if out["status"] != "success" {
		return nil, fmt.Errorf("Prometheus 查询失败: %v", out["error"])
	}
	return out, nil
}

// ---------- 告警 ----------

// AlertRule 一条告警规则（含状态 / 严重级 / 描述 / 当前活动实例）
type AlertRule struct {
	AlertName string            `json:"alertName"`
	State     string            `json:"state"`    // firing / pending / inactive
	Severity  string            `json:"severity"` // critical / warning / info / none
	Namespace string            `json:"namespace"`
	Summary   string            `json:"summary"`
	Detail    string            `json:"detail"`
	Runbook   string            `json:"runbook"`
	Group     string            `json:"group"`
	Query     string            `json:"query"`
	Duration  float64           `json:"duration"` // 触发持续时长（秒）
	LastEval  string            `json:"lastEval"`
	Instances []AlertInstance   `json:"instances"`

	// 规则检查（lint）：供「规则检查」页定位配置缺陷
	HasSeverity  bool `json:"hasSeverity"`
	HasSummary   bool `json:"hasSummary"`
	HasRunbook   bool `json:"hasRunbook"`
	HasNamespace bool `json:"hasNamespace"`
}

// GroupSummary 一个告警分组的汇总（供「告警分组」页）
type GroupSummary struct {
	Name       string   `json:"name"`
	Interval   string   `json:"interval"`
	LastEval   string   `json:"lastEval"`
	AlertRules int      `json:"alertRules"`
	RecRules   int      `json:"recRules"`
	Firing     int      `json:"firing"`
	Severities []string `json:"severities"`
}

// AlertInstance 告警的一条活动实例（firing 的具体对象，如某个 Pod/节点）
type AlertInstance struct {
	Labels  map[string]string `json:"labels"`
	Started string            `json:"started"` // 触发时间 RFC3339
	Value   string            `json:"value"`
}

// AlertSummary 顶部统计
type AlertSummary struct {
	Firing   int `json:"firing"`
	Pending  int `json:"pending"`
	Inactive int `json:"inactive"`
	Total    int `json:"total"`
	BySeverity map[string]int `json:"bySeverity"`
}

// AlertRulesResponse /api/v1/rules 解析后的告警视图
type AlertRulesResponse struct {
	Summary    AlertSummary   `json:"summary"`
	Rules      []AlertRule    `json:"rules"`
	Groups     int            `json:"groups"`
	GroupList  []GroupSummary `json:"groupList"`
	LastEval   string         `json:"lastEval"`
}

// AlertRules 拉取并解析 Prometheus /api/v1/rules，返回告警规则（含活动告警实例）。
// 这是当前集群可用的告警数据源（alertmanager v2 API 被 NetworkPolicy 拦截）。
func (m *MonitorService) AlertRules(ctx context.Context, c *kube.Client, cluster *model.Cluster) (*AlertRulesResponse, error) {
	cfg := PromConfigOf(cluster)
	raw, err := m.doPromRaw(ctx, c, cfg, "rules")
	if err != nil {
		return nil, err
	}
	data, _ := raw["data"].(map[string]interface{})
	groupsRaw, _ := data["groups"].([]interface{})

	out := &AlertRulesResponse{Groups: len(groupsRaw), Rules: []AlertRule{}, GroupList: []GroupSummary{}}
	out.Summary.BySeverity = map[string]int{}

	for _, gRaw := range groupsRaw {
		g, _ := gRaw.(map[string]interface{})
		groupName, _ := g["name"].(string)
		lastEval, _ := g["lastEvaluation"].(string)
		if out.LastEval == "" && lastEval != "" {
			out.LastEval = lastEval
		}
		interval, _ := g["interval"].(float64)
		rulesRaw, _ := g["rules"].([]interface{})

		// 分组级汇总
		gs := GroupSummary{Name: groupName, Interval: fmt.Sprintf("%.0fs", interval), LastEval: lastEval}
		sevSeen := map[string]bool{}

		for _, rRaw := range rulesRaw {
			r, _ := rRaw.(map[string]interface{})
			rType, _ := r["type"].(string)
			if rType != "alerting" {
				// 记录型规则仅计入分组统计，不进入告警规则列表
				gs.RecRules++
				continue
			}
			gs.AlertRules++

			name, _ := r["name"].(string)
			state, _ := r["state"].(string)
			duration, _ := r["duration"].(float64)
			labels, _ := r["labels"].(map[string]interface{})
			ann, _ := r["annotations"].(map[string]interface{})
			summaryText, _ := ann["summary"].(string)
			descText, _ := ann["description"].(string)
			runbook, _ := ann["runbook_url"].(string)
			severity, _ := labels["severity"].(string)
			severityPresent := severity != ""
			if severity == "" {
				severity = "none"
			}
			ns, _ := labels["namespace"].(string)

			if state == "firing" {
				gs.Firing++
			}
			if !sevSeen[severity] {
				sevSeen[severity] = true
				gs.Severities = append(gs.Severities, severity)
			}

			// 活动实例（state=firing 时才有）
			var instances []AlertInstance
			if alertsRaw, ok := r["alerts"].([]interface{}); ok {
				for _, aRaw := range alertsRaw {
					a, _ := aRaw.(map[string]interface{})
					inst := AlertInstance{}
					if aLabels, ok := a["labels"].(map[string]interface{}); ok {
						inst.Labels = make(map[string]string, len(aLabels))
						for k, v := range aLabels {
							inst.Labels[k] = fmt.Sprintf("%v", v)
						}
						if ns == "" {
							ns = inst.Labels["namespace"]
						}
					}
					inst.Started, _ = a["activeAt"].(string)
					inst.Value, _ = a["value"].(string)
					instances = append(instances, inst)
				}
			}

			out.Rules = append(out.Rules, AlertRule{
				AlertName:    name,
				State:        state,
				Severity:     severity,
				Namespace:    ns,
				Summary:      summaryText,
				Detail:       descText,
				Runbook:      runbook,
				Group:        groupName,
				Query:        fmt.Sprintf("%v", r["query"]),
				Duration:     duration,
				LastEval:     lastEval,
				Instances:    instances,
				HasSeverity:  severityPresent,
				HasSummary:   summaryText != "",
				HasRunbook:   runbook != "",
				HasNamespace: ns != "" || instHasNamespace(instances),
			})

			// 统计
			out.Summary.Total++
			switch state {
			case "firing":
				out.Summary.Firing++
			case "pending":
				out.Summary.Pending++
			default:
				out.Summary.Inactive++
			}
			out.Summary.BySeverity[severity]++
		}
		out.GroupList = append(out.GroupList, gs)
	}
	return out, nil
}

// instHasNamespace 任一活动实例带 namespace 标签即视为有命名空间
func instHasNamespace(insts []AlertInstance) bool {
	for _, it := range insts {
		if it.Labels["namespace"] != "" {
			return true
		}
	}
	return false
}

// RangeSpec 时间范围换算
type RangeSpec struct {
	Start time.Time
	End   time.Time
	Step  time.Duration
}

func rangeSpec(r string) RangeSpec {
	end := time.Now()
	// step 取「约 100~150 个数据点」粒度：更细的步长对趋势图无视觉收益，
	// 但 node_* 指标 range 查询成本随点数线性增长（24h@5min 单条可达 ~9s）。
	switch r {
	case "1h":
		return RangeSpec{Start: end.Add(-1 * time.Hour), End: end, Step: 30 * time.Second}
	case "6h":
		return RangeSpec{Start: end.Add(-6 * time.Hour), End: end, Step: 3 * time.Minute}
	default:
		return RangeSpec{Start: end.Add(-24 * time.Hour), End: end, Step: 15 * time.Minute}
	}
}


// nodeCPUQL 节点 CPU 使用率排行（join node_uname_info 获取 nodename 标签）
func nodeCPUQL() string {
	return `100 - (avg by(nodename)(rate(node_cpu_seconds_total{mode="idle"}[5m]) * on(instance) group_left(nodename) node_uname_info) * 100)`
}

// nodeSel 节点选择子句：node_exporter 指标 join nodename 并按节点过滤
func nodeSel(expr, nodeName string) string {
	filter := ""
	if nodeName != "" {
		filter = fmt.Sprintf(`{nodename="%s"}`, nodeName)
	}
	return fmt.Sprintf(`(%s) * on(instance) group_left(nodename) node_uname_info%s`, expr, filter)
}

// nodeMemQL 节点内存使用率查询（可选按节点过滤）
func nodeMemQL(nodeName string) string {
	expr := `(1 - node_memory_MemAvailable_bytes/node_memory_MemTotal_bytes) * 100`
	return fmt.Sprintf(`avg by(nodename)(%s)`, nodeSel(expr, nodeName))
}

// series 提取范围查询结果为时序数据（前端图表用）
func series(result []PromResult) [][]float64 {
	out := make([][]float64, 0, len(result))
	for _, r := range result {
		for _, v := range r.Values {
			if len(v) >= 2 {
				ts, _ := v[0].(float64)
				val, _ := strconv.ParseFloat(fmt.Sprintf("%v", v[1]), 64)
				out = append(out, []float64{ts, val})
			}
		}
	}
	return out
}




// ------------------- 指标查询（五级） -------------------

// ClusterMonitor 集群级监控数据
type ClusterMonitor struct {
	CPUCores         int                `json:"cpuCores"`
	MemTotalGi       float64            `json:"memTotalGi"`
	CPUUsagePct      float64            `json:"cpuUsagePct"`
	MemUsagePct      float64            `json:"memUsagePct"`
	DiskUsagePct     float64            `json:"diskUsagePct"`
	NetRxMBs         float64            `json:"netRxMBs"`
	NetTxMBs         float64            `json:"netTxMBs"`
	CPUUsageTrend    [][]float64        `json:"cpuUsageTrend"`
	MemUsageTrend    [][]float64        `json:"memUsageTrend"`
	DiskUsageTrend   [][]float64        `json:"diskUsageTrend"`
	NetRxTrend       [][]float64        `json:"netRxTrend"`
	NetTxTrend       [][]float64        `json:"netTxTrend"`
	NodeCPURank      []RankItem         `json:"nodeCpuRank"`
	NodeMemRank      []RankItem         `json:"nodeMemRank"`
	NamespaceCPURank []RankItem         `json:"namespaceCpuRank"`
	ControlPlane     []ControlPlaneItem `json:"controlPlane"`
}

type RankItem struct {
	Name    string  `json:"name"`
	Value   float64 `json:"value"` // 百分比
}

// ControlPlaneItem 控制面组件监控（kube-apiserver/controller-manager/scheduler 等）
type ControlPlaneItem struct {
	Name  string        `json:"name"`
	Ready int           `json:"ready"`
	Total int           `json:"total"`
	Cards []ControlCard `json:"cards"`
}

type ControlCard struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
	Dec   int     `json:"dec"` // 小数位数
}

// controlCardDef 组件关键指标定义
type controlCardDef struct {
	label string
	unit  string
	dec   int
	ql    string
}

// controlPlane 采集控制面组件监控数据（up 状态 + 关键指标，缺失时静默降级）
// 组件 job 名随监控栈发行版而异（如 apiserver vs kube-apiserver、kube-dns vs coredns），用候选列表匹配
func (m *MonitorService) controlPlane(ctx context.Context, c *kube.Client, cfg PromConfig) []ControlPlaneItem {
	// 组件 up 状态：按 job 统计就绪/总数
	upByJob := map[string][2]int{}
	if res, err := m.Query(ctx, c, cfg, `up{job=~"kube-apiserver|apiserver|kube-controller-manager|kube-scheduler|kube-proxy|coredns|kube-dns|etcd|kube-etcd"}`); err == nil {
		for _, r := range res {
			job := r.Metric["job"]
			v, _ := strconv.ParseFloat(fmt.Sprintf("%v", r.Value[1]), 64)
			stat := upByJob[job]
			stat[1]++
			if v == 1 {
				stat[0]++
			}
			upByJob[job] = stat
		}
	}

	comps := []struct {
		name  string
		jobs  []string
		cards []controlCardDef
	}{
		{"kube-apiserver", []string{"kube-apiserver", "apiserver"}, []controlCardDef{
			{"请求 QPS", "req/s", 0, `sum(rate(apiserver_request_total[5m]))`},
			{"错误率", "%", 2, `sum(rate(apiserver_request_total{code=~"5.."}[5m])) / clamp_min(sum(rate(apiserver_request_total[5m])), 0.001) * 100`},
			{"P99 延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(apiserver_request_sli_duration_seconds_bucket[5m]))) * 1000`},
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-apiserver|apiserver"})`},
		}},
		{"kube-controller-manager", []string{"kube-controller-manager"}, []controlCardDef{
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-controller-manager"})`},
			{"任务处理率", "次/s", 1, `sum(rate(workqueue_adds_total{job=~"kube-controller-manager"}[5m]))`},
		}},
		{"kube-scheduler", []string{"kube-scheduler"}, []controlCardDef{
			{"待调度 Pod", "个", 0, `sum(scheduler_pending_pods)`},
			{"P99 调度尝试延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(scheduler_scheduling_attempt_duration_seconds_bucket[5m]))) * 1000`},
		}},
		{"kube-proxy", []string{"kube-proxy"}, []controlCardDef{
			{"P99 网络编程延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(kubeproxy_network_programming_duration_seconds_bucket[5m]))) * 1000`},
			{"无本地端点", "次/s", 1, `sum(rate(kubeproxy_sync_proxy_rules_no_local_endpoints_total[5m]))`},
		}},
		{"coredns", []string{"coredns", "kube-dns"}, []controlCardDef{
			{"DNS QPS", "req/s", 0, `sum(rate(coredns_dns_requests_total[5m]))`},
			{"SERVFAIL 错误率", "%", 2, `sum(rate(coredns_dns_responses_total{rcode="SERVFAIL"}[5m])) / clamp_min(sum(rate(coredns_dns_requests_total[5m])), 0.001) * 100`},
		}},
		{"etcd", []string{"etcd", "kube-etcd"}, []controlCardDef{
			{"Leader", "", 0, `etcd_server_has_leader{job=~"etcd|kube-etcd"}`},
			{"WAL fsync P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(etcd_disk_wal_fsync_duration_seconds_bucket{job=~"etcd|kube-etcd"}[5m]))) * 1000`},
			{"DB 大小", "Mi", 1, `etcd_mvcc_db_total_size_in_bytes{job=~"etcd|kube-etcd"} / 1024 / 1024`},
		}},
	}

	// 收集所有待查询的指标（组件 + 卡片），统一并行执行
	type cardRef struct {
		cpIdx int
		card  controlCardDef
	}
	var refs []cardRef
	for i, cp := range comps {
		for _, card := range cp.cards {
			refs = append(refs, cardRef{cpIdx: i, card: card})
		}
	}
	resByRef := make([][]PromResult, len(refs))
	{
		const workers = 8
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for i := range refs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if res, err := m.Query(ctx, c, cfg, refs[i].card.ql); err == nil && len(res) > 0 {
					resByRef[i] = res
				}
			}(i)
		}
		wg.Wait()
	}

	out := make([]ControlPlaneItem, 0, len(comps))
	for i, cp := range comps {
		item := ControlPlaneItem{Name: cp.name}
		for _, job := range cp.jobs {
			if stat, ok := upByJob[job]; ok {
				item.Total += stat[1]
				item.Ready += stat[0]
			}
		}
		// 回填指标卡片（保持原始顺序，组件未采集时静默跳过）
		for j := range refs {
			if refs[j].cpIdx != i {
				continue
			}
			if res := resByRef[j]; res != nil {
				v := parsePromFloat(res[0].Value[1])
				item.Cards = append(item.Cards, ControlCard{Label: refs[j].card.label, Value: v, Unit: refs[j].card.unit, Dec: refs[j].card.dec})
			}
		}
		out = append(out, item)
	}
	return out
}

// parsePromFloat 解析 Prometheus 值，NaN/Inf 归一为 0（JSON 不支持）
func parsePromFloat(v any) float64 {
	f, err := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// Overview 集群级监控：CPU/内存使用率 + 排行
// 所有 Prometheus 查询彼此独立，用有界 worker 池并行执行，避免逐个串行累加延迟
// （集群的 node_* range 查询单次可达数秒，串行会让总耗时到 10s+）。
func (m *MonitorService) Overview(ctx context.Context, c *kube.Client, cluster *model.Cluster, r string) (*ClusterMonitor, error) {
	cfg := PromConfigOf(cluster)
	rs := rangeSpec(r)
	out := &ClusterMonitor{}

	qInst := `100 - (avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`
	qMem := `(1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes)) * 100`
	qDisk := `(1 - sum(node_filesystem_avail_bytes{mountpoint="/"}) / sum(node_filesystem_size_bytes{mountpoint="/"})) * 100`
	qNetRx := `sum(rate(node_network_receive_bytes_total[5m])) / 1024 / 1024`
	qNetTx := `sum(rate(node_network_transmit_bytes_total[5m])) / 1024 / 1024`
	qNS := `sum by(namespace)(rate(container_cpu_usage_seconds_total{container!="",cpu="total",namespace!=""}[5m])) / scalar(count(node_cpu_seconds_total{mode="idle"})) * 100`

	// task = 一个独立的 Prometheus 查询任务（互不依赖，可并行）
	type task func()
	var tasks []task

	// 瞬时使用率 / 容量
	tasks = append(tasks,
		func() { if res, err := m.Query(ctx, c, cfg, qInst); err == nil && len(res) > 0 { out.CPUUsagePct, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
		func() { if res, err := m.Query(ctx, c, cfg, qMem); err == nil && len(res) > 0 { out.MemUsagePct, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
		func() { if res, err := m.Query(ctx, c, cfg, `count(node_cpu_seconds_total{mode="idle"})`); err == nil && len(res) > 0 { out.CPUCores, _ = strconv.Atoi(fmt.Sprintf("%v", res[0].Value[1])) } },
		func() { if res, err := m.Query(ctx, c, cfg, `sum(node_memory_MemTotal_bytes) / 1024 / 1024 / 1024`); err == nil && len(res) > 0 { out.MemTotalGi, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
		func() { if res, err := m.Query(ctx, c, cfg, qDisk); err == nil && len(res) > 0 { out.DiskUsagePct, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
		func() { if res, err := m.Query(ctx, c, cfg, qNetRx); err == nil && len(res) > 0 { out.NetRxMBs, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
		func() { if res, err := m.Query(ctx, c, cfg, qNetTx); err == nil && len(res) > 0 { out.NetTxMBs, _ = strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64) } },
	)
	// 趋势（node_* range 查询，最慢，必须并行）
	tasks = append(tasks,
		func() { if res, err := m.QueryRange(ctx, c, cfg, qInst, rs.Start, rs.End, rs.Step); err == nil { out.CPUUsageTrend = series(res) } },
		func() { if res, err := m.QueryRange(ctx, c, cfg, qMem, rs.Start, rs.End, rs.Step); err == nil { out.MemUsageTrend = series(res) } },
		func() { if res, err := m.QueryRange(ctx, c, cfg, qDisk, rs.Start, rs.End, rs.Step); err == nil { out.DiskUsageTrend = series(res) } },
		func() { if res, err := m.QueryRange(ctx, c, cfg, qNetRx, rs.Start, rs.End, rs.Step); err == nil { out.NetRxTrend = series(res) } },
		func() { if res, err := m.QueryRange(ctx, c, cfg, qNetTx, rs.Start, rs.End, rs.Step); err == nil { out.NetTxTrend = series(res) } },
	)
	// 排行
	tasks = append(tasks,
		func() {
			if res, err := m.Query(ctx, c, cfg, nodeCPUQL()); err == nil {
				for _, rr := range res {
					v, _ := strconv.ParseFloat(fmt.Sprintf("%v", rr.Value[1]), 64)
					out.NodeCPURank = append(out.NodeCPURank, RankItem{Name: rr.Metric["nodename"], Value: v})
				}
			}
		},
		func() {
			if res, err := m.Query(ctx, c, cfg, nodeMemQL("")); err == nil {
				for _, rr := range res {
					v, _ := strconv.ParseFloat(fmt.Sprintf("%v", rr.Value[1]), 64)
					out.NodeMemRank = append(out.NodeMemRank, RankItem{Name: rr.Metric["nodename"], Value: v})
				}
			}
		},
		func() {
			if res, err := m.Query(ctx, c, cfg, qNS); err == nil {
				for _, rr := range res {
					v, _ := strconv.ParseFloat(fmt.Sprintf("%v", rr.Value[1]), 64)
					out.NamespaceCPURank = append(out.NamespaceCPURank, RankItem{Name: rr.Metric["namespace"], Value: v})
				}
			}
		},
	)

	// 有界 worker 池：限制并发（避免压垮单实例 Prometheus）。
	// 单实例 Prometheus 对并发 range 查询会互相拖慢，过高并发反而更慢，取折中值 4。
	const workers = 4
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t()
		}(t)
	}
	wg.Wait()

	// 控制面组件监控（内部也并行查询各组件指标）
	out.ControlPlane = m.controlPlane(ctx, c, cfg)
	return out, nil
}

// NodeMonitor 节点级监控
type NodeMonitor struct {
	CPUUsagePct    float64     `json:"cpuUsagePct"`
	MemUsagePct    float64     `json:"memUsagePct"`
	DiskUsagePct   float64     `json:"diskUsagePct"`
	NetRxMBs       float64     `json:"netRxMBs"`
	NetTxMBs       float64     `json:"netTxMBs"`
	CPUUsageTrend  [][]float64 `json:"cpuUsageTrend"`
	MemUsageTrend  [][]float64 `json:"memUsageTrend"`
	DiskUsageTrend [][]float64 `json:"diskUsageTrend"`
	NetRxTrend     [][]float64 `json:"netRxTrend"`
	NetTxTrend     [][]float64 `json:"netTxTrend"`
}

// Node 节点监控
func (m *MonitorService) Node(ctx context.Context, c *kube.Client, cluster *model.Cluster, name, r string) (*NodeMonitor, error) {
	cfg := PromConfigOf(cluster)
	rs := rangeSpec(r)
	out := &NodeMonitor{}

	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	diskExpr := `(1 - sum by(instance)(node_filesystem_avail_bytes{mountpoint="/"}) / sum by(instance)(node_filesystem_size_bytes{mountpoint="/"})) * 100`
	netExpr := func(dir string) string {
		return fmt.Sprintf(`sum by(nodename)(rate(node_network_%s_bytes_total[5m]) * on(instance) group_left(nodename) node_uname_info{nodename="%s"}) / 1024 / 1024`, dir, name)
	}
	out.CPUUsagePct, _ = q(fmt.Sprintf(`100 - (avg by(nodename)(rate(node_cpu_seconds_total{mode="idle"}[5m]) * on(instance) group_left(nodename) node_uname_info{nodename="%s"}) * 100)`, name))
	out.MemUsagePct, _ = q(nodeMemQL(name))
	out.DiskUsagePct, _ = q(fmt.Sprintf(`avg by(nodename)(%s)`, nodeSel(diskExpr, name)))
	out.NetRxMBs, _ = q(netExpr("receive"))
	out.NetTxMBs, _ = q(netExpr("transmit"))

	qr := func(promql string) [][]float64 {
		res, err := m.QueryRange(ctx, c, cfg, promql, rs.Start, rs.End, rs.Step)
		if err != nil {
			return nil
		}
		return series(res)
	}
	out.CPUUsageTrend = qr(fmt.Sprintf(`100 - (avg by(nodename)(rate(node_cpu_seconds_total{mode="idle"}[5m]) * on(instance) group_left(nodename) node_uname_info{nodename="%s"}) * 100)`, name))
	out.MemUsageTrend = qr(nodeMemQL(name))
	out.DiskUsageTrend = qr(fmt.Sprintf(`avg by(nodename)(%s)`, nodeSel(diskExpr, name)))
	out.NetRxTrend = qr(netExpr("receive"))
	out.NetTxTrend = qr(netExpr("transmit"))
	return out, nil
}

// NamespaceMonitor 命名空间级监控
type NamespaceMonitor struct {
	CPUUsagePct    float64     `json:"cpuUsagePct"`
	MemUsagePct    float64     `json:"memUsagePct"`
	PodCount       int         `json:"podCount"`
	DiskWriteMBs   float64     `json:"diskWriteMBs"`
	NetRxMBs       float64     `json:"netRxMBs"`
	NetTxMBs       float64     `json:"netTxMBs"`
	CPUUsageTrend  [][]float64 `json:"cpuUsageTrend"`
	MemUsageTrend  [][]float64 `json:"memUsageTrend"`
	PodCountTrend  [][]float64 `json:"podCountTrend"`
	DiskWriteTrend [][]float64 `json:"diskWriteTrend"`
	NetRxTrend     [][]float64 `json:"netRxTrend"`
	NetTxTrend     [][]float64 `json:"netTxTrend"`
}

// Namespace 命名空间监控
func (m *MonitorService) Namespace(ctx context.Context, c *kube.Client, cluster *model.Cluster, name, r string) (*NamespaceMonitor, error) {
	cfg := PromConfigOf(cluster)
	rs := rangeSpec(r)
	out := &NamespaceMonitor{}

	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	// CPU 使用率（容器 CPU / 集群总核数）
	out.CPUUsagePct, _ = q(fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",container!="",cpu="total"}[5m])) / scalar(count(node_cpu_seconds_total{mode="idle"})) * 100`, name))
	out.MemUsagePct, _ = q(fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",container!=""}) / sum(node_memory_MemTotal_bytes) * 100`, name))
	if res, err := m.Query(ctx, c, cfg, fmt.Sprintf(`count(kube_pod_info{namespace="%s"})`, name)); err == nil && len(res) > 0 {
		v, _ := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		out.PodCount = int(v)
	}
	// 磁盘写 / 网络吞吐（MB/s）
	out.DiskWriteMBs, _ = q(fmt.Sprintf(`sum(rate(container_fs_writes_bytes_total{namespace="%s",container!=""}[5m])) / 1024 / 1024`, name))
	out.NetRxMBs, _ = q(fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace="%s"}[5m])) / 1024 / 1024`, name))
	out.NetTxMBs, _ = q(fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace="%s"}[5m])) / 1024 / 1024`, name))

	qr := func(promql string) [][]float64 {
		res, err := m.QueryRange(ctx, c, cfg, promql, rs.Start, rs.End, rs.Step)
		if err != nil {
			return nil
		}
		return series(res)
	}
	out.CPUUsageTrend = qr(fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",container!="",cpu="total"}[5m])) / scalar(count(node_cpu_seconds_total{mode="idle"})) * 100`, name))
	out.MemUsageTrend = qr(fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",container!=""}) / sum(node_memory_MemTotal_bytes) * 100`, name))
	out.PodCountTrend = qr(fmt.Sprintf(`count(kube_pod_info{namespace="%s"})`, name))
	out.DiskWriteTrend = qr(fmt.Sprintf(`sum(rate(container_fs_writes_bytes_total{namespace="%s",container!=""}[5m])) / 1024 / 1024`, name))
	out.NetRxTrend = qr(fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace="%s"}[5m])) / 1024 / 1024`, name))
	out.NetTxTrend = qr(fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace="%s"}[5m])) / 1024 / 1024`, name))
	return out, nil
}

// WorkloadMonitor 工作负载（控制器）级监控
// CPUUsagePct / MemUsagePct 用 *float64：无有效 limit（容器未全覆盖）时为 nil，前端改用 CPUUsage/MemUsageMi 显示绝对值
type WorkloadMonitor struct {
	CPUUsagePct    *float64    `json:"cpuUsagePct"`
	MemUsagePct    *float64    `json:"memUsagePct"`
	CPUUsage       float64     `json:"cpuUsage"`   // 核数（绝对值，百分比不可用时前端展示）
	MemUsageMi     float64     `json:"memUsageMi"` // Mi（绝对值，百分比不可用时前端展示）
	PodCount       int         `json:"podCount"`
	DiskWriteMBs   float64     `json:"diskWriteMBs"`
	NetRxMBs       float64     `json:"netRxMBs"`
	NetTxMBs       float64     `json:"netTxMBs"`
	CPUUsageTrend  [][]float64 `json:"cpuUsageTrend"`  // 百分比或核数（见 cpuTrendIsPct）
	MemUsageTrend  [][]float64 `json:"memUsageTrend"`  // 百分比或 Mi（见 memTrendIsPct）
	CPUTrendIsPct  bool        `json:"cpuTrendIsPct"`
	MemTrendIsPct  bool        `json:"memTrendIsPct"`
	DiskWriteTrend [][]float64 `json:"diskWriteTrend"`
	NetRxTrend     [][]float64 `json:"netRxTrend"`
	NetTxTrend     [][]float64 `json:"netTxTrend"`
}

// countContainers 返回 pod 集合中带 cadvisor 指标的实际容器数（去重）。
// 用 container_memory_working_set_bytes 判定（运行中的容器都会上报），
// 与 kube-state-metrics / cadvisor 的 per-container 行一一对应。
func (m *MonitorService) countContainers(ctx context.Context, c *kube.Client, cfg PromConfig, namespace, podRe string) (int, bool) {
	res, err := m.Query(ctx, c, cfg, fmt.Sprintf(`count(count by(container) (container_memory_working_set_bytes{namespace="%s",pod=~"%s",container!=""}))`, namespace, podRe))
	if err != nil || len(res) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

// sumLimitedCpu 返回 pod 集合中"设置了 CPU limit"的容器数及其 limit 总和（核数）。
// kube_pod_container_resource_limits 只对设置了 limit 的容器有数据，
// 无 limit 的容器不出现，因此需用 container 行数判断覆盖率。
func (m *MonitorService) sumLimitedCpu(ctx context.Context, c *kube.Client, cfg PromConfig, namespace, podRe string) (float64, int, bool) {
	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	sum, ok := q(fmt.Sprintf(`sum(kube_pod_container_resource_limits{namespace="%s",pod=~"%s",resource="cpu"})`, namespace, podRe))
	if !ok || sum <= 0 {
		return 0, 0, false
	}
	cnt, ok := q(fmt.Sprintf(`count(kube_pod_container_resource_limits{namespace="%s",pod=~"%s",resource="cpu"})`, namespace, podRe))
	if !ok {
		return 0, 0, false
	}
	return sum, int(cnt), true
}

// sumLimitedMem 返回 pod 集合中"设置了内存 limit"的容器数及其 limit 总和（字节）。
// cadvisor 的 container_spec_memory_limit_bytes 对无 limit 的容器上报 ~0.99PiB，
// 因此用 limit < 1TiB 过滤出真正设置了 limit 的容器。
func (m *MonitorService) sumLimitedMem(ctx context.Context, c *kube.Client, cfg PromConfig, namespace, podRe string) (float64, int, bool) {
	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	const oneTiB = int64(1024 * 1024 * 1024 * 1024)
	sum, ok := q(fmt.Sprintf(`sum(container_spec_memory_limit_bytes{namespace="%s",pod=~"%s",container!="",container_spec_memory_limit_bytes<%d})`, namespace, podRe, oneTiB))
	if !ok || sum <= 0 {
		return 0, 0, false
	}
	cnt, ok := q(fmt.Sprintf(`count(container_spec_memory_limit_bytes{namespace="%s",pod=~"%s",container!="",container_spec_memory_limit_bytes<%d})`, namespace, podRe, oneTiB))
	if !ok {
		return 0, 0, false
	}
	return sum, int(cnt), true
}

// Workload 工作负载监控（按 selector 聚合容器指标）
func (m *MonitorService) Workload(ctx context.Context, c *kube.Client, cluster *model.Cluster, kind, namespace, name, r string) (*WorkloadMonitor, error) {
	cfg := PromConfigOf(cluster)
	rs := rangeSpec(r)
	out := &WorkloadMonitor{}

	// 获取工作负载的 Pod 列表（复用 K8sService）
	k8s := NewK8sService("")
	detail, err := k8s.GetWorkloadDetail(ctx, c, kind, namespace, name)
	if err != nil {
		return nil, err
	}
	out.PodCount = len(detail.Pods)
	if len(detail.Pods) == 0 {
		return out, nil
	}
	var names []string
	for _, p := range detail.Pods {
		names = append(names, p.Name)
	}
	podRe := "^(" + strings.Join(names, "|") + ")$"

	// CPU 核数（容器聚合）
	cpuQL := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s",container!="",cpu="total"}[5m]))`, namespace, podRe)
	memQL := fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",pod=~"%s",container!=""})`, namespace, podRe)

	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	qr := func(promql string) [][]float64 {
		res, err := m.QueryRange(ctx, c, cfg, promql, rs.Start, rs.End, rs.Step)
		if err != nil {
			return nil
		}
		return series(res)
	}
	// CPU/内存使用率：仅当"所有容器都设置了 limit"时才有意义。
	// kube-state-metrics / cadvisor 的 per-container limit 指标只对设置了 limit 的容器有数据，
	// 无 limit 的容器不出现，直接 sum 会低估分母导致百分比虚高（如 5000%）。
	// 因此先比较"设置了 limit 的容器数"与"实际容器数"，覆盖不全时不展示百分比。
	cpuLim, cpuLimCnt, cpuLimOk := m.sumLimitedCpu(ctx, c, cfg, namespace, podRe)
	memLim, memLimCnt, memLimOk := m.sumLimitedMem(ctx, c, cfg, namespace, podRe)
	totalContainers, contOk := m.countContainers(ctx, c, cfg, namespace, podRe)

	cpuPctTrend := cpuLimOk && contOk && cpuLimCnt >= totalContainers && cpuLim > 0
	memPctTrend := memLimOk && contOk && memLimCnt >= totalContainers && memLim > 0

	if cpu, ok := q(cpuQL); ok {
		out.CPUUsage = cpu // 绝对值（核数），百分比不可用时前端展示
		if cpuPctTrend {
			v := cpu / cpuLim * 100
			out.CPUUsagePct = &v
		}
	}
	if mem, ok := q(memQL); ok {
		out.MemUsageMi = mem / 1024 / 1024 // 绝对值（Mi）
		if memPctTrend {
			v := mem / memLim * 100
			out.MemUsagePct = &v
		}
	}
	// 磁盘写 / 网络吞吐（MB/s，按工作负载 Pod 聚合）
	diskWriteQL := fmt.Sprintf(`sum(rate(container_fs_writes_bytes_total{namespace="%s",pod=~"%s",container!=""}[5m])) / 1024 / 1024`, namespace, podRe)
	netRxQL := fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace="%s",pod=~"%s"}[5m])) / 1024 / 1024`, namespace, podRe)
	netTxQL := fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace="%s",pod=~"%s"}[5m])) / 1024 / 1024`, namespace, podRe)
	out.DiskWriteMBs, _ = q(diskWriteQL)
	out.NetRxMBs, _ = q(netRxQL)
	out.NetTxMBs, _ = q(netTxQL)

	// 趋势：有有效 limit 时换算百分比；否则 CPU 保持核数、内存换算为 Mi（绝对值），
	// 并把单位标志传给前端，避免「字节值画在 % 坐标轴上」造成视觉虚高
	out.CPUTrendIsPct = cpuPctTrend
	out.MemTrendIsPct = memPctTrend
	if res, err := m.QueryRange(ctx, c, cfg, cpuQL, rs.Start, rs.End, rs.Step); err == nil {
		raw := series(res)
		if cpuPctTrend {
			for i := range raw {
				raw[i][1] = raw[i][1] / cpuLim * 100
			}
		}
		out.CPUUsageTrend = raw
	}
	if res, err := m.QueryRange(ctx, c, cfg, memQL, rs.Start, rs.End, rs.Step); err == nil {
		raw := series(res)
		if memPctTrend {
			for i := range raw {
				raw[i][1] = raw[i][1] / memLim * 100
			}
		} else {
			for i := range raw {
				raw[i][1] = raw[i][1] / 1024 / 1024 // 字节 -> Mi
			}
		}
		out.MemUsageTrend = raw
	}
	out.DiskWriteTrend = qr(diskWriteQL)
	out.NetRxTrend = qr(netRxQL)
	out.NetTxTrend = qr(netTxQL)
	return out, nil
}

// PodMonitor Pod 级监控
// CPUUsagePct / MemUsagePct 用 *float64：无有效 limit（容器未全覆盖）时为 nil，前端显示 --
type PodMonitor struct {
	CPUUsagePct   *float64    `json:"cpuUsagePct"`
	MemUsageMi    float64     `json:"memUsageMi"`
	MemUsagePct   *float64    `json:"memUsagePct"`
	NetRxMBs      float64     `json:"netRxMBs"`
	NetTxMBs      float64     `json:"netTxMBs"`
	DiskWriteMBs  float64     `json:"diskWriteMBs"`
	FsUsageMi     float64     `json:"fsUsageMi,omitempty"`
	CPUUsageTrend [][]float64 `json:"cpuUsageTrend"`
	MemUsageTrend [][]float64 `json:"memUsageTrend"`
	NetRxTrend    [][]float64 `json:"netRxTrend"`
	NetTxTrend    [][]float64 `json:"netTxTrend"`
	DiskWriteTrend [][]float64 `json:"diskWriteTrend"`
	FsUsageTrend  [][]float64 `json:"fsUsageTrend"`
}

// Pod Pod 监控
func (m *MonitorService) Pod(ctx context.Context, c *kube.Client, cluster *model.Cluster, namespace, name, r string) (*PodMonitor, error) {
	cfg := PromConfigOf(cluster)
	rs := rangeSpec(r)
	out := &PodMonitor{}

	cpuQL := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod="%s",container!="",cpu="total"}[5m]))`, namespace, name)
	memQL := fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",pod="%s",container!=""})`, namespace, name)
	netRxQL := fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace="%s",pod="%s"}[5m])) / 1024 / 1024`, namespace, name)
	netTxQL := fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace="%s",pod="%s"}[5m])) / 1024 / 1024`, namespace, name)
	diskWriteQL := fmt.Sprintf(`sum(rate(container_fs_writes_bytes_total{namespace="%s",pod="%s",container!=""}[5m])) / 1024 / 1024`, namespace, name)
	fsUsageQL := fmt.Sprintf(`sum(container_fs_usage_bytes{namespace="%s",pod="%s",container!=""}) / 1024 / 1024`, namespace, name)

	q := func(promql string) (float64, bool) {
		res, err := m.Query(ctx, c, cfg, promql)
		if err != nil || len(res) == 0 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fmt.Sprintf("%v", res[0].Value[1]), 64)
		return v, err == nil
	}
	// CPU/内存使用率：仅当该 Pod 所有容器都设置了 limit 时才有意义（覆盖不全时分母被低估，百分比虚高）
	podRe := "^(" + name + ")$"
	cpuLim, cpuLimCnt, cpuLimOk := m.sumLimitedCpu(ctx, c, cfg, namespace, podRe)
	memLim, memLimCnt, memLimOk := m.sumLimitedMem(ctx, c, cfg, namespace, podRe)
	totalContainers, contOk := m.countContainers(ctx, c, cfg, namespace, podRe)

	cpuPctTrend := cpuLimOk && contOk && cpuLimCnt >= totalContainers && cpuLim > 0
	memPctTrend := memLimOk && contOk && memLimCnt >= totalContainers && memLim > 0

	if cpu, ok := q(cpuQL); ok && cpuPctTrend {
		v := cpu / cpuLim * 100
		out.CPUUsagePct = &v
	}
	if mem, ok := q(memQL); ok {
		out.MemUsageMi = mem / 1024 / 1024
		if memPctTrend {
			v := mem / memLim * 100
			out.MemUsagePct = &v
		}
	}
	out.NetRxMBs, _ = q(netRxQL)
	out.NetTxMBs, _ = q(netTxQL)
	out.DiskWriteMBs, _ = q(diskWriteQL)
	// 容器文件系统指标部分环境不可用，仅在查询成功时赋值（前端显示 -）
	if v, ok := q(fsUsageQL); ok {
		out.FsUsageMi = v
	}

	qr := func(promql string) [][]float64 {
		res, err := m.QueryRange(ctx, c, cfg, promql, rs.Start, rs.End, rs.Step)
		if err != nil {
			return nil
		}
		return series(res)
	}
	// CPU 趋势换算百分比（相对限额）；覆盖不全时保持原始核数；内存趋势保持 Mi 绝对值
	if res, err := m.QueryRange(ctx, c, cfg, cpuQL, rs.Start, rs.End, rs.Step); err == nil {
		raw := series(res)
		if cpuPctTrend {
			for i := range raw {
				raw[i][1] = raw[i][1] / cpuLim * 100
			}
		}
		out.CPUUsageTrend = raw
	}
	out.MemUsageTrend = convertTrend(qr(memQL), func(v float64) float64 { return v / 1024 / 1024 })
	out.NetRxTrend = qr(netRxQL)
	out.NetTxTrend = qr(netTxQL)
	out.DiskWriteTrend = qr(diskWriteQL)
	out.FsUsageTrend = qr(fsUsageQL)
	return out, nil
}

// convertTrend 对趋势序列逐点做数值转换（如字节 -> Mi）
func convertTrend(points [][]float64, fn func(float64) float64) [][]float64 {
	if points == nil {
		return nil
	}
	out := make([][]float64, len(points))
	for i, p := range points {
		out[i] = []float64{p[0], fn(p[1])}
	}
	return out
}


// FiringAlerts 拉取当前 firing 状态的告警（供通知轮询使用）
func (m *MonitorService) FiringAlerts(ctx context.Context, c *kube.Client, cfg PromConfig) ([]PromAlertRef, error) {
	u, err := proxyURL(c, cfg, "alerts", url.Values{})
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(c.Config)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if bearer := c.Config.BearerToken; bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus 返回 %d", resp.StatusCode)
	}
	var ar struct {
		Data struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    string            `json:"activeAt"`
				Fingerprint string            `json:"fingerprint"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, err
	}
	out := []PromAlertRef{}
	for _, a := range ar.Data.Alerts {
		if a.State != "firing" && a.State != "" && a.State != "active" {
			continue
		}
		out = append(out, PromAlertRef{
			Fingerprint: a.Fingerprint,
			Name:        a.Labels["alertname"],
			Severity:    a.Labels["severity"],
			Summary:     a.Annotations["summary"],
			Description: a.Annotations["description"],
			ActiveAt:    a.ActiveAt,
		})
	}
	return out, nil
}
