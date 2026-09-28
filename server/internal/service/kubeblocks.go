// kubeblocks.go KubeBlocks 数据库集群的读写适配（列表/详情/创建/删除/运维工单/配置/凭据/监控接入）。
//
// 实测契约（集群里 operator 1.0.2 + v0.9 风格 CRD，写代码前逐条验证过）：
//   - 集群：apps.kubeblocks.io/v1，字段是 spec.clusterDef + topology + componentSpecs[]
//     （v1alpha1 是另一套 clusterDefinitionRef，strict decoding 会拒绝 clusterDef）
//   - 运维：operations.kubeblocks.io/v1alpha1 OpsRequest，spec.clusterName + type +
//     restart[]/horizontalScaling[]/verticalScaling[]/reconfigures[]，列表项是对象（componentName 键）
//   - 引擎元数据：ClusterDefinition.spec.topologies[].components[].{name,compDef}，
//     副本范围/存储卷/是否带 exporter/可改配置 全在 ComponentDefinition.spec
//   - compDef 往往是模式而非名字（^mysql-\d+\.\d+.*$、postgresql-），下发前必须解析成
//     具体 ComponentDefinition，否则 apiserver 直接拒：componentDef ... should match '^[a-z]...'
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	resourceapi "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"kube-console/server/internal/kube"
)

var (
	kbClusterGVR    = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "clusters"}
	kbClusterDefGVR = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "clusterdefinitions"}
	kbCompDefGVR    = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "componentdefinitions"}
	// 分片型拓扑的模板定义（redis-cluster、mongo-shard-1.0.2 等）
	kbShardingDefGVR = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "shardingdefinitions"}
	// 集群实际生成的组件（分片集群的每个 shard 都是一个 Component）
	kbComponentGVR = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "components"}
	kbOpsGVR       = schema.GroupVersionResource{Group: "operations.kubeblocks.io", Version: "v1alpha1", Resource: "opsrequests"}
	kbSMGVR        = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
	// 副本就绪数只在 InstanceSet 上（status.components 里该版本没有 readyReplicas）
	kbInstanceSetGVR = schema.GroupVersionResource{Group: "workloads.kubeblocks.io", Version: "v1", Resource: "instancesets"}
	// 1.0 拆出的 Component 对象（每组件一个，名字 <cluster>-<component>）：分片成员只在这里可见
	kbCompGVR = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "components"}
	// 版本升级的可选版本链在 ComponentVersion（集群级）：compatibilityRules[].compDefs → releases[].name
	kbCompVerGVR = schema.GroupVersionResource{Group: "apps.kubeblocks.io", Version: "v1", Resource: "componentversions"}
	// 1.0 的配置体系在 parameters.kubeblocks.io（实测该版本 ConfigConstraint/Configuration 里是 0 个对象，
	// 不要再用）：schema 在 ParametersDefinition（集群级），当前值在 ComponentParameter（<cluster>-<component>），
	// 二者经 ParamConfigRenderer.spec.componentDef（精确等于 compDef）→ parametersDefs[] 关联
	kbPCRGVR = schema.GroupVersionResource{Group: "parameters.kubeblocks.io", Version: "v1alpha1", Resource: "paramconfigrenderers"}
	kbPDGVR  = schema.GroupVersionResource{Group: "parameters.kubeblocks.io", Version: "v1alpha1", Resource: "parametersdefinitions"}
	kbCPGVR  = schema.GroupVersionResource{Group: "parameters.kubeblocks.io", Version: "v1alpha1", Resource: "componentparameters"}
)

// KBEssentialLabel KubeBlocks 给组件 headless Service 打的实例标签，ServiceMonitor 靠它选目标
const KBEssentialLabel = "workloads.kubeblocks.io/instance"

var kbNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// kbCompDefNameRe 组件模板名允许点号（mysql-8.4-1.0.2）——与 CRD 里的
// '^[a-z]([a-z0-9\.\-]*[a-z0-9])?$' 同口径，集群名的 kbNameRe 不能复用
var kbCompDefNameRe = regexp.MustCompile(`^[a-z]([a-z0-9.\-]*[a-z0-9])?$`)

// randSuffix 工单名后缀（避免同秒重名）
func randSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)
}

// ---------- 引擎目录（创建向导数据源）----------

// KBEngine 一个可安装引擎（ClusterDefinition）及其拓扑
type KBEngine struct {
	Name       string       `json:"name"`
	Topologies []KBTopology `json:"topologies"`
}

type KBTopology struct {
	Name       string                `json:"name"`
	Default    bool                  `json:"default"`
	Components []KBTemplateComponent `json:"components"`
	// Shardings：分片型拓扑（redis 的 cluster、mongodb 的 sharding）不写 components，
	// 而是引用 ShardingDefinition；这类拓扑早先被当成"没有组件"，导致建不了也看不见
	Shardings []KBShardingTemplate `json:"shardings,omitempty"`
}

// KBShardingTemplate 一个分片组：分片数范围来自 ShardingDefinition.spec.shardsLimit，
// 组件模板来自其 spec.template.compDef（同样允许是正则）
type KBShardingTemplate struct {
	Name        string `json:"name"`
	ShardingDef string `json:"shardingDef"`
	// ShardingDefPattern 拓扑里原始写法（可能是前缀/正则，如 mongodb 的 "mongo-shard-"）
	ShardingDefPattern string                `json:"shardingDefPattern,omitempty"`
	ShardsMin          int64                 `json:"shardsMin"`
	ShardsMax          int64                 `json:"shardsMax"`
	Components         []KBTemplateComponent `json:"components"`
}

// KBTemplateComponent 拓扑里的一个组件，字段来自其 ComponentDefinition
type KBTemplateComponent struct {
	Name           string `json:"name"`
	CompDef        string `json:"compDef"`
	CompDefPattern string `json:"compDefPattern,omitempty"`
	// ReplicasMin/ReplicasMax 取自 ComponentDefinition.spec.replicasLimit；
	// max=0 表示该版本模板没声明上限（1.0.2 的 mysql/redis addon 就没有），不能当成 1
	ReplicasMin      int64          `json:"replicasMin"`
	ReplicasMax      int64          `json:"replicasMax"`
	ServiceVersion   string         `json:"serviceVersion"`
	Volumes          []KBVolume     `json:"volumes"`
	HasExporter      bool           `json:"hasExporter"`
	Configs          []string       `json:"configs"`
	HighAvailability bool           `json:"highAvailability"`
	CompDefs         []KBCompOption `json:"compDefs,omitempty"`
}

// KBCompOption 组件的一个具体模板可选值（版本取自该 ComponentDefinition 的默认版本）
type KBCompOption struct {
	CompDef        string `json:"compDef"`
	ServiceVersion string `json:"serviceVersion"`
}

type KBVolume struct {
	Name         string   `json:"name"`
	AccessModes  []string `json:"accessModes"`
	Optional     bool     `json:"optional"`
	Snapshotable bool     `json:"snapshotable"`
}

// KBEngines 列出全部引擎与拓扑；ComponentDefinition 缺失时仍返回组件名（降级不报错）
func KBEngines(ctx context.Context, c *kube.Client) ([]KBEngine, error) {
	list, err := c.Dynamic.Resource(kbClusterDefGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || metaNoMatch(err) {
			return []KBEngine{}, nil // 未装 KubeBlocks：空目录，前端显示"未接入"
		}
		return nil, err
	}
	compIdx := kbLoadCompDefs(ctx, c)
	engines := make([]KBEngine, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		name := item.GetName()
		topo := []KBTopology{}
		comps, found, err := unstructured.NestedSlice(item.Object, "spec", "topologies")
		if err == nil && found {
			for _, t := range comps {
				tm, ok := t.(map[string]any)
				if !ok {
					continue
				}
				topo = append(topo, buildTopology(tm, compIdx))
			}
		}
		engines = append(engines, KBEngine{Name: name, Topologies: topo})
	}
	sort.Slice(engines, func(i, j int) bool { return engines[i].Name < engines[j].Name })
	return engines, nil
}

func buildTopology(tm map[string]any, idx *kbCompDefIndex) KBTopology {
	out := KBTopology{}
	out.Name, _ = tm["name"].(string)
	out.Default, _ = tm["default"].(bool)
	comps, _, _ := unstructured.NestedSlice(tm, "components")
	for _, cp := range comps {
		cm, ok := cp.(map[string]any)
		if !ok {
			continue
		}
		name, _ := cm["name"].(string)
		compDef, _ := cm["compDef"].(string)
		tc := kbTemplateFor(name, compDef, idx)
		out.Components = append(out.Components, tc)
	}
	shardings, _, _ := unstructured.NestedSlice(tm, "shardings")
	for _, sp := range shardings {
		sm, ok := sp.(map[string]any)
		if !ok {
			continue
		}
		st := KBShardingTemplate{ShardsMin: 1}
		st.Name, _ = sm["name"].(string)
		declared, _ := sm["shardingDef"].(string)
		sd, resolved := idx.shardingDefOf(declared)
		st.ShardingDef = resolved // 解析后的真实 ShardingDefinition 名（创建时按它下发）
		st.ShardingDefPattern = declared
		if sd == nil {
			out.Shardings = append(out.Shardings, st) // ShardingDefinition 取不到：至少让拓扑可见
			continue
		}
		if lim, ok, _ := unstructured.NestedMap(sd.Object, "spec", "shardsLimit"); ok {
			if v, b, _ := unstructured.NestedInt64(lim, "minShards"); b {
				st.ShardsMin = v
			}
			if v, b, _ := unstructured.NestedInt64(lim, "maxShards"); b {
				st.ShardsMax = v
			}
		}
		// 分片模板的 compDef 同样可以是正则（redis-cluster 写的是 ^redis-cluster-\d+）
		compDef, _, _ := unstructured.NestedString(sd.Object, "spec", "template", "compDef")
		if tc := kbTemplateFor(st.Name, compDef, idx); tc.CompDef != "" || compDef == "" {
			st.Components = append(st.Components, tc)
		}
		out.Shardings = append(out.Shardings, st)
	}
	return out
}

