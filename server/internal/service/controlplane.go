// controlplane.go 控制平面组件监控（可观测性独立页）。
//
// 为什么从集群总览搬出来独立成页、且分「列表 / 详情」两级：
//   - 总览页每次自动刷新都背着 ~18 个控制面 instant 查询 + 3 个 range 查询，
//     用户只看资源排行时也照付这份 Prometheus 成本；搬出后总览归零。
//   - 列表只查 up + 每组件 1 个概要指标（8 个 instant），详情只在点开
//     某个组件时查该组件的卡片与趋势（单组件 ≤6 instant + ≤4 range，
//     step 随时间档位放宽到 30s/3m/15m，点数封顶 ~150），
//     信息更全但 Prometheus 压力反而比旧的整块拉取更低。
package service

import (
	"context"
	"fmt"
	"sync"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// cpCard 组件详情的一张指标卡（instant 查询，空结果=未采集，静默降级）
type cpCard struct {
	Label string
	Unit  string
	Dec   int
	Ql    string
}

// cpChart 组件详情的一条趋势（range 查询）
type cpChart struct {
	Name string
	Unit string
	Dec  int
	Ql   string
}

// cpComponent 组件定义：jobs 候选随监控栈发行版而异（apiserver vs kube-apiserver 等）
type cpComponent struct {
	Name       string // canonical 名（详情接口路径参数）
	Title      string
	Desc       string
	Jobs       []string
	Summary    cpCard // 列表页概要指标
	Cards      []cpCard
	Charts     []cpChart
	UpFallback bool // job 名没匹配上但 Prometheus 可达时按 1/1 计（prometheus 自监控场景）
}

// controlPlaneComponents 组件定义表（唯一事实来源，列表/详情共用）
var controlPlaneComponents = []cpComponent{
	{
		Name: "kube-apiserver", Title: "API Server", Desc: "集群统一入口：认证鉴权、资源读写、调度各控制器的请求汇聚点",
		Jobs:    []string{"kube-apiserver", "apiserver"},
		Summary: cpCard{"请求 QPS", "req/s", 0, `sum(rate(apiserver_request_total[5m]))`},
		Cards: []cpCard{
			{"请求 QPS", "req/s", 0, `sum(rate(apiserver_request_total[5m]))`},
			{"错误率 (5xx)", "%", 2, `sum(rate(apiserver_request_total{code=~"5.."}[5m])) / clamp_min(sum(rate(apiserver_request_total[5m])), 0.001) * 100`},
			{"P99 延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(apiserver_request_sli_duration_seconds_bucket[5m]))) * 1000`},
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-apiserver|apiserver"})`},
			{"在途请求 (读)", "个", 0, `max(apiserver_current_inflight_requests{request_kind="READONLY"})`},
			{"在途请求 (写)", "个", 0, `max(apiserver_current_inflight_requests{request_kind="MUTATING"})`},
			{"内存 RSS", "Mi", 1, `max(process_resident_memory_bytes{job=~"kube-apiserver|apiserver"}) / 1048576`},
			{"Goroutine", "个", 0, `max(go_goroutines{job=~"kube-apiserver|apiserver"})`},
		},
		Charts: []cpChart{
			{"请求 QPS", "req/s", 0, `sum(rate(apiserver_request_total[5m]))`},
			{"错误率 (5xx)", "%", 2, `sum(rate(apiserver_request_total{code=~"5.."}[5m])) / clamp_min(sum(rate(apiserver_request_total[5m])), 0.001) * 100`},
			{"P99 延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(apiserver_request_sli_duration_seconds_bucket[5m]))) * 1000`},
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-apiserver|apiserver"})`},
		},
	},
	{
		Name: "kube-controller-manager", Title: "Controller Manager", Desc: "内置控制器（Node/Namespace/Endpoint 等协调回路）宿主，队列积压会拖平整个控制回路",
		Jobs:    []string{"kube-controller-manager"},
		Summary: cpCard{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-controller-manager"})`},
		Cards: []cpCard{
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-controller-manager"})`},
			{"任务入队率", "次/s", 1, `sum(rate(workqueue_adds_total{job=~"kube-controller-manager"}[5m]))`},
			{"队列等待 P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(workqueue_queue_duration_seconds_bucket{job=~"kube-controller-manager"}[5m]))) * 1000`},
			{"Leader", "", 0, `max(leader_election_master_status{job=~"kube-controller-manager"})`},
			{"Goroutine", "个", 0, `max(go_goroutines{job=~"kube-controller-manager"})`},
			{"内存 RSS", "Mi", 1, `max(process_resident_memory_bytes{job=~"kube-controller-manager"}) / 1048576`},
		},
		Charts: []cpChart{
			{"工作队列深度", "条", 0, `sum(workqueue_depth{job=~"kube-controller-manager"})`},
			{"任务入队率", "次/s", 1, `sum(rate(workqueue_adds_total{job=~"kube-controller-manager"}[5m]))`},
			{"队列等待 P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(workqueue_queue_duration_seconds_bucket{job=~"kube-controller-manager"}[5m]))) * 1000`},
		},
	},
	{
		Name: "kube-scheduler", Title: "Scheduler", Desc: "Pod 绑定决策；待调度积压增长意味着新 Pod 起不来",
		Jobs:    []string{"kube-scheduler"},
		Summary: cpCard{"待调度 Pod", "个", 0, `max(scheduler_pending_pods) or max(scheduler_pending_pods_total{queue="active"})`},
		Cards: []cpCard{
			{"待调度 Pod", "个", 0, `max(scheduler_pending_pods) or max(scheduler_pending_pods_total{queue="active"})`},
			{"不可调度", "个", 0, `max(scheduler_pending_pods_total{queue="unschedulable"})`},
			{"调度尝试率", "次/s", 2, `sum(rate(scheduler_scheduling_attempts_count[5m])) or sum(rate(scheduler_scheduling_attempt_duration_seconds_count[5m]))`},
			{"P99 调度尝试延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(scheduler_scheduling_attempt_duration_seconds_bucket[5m]))) * 1000`},
			{"Goroutine", "个", 0, `max(go_goroutines{job=~"kube-scheduler"})`},
		},
		Charts: []cpChart{
			{"待调度 Pod", "个", 0, `max(scheduler_pending_pods) or max(scheduler_pending_pods_total{queue="active"})`},
			{"P99 调度尝试延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(scheduler_scheduling_attempt_duration_seconds_bucket[5m]))) * 1000`},
			{"调度尝试率", "次/s", 2, `sum(rate(scheduler_scheduling_attempts_count[5m])) or sum(rate(scheduler_scheduling_attempt_duration_seconds_count[5m]))`},
		},
	},
	{
		Name: "kube-proxy", Title: "kube-proxy", Desc: "每节点 Service 转发规则下发；编程延迟变高多因 Service/Endpoint 抖动",
		Jobs:    []string{"kube-proxy"},
		Summary: cpCard{"P99 网络编程延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(kubeproxy_network_programming_duration_seconds_bucket[5m]))) * 1000`},
		Cards: []cpCard{
			{"P99 网络编程延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(kubeproxy_network_programming_duration_seconds_bucket[5m]))) * 1000`},
			{"规则同步率", "次/s", 2, `sum(rate(kubeproxy_sync_proxy_rules_duration_count[5m]))`},
			{"无本地端点", "次/s", 2, `sum(rate(kubeproxy_sync_proxy_rules_no_local_endpoints_total[5m]))`},
			{"同步失败", "个/s", 2, `sum(rate(kubeproxy_sync_proxy_rules_last_sync_error[5m]))`},
		},
		Charts: []cpChart{
			{"P99 网络编程延迟", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(kubeproxy_network_programming_duration_seconds_bucket[5m]))) * 1000`},
			{"无本地端点", "次/s", 2, `sum(rate(kubeproxy_sync_proxy_rules_no_local_endpoints_total[5m]))`},
		},
	},
	{
		Name: "coredns", Title: "CoreDNS", Desc: "集群 DNS；SERVFAIL 比例升高直接影响业务域名解析",
		Jobs:    []string{"coredns", "kube-dns"},
		Summary: cpCard{"DNS QPS", "req/s", 0, `sum(rate(coredns_dns_requests_total[5m]))`},
		Cards: []cpCard{
			{"DNS QPS", "req/s", 0, `sum(rate(coredns_dns_requests_total[5m]))`},
			{"SERVFAIL 错误率", "%", 2, `sum(rate(coredns_dns_responses_total{rcode="SERVFAIL"}[5m])) / clamp_min(sum(rate(coredns_dns_requests_total[5m])), 0.001) * 100`},
			{"P99 解析延迟", "ms", 2, `histogram_quantile(0.99, sum by (le) (rate(coredns_dns_request_duration_seconds_bucket[5m]))) * 1000`},
			{"缓存命中率", "%", 1, `sum(rate(coredns_cache_hits_total[5m])) / clamp_min(sum(rate(coredns_dns_requests_total[5m])), 0.001) * 100`},
			{"内存 RSS", "Mi", 1, `max(process_resident_memory_bytes{job=~"coredns|kube-dns"}) / 1048576`},
		},
		Charts: []cpChart{
			{"DNS QPS", "req/s", 0, `sum(rate(coredns_dns_requests_total[5m]))`},
			{"SERVFAIL 错误率", "%", 2, `sum(rate(coredns_dns_responses_total{rcode="SERVFAIL"}[5m])) / clamp_min(sum(rate(coredns_dns_requests_total[5m])), 0.001) * 100`},
			{"P99 解析延迟", "ms", 2, `histogram_quantile(0.99, sum by (le) (rate(coredns_dns_request_duration_seconds_bucket[5m]))) * 1000`},
		},
	},
	{
		Name: "etcd", Title: "etcd", Desc: "集群唯一状态存储；fsync/commit 延迟与 DB 大小是写入健康的首要信号",
		Jobs:    []string{"etcd", "kube-etcd"},
		Summary: cpCard{"DB 大小", "Mi", 1, `max(etcd_mvcc_db_total_size_in_bytes{job=~"etcd|kube-etcd"}) / 1048576`},
		Cards: []cpCard{
			{"Leader", "", 0, `max(etcd_server_has_leader{job=~"etcd|kube-etcd"})`},
			{"WAL fsync P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(etcd_disk_wal_fsync_duration_seconds_bucket{job=~"etcd|kube-etcd"}[5m]))) * 1000`},
			{"后端 commit P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(etcd_disk_backend_commit_duration_seconds_bucket{job=~"etcd|kube-etcd"}[5m]))) * 1000`},
			{"DB 大小", "Mi", 1, `max(etcd_mvcc_db_total_size_in_bytes{job=~"etcd|kube-etcd"}) / 1048576`},
			{"提案失败率", "次/s", 2, `sum(rate(etcd_server_proposals_failed_total{job=~"etcd|kube-etcd"}[5m]))`},
			{"待提交提案", "条", 0, `max(etcd_server_proposals_pending{job=~"etcd|kube-etcd"})`},
			{"实例数", "个", 0, `count(up{job=~"etcd|kube-etcd"})`},
		},
		Charts: []cpChart{
			{"DB 大小", "Gi", 2, `max(etcd_mvcc_db_total_size_in_bytes{job=~"etcd|kube-etcd"}) / 1073741824`},
			{"WAL fsync P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(etcd_disk_wal_fsync_duration_seconds_bucket{job=~"etcd|kube-etcd"}[5m]))) * 1000`},
			{"后端 commit P99", "ms", 1, `histogram_quantile(0.99, sum by (le) (rate(etcd_disk_backend_commit_duration_seconds_bucket{job=~"etcd|kube-etcd"}[5m]))) * 1000`},
			{"提案失败率", "次/s", 2, `sum(rate(etcd_server_proposals_failed_total{job=~"etcd|kube-etcd"}[5m]))`},
		},
	},
	{
		Name: "prometheus", Title: "Prometheus", Desc: "监控本体：抓取覆盖率与 TSDB 写入量决定本页所有数据的新鲜度",
		Jobs:       []string{"prometheus", "prometheus-k8s"},
		UpFallback: true,
		Summary:    cpCard{"存活目标", "个", 0, `sum(up == 1)`},
		Cards: []cpCard{
			{"存活目标", "个", 0, `sum(up == 1)`},
			{"抓取失败", "个", 0, `sum(up == 0)`},
			{"样本写入", "条/s", 0, `sum(rate(prometheus_tsdb_head_samples_appended_total[5m]))`},
			{"Head 序列数", "条", 0, `max(prometheus_tsdb_head_series)`},
			{"压缩失败率", "次/s", 3, `sum(rate(prometheus_tsdb_compactions_failed_total[5m]))`},
			{"配置加载", "", 0, `max(prometheus_config_last_reload_successful)`},
		},
		Charts: []cpChart{
			{"样本写入", "条/s", 0, `sum(rate(prometheus_tsdb_head_samples_appended_total[5m]))`},
			{"Head 序列数", "条", 0, `max(prometheus_tsdb_head_series)`},
			{"抓取失败目标", "个", 0, `count(up == 0)`},
		},
	},
}

func findCPComponent(name string) (*cpComponent, bool) {
	for i := range controlPlaneComponents {
		if controlPlaneComponents[i].Name == name {
			return &controlPlaneComponents[i], true
		}
	}
	return nil, false
}

// ============ 对外类型 ============

// ControlPlaneSummary 列表页一项：状态 + 概要指标
type ControlPlaneSummary struct {
	Name  string        `json:"name"`
	Title string        `json:"title"`
	Ready int           `json:"ready"`
	Total int           `json:"total"`
	Cards []ControlCard `json:"cards"`
}

// ControlChart 详情页趋势（一条 range 查询）
type ControlChart struct {
	Name   string      `json:"name"`
	Unit   string      `json:"unit"`
	Dec    int         `json:"dec"`
	Points [][]float64 `json:"points"`
}

// ControlPlaneDetail 详情页整包
type ControlPlaneDetail struct {
	Name   string         `json:"name"`
	Title  string         `json:"title"`
	Desc   string         `json:"desc"`
	Ready  int            `json:"ready"`
	Total  int            `json:"total"`
	Range  string         `json:"range"`
	Cards  []ControlCard  `json:"cards"`
	Charts []ControlChart `json:"charts"`
}

// ============ 采集 ============

// cpUpStatus 组件实例就绪状态（job 名随发行版而异，候选 job 一并查）
func (m *MonitorService) cpUpStatus(ctx context.Context, c *kube.Client, cfg PromConfig) (map[string][2]int, bool) {
	upByJob := map[string][2]int{}
	reachable := false
	ql := `up{job=~"kube-apiserver|apiserver|kube-controller-manager|kube-scheduler|kube-proxy|coredns|kube-dns|etcd|kube-etcd|prometheus|prometheus-k8s"}`
	res, err := m.Query(ctx, c, cfg, ql)
	if err != nil {
		return upByJob, false
	}
	reachable = true
	for _, r := range res {
		job := r.Metric["job"]
		v := parsePromFloat(r.Value[1])
		stat := upByJob[job]
		stat[1]++
		if v == 1 {
			stat[0]++
		}
		upByJob[job] = stat
	}
	return upByJob, reachable
}

// cpStatusOf 组件在 up 结果里的就绪统计；UpFallback 处理 job 名没猜中的自监控场景
func cpStatusOf(comp *cpComponent, upByJob map[string][2]int, reachable bool) (ready, total int) {
	for _, job := range comp.Jobs {
		if stat, ok := upByJob[job]; ok {
			total += stat[1]
			ready += stat[0]
		}
	}
	if comp.UpFallback && total == 0 && reachable {
		total, ready = 1, 1
	}
	return
}

func cpItem(card cpCard, v float64) ControlCard {
	return ControlCard{Label: card.Label, Value: v, Unit: card.Unit, Dec: card.Dec}
}

// ControlPlaneList 列表：up + 每组件 1 个概要 instant 查询（约 8 条，点开详情才付重查询的账）
func (m *MonitorService) ControlPlaneList(ctx context.Context, c *kube.Client, cluster *model.Cluster) ([]ControlPlaneSummary, error) {
	cfg := PromConfigOf(cluster)
	upByJob, reachable := m.cpUpStatus(ctx, c, cfg)

	summaries := make([]ControlPlaneSummary, len(controlPlaneComponents))
	for i := range controlPlaneComponents {
		comp := &controlPlaneComponents[i]
		ready, total := cpStatusOf(comp, upByJob, reachable)
		summaries[i] = ControlPlaneSummary{Name: comp.Name, Title: comp.Title, Ready: ready, Total: total}
	}
	// 概要卡并行查（有界并发，与全站 Prometheus 压测口径一致取 4）
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range controlPlaneComponents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			comp := &controlPlaneComponents[i]
			if res, err := m.Query(ctx, c, cfg, comp.Summary.Ql); err == nil && len(res) > 0 {
				summaries[i].Cards = []ControlCard{cpItem(comp.Summary, parsePromFloat(res[0].Value[1]))}
			}
		}(i)
	}
	wg.Wait()
	return summaries, nil
}

// ControlPlaneDetail 单组件详情：全部卡片 + 趋势 range。
// 查询全部限定在该组件（cards≤8、charts≤4），step 由时间档位放宽（1h/30s、6h/3m、24h/15m）。
func (m *MonitorService) ControlPlaneDetail(ctx context.Context, c *kube.Client, cluster *model.Cluster, name, r string) (*ControlPlaneDetail, error) {
	comp, ok := findCPComponent(name)
	if !ok {
		return nil, fmt.Errorf("未知控制面组件: %s", name)
	}
	cfg := PromConfigOf(cluster)
	upByJob, reachable := m.cpUpStatus(ctx, c, cfg)
	ready, total := cpStatusOf(comp, upByJob, reachable)
	out := &ControlPlaneDetail{Name: comp.Name, Title: comp.Title, Desc: comp.Desc, Ready: ready, Total: total, Range: r}

	rs := rangeSpec(r)
	// 卡片（instant）与趋势（range）全部并行，同一 bounded pool 里排队
	out.Cards = make([]ControlCard, 0, len(comp.Cards))
	cardRes := make([]*float64, len(comp.Cards))
	chartRes := make([][][]float64, len(comp.Charts))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range comp.Cards {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if res, err := m.Query(ctx, c, cfg, comp.Cards[i].Ql); err == nil && len(res) > 0 {
				v := parsePromFloat(res[0].Value[1])
				cardRes[i] = &v
			}
		}(i)
	}
	for i := range comp.Charts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if res, err := m.QueryRange(ctx, c, cfg, comp.Charts[i].Ql, rs.Start, rs.End, rs.Step); err == nil {
				chartRes[i] = series(res)
			}
		}(i)
	}
	wg.Wait()
	for i, cd := range comp.Cards {
		if cardRes[i] != nil {
			out.Cards = append(out.Cards, cpItem(cd, *cardRes[i]))
		}
	}
	out.Charts = make([]ControlChart, 0, len(comp.Charts))
	for i, ch := range comp.Charts {
		if len(chartRes[i]) > 0 {
			// 点数封顶防呆：range 查询 step 已由 rangeSpec 控制，这里只兜异常
			if len(chartRes[i]) > 1500 {
				chartRes[i] = chartRes[i][len(chartRes[i])-1500:]
			}
			out.Charts = append(out.Charts, ControlChart{Name: ch.Name, Unit: ch.Unit, Dec: ch.Dec, Points: chartRes[i]})
		}
	}
	return out, nil
}