// kbTemplateFor 把拓扑/分片模板里的 compDef（可能是模式）解析成目录项：
// 默认给版本最高的模板，全部候选放进 CompDefs 供前端选
func kbTemplateFor(name, compDef string, idx *kbCompDefIndex) KBTemplateComponent {
	tc := KBTemplateComponent{Name: name, ReplicasMin: 1, CompDef: compDef}
	opts := idx.match(compDef)
	if len(opts) == 0 {
		return tc
	}
	tc.CompDefPattern = compDef
	for _, o := range opts {
		tc.CompDefs = append(tc.CompDefs, KBCompOption{CompDef: o.name, ServiceVersion: o.serviceVersion})
	}
	tc.CompDef = opts[0].name
	fillFromCompDef(&tc, opts[0].obj)
	return tc
}

// ---------- 组件模板解析（compDef 可以是正则） ----------
//
// Addon 的 ClusterDefinition 里 topologies[].components[].compDef 常写成正则或前缀
// （^mysql-\d+\.\d+.*$、postgresql-、mysql-mgr），而 Cluster.spec.componentSpecs[].componentDef
// 必须是具体 ComponentDefinition 名——所以目录与下发前都要先把模式解析成真实模板。

type kbCompDefEntry struct {
	name           string
	serviceVersion string
	engine         string
	obj            *unstructured.Unstructured
}

type kbCompDefIndex struct {
	entries []kbCompDefEntry
	byName  map[string]int
	// shardings：ShardingDefinition 名 → 对象（分片型拓扑的模板来源）
	shardings map[string]*unstructured.Unstructured
}

// shardingDef 取分片模板定义；未装或无权限时返回 nil（目录退化为"只有分片名"）。
// 名字与 compDef 同口径：ClusterDefinition 里可能写精确名（redis-cluster），也可能写前缀/正则
// （mongodb 写的是 "mongo-shard-"，真实对象叫 mongo-shard-1.0.2），所以精确查不到就按正则匹配，
// 多个命中取版本最高的那个。
func (x *kbCompDefIndex) shardingDef(name string) *unstructured.Unstructured {
	sd, _ := x.shardingDefOf(name)
	return sd
}

func (x *kbCompDefIndex) shardingDefOf(name string) (*unstructured.Unstructured, string) {
	if name == "" || x.shardings == nil {
		return nil, ""
	}
	if sd, ok := x.shardings[name]; ok {
		return sd, name
	}
	re, err := regexp.Compile(name)
	if err != nil {
		return nil, ""
	}
	var best *unstructured.Unstructured
	var bestName string
	for n, sd := range x.shardings {
		if !re.MatchString(n) {
			continue
		}
		if best == nil || kbVerCompare(n, bestName) > 0 {
			best, bestName = sd, n
		}
	}
	return best, bestName
}

// kbLoadCompDefs 一次列出全部 ComponentDefinition 与 ShardingDefinition。
// 未装 KubeBlocks 或无 list 权限时返回空索引，由调用方走降级路径（目录缺字段 / 原样下发），不因此报错。
func kbLoadCompDefs(ctx context.Context, c *kube.Client) *kbCompDefIndex {
	idx := &kbCompDefIndex{byName: map[string]int{}, shardings: map[string]*unstructured.Unstructured{}}
	list, err := c.Dynamic.Resource(kbCompDefGVR).List(ctx, metav1.ListOptions{})
	if err == nil {
		for i := range list.Items {
			it := &list.Items[i]
			sv, _, _ := unstructured.NestedString(it.Object, "spec", "serviceVersion")
			idx.byName[it.GetName()] = len(idx.entries)
			idx.entries = append(idx.entries, kbCompDefEntry{name: it.GetName(), serviceVersion: sv, engine: kbEngineOfName(it.GetName(), it.GetLabels()), obj: it})
		}
	}
	if sds, err := c.Dynamic.Resource(kbShardingDefGVR).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range sds.Items {
			idx.shardings[sds.Items[i].GetName()] = &sds.Items[i]
		}
	}
	return idx
}

// kbEngineOfName 模板属于哪个引擎：addon 的 ComponentDefinition 都带 app.kubernetes.io/name，
// 手工建的模板没标签时只能从名字猜（去掉版本号段）——至少不把它当成"无引擎"而整条隐藏
func kbEngineOfName(name string, labels map[string]string) string {
	if e := labels["app.kubernetes.io/name"]; e != "" {
		return e
	}
	keep := make([]string, 0, 3)
	for _, seg := range strings.Split(name, "-") {
		if seg == "" || (seg[0] >= '0' && seg[0] <= '9') {
			continue
		}
		keep = append(keep, seg)
		if len(keep) == 2 {
			break
		}
	}
	return strings.Join(keep, "-")
}

// engineOf 按模板名取引擎名；列表取不到（无权限）时退化成从名字猜
func (x *kbCompDefIndex) engineOf(compDef string) string {
	if compDef == "" {
		return ""
	}
	if i, ok := x.byName[compDef]; ok && x.entries[i].engine != "" {
		return x.entries[i].engine
	}
	return kbEngineOfName(compDef, nil)
}

// match 按模式取候选：精确名优先，否则按正则（与 KubeBlocks 的非锚定 MatchString 同口径），按版本从高到低
func (x *kbCompDefIndex) match(pattern string) []kbCompDefEntry {
	if pattern == "" {
		return nil
	}
	if i, ok := x.byName[pattern]; ok {
		return []kbCompDefEntry{x.entries[i]}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	out := make([]kbCompDefEntry, 0, 4)
	for _, e := range x.entries {
		if re.MatchString(e.name) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return kbVerCompare(kbVerOf(out[i]), kbVerOf(out[j])) > 0 })
	return out
}

func kbVerOf(e kbCompDefEntry) string {
	if e.serviceVersion != "" {
		return e.serviceVersion
	}
	return e.name
}

// kbVerKey 取字符串里的数字段做版本序（mysql-8.4-1.0.2 → 8,4,1,0,2）
func kbVerKey(v string) []int {
	ms := regexp.MustCompile(`\d+`).FindAllString(v, -1)
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		n, err := strconv.Atoi(m)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func kbVerCompare(a, b string) int {
	ka, kb := kbVerKey(a), kbVerKey(b)
	for i := 0; i < len(ka) || i < len(kb); i++ {
		var x, y int
		if i < len(ka) {
			x = ka[i]
		}
		if i < len(kb) {
			y = kb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// kbPickCompDef 选定一个组件的实际模板：wantVersion 为空取最高版本候选，非空则必须命中
func kbPickCompDef(idx *kbCompDefIndex, pattern, wantVersion string) (kbCompDefEntry, error) {
	if len(idx.entries) == 0 && kbCompDefNameRe.MatchString(pattern) {
		return kbCompDefEntry{name: pattern, serviceVersion: wantVersion}, nil // 列不出模板：只能原样下发
	}
	opts := idx.match(pattern)
	if len(opts) == 0 {
		return kbCompDefEntry{}, fmt.Errorf("没有组件模板匹配 %q（引擎 addon 未安装或版本不兼容）", pattern)
	}
	if wantVersion == "" {
		return opts[0], nil
	}
	for _, o := range opts {
		if o.serviceVersion == wantVersion || strings.HasSuffix(o.name, "-"+wantVersion) {
			return o, nil
		}
	}
	vers := make([]string, 0, len(opts))
	for _, o := range opts {
		vers = append(vers, kbVerOf(o))
	}
	return kbCompDefEntry{}, fmt.Errorf("版本 %s 没有对应模板，可选：%s", wantVersion, strings.Join(vers, "、"))
}

func fillFromCompDef(tc *KBTemplateComponent, cd *unstructured.Unstructured) {
	if lim, ok, _ := unstructured.NestedMap(cd.Object, "spec", "replicasLimit"); ok && (len(lim) > 0) {
		if v, b, _ := unstructured.NestedInt64(lim, "min"); b {
			tc.ReplicasMin = v
		}
		if v, b, _ := unstructured.NestedInt64(lim, "max"); b {
			tc.ReplicasMax = v
		}
	}
	if sv, ok, _ := unstructured.NestedString(cd.Object, "spec", "serviceVersion"); ok {
		tc.ServiceVersion = sv
	}
	if _, ok, _ := unstructured.NestedMap(cd.Object, "spec", "exporter"); ok {
		tc.HasExporter = true
	}
	if vols, ok, _ := unstructured.NestedSlice(cd.Object, "spec", "volumes"); ok {
		for _, v := range vols {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			kv := KBVolume{}
			kv.Name, _ = vm["name"].(string)
			kv.Optional, _ = vm["optional"].(bool)
			kv.Snapshotable, _ = vm["snapshotable"].(bool)
			if modes, ok, _ := unstructured.NestedStringSlice(vm, "accessModes"); ok {
				kv.AccessModes = modes
			}
			tc.Volumes = append(tc.Volumes, kv)
		}
	}
	if cfgs, ok, _ := unstructured.NestedSlice(cd.Object, "spec", "configs"); ok {
		for _, v := range cfgs {
			cm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if n, _ := cm["name"].(string); n != "" {
				tc.Configs = append(tc.Configs, n)
			}
		}
	}
	if roles, ok, _ := unstructured.NestedSlice(cd.Object, "spec", "roles"); ok && len(roles) > 1 {
		tc.HighAvailability = true
	}
}

// ---------- 集群列表与详情 ----------

type KBCluster struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	ClusterDef string `json:"clusterDef"`
	// Engine 该集群归属的引擎名（子菜单过滤与指标模板都按它）：有 clusterDef 时同值，
	// 没有则是 KubeBlocks 的"无拓扑"建法（spec 只有 componentSpecs），从组件模板的
	// app.kubernetes.io/name 标签反推——否则这类集群在引擎页里会整个看不见。
	Engine            string        `json:"engine"`
	Topology          string        `json:"topology"`
	Phase             string        `json:"phase"`
	TerminationPolicy string        `json:"terminationPolicy"`
	Message           string        `json:"message"`
	CreatedAt         string        `json:"createdAt"`
	Components        []KBComponent `json:"components"`
	// Conditions 集群级条件时间线（ProvisioningStarted/ApplyResources/Ready...），排障第一现场
	Conditions []KBCondition `json:"conditions,omitempty"`
	// Shardings 分片拓扑（spec.shardings + status.shardings 的 phase）；分片成员组件在详情接口里带出
	Shardings []KBSharding `json:"shardings,omitempty"`
}

// KBCondition k8s conditions 的通用形态（集群 / 工单 / Component 都用这套字段）
type KBCondition struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
}

// KBSharding 一个分片拓扑：spec 声明 + status phase；成员组件名是 <sharding>-<随机ID>
type KBSharding struct {
	Name        string        `json:"name"`
	ShardingDef string        `json:"shardingDef"`
	Shards      int64         `json:"shards"`
	Phase       string        `json:"phase,omitempty"`
	Message     string        `json:"message,omitempty"`
	UpToDate    *bool         `json:"upToDate,omitempty"`
	Components  []KBComponent `json:"components,omitempty"` // 仅详情接口填充
}

type KBComponent struct {
	Name            string `json:"name"`
	CompDef         string `json:"compDef"`
	ServiceVersion  string `json:"serviceVersion"`
	Replicas        int64  `json:"replicas"`
	ReadyReplicas   int64  `json:"readyReplicas"`
	Phase           string `json:"phase"`
	Message         string `json:"message"`
	HeadlessService string `json:"headlessService"`
	InstanceLabel   string `json:"instanceLabel"`
	CPULimit        string `json:"cpuLimit"`
	MemLimit        string `json:"memLimit"`
	Storage         string `json:"storage"`
	StorageClass    string `json:"storageClass"`
}

// KBListClusters ns 为空列全集群（调用方按 ns 权限过滤）
func KBListClusters(ctx context.Context, c *kube.Client, ns string) ([]KBCluster, error) {
	list, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || metaNoMatch(err) {
			return []KBCluster{}, nil
		}
		return nil, err
	}
	views := make([]KBCluster, 0, len(list.Items))
	idx := kbLoadCompDefs(ctx, c)
	for i := range list.Items {
		views = append(views, clusterView(&list.Items[i], idx))
	}
	// 指针必须在切片定稿后再取：append 触发扩容会让先前取到的地址失效
	ptrs := make([]*KBCluster, 0, len(views))
	for i := range views {
		ptrs = append(ptrs, &views[i])
	}
	kbFillReady(ctx, c, ptrs)
	out := views
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func clusterView(obj *unstructured.Unstructured, idx *kbCompDefIndex) KBCluster {
	v := KBCluster{Namespace: obj.GetNamespace(), Name: obj.GetName()}
	v.ClusterDef, _, _ = unstructured.NestedString(obj.Object, "spec", "clusterDef")
	v.Engine = v.ClusterDef
	v.Topology, _, _ = unstructured.NestedString(obj.Object, "spec", "topology")
	v.TerminationPolicy, _, _ = unstructured.NestedString(obj.Object, "spec", "terminationPolicy")
	v.Phase, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
	v.Message, _, _ = unstructured.NestedString(obj.Object, "status", "message")
	if ts := obj.GetCreationTimestamp(); !ts.IsZero() {
		v.CreatedAt = ts.Format("2006-01-02 15:04:05")
	}
	statusComps := map[string]map[string]any{}
	if cs, ok, _ := unstructured.NestedMap(obj.Object, "status", "components"); ok {
		for k, val := range cs {
			if m, ok := val.(map[string]any); ok {
				statusComps[k] = m
			}
		}
	}
	specComps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	for _, sp := range specComps {
		sm, ok := sp.(map[string]any)
		if !ok {
			continue
		}
		comp := KBComponent{}
		comp.Name, _ = sm["name"].(string)
		comp.CompDef, _ = sm["componentDef"].(string)
		comp.ServiceVersion, _ = sm["serviceVersion"].(string)
		comp.Replicas, _, _ = unstructured.NestedInt64(sm, "replicas")
		if res, ok, _ := unstructured.NestedMap(sm, "resources"); ok {
			comp.CPULimit, _, _ = unstructured.NestedString(res, "limits", "cpu")
			comp.MemLimit, _, _ = unstructured.NestedString(res, "limits", "memory")
		}
		if vct, ok, _ := unstructured.NestedSlice(sm, "volumeClaimTemplates"); ok && len(vct) > 0 {
			if first, ok := vct[0].(map[string]any); ok {
				if spec, ok := first["spec"].(map[string]any); ok {
					comp.StorageClass, _ = spec["storageClassName"].(string)
					if res, ok := spec["resources"].(map[string]any); ok {
						if req, ok := res["requests"].(map[string]any); ok {
							comp.Storage, _ = req["storage"].(string)
						}
					}
				}
			}
		}
		if st, ok := statusComps[comp.Name]; ok {
			comp.Phase, _ = st["phase"].(string)
			comp.Message, _ = st["message"].(string)
			comp.ServiceVersion, _ = st["serviceVersion"].(string)
			if n, ok := st["readyReplicas"].(float64); ok {
				comp.ReadyReplicas = int64(n)
			}
		}
		v.Components = append(v.Components, comp)
	}
	if v.Engine == "" {
		for _, comp := range v.Components {
			if e := idx.engineOf(comp.CompDef); e != "" {
				v.Engine = e
				break
			}
		}
	}
	v.Conditions = kbConditionsOf(obj)
	// 分片拓扑：spec 声明 + status phase（成员组件在详情接口里从 Components CR 补齐）
	if shs, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "shardings"); ok {
		statusSh := map[string]map[string]any{}
		if m, ok, _ := unstructured.NestedMap(obj.Object, "status", "shardings"); ok {
			for k, val := range m {
				if sm, ok := val.(map[string]any); ok {
					statusSh[k] = sm
				}
			}
		}
		for _, sh := range shs {
			sm, ok := sh.(map[string]any)
			if !ok {
				continue
			}
			name, _ := sm["name"].(string)
			s := KBSharding{Name: name}
			s.ShardingDef, _ = sm["shardingDef"].(string)
			s.Shards, _, _ = unstructured.NestedInt64(sm, "shards")
			if st := statusSh[name]; st != nil {
				s.Phase, _ = st["phase"].(string)
				s.Message, _ = st["message"].(string)
				if b, ok := st["upToDate"].(bool); ok {
					s.UpToDate = &b
				}
			}
			v.Shardings = append(v.Shardings, s)
		}
	}
	return v
}

func kbConditionsOf(obj *unstructured.Unstructured) []KBCondition {
	var out []KBCondition
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, ci := range conds {
		cm, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		cd := KBCondition{}
		cd.Type, _ = cm["type"].(string)
		cd.Status, _ = cm["status"].(string)
		cd.Reason, _ = cm["reason"].(string)
		cd.Message, _ = cm["message"].(string)
		cd.LastTransition, _ = cm["lastTransitionTime"].(string)
		out = append(out, cd)
	}
	return out
}

// kbFillReady 用 InstanceSet 的 readyReplicas 补齐各组件就绪数。
// InstanceSet 名即 <cluster>-<component>；一次列全命名空间，避免逐集群 N 次请求。
func kbFillReady(ctx context.Context, c *kube.Client, clusters []*KBCluster) {
	if len(clusters) == 0 {
		return
	}
	byNS := map[string][]int{}
	for i := range clusters {
		byNS[clusters[i].Namespace] = append(byNS[clusters[i].Namespace], i)
	}
	for ns, idxs := range byNS {
		list, err := c.Dynamic.Resource(kbInstanceSetGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // 拿不到就留空（就绪数未知），不影响列表
		}
		ready := map[string]int64{}
		for i := range list.Items {
			it := &list.Items[i]
			if v, ok, _ := unstructured.NestedInt64(it.Object, "status", "readyReplicas"); ok {
				ready[it.GetName()] = v
			}
		}
		for _, ci := range idxs {
			for j := range clusters[ci].Components {
				comp := &clusters[ci].Components[j]
				comp.ReadyReplicas = ready[clusters[ci].Name+"-"+comp.Name]
			}
		}
	}
}

// KBHeadlessServices 组件的 headless Service 名与实例标签（监控接入用）
func KBHeadlessServices(ctx context.Context, c *kube.Client, ns, cluster string) (map[string]KBServiceRef, error) {
	svcs, err := c.Clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]KBServiceRef{}
	for _, s := range svcs.Items {
		if !strings.HasPrefix(s.Name, cluster+"-") || !strings.HasSuffix(s.Name, "-headless") {
			continue
		}
		inst := s.Labels[KBEssentialLabel]
		if inst == "" {
			continue
		}
		// exporter 端口名不统一：mysql/redis/pg 用 http-metrics，kafka-exporter 用 metrics
		metricsPort := ""
		for _, p := range s.Spec.Ports {
			if p.Name == "http-metrics" {
				metricsPort = p.Name
				break
			}
			if p.Name == "metrics" {
				metricsPort = p.Name
			}
		}
		comp := strings.TrimSuffix(strings.TrimPrefix(s.Name, cluster+"-"), "-headless")
		out[comp] = KBServiceRef{Service: s.Name, InstanceLabel: inst, HasMetricsPort: metricsPort != "", MetricsPort: metricsPort}
	}
	return out, nil
}

type KBServiceRef struct {
	Service        string `json:"service"`
	InstanceLabel  string `json:"instanceLabel"`
	HasMetricsPort bool   `json:"hasMetricsPort"`
	// MetricsPort 实际端口名（http-metrics 或 metrics），ServiceMonitor 按它写
	MetricsPort string `json:"metricsPort"`
}

// KBGetCluster 详情（含组件的 headless service 与 exporter 情况、分片成员组件）
func KBGetCluster(ctx context.Context, c *kube.Client, ns, name string) (*KBCluster, error) {
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	v := clusterView(obj, kbLoadCompDefs(ctx, c))
	kbFillReady(ctx, c, []*KBCluster{&v})
	refs, err := KBHeadlessServices(ctx, c, ns, name)
	if err == nil {
		for i := range v.Components {
			if r, ok := refs[v.Components[i].Name]; ok {
				v.Components[i].HeadlessService = r.Service
				v.Components[i].InstanceLabel = r.InstanceLabel
			}
		}
	}
	// 分片成员只存在于 Components CR（名字 <sharding>-<随机ID>），不在 spec.componentSpecs 里
	if len(v.Shardings) > 0 {
		shardComps, err := kbShardingComponents(ctx, c, ns, name, v)
		if err == nil {
			for i := range v.Shardings {
				v.Shardings[i].Components = shardComps[v.Shardings[i].Name]
			}
		}
	}
	return &v, nil
}

// kbShardingComponents 从 Components CR 找出各分片的成员组件（component-name 不在
// componentSpecs 里、且命中 <sharding>- 前缀的都算），带 InstanceSet 就绪数
func kbShardingComponents(ctx context.Context, c *kube.Client, ns, cluster string, v KBCluster) (map[string][]KBComponent, error) {
	declared := map[string]bool{}
	for _, comp := range v.Components {
		declared[comp.Name] = true
	}
	list, err := c.Dynamic.Resource(kbCompGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + cluster,
	})
	if err != nil {
		return nil, err
	}
	byShard := map[string][]KBComponent{}
	members := map[string]*KBComponent{} // 组件名 → 视图指针（InstanceSet 就绪数回填用）
	for i := range list.Items {
		it := &list.Items[i]
		compName, _, _ := unstructured.NestedString(it.Object, "metadata", "labels", "apps.kubeblocks.io/component-name")
		if compName == "" {
			compName = it.GetName()
		}
		if declared[compName] {
			continue
		}
		comp := KBComponent{Name: compName}
		comp.CompDef, _, _ = unstructured.NestedString(it.Object, "metadata", "labels", "componentdefinition.kubeblocks.io/name")
		comp.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
		comp.Message, _, _ = unstructured.NestedString(it.Object, "status", "message")
		for _, sh := range v.Shardings {
			if strings.HasPrefix(compName, sh.Name+"-") {
				byShard[sh.Name] = append(byShard[sh.Name], comp)
				members[compName] = &byShard[sh.Name][len(byShard[sh.Name])-1]
				break
			}
		}
	}
	// 就绪/副本数按 InstanceSet（名 = <cluster>-<组件名>）一次补齐：分片成员不在
	// componentSpecs 里，声明副本数也只能从 InstanceSet 拿
	if len(members) > 0 {
		isl, err := c.Dynamic.Resource(kbInstanceSetGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range isl.Items {
				it := &isl.Items[i]
				if comp, ok := members[strings.TrimPrefix(it.GetName(), cluster+"-")]; ok {
					comp.Replicas, _, _ = unstructured.NestedInt64(it.Object, "status", "replicas")
					comp.ReadyReplicas, _, _ = unstructured.NestedInt64(it.Object, "status", "readyReplicas")
				}
			}
		}
	}
	return byShard, nil
}

// ---------- 创建与删除 ----------

// KBComponentInput 创建向导里一个组件的取值
type KBComponentInput struct {
	Name           string `json:"name"`
	CompDef        string `json:"compDef"`
	Replicas       int64  `json:"replicas"`
	ServiceVersion string `json:"serviceVersion"`
	CPURequest     string `json:"cpuRequest"`
	MemRequest     string `json:"memRequest"`
	CPULimit       string `json:"cpuLimit"`
	MemLimit       string `json:"memLimit"`
	Storage        string `json:"storage"`
	StorageClass   string `json:"storageClass"`
}

// KBShardingInput 一个分片组的取值（redis 的 cluster、mongodb 的 sharding 拓扑）
type KBShardingInput struct {
	Name           string `json:"name"`
	ShardingDef    string `json:"shardingDef"`
	Shards         int64  `json:"shards"`
	CompDef        string `json:"compDef"`
	Replicas       int64  `json:"replicas"`
	ServiceVersion string `json:"serviceVersion"`
	CPURequest     string `json:"cpuRequest"`
	MemRequest     string `json:"memRequest"`
	CPULimit       string `json:"cpuLimit"`
	MemLimit       string `json:"memLimit"`
	Storage        string `json:"storage"`
	StorageClass   string `json:"storageClass"`
}

// KBCreateInput 创建集群请求。Components 与 Shardings 二者至少给一个：
// 分片型拓扑（ClusterDefinition 里该拓扑只有 shardings）必须走 Shardings
type KBCreateInput struct {
	Name              string             `json:"name"`
	ClusterDef        string             `json:"clusterDef"`
	Topology          string             `json:"topology"`
	TerminationPolicy string             `json:"terminationPolicy"`
	Components        []KBComponentInput `json:"components"`
	Shardings         []KBShardingInput  `json:"shardings"`
}

// KBCreateCluster 按 v1 形态下发 Cluster；组件模板可为 KBEngines 给的具体名或模式，此处统一解析
func KBCreateCluster(ctx context.Context, c *kube.Client, ns string, in *KBCreateInput) error {
	if in.Name == "" || in.ClusterDef == "" {
		return fmt.Errorf("集群名与引擎不能为空")
	}
	if !kbNameRe.MatchString(in.Name) {
		return fmt.Errorf("集群名不合法（须为 RFC 1123 小写名）: %s", in.Name)
	}
	if len(in.Components) == 0 && len(in.Shardings) == 0 {
		return fmt.Errorf("至少需要一个组件（分片型拓扑请填分片组）")
	}
	comps := make([]any, 0, len(in.Components))
	compIdx := kbLoadCompDefs(ctx, c)
	for _, ci := range in.Components {
		if ci.Name == "" || ci.CompDef == "" {
			return fmt.Errorf("组件 name/compDef 不能为空")
		}
		// 目录给的 compDef 可能是模式（^mysql-\d+\.\d+.*$），必须解析成具体模板名再下发
		picked, err := kbPickCompDef(compIdx, ci.CompDef, ci.ServiceVersion)
		if err != nil {
			return fmt.Errorf("组件 %s: %w", ci.Name, err)
		}
		if !kbCompDefNameRe.MatchString(picked.name) {
			return fmt.Errorf("组件 %s 的模板名不合法: %s", ci.Name, picked.name)
		}
		replicas := ci.Replicas
		if replicas < 1 {
			replicas = 1
		}
		spec := map[string]any{
			"name":         ci.Name,
			"componentDef": picked.name,
			"replicas":     replicas,
		}
		if sv := picked.serviceVersion; sv != "" {
			spec["serviceVersion"] = sv
		}
		if res := resourceMap(ci); len(res) > 0 {
			spec["resources"] = res
		}
		if ci.Storage != "" {
			spec["volumeClaimTemplates"] = []any{pvcTemplate(ci)}
		}
		comps = append(comps, spec)
	}
	shardSpecs, err := kbShardingSpecs(ctx, c, ns, in, compIdx)
	if err != nil {
		return err
	}
	tp := in.TerminationPolicy
	if tp == "" {
		tp = "Delete"
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kbClusterGVR.Group + "/" + kbClusterGVR.Version,
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      in.Name,
			"namespace": ns,
		},
		"spec": map[string]any{
			"clusterDef":        in.ClusterDef,
			"terminationPolicy": tp,
		},
	}}
	// 分片拓扑没有普通组件：留空就别下发 componentSpecs:[]，免得看着像"配了 0 个组件"
	if len(comps) > 0 {
		if err := unstructured.SetNestedSlice(obj.Object, comps, "spec", "componentSpecs"); err != nil {
			return err
		}
	}
	if in.Topology != "" {
		if err := unstructured.SetNestedField(obj.Object, in.Topology, "spec", "topology"); err != nil {
			return err
		}
	}
	if len(shardSpecs) > 0 {
		if err := unstructured.SetNestedSlice(obj.Object, shardSpecs, "spec", "shardings"); err != nil {
			return err
		}
	}
	if _, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("集群 %s/%s 已存在", ns, in.Name)
		}
		return fmt.Errorf("创建集群失败: %s", kbErrText(err))
	}
	return nil
}

func resourceMap(ci KBComponentInput) map[string]any {
	req, lim := map[string]any{}, map[string]any{}
	if ci.CPURequest != "" {
		req["cpu"] = ci.CPURequest
	}
	if ci.MemRequest != "" {
		req["memory"] = ci.MemRequest
	}
	if ci.CPULimit != "" {
		lim["cpu"] = ci.CPULimit
	}
	if ci.MemLimit != "" {
		lim["memory"] = ci.MemLimit
	}
	out := map[string]any{}
	if len(req) > 0 {
		out["requests"] = req
	}
	if len(lim) > 0 {
		out["limits"] = lim
	}
	return out
}

func pvcTemplate(ci KBComponentInput) map[string]any {
	spec := map[string]any{
		"accessModes": []any{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]any{"storage": ci.Storage}},
	}
	if ci.StorageClass != "" {
		spec["storageClassName"] = ci.StorageClass
	}
	return map[string]any{"name": "data", "spec": spec}
}

// KBDeleteCluster 删除集群（terminationPolicy 由用户在建集群时决定，Delete 会连带清理实例资源）
func KBDeleteCluster(ctx context.Context, c *kube.Client, ns, name string) error {
	err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("集群不存在")
	}
	return err
}

// ---------- 运维工单 OpsRequest ----------

// KBOpsInput 一次运维请求；Type 的全集见 OpsRequest CRD 枚举（1.0.2 实测）：
// Upgrade|VerticalScaling|VolumeExpansion|HorizontalScaling|Restart|Reconfiguring|Start|Stop|Expose|Switchover
type KBOpsInput struct {
	Type        string            `json:"type"`
	Component   string            `json:"component"`
	Replicas    int64             `json:"replicas"`
	CPULimit    string            `json:"cpuLimit"`
	MemLimit    string            `json:"memLimit"`
	Parameters  map[string]string `json:"parameters"`
	Description string            `json:"description"`
	// Volumes VolumeExpansion 的目标卷与新容量（vct 名默认 data，可从集群详情读）
	Volumes []KBOpsVolume `json:"volumes,omitempty"`
	// ServiceType Expose 的暴露方式：ClusterIP|NodePort|LoadBalancer；Enable=false 时回收
	ServiceType string `json:"serviceType,omitempty"`
	ExposeName  string `json:"exposeName,omitempty"`
	Enable      *bool  `json:"enable,omitempty"`
	// Instance Switchover 的候选实例（空 = operator 自选）
	Instance string `json:"instance,omitempty"`
	// ServiceVersion Upgrade 的目标版本（可选值从 KBUpgradeCandidates 拿）
	ServiceVersion string `json:"serviceVersion,omitempty"`
	// BackupName/InPlace RebuildInstance：用备份重建组件实例
	BackupName string `json:"backupName,omitempty"`
	InPlace    bool   `json:"inPlace,omitempty"`
}

// KBOpsVolume 磁盘扩容的一个卷（name 是组件 volumeClaimTemplates 里的名字，通常 data）
type KBOpsVolume struct {
	Name    string `json:"name"`
	Storage string `json:"storage"`
}

type KBOps struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Component string `json:"component"`
	Phase     string `json:"phase"`
	Progress  string `json:"progress"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}

// KBListOps 该集群的运维工单（按创建时间倒序，最多 50 条）。
// 不能用 label selector 过滤：实测 kubectl 手建的 OpsRequest 可能没有
// app.kubernetes.io/instance 标签，label 过滤会漏单。改为分页拉全（上限 1000 条）
// 后按 spec.clusterName 过滤——creator 无关，老工单也不会丢。
func KBListOps(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBOps, error) {
	out := []KBOps{}
	opts := metav1.ListOptions{Limit: 200}
	for scanned := 0; ; {
		list, err := c.Dynamic.Resource(kbOpsGVR).Namespace(ns).List(ctx, opts)
		if err != nil {
			if metaNoMatch(err) {
				return out, nil
			}
			return nil, err
		}
		for i := range list.Items {
			it := &list.Items[i]
			if cn, _, _ := unstructured.NestedString(it.Object, "spec", "clusterName"); cn != cluster {
				continue
			}
			out = append(out, opsView(it))
		}
		scanned += len(list.Items)
		if list.GetContinue() == "" || scanned >= 1000 || len(out) >= 200 {
			break
		}
		opts.Continue = list.GetContinue()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

func opsView(it *unstructured.Unstructured) KBOps {
	o := KBOps{Name: it.GetName()}
	o.Type, _, _ = unstructured.NestedString(it.Object, "spec", "type")
	for _, key := range []string{"restart", "stop", "start", "horizontalScaling", "verticalScaling", "reconfigures", "volumeExpansion", "switchover", "expose", "upgrade"} {
		if lst, ok, _ := unstructured.NestedSlice(it.Object, "spec", key); ok && len(lst) > 0 {
			if m, ok := lst[0].(map[string]any); ok {
				o.Component, _ = m["componentName"].(string)
				break
			}
		}
	}
	o.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
	o.Progress, _, _ = unstructured.NestedString(it.Object, "status", "progress")
	o.Message, _, _ = unstructured.NestedString(it.Object, "status", "message")
	if ts := it.GetCreationTimestamp(); !ts.IsZero() {
		o.CreatedAt = ts.Format("2006-01-02 15:04:05")
	}
	return o
}

// KBOpsCondition 工单条件时间线（Validated → Reconfigure/Scaling → Succeed/Failed）
type KBOpsCondition struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
}

type KBOpsDetail struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Type        string `json:"type"`
	ClusterName string `json:"clusterName"`
	Phase       string `json:"phase"`
	Progress    string `json:"progress"`
	Message     string `json:"message"`
	CreatedAt   string `json:"createdAt"`
	StartAt     string `json:"startAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	// Cancelable phase 还在 Pending/Creating/Running 时可取消（patch spec.cancel=true）
	Cancelable bool `json:"cancelable"`
	// LastConfiguration status.lastConfiguration（变更前的旧值），前端做 diff 展示
	LastConfiguration map[string]any   `json:"lastConfiguration,omitempty"`
	Conditions        []KBOpsCondition `json:"conditions"`
}

// kbOpsCancelable KubeBlocks 只处理进行中工单的取消，终态（Succeed/Failed/Cancelled/Aborted...）patch 了也没用
func kbOpsCancelable(phase string) bool {
	switch phase {
	case "", "Pending", "Creating", "Running", "ConditionTrue":
		return true
	}
	return false
}

// KBGetOps 单条工单详情：条件时间线 + 进度 + 变更前旧值
func KBGetOps(ctx context.Context, c *kube.Client, ns, name string) (*KBOpsDetail, error) {
	obj, err := c.Dynamic.Resource(kbOpsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	d := &KBOpsDetail{
		Name:        obj.GetName(),
		Namespace:   ns,
		ClusterName: str(obj, "spec", "clusterName"),
		Type:        str(obj, "spec", "type"),
		Phase:       str(obj, "status", "phase"),
		Progress:    str(obj, "status", "progress"),
		Message:     str(obj, "status", "message"),
		Cancelable:  kbOpsCancelable(str(obj, "status", "phase")),
	}
	d.LastConfiguration, _, _ = unstructured.NestedMap(obj.Object, "status", "lastConfiguration")
	if t, ok, _ := unstructured.NestedString(obj.Object, "status", "startTimestamp"); ok {
		d.StartAt = kbTime(t)
	}
	if t, ok, _ := unstructured.NestedString(obj.Object, "status", "completionTimestamp"); ok {
		d.CompletedAt = kbTime(t)
	}
	if ts := obj.GetCreationTimestamp(); !ts.IsZero() {
		d.CreatedAt = ts.Format("2006-01-02 15:04:05")
	}
	if conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		for _, ci := range conds {
			cm, ok := ci.(map[string]any)
			if !ok {
				continue
			}
			cd := KBOpsCondition{}
			cd.Type, _ = cm["type"].(string)
			cd.Status, _ = cm["status"].(string)
			cd.Reason, _ = cm["reason"].(string)
			cd.Message, _ = cm["message"].(string)
			cd.LastTransition, _ = cm["lastTransitionTime"].(string)
			d.Conditions = append(d.Conditions, cd)
		}
	}
	return d, nil
}

// KBCancelOps 取消进行中的工单：KubeBlocks 的取消协议是 patch spec.cancel=true，
// 由 operator 侧判定能否取消（此处只拦终态，避免必然无效的请求）。
func KBCancelOps(ctx context.Context, c *kube.Client, ns, name string) error {
	obj, err := c.Dynamic.Resource(kbOpsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if !kbOpsCancelable(phase) {
		return fmt.Errorf("工单已进入终态（%s），无法取消", phase)
	}
	if cancel, ok, _ := unstructured.NestedBool(obj.Object, "spec", "cancel"); ok && cancel {
		return nil // 已请求过取消，幂等返回
	}
	patch := []byte(`{"spec":{"cancel":true}}`)
	if _, err := c.Dynamic.Resource(kbOpsGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("取消工单失败: %s", kbErrText(err))
	}
	return nil
}

// KBSubmitOps 下发运维工单。列表项形态实测为对象数组（componentName 键），
// 写成字符串数组会被 CRD strict decoding 拒绝。类型枚举见 CRD（1.0.2 实测）：
// Upgrade|VerticalScaling|VolumeExpansion|HorizontalScaling|Restart|Reconfiguring|Start|Stop|Expose|Switchover
// （另有 Backup|Restore|RebuildInstance|Custom 由备份页/自定义 runbook 走）。
func KBSubmitOps(ctx context.Context, c *kube.Client, ns, cluster string, in *KBOpsInput) (string, error) {
	if in.Type == "" || in.Component == "" {
		return "", fmt.Errorf("运维类型与组件不能为空")
	}
	spec := map[string]any{"clusterName": cluster, "type": in.Type}
	switch in.Type {
	case "Restart", "Stop", "Start":
		spec[lower(in.Type)] = []any{map[string]any{"componentName": in.Component}}
	case "HorizontalScaling":
		cur, err := kbComponentReplicas(ctx, c, ns, cluster, in.Component)
		if err != nil {
			return "", err
		}
		if in.Replicas < 1 {
			return "", fmt.Errorf("目标副本数至少为 1")
		}
		item := map[string]any{"componentName": in.Component}
		switch {
		case in.Replicas == cur:
			return "", fmt.Errorf("目标副本数与当前一致（%d）", cur)
		case in.Replicas > cur:
			item["scaleOut"] = map[string]any{"replicaChanges": in.Replicas - cur}
		default:
			item["scaleIn"] = map[string]any{"replicaChanges": cur - in.Replicas}
		}
		spec["horizontalScaling"] = []any{item}
	case "VerticalScaling":
		lim := map[string]any{}
		if in.CPULimit != "" {
			lim["cpu"] = in.CPULimit
		}
		if in.MemLimit != "" {
			lim["memory"] = in.MemLimit
		}
		if len(lim) == 0 {
			return "", fmt.Errorf("垂直扩缩容至少给出 cpu 或 memory 限制")
		}
		spec["verticalScaling"] = []any{map[string]any{"componentName": in.Component, "limits": lim}}
	case "Reconfigure", "Reconfiguring":
		spec["type"] = "Reconfiguring" // CRD 枚举是 Reconfiguring，UI 传 Reconfigure 也接受
		if len(in.Parameters) == 0 {
			return "", fmt.Errorf("改配置需至少一个参数")
		}
		params := make([]any, 0, len(in.Parameters))
		for k, v := range in.Parameters {
			params = append(params, map[string]any{"key": k, "value": v})
		}
		spec["reconfigures"] = []any{map[string]any{"componentName": in.Component, "parameters": params}}
	case "VolumeExpansion":
		// 卷名必须来自该组件的 volumeClaimTemplates（通常 data）；KubeBlocks 只允许扩不允许缩
		if len(in.Volumes) == 0 {
			return "", fmt.Errorf("磁盘扩容需至少一个卷")
		}
		cur, err := kbComponentVolumes(ctx, c, ns, cluster, in.Component)
		if err != nil {
			return "", err
		}
		items := make([]any, 0, len(in.Volumes))
		for _, v := range in.Volumes {
			if v.Name == "" || v.Storage == "" {
				return "", fmt.Errorf("卷名与新容量都不能为空")
			}
			cq, ok := cur[v.Name]
			if !ok {
				return "", fmt.Errorf("组件 %s 没有 %s 卷（可选：%s）", in.Component, v.Name, kbJoinKeys(cur))
			}
			want, err := resourceapi.ParseQuantity(v.Storage)
			if err != nil {
				return "", fmt.Errorf("容量 %s 不合法: %v", v.Storage, err)
			}
			if want.Cmp(cq) < 0 {
				return "", fmt.Errorf("卷 %s 只能扩容（当前 %s，目标 %s）", v.Name, cq.String(), v.Storage)
			}
			if want.Cmp(cq) == 0 {
				return "", fmt.Errorf("卷 %s 容量与当前一致（%s）", v.Name, cq.String())
			}
			items = append(items, map[string]any{
				"componentName":        in.Component,
				"volumeClaimTemplates": []any{map[string]any{"name": v.Name, "storage": v.Storage}},
			})
		}
		spec["volumeExpansion"] = items
	case "Upgrade":
		if in.ServiceVersion == "" {
			return "", fmt.Errorf("版本升级需给出目标 serviceVersion")
		}
		cur, err := kbComponentVersion(ctx, c, ns, cluster, in.Component)
		if err != nil {
			return "", err
		}
		if in.ServiceVersion == cur {
			return "", fmt.Errorf("目标版本与当前一致（%s）", cur)
		}
		// 可选版本来自 ComponentVersion；不在链里的版本 operator 也会拒，提前给出可选项
		cands, err := KBUpgradeCandidates(ctx, c, ns, cluster)
		if err != nil {
			return "", err
		}
		allowed := false
		for _, cd := range cands {
			if cd.Component == in.Component {
				for _, v := range cd.Candidates {
					if v == in.ServiceVersion {
						allowed = true
					}
				}
			}
		}
		if !allowed && len(cands) > 0 {
			for _, cd := range cands {
				if cd.Component == in.Component && len(cd.Candidates) > 0 {
					return "", fmt.Errorf("版本 %s 不在升级链里，可选：%s", in.ServiceVersion, strings.Join(cd.Candidates, "、"))
				}
			}
			return "", fmt.Errorf("组件 %s 没有找到 ComponentVersion 升级链，无法升级", in.Component)
		}
		spec["upgrade"] = map[string]any{"components": []any{map[string]any{
			"componentName": in.Component, "serviceVersion": in.ServiceVersion,
		}}}
	case "Switchover":
		item := map[string]any{"componentName": in.Component}
		if in.Instance != "" {
			item["instanceName"] = in.Instance // 候选实例（实测字段名，见 CRD switchover.items）
		}
		spec["switchover"] = []any{item}
	case "Expose":
		enable := in.Enable == nil || *in.Enable
		if in.ServiceType == "" {
			in.ServiceType = "NodePort"
		}
		if in.ServiceType != "ClusterIP" && in.ServiceType != "NodePort" && in.ServiceType != "LoadBalancer" {
			return "", fmt.Errorf("暴露方式只支持 ClusterIP/NodePort/LoadBalancer")
		}
		name := in.ExposeName
		if name == "" {
			name = "np" // 生成的 Service 名为 <cluster>-<component>-<name>，如 mysql-mysql-np
		}
		spec["expose"] = []any{map[string]any{
			"componentName": in.Component,
			"services":      []any{map[string]any{"name": name, "spec": map[string]any{"type": in.ServiceType}}},
			"switch":        map[bool]string{true: "Enable", false: "Disable"}[enable],
		}}
	case "RebuildInstance":
		if in.BackupName == "" {
			return "", fmt.Errorf("重建实例需给出备份名")
		}
		item := map[string]any{"componentName": in.Component, "backupName": in.BackupName, "inPlace": in.InPlace}
		spec["rebuildFrom"] = []any{item}
	default:
		return "", fmt.Errorf("不支持的运维类型: %s", in.Type)
	}
	name := fmt.Sprintf("kc-%s-%s-%s", cluster, lower(in.Type), randSuffix())
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kbOpsGVR.Group + "/" + kbOpsGVR.Version,
		"kind":       "OpsRequest",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}}
	if in.Description != "" {
		if err := unstructured.SetNestedField(obj.Object, in.Description, "spec", "description"); err != nil {
			return "", err
		}
	}
	if _, err := c.Dynamic.Resource(kbOpsGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("下发运维工单失败: %s", kbErrText(err))
	}
	return name, nil
}

// ---------- 版本升级链（ComponentVersion）----------

// KBComponentVersions 一个组件的可升级版本链（来自 ComponentVersion.compatibilityRules[].releases）
type KBComponentVersions struct {
	Component  string   `json:"component"`
	CompDef    string   `json:"compDef"`
	Current    string   `json:"current"`
	Candidates []string `json:"candidates"` // 版本号降序，含当前版本
}

// KBUpgradeCandidates 列出各组件可升级的版本。ComponentVersion 的 compDefs 匹配规则
// 与 operator 同口径：模式是**未锚定的正则**，对 ComponentDefinition 名做子串匹配——
// 所以 addon 里既有精确名（mysql-8.4-1.0.2）、有 ^ 锚定前缀（^kafka-broker-、^etcd-3.*），
// 也有裸前缀（redis-7、postgresql-17-），四种形态都在正则语义下自然成立。
// 返回的候选是 release 对应的 serviceVersion（OpsRequest 升级填的就是它）；
// release 名与 serviceVersion 可能不同（如 kafka 的 3.9.0-debian-12-r13 vs 3.9.0）。
func KBUpgradeCandidates(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBComponentVersions, error) {
	cl, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	comps, _, _ := unstructured.NestedSlice(cl.Object, "spec", "componentSpecs")
	if len(comps) == 0 {
		return []KBComponentVersions{}, nil
	}
	cvList, err := c.Dynamic.Resource(kbCompVerGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if metaNoMatch(err) {
			return []KBComponentVersions{}, nil
		}
		return nil, err
	}
	releases := map[string][]string{} // compDef 匹配模式 → 可升级的 serviceVersion 名单
	for i := range cvList.Items {
		cv := &cvList.Items[i]
		// release 名 → serviceVersion：升级填 serviceVersion，release 名可能带镜像后缀
		svOf := map[string]string{}
		relSpecs, _, _ := unstructured.NestedSlice(cv.Object, "spec", "releases")
		for _, rs := range relSpecs {
			rm, ok := rs.(map[string]any)
			if !ok {
				continue
			}
			name, _ := rm["name"].(string)
			sv, _ := rm["serviceVersion"].(string)
			if name != "" && sv != "" {
				svOf[name] = sv
			}
		}
		rules, _, _ := unstructured.NestedSlice(cv.Object, "spec", "compatibilityRules")
		for _, r := range rules {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			defs, _, _ := unstructured.NestedStringSlice(rm, "compDefs")
			// 可选版本是每条规则自己声明的 releases 名单（同一 CV 里不同 compDef 各有一份），
			// 不能用 spec.releases 全量——那里混着其他引擎线的版本
			rels, _, _ := unstructured.NestedStringSlice(rm, "releases")
			for _, d := range defs {
				for _, rel := range rels {
					if sv := svOf[rel]; sv != "" {
						releases[d] = append(releases[d], sv)
					} else {
						releases[d] = append(releases[d], rel)
					}
				}
			}
		}
	}
	out := make([]KBComponentVersions, 0, len(comps))
	for _, sp := range comps {
		sm, ok := sp.(map[string]any)
		if !ok {
			continue
		}
		name, _ := sm["name"].(string)
		def, _ := sm["componentDef"].(string)
		cur, _ := sm["serviceVersion"].(string)
		cv := KBComponentVersions{Component: name, CompDef: def, Current: cur}
		seen := map[string]bool{}
		for pattern, vers := range releases {
			if !kbCompDefMatch(pattern, def) {
				continue
			}
			for _, v := range vers {
				if !seen[v] {
					seen[v] = true
					cv.Candidates = append(cv.Candidates, v)
				}
			}
		}
		sort.Slice(cv.Candidates, func(i, j int) bool { return kbVerCompare(cv.Candidates[i], cv.Candidates[j]) > 0 })
		out = append(out, cv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	return out, nil
}

// kbCompDefMatch ComponentVersion.compatibilityRules[].compDefs 的匹配：
// 与 operator 一致按未锚定正则处理；模式不是合法正则时退回精确相等。
func kbCompDefMatch(pattern, def string) bool {
	if matched, err := regexp.MatchString(pattern, def); err == nil {
		return matched
	}
	return pattern == def
}

// ---------- 监控接入 ----------

// KBEnsureServiceMonitor 为集群各组件建 exporter 抓取配置。
// 实测：该集群 Prometheus 的 serviceMonitorSelector / serviceMonitorNamespaceSelector 均为 {}
// （匹配全部），所以建出来即被抓取；端口名固定 http-metrics（KubeBlocks exporter 约定）。
func KBEnsureServiceMonitor(ctx context.Context, c *kube.Client, ns, cluster string) ([]string, error) {
	refs, err := KBHeadlessServices(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	created := []string{}
	for comp, r := range refs {
		if !r.HasMetricsPort {
			continue
		}
		name := fmt.Sprintf("kb-%s-%s-exporter", cluster, comp)
		sm := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ServiceMonitor",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels": map[string]any{
					"app.kubernetes.io/managed-by": "kube-console",
					"kubeblocks.io/cluster":        cluster,
				},
			},
			"spec": map[string]any{
				"namespaceSelector": map[string]any{},
				"selector": map[string]any{"matchLabels": map[string]any{
					KBEssentialLabel: r.InstanceLabel,
				}},
				"endpoints": []any{map[string]any{
					"port":     r.MetricsPort,
					"interval": "30s",
					"path":     "/metrics",
				}},
			},
		}}
		cli := c.Dynamic.Resource(kbSMGVR).Namespace(ns)
		if _, err := cli.Create(ctx, sm, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			if metaNoMatch(err) {
				return created, fmt.Errorf("集群未安装 Prometheus Operator（无 ServiceMonitor CRD），无法接入抓取")
			}
			return created, fmt.Errorf("创建 ServiceMonitor 失败: %s", kbErrText(err))
		}
		created = append(created, name)
	}
	sort.Strings(created)
	return created, nil
}

// ---------- 连接信息 ----------

type KBConnPort struct {
	Name     string `json:"name"`
	Port     int64  `json:"port"`
	NodePort int64  `json:"nodePort,omitempty"`
}

type KBConnService struct {
	Component string       `json:"component"`
	Service   string       `json:"service"`
	Dns       string       `json:"dns"` // <svc>.<ns>.svc[:port]
	Type      string       `json:"type"`
	ClusterIP string       `json:"clusterIP,omitempty"`
	Ports     []KBConnPort `json:"ports"`
}

type KBConnAccount struct {
	Component string `json:"component"`
	Secret    string `json:"secret"`
	Username  string `json:"username,omitempty"`
}

type KBConnection struct {
	Services []KBConnService `json:"services"`
	Accounts []KBConnAccount `json:"accounts"`
	Note     string          `json:"note"`
}

// KBConnectionInfo "怎么连"：组件的 Service 端点 + 账号用户名。
// 密码永不回显（沿用 accounts 接口的安全口径），只指出所在 Secret。
func KBConnectionInfo(ctx context.Context, c *kube.Client, ns, cluster string) (*KBConnection, error) {
	out := &KBConnection{Services: []KBConnService{}, Accounts: []KBConnAccount{}}
	svcs, err := c.Clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + cluster,
	})
	if err != nil {
		return nil, err
	}
	for _, s := range svcs.Items {
		if strings.HasSuffix(s.Name, "-headless") {
			continue
		}
		// operator 在滚动发布期间会临时建 pre-provision 之类的 Service，只留 stable
		if p := s.Labels["apps.kubeblocks.io/release-phase"]; p != "" && p != "stable" {
			continue
		}
		cs := KBConnService{
			Component: s.Labels["apps.kubeblocks.io/component-name"],
			Service:   s.Name,
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
		}
		for _, p := range s.Spec.Ports {
			cp := KBConnPort{Name: p.Name, Port: int64(p.Port)}
			if p.NodePort > 0 {
				cp.NodePort = int64(p.NodePort)
			}
			cs.Ports = append(cs.Ports, cp)
		}
		if len(cs.Ports) > 0 {
			cs.Dns = fmt.Sprintf("%s.%s.svc:%d", s.Name, ns, cs.Ports[0].Port)
		}
		out.Services = append(out.Services, cs)
	}
	sort.Slice(out.Services, func(i, j int) bool {
		if out.Services[i].Component != out.Services[j].Component {
			return out.Services[i].Component < out.Services[j].Component
		}
		return out.Services[i].Service < out.Services[j].Service
	})
	secrets, err := c.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + cluster,
	})
	if err != nil {
		return out, nil
	}
	for _, s := range secrets.Items {
		if !strings.Contains(s.Name, "-account-") {
			continue
		}
		acc := KBConnAccount{Component: s.Labels["apps.kubeblocks.io/component-name"], Secret: s.Name}
		if comp := strings.TrimSuffix(strings.TrimPrefix(s.Name, cluster+"-"), ""); comp != "" && acc.Component == "" {
			// secret 名形态 <cluster>-<component>-account-<name>，标签缺失时从名字反推
			if i := strings.Index(comp, "-account-"); i > 0 {
				acc.Component = comp[:i]
			}
		}
		if u, ok := s.Data["username"]; ok {
			acc.Username = string(u)
		}
		if acc.Username != "" || acc.Component != "" {
			out.Accounts = append(out.Accounts, acc)
		}
	}
	sort.Slice(out.Accounts, func(i, j int) bool {
		if out.Accounts[i].Component != out.Accounts[j].Component {
			return out.Accounts[i].Component < out.Accounts[j].Component
		}
		return out.Accounts[i].Secret < out.Accounts[j].Secret
	})
	out.Note = "密码存于对应 Secret（键 password），控制台一律不回显"
	return out, nil
}

// ---------- 集群告警事件 ----------

type KBEvent struct {
	Type          string `json:"type"`
	Reason        string `json:"reason"`
	Object        string `json:"object"`
	Message       string `json:"message"`
	Count         int32  `json:"count"`
	LastTimestamp string `json:"lastTimestamp"`
}

// KBEvents 该集群资源（Pod/InstanceSet/PVC 等，名字都以 <cluster>- 开头）的 Warning 事件
func KBEvents(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBEvent, error) {
	list, err := c.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
		Limit:         300,
	})
	if err != nil {
		return nil, err
	}
	out := []KBEvent{}
	for i := range list.Items {
		ev := &list.Items[i]
		if !strings.HasPrefix(ev.InvolvedObject.Name, cluster+"-") {
			continue
		}
		e := KBEvent{Type: ev.Type, Reason: ev.Reason, Object: ev.InvolvedObject.Name, Message: ev.Message, Count: ev.Count}
		if !ev.LastTimestamp.IsZero() {
			e.LastTimestamp = ev.LastTimestamp.Format("2006-01-02 15:04:05")
		} else if !ev.FirstTimestamp.IsZero() {
			e.LastTimestamp = ev.FirstTimestamp.Format("2006-01-02 15:04:05")
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastTimestamp > out[j].LastTimestamp })
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

// ---------- 辅助 ----------

// kbComponentReplicas 读组件当前声明副本数（扩缩容要算增量）
func kbComponentReplicas(ctx context.Context, c *kube.Client, ns, cluster, component string) (int64, error) {
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("读取集群失败: %s", kbErrText(err))
	}
	comps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	for _, sp := range comps {
		sm, ok := sp.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := sm["name"].(string); n == component {
			r, _, _ := unstructured.NestedInt64(sm, "replicas")
			if r == 0 {
				r = 1
			}
			return r, nil
		}
	}
	return 0, fmt.Errorf("集群 %s 里没有组件 %s", cluster, component)
}

func lower(s string) string { return strings.ToLower(s) }

// str 取嵌套字符串字段的简写（详情类接口大量使用）
func str(obj *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, fields...)
	return v
}

// kbTime RFC3339 时间串转本地展示串（KubeBlocks status 里都是 RFC3339）
func kbTime(t string) string {
	if t == "" {
		return ""
	}
	if ts, err := time.Parse(time.RFC3339, t); err == nil {
		return ts.Local().Format("2006-01-02 15:04:05")
	}
	return t
}

func kbJoinKeys(m map[string]resourceapi.Quantity) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "、")
}

// kbComponentVolumes 组件各卷当前容量（VolumeExpansion 校验卷名与只扩不缩）
func kbComponentVolumes(ctx context.Context, c *kube.Client, ns, cluster, component string) (map[string]resourceapi.Quantity, error) {
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("读取集群失败: %s", kbErrText(err))
	}
	comps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	for _, sp := range comps {
		sm, ok := sp.(map[string]any)
		if !ok || sm["name"] != component {
			continue
		}
		out := map[string]resourceapi.Quantity{}
		vcts, _, _ := unstructured.NestedSlice(sm, "volumeClaimTemplates")
		for _, v := range vcts {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			vn, _ := vm["name"].(string)
			s, _, _ := unstructured.NestedString(vm, "spec", "resources", "requests", "storage")
			if q, err := resourceapi.ParseQuantity(s); err == nil && vn != "" {
				out[vn] = q
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("集群 %s 里没有组件 %s", cluster, component)
}

// kbComponentVersion 组件当前 serviceVersion（版本升级去重用）
func kbComponentVersion(ctx context.Context, c *kube.Client, ns, cluster, component string) (string, error) {
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("读取集群失败: %s", kbErrText(err))
	}
	comps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	for _, sp := range comps {
		sm, ok := sp.(map[string]any)
		if !ok || sm["name"] != component {
			continue
		}
		if sv, _, _ := unstructured.NestedString(sm, "serviceVersion"); sv != "" {
			return sv, nil
		}
		// spec 没写版本（跟随模板默认）时看 status.components
		return str(obj, "status", "components", component, "serviceVersion"), nil
	}
	return "", fmt.Errorf("集群 %s 里没有组件 %s", cluster, component)
}

// metaNoMatch RESTMapping 缺失（CRD 未安装）时返回 true，让上层降级而不是 500
func metaNoMatch(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") || strings.Contains(msg, "could not find the requested resource")
}

func kbErrText(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, `": `); i > 0 && len(msg)-i < 400 {
		return msg[i+3:]
	}
	if len(msg) > 400 {
		return msg[:400]
	}
	return msg
}

// kbTopologyShardings 从 ClusterDefinition 取该拓扑声明的分片组（名字 → ShardingDefinition）。
// 前端没填 shardingDef 时靠它兜底；拓扑名为空则把所有拓扑的分片组并进来。
func kbTopologyShardings(ctx context.Context, c *kube.Client, clusterDef, topology string) (map[string]string, error) {
	out := map[string]string{}
	obj, err := c.Dynamic.Resource(kbClusterDefGVR).Get(ctx, clusterDef, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || metaNoMatch(err) {
			return out, nil // 拿不到定义就让调用方显式给 shardingDef
		}
		return nil, err
	}
	tops, _, _ := unstructured.NestedSlice(obj.Object, "spec", "topologies")
	for _, t := range tops {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(tm, "name"); topology != "" && name != topology {
			continue
		}
		shs, _, _ := unstructured.NestedSlice(tm, "shardings")
		for _, sh := range shs {
			sm, ok := sh.(map[string]any)
			if !ok {
				continue
			}
			n, _ := sm["name"].(string)
			def, _ := sm["shardingDef"].(string)
			if n != "" {
				out[n] = def
			}
		}
	}
	return out, nil
}

// kbShardingSpecs 组装 spec.shardings。
//
// 两件事必须在这里做，否则要么建出跑不起来的集群、要么被 apiserver 静默放过：
//  1. 分片数按 ShardingDefinition.spec.shardsLimit 校验——实测 dryRun 传 shards=2
//     （minShards=3）apiserver 与 webhook 都不拦，只能我们自己拦；
//  2. 模板 compDef 解析成具体 ComponentDefinition（ShardingDefinition 里写的也是模式，
//     如 redis-cluster 的 ^redis-cluster-\d+），并校验它确实属于该分片定义。
func kbShardingSpecs(ctx context.Context, c *kube.Client, ns string, in *KBCreateInput, idx *kbCompDefIndex) ([]any, error) {
	if len(in.Shardings) == 0 {
		return nil, nil
	}
	declared, err := kbTopologyShardings(ctx, c, in.ClusterDef, in.Topology)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(in.Shardings))
	seen := map[string]bool{}
	for _, si := range in.Shardings {
		if !kbNameRe.MatchString(si.Name) {
			return nil, fmt.Errorf("分片组名不合法（须为 RFC 1123 小写名）: %s", si.Name)
		}
		if seen[si.Name] {
			return nil, fmt.Errorf("分片组名重复: %s", si.Name)
		}
		seen[si.Name] = true
		defName := si.ShardingDef
		if defName == "" {
			defName = declared[si.Name]
		}
		if defName == "" {
			return nil, fmt.Errorf("分片组 %s 没指定 ShardingDefinition，引擎 %s 的拓扑 %s 里也没有这个名字", si.Name, in.ClusterDef, in.Topology)
		}
		sd := idx.shardingDef(defName)
		if sd == nil {
			return nil, fmt.Errorf("ShardingDefinition %s 不存在（该引擎 addon 未装分片模板？）", defName)
		}
		minS, maxS := int64(1), int64(0)
		if lim, ok, _ := unstructured.NestedMap(sd.Object, "spec", "shardsLimit"); ok {
			if v, b, _ := unstructured.NestedInt64(lim, "minShards"); b {
				minS = v
			}
			if v, b, _ := unstructured.NestedInt64(lim, "maxShards"); b {
				maxS = v
			}
		}
		if si.Shards < minS || (maxS > 0 && si.Shards > maxS) {
			lim := fmt.Sprintf("至少 %d", minS)
			if maxS > 0 {
				lim += fmt.Sprintf("、最多 %d", maxS)
			}
			return nil, fmt.Errorf("分片组 %s 的分片数 %d 不合法（需 %s）", si.Name, si.Shards, lim)
		}
		pattern, _, _ := unstructured.NestedString(sd.Object, "spec", "template", "compDef")
		want := si.CompDef
		if want == "" {
			want = pattern
		}
		opts := idx.match(pattern)
		if len(opts) == 0 {
			return nil, fmt.Errorf("分片定义 %s 没有可用的组件模板（模式 %q 匹配为空）", defName, pattern)
		}
		picked, err := kbPickCompDef(idx, want, si.ServiceVersion)
		if err != nil {
			return nil, fmt.Errorf("分片组 %s: %w", si.Name, err)
		}
		belongs := false
		for _, o := range opts {
			if o.name == picked.name {
				belongs = true
				break
			}
		}
		if !belongs {
			cands := make([]string, 0, len(opts))
			for _, o := range opts {
				cands = append(cands, o.name)
			}
			return nil, fmt.Errorf("分片组 %s 的模板 %s 不属于 %s（可选：%s）", si.Name, picked.name, defName, strings.Join(cands, "、"))
		}
		ci := KBComponentInput{
			Name: si.Name, CompDef: picked.name, ServiceVersion: picked.serviceVersion,
			CPURequest: si.CPURequest, MemRequest: si.MemRequest, CPULimit: si.CPULimit, MemLimit: si.MemLimit,
			Storage: si.Storage, StorageClass: si.StorageClass,
		}
		replicas := si.Replicas
		if replicas < 1 {
			replicas = 1
		}
		tpl := map[string]any{"componentDef": picked.name, "replicas": replicas}
		if picked.serviceVersion != "" {
			tpl["serviceVersion"] = picked.serviceVersion
		}
		if res := resourceMap(ci); len(res) > 0 {
			tpl["resources"] = res
		}
		if si.Storage != "" {
			tpl["volumeClaimTemplates"] = []any{pvcTemplate(ci)}
		}
		out = append(out, map[string]any{"name": si.Name, "shardingDef": defName, "shards": si.Shards, "template": tpl})
	}
	return out, nil
}
