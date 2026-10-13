package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

func kbUD(name string, gvr schema.GroupVersionResource, kind, ns string, obj map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: obj}
	u.SetName(name)
	if ns != "" {
		u.SetNamespace(ns)
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: kind})
	return u
}

func kbCompDef(name, serviceVersion string) *unstructured.Unstructured {
	o := map[string]any{"spec": map[string]any{}}
	if serviceVersion != "" {
		o["spec"].(map[string]any)["serviceVersion"] = serviceVersion
	}
	o["spec"].(map[string]any)["replicasLimit"] = map[string]any{"min": int64(1), "max": int64(3)}
	return kbUD(name, kbCompDefGVR, "ComponentDefinition", "", o)
}

func kbClusterDef(name, topo, comp, compDefPattern string) *unstructured.Unstructured {
	return kbUD(name, kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
		"topologies": []any{map[string]any{
			"name":    topo,
			"default": true,
			"components": []any{map[string]any{
				"name":    comp,
				"compDef": compDefPattern,
			}},
		}},
	}})
}

func kbFakeDyn(objs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		kbClusterDefGVR:  "ClusterDefinitionList",
		kbCompDefGVR:     "ComponentDefinitionList",
		kbClusterGVR:     "ClusterList",
		kbInstanceSetGVR: "InstanceSetList",
		kbOpsGVR:         "OpsRequestList",
		kbCompGVR:        "ComponentList",
		kbShardingDefGVR: "ShardingDefinitionList",
		kbCompVerGVR:     "ComponentVersionList",
		kbPCRGVR:         "ParamConfigRendererList",
		kbPDGVR:          "ParametersDefinitionList",
		kbCPGVR:          "ComponentParameterList",
		kbBackupGVR:      "BackupList",
		kbPolicyGVR:      "BackupPolicyList",
		kbSchedGVR:       "BackupScheduleList",
		kbRepoGVR:        "BackupRepoList",
		kbProviderGVR:    "StorageProviderList",
		kbRestoreGVR:     "RestoreList",
		// 抓取自检这条链路要列/建这两类对象；fake dynamic client 对没登记 list kind 的
		// GVR 是 panic 而不是报错，新增一类就要在这里补一行
		kbSMGVR:         "ServiceMonitorList",
		kbPrometheusGVR: "PrometheusList",
	}
	runtimeObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, runtimeObjs...)
}

// 实测：addon 的 topologies[].components[].compDef 写的是正则（^mysql-\d+\.\d+.*$），
// 目录必须把它解析成具体 ComponentDefinition 候选，默认取版本最高的那个
func TestKBEnginesResolvesCompDefPattern(t *testing.T) {
	dyn := kbFakeDyn(
		kbClusterDef("mysql", "semisync", "mysql", `^mysql-\d+\.\d+.*$`),
		kbCompDef("mysql-5.7-1.0.2", "5.7.44"),
		kbCompDef("mysql-8.0-1.0.2", "8.0.33"),
		kbCompDef("mysql-8.4-1.0.2", "8.4.2"),
		kbCompDef("mysql-mgr-8.4-1.0.2", "8.4.2"),
		kbCompDef("redis-7-1.0.2", "7.2.11"),
	)
	engines, err := KBEngines(context.Background(), &kube.Client{Dynamic: dyn})
	if err != nil {
		t.Fatalf("KBEngines: %v", err)
	}
	if len(engines) != 1 || len(engines[0].Topologies) != 1 {
		t.Fatalf("引擎目录结构不对: %+v", engines)
	}
	comp := engines[0].Topologies[0].Components[0]
	if comp.CompDef != "mysql-8.4-1.0.2" {
		t.Errorf("默认模板应取最高版本 mysql-8.4-1.0.2，实际 %s", comp.CompDef)
	}
	if comp.CompDefPattern != `^mysql-\d+\.\d+.*$` {
		t.Errorf("模式应保留供展示，实际 %s", comp.CompDefPattern)
	}
	if comp.ServiceVersion != "8.4.2" {
		t.Errorf("默认版本 %s，实际 %s", "8.4.2", comp.ServiceVersion)
	}
	got := []string{}
	for _, o := range comp.CompDefs {
		got = append(got, o.CompDef)
	}
	want := []string{"mysql-8.4-1.0.2", "mysql-8.0-1.0.2", "mysql-5.7-1.0.2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("候选 = %v，实际 %v", want, got)
	}
	if comp.ReplicasMax != 3 {
		t.Errorf("副本上限应取自模板，实际 %d", comp.ReplicasMax)
	}
}

// 前缀式写法（postgresql- / mysql-mgr）按非锚定正则匹配，与 KubeBlocks 同口径
func TestKBCompDefIndexPrefixMatch(t *testing.T) {
	idx := kbLoadCompDefs(context.Background(), &kube.Client{
		Dynamic: kbFakeDyn(
			kbCompDef("postgresql-12-1.0.2", "12.22.0"),
			kbCompDef("postgresql-16-1.0.2", "16.9.0"),
			kbCompDef("mysql-mgr-8.4-1.0.2", "8.4.2"),
		),
	})
	for _, tc := range []struct{ pattern, first string }{{"postgresql-", "postgresql-16-1.0.2"}, {"mysql-mgr", "mysql-mgr-8.4-1.0.2"}} {
		opts := idx.match(tc.pattern)
		if len(opts) == 0 || opts[0].name != tc.first {
			t.Errorf("%s -> %v，期望首个 %s", tc.pattern, opts, tc.first)
		}
	}
	if opts := idx.match(`^nope-\d+$`); len(opts) != 0 {
		t.Errorf("不该匹配到任何东西：%v", opts)
	}
}

// 创建时把模式解析成具体 componentDef 下发——直接发模式会被 apiserver 拒
// （spec.componentSpecs[0].componentDef in body should match '^[a-z]...'）
func TestKBCreateClusterResolvesPattern(t *testing.T) {
	dyn := kbFakeDyn(
		kbCompDef("mysql-5.7-1.0.2", "5.7.44"),
		kbCompDef("mysql-8.4-1.0.2", "8.4.2"),
	)
	in := &KBCreateInput{Name: "order-db", ClusterDef: "mysql", Topology: "semisync",
		Components: []KBComponentInput{{Name: "mysql", CompDef: `^mysql-\d+\.\d+.*$`, Replicas: 2, Storage: "20Gi"}}}
	if err := KBCreateCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	obj, err := dyn.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "order-db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回集群失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	m := specs[0].(map[string]any)
	if m["componentDef"] != "mysql-8.4-1.0.2" {
		t.Errorf("componentDef = %v，期望 mysql-8.4-1.0.2", m["componentDef"])
	}
	if m["serviceVersion"] != "8.4.2" {
		t.Errorf("serviceVersion = %v，期望 8.4.2", m["serviceVersion"])
	}
}

// 用户指定的版本没有对应模板时要说清可选项，而不是让 apiserver 报正则错
func TestKBCreateClusterUnknownVersion(t *testing.T) {
	dyn := kbFakeDyn(kbCompDef("mysql-8.4-1.0.2", "8.4.2"), kbCompDef("mysql-5.7-1.0.2", "5.7.44"))
	in := &KBCreateInput{Name: "db", ClusterDef: "mysql",
		Components: []KBComponentInput{{Name: "mysql", CompDef: `^mysql-\d+\.\d+.*$`, ServiceVersion: "8.4.7"}}}
	err := KBCreateCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", in)
	if err == nil {
		t.Fatal("版本不存在时应报错")
	}
	if !strings.Contains(err.Error(), "8.4.2") || !strings.Contains(err.Error(), "5.7.44") {
		t.Errorf("错误里要列出可选版本，实际: %v", err)
	}
}

// 模板列表取不到（无 list 权限）时按原样下发，保持旧行为
func TestKBCreateClusterPassesThroughWhenIndexEmpty(t *testing.T) {
	dyn := kbFakeDyn()
	in := &KBCreateInput{Name: "db", ClusterDef: "mysql",
		Components: []KBComponentInput{{Name: "mysql", CompDef: "mysql-8.0-1.0.2", ServiceVersion: "8.0.33"}}}
	if err := KBCreateCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	obj, _ := dyn.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "db", metav1.GetOptions{})
	specs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	if got := specs[0].(map[string]any)["componentDef"]; got != "mysql-8.0-1.0.2" {
		t.Errorf("componentDef = %v", got)
	}
}

// 无拓扑（standalone）建法只有 spec.componentSpecs，没有 clusterDef：
// 引擎必须从组件模板的 app.kubernetes.io/name 标签反推，否则这类集群在任何引擎子菜单里都不显示
func TestKBListClustersDerivesEngineWithoutClusterDef(t *testing.T) {
	comp := kbCompDef("mysql-8.0-1.0.2", "8.0.33")
	comp.SetLabels(map[string]string{"app.kubernetes.io/name": "mysql"})
	cluster := kbUD("mysql-cluster", kbClusterGVR, "Cluster", "public-service", map[string]any{"spec": map[string]any{
		"terminationPolicy": "Delete",
		"componentSpecs": []any{map[string]any{
			"name": "mysql", "componentDef": "mysql-8.0-1.0.2", "replicas": int64(2),
		}},
	}})
	list, err := KBListClusters(context.Background(), &kube.Client{Dynamic: kbFakeDyn(comp, cluster)}, "")
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 个集群，实际 %d", len(list))
	}
	if list[0].ClusterDef != "" || list[0].Engine != "mysql" {
		t.Errorf("clusterDef=%q engine=%q，期望空 / mysql", list[0].ClusterDef, list[0].Engine)
	}
}

// 模板没打标签时按名字猜，至少不要留空把集群藏起来
func TestKBEngineOfNameFallback(t *testing.T) {
	for name, want := range map[string]string{
		"mysql-8.0-1.0.2":              "mysql",
		"elasticsearch-master-8-1.0.5": "elasticsearch-master",
		"redis-8-1.0.2":                "redis",
	} {
		if got := kbEngineOfName(name, nil); got != want {
			t.Errorf("%s -> %s，期望 %s", name, got, want)
		}
	}
}

func TestKBVerCompare(t *testing.T) {
	cases := [][3]string{{"8.4.2", "8.0.33", ">"}, {"12.22.0", "9.1.0", ">"}, {"7.2.11", "7.2.11", "="}, {"5.0.12", "7.2.11", "<"}}
	for _, c := range cases {
		got := kbVerCompare(c[0], c[1])
		want := map[string]int{">": 1, "=": 0, "<": -1}[c[2]]
		if got != want {
			t.Errorf("kbVerCompare(%s,%s)=%d 期望 %d", c[0], c[1], got, want)
		}
	}
}

// ---------- ① 工单生命周期补全 ----------

// 取消 = patch spec.cancel=true；终态工单要先拦下（operator 不会理会的请求不该发出去）
func TestKBCancelOpsPatchesCancel(t *testing.T) {
	running := kbUD("kc-db-restart-x", kbOpsGVR, "OpsRequest", "app", map[string]any{
		"spec":   map[string]any{"clusterName": "db", "type": "Restart"},
		"status": map[string]any{"phase": "Running"},
	})
	done := kbUD("kc-db-stop-y", kbOpsGVR, "OpsRequest", "app", map[string]any{
		"spec":   map[string]any{"clusterName": "db", "type": "Stop"},
		"status": map[string]any{"phase": "Succeed"},
	})
	dyn := kbFakeDyn(running, done)
	if err := KBCancelOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "kc-db-restart-x"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	obj, _ := dyn.Resource(kbOpsGVR).Namespace("app").Get(context.Background(), "kc-db-restart-x", metav1.GetOptions{})
	if cancel, _, _ := unstructured.NestedBool(obj.Object, "spec", "cancel"); !cancel {
		t.Errorf("spec.cancel 应为 true")
	}
	if err := KBCancelOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "kc-db-stop-y"); err == nil || !strings.Contains(err.Error(), "终态") {
		t.Errorf("终态工单应报错，实际: %v", err)
	}
}

// 工单详情要带条件时间线与可取消标记
func TestKBGetOpsConditions(t *testing.T) {
	ops := kbUD("kc-db-restart-x", kbOpsGVR, "OpsRequest", "app", map[string]any{
		"spec": map[string]any{"clusterName": "db", "type": "Restart"},
		"status": map[string]any{
			"phase": "Running", "progress": "1/2",
			"startTimestamp": "2026-09-26T02:00:00Z",
			"lastConfiguration": map[string]any{"components": map[string]any{
				"mysql": map[string]any{"replicas": int64(2)},
			}},
			"conditions": []any{
				map[string]any{"type": "Validated", "status": "True", "reason": "ValidateOpsRequestPassed", "message": "ok", "lastTransitionTime": "2026-09-26T02:00:01Z"},
				map[string]any{"type": "Restarting", "status": "True", "message": "restarting"},
			},
		},
	})
	d, err := KBGetOps(context.Background(), &kube.Client{Dynamic: kbFakeDyn(ops)}, "app", "kc-db-restart-x")
	if err != nil {
		t.Fatalf("KBGetOps: %v", err)
	}
	if !d.Cancelable || d.Phase != "Running" || len(d.Conditions) != 2 {
		t.Fatalf("详情不对: %+v", d)
	}
	if d.Conditions[0].Reason != "ValidateOpsRequestPassed" || d.StartAt == "" {
		t.Errorf("条件/时间没解析出来: %+v", d.Conditions)
	}
	if d.LastConfiguration == nil {
		t.Errorf("变更前旧值应保留")
	}
}

// 列表：跨分页拉全 + 按 spec.clusterName 过滤（kubectl 手建的无标签工单不能丢）
func TestKBListOpsPaginatesAndFilters(t *testing.T) {
	objs := []*unstructured.Unstructured{}
	for i := 0; i < 210; i++ {
		objs = append(objs, kbUD(fmt.Sprintf("other-%03d", i), kbOpsGVR, "OpsRequest", "app", map[string]any{
			"spec":   map[string]any{"clusterName": "other", "type": "Restart"},
			"status": map[string]any{"phase": "Succeed"},
		}))
	}
	// 无 label 的老工单 + 目标集群 3 条
	objs = append(objs, kbUD("db-old-ops", kbOpsGVR, "OpsRequest", "app", map[string]any{
		"spec": map[string]any{"clusterName": "db", "type": "VolumeExpansion"},
	}))
	for i, ty := range []string{"Restart", "Stop", "Restart"} {
		objs = append(objs, kbUD(fmt.Sprintf("kc-db-%s-%d", strings.ToLower(ty), i), kbOpsGVR, "OpsRequest", "app", map[string]any{
			"spec":   map[string]any{"clusterName": "db", "type": ty},
			"status": map[string]any{"phase": "Succeed"},
		}))
	}
	ops, err := KBListOps(context.Background(), &kube.Client{Dynamic: kbFakeDyn(objs...)}, "app", "db")
	if err != nil {
		t.Fatalf("KBListOps: %v", err)
	}
	if len(ops) != 4 {
		t.Fatalf("应只看到 db 集群的 4 条（跨页拉全），实际 %d", len(ops))
	}
}

// VolumeExpansion：卷名必须来自 volumeClaimTemplates，只许扩不许缩
func TestKBSubmitOpsVolumeExpansion(t *testing.T) {
	cluster := kbUD("db", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{
		"clusterDef": "mysql",
		"componentSpecs": []any{map[string]any{
			"name": "mysql", "componentDef": "mysql-8.0-1.0.2", "replicas": int64(1),
			"volumeClaimTemplates": []any{map[string]any{
				"name": "data",
				"spec": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "20Gi"}}},
			}},
		}},
	}})
	dyn := kbFakeDyn(cluster)
	submit := func(vols []KBOpsVolume) error {
		_, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
			&KBOpsInput{Type: "VolumeExpansion", Component: "mysql", Volumes: vols})
		return err
	}
	if err := submit([]KBOpsVolume{{Name: "data", Storage: "50Gi"}}); err != nil {
		t.Fatalf("扩容应成功下发: %v", err)
	}
	if err := submit([]KBOpsVolume{{Name: "data", Storage: "10Gi"}}); err == nil || !strings.Contains(err.Error(), "只能扩容") {
		t.Errorf("缩容应被拦下，实际: %v", err)
	}
	if err := submit([]KBOpsVolume{{Name: "logs", Storage: "50Gi"}}); err == nil || !strings.Contains(err.Error(), "data") {
		t.Errorf("未知卷名要报可选卷，实际: %v", err)
	}
	objs, _ := dyn.Resource(kbOpsGVR).Namespace("app").List(context.Background(), metav1.ListOptions{})
	item, _, _ := unstructured.NestedSlice(objs.Items[0].Object, "spec", "volumeExpansion")
	vct, _, _ := unstructured.NestedSlice(item[0].(map[string]any), "volumeClaimTemplates")
	if got := vct[0].(map[string]any); got["name"] != "data" || got["storage"] != "50Gi" {
		t.Errorf("volumeExpansion 项形态不对: %v", got)
	}
}

// Upgrade：版本必须在 ComponentVersion 升级链里（前缀/精确两种规则）
func TestKBUpgradeCandidatesAndSubmit(t *testing.T) {
	cluster := kbUD("db", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{
		"componentSpecs": []any{
			map[string]any{"name": "mysql", "componentDef": "mysql-8.4-1.0.2", "serviceVersion": "8.4.2"},
			map[string]any{"name": "proxy", "componentDef": "apecloud-mysql-1.0.1", "serviceVersion": "8.0.30"},
		},
	}})
	cv := kbUD("mysql", kbCompVerGVR, "ComponentVersion", "", map[string]any{"spec": map[string]any{
		"compatibilityRules": []any{
			map[string]any{"compDefs": []any{"mysql-8.4-1.0.2"}, "releases": []any{"8.4.0", "8.4.2", "8.4.7"}},
			map[string]any{"compDefs": []any{"apecloud-mysql-"}, "releases": []any{"8.0.30"}},
		},
		"releases": []any{
			map[string]any{"name": "8.4.7", "images": map[string]any{"mysql": "img-8.4.7"}},
			map[string]any{"name": "8.4.2", "images": map[string]any{"mysql": "img-8.4.2"}},
			map[string]any{"name": "8.4.0", "images": map[string]any{"mysql": "img-8.4.0"}},
			map[string]any{"name": "8.0.30", "images": map[string]any{"mysql": "img-8.0.30"}},
		},
	}})
	dyn := kbFakeDyn(cluster, cv)
	cands, err := KBUpgradeCandidates(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db")
	if err != nil {
		t.Fatalf("KBUpgradeCandidates: %v", err)
	}
	if len(cands) != 2 || cands[0].Component != "mysql" || len(cands[0].Candidates) != 3 {
		t.Fatalf("版本链不对: %+v", cands)
	}
	if cands[0].Candidates[0] != "8.4.7" || cands[1].Candidates[0] != "8.0.30" {
		t.Errorf("候选应按版本降序且前缀规则生效: %+v", cands)
	}
	// 不在链里的版本拒掉并给可选项
	_, err = KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Upgrade", Component: "mysql", ServiceVersion: "9.9.9"})
	if err == nil || !strings.Contains(err.Error(), "8.4.7") {
		t.Errorf("链外版本应报可选版本，实际: %v", err)
	}
	// 链内版本正常下发
	if _, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Upgrade", Component: "mysql", ServiceVersion: "8.4.7"}); err != nil {
		t.Fatalf("升级应成功下发: %v", err)
	}
	// Switchover / Expose 的 spec 形态
	if _, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Switchover", Component: "mysql", Instance: "db-mysql-1"}); err != nil {
		t.Fatalf("switchover 下发失败: %v", err)
	}
	enable := true
	if _, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Expose", Component: "mysql", ServiceType: "NodePort", Enable: &enable}); err != nil {
		t.Fatalf("expose 下发失败: %v", err)
	}
	ops, _ := dyn.Resource(kbOpsGVR).Namespace("app").List(context.Background(), metav1.ListOptions{})
	var sw, ex map[string]any
	for _, o := range ops.Items {
		ty, _, _ := unstructured.NestedString(o.Object, "spec", "type")
		switch ty {
		case "Switchover":
			lst, _, _ := unstructured.NestedSlice(o.Object, "spec", "switchover")
			sw = lst[0].(map[string]any)
		case "Expose":
			lst, _, _ := unstructured.NestedSlice(o.Object, "spec", "expose")
			ex = lst[0].(map[string]any)
		}
	}
	if sw == nil || sw["instanceName"] != "db-mysql-1" {
		t.Errorf("switchover 项应带 instanceName: %v", sw)
	}
	if ex == nil || ex["switch"] != "Enable" {
		t.Errorf("expose 项应带 switch=Enable: %v", ex)
	}
}

// compDefs 的四种线上真实形态（精确名 / - 结尾前缀 / ^ 锚定正则 / 裸前缀）都要命中；
// 且 release 名 ≠ serviceVersion 时（kafka 的 -debian-… 后缀），候选应给 serviceVersion
func TestKBUpgradeCandidatesMatchPatterns(t *testing.T) {
	cluster := kbUD("db", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{
		"componentSpecs": []any{
			map[string]any{"name": "broker", "componentDef": "kafka-broker-1.0.2", "serviceVersion": "3.3.2"},
			map[string]any{"name": "redis", "componentDef": "redis-7-1.0.2", "serviceVersion": "7.4.7"},
		},
	}})
	cvKafka := kbUD("kafka-broker", kbCompVerGVR, "ComponentVersion", "", map[string]any{"spec": map[string]any{
		"compatibilityRules": []any{
			map[string]any{"compDefs": []any{"^kafka27-broker-"}, "releases": []any{"2.8.2-r1"}},
			map[string]any{"compDefs": []any{"^kafka-broker-"}, "releases": []any{"3.3.2-debian-11-r54", "3.9.0-debian-12-r13"}},
		},
		"releases": []any{
			map[string]any{"name": "3.3.2-debian-11-r54", "serviceVersion": "3.3.2"},
			map[string]any{"name": "3.9.0-debian-12-r13", "serviceVersion": "3.9.0"},
			map[string]any{"name": "2.8.2-r1", "serviceVersion": "2.8.2"},
		},
	}})
	cvRedis := kbUD("redis", kbCompVerGVR, "ComponentVersion", "", map[string]any{"spec": map[string]any{
		"compatibilityRules": []any{
			map[string]any{"compDefs": []any{"redis-7"}, "releases": []any{"7.4.7", "7.2.12"}},
			map[string]any{"compDefs": []any{"redis-8"}, "releases": []any{"8.4.0"}},
		},
	}})
	dyn := kbFakeDyn(cluster, cvKafka, cvRedis)
	cands, err := KBUpgradeCandidates(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db")
	if err != nil {
		t.Fatalf("KBUpgradeCandidates: %v", err)
	}
	byComp := map[string]KBComponentVersions{}
	for _, c := range cands {
		byComp[c.Component] = c
	}
	// ^ 锚定规则命中且只命中本线：候选是 serviceVersion（release 名已映射）并降序
	if got := byComp["broker"].Candidates; len(got) != 2 || got[0] != "3.9.0" || got[1] != "3.3.2" {
		t.Errorf("^kafka-broker- 应命中且候选为 serviceVersion: %+v", got)
	}
	// 裸前缀（不带 ^ 也不带 - 结尾）同样按正则子串命中；redis-8 不能误命中 redis-7
	if got := byComp["redis"].Candidates; len(got) != 2 || got[0] != "7.4.7" || got[1] != "7.2.12" {
		t.Errorf("裸前缀 redis-7 应命中 redis-7-1.0.2: %+v", got)
	}
	// 升级提交按 serviceVersion 校验与下发
	if _, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Upgrade", Component: "broker", ServiceVersion: "3.9.0"}); err != nil {
		t.Fatalf("按 serviceVersion 升级应成功: %v", err)
	}
	if _, err := KBSubmitOps(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBOpsInput{Type: "Upgrade", Component: "broker", ServiceVersion: "3.9.0-debian-12-r13"}); err == nil {
		t.Errorf("release 名不是 serviceVersion，应被拒")
	}
}

// ---------- ④ 连接信息 / 分片 / 事件 ----------

func TestKBConnectionInfo(t *testing.T) {
	svc := func(name string, nodePort int32, labels map[string]string) *corev1.Service {
		s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", Labels: labels}}
		s.Spec.Type = corev1.ServiceTypeClusterIP
		s.Spec.ClusterIP = "10.68.1.1"
		p := corev1.ServicePort{Name: "mysql", Port: 3306}
		if nodePort > 0 {
			s.Spec.Type = corev1.ServiceTypeNodePort
			p.NodePort = nodePort
		}
		s.Spec.Ports = []corev1.ServicePort{p}
		return s
	}
	base := map[string]string{"app.kubernetes.io/instance": "db", "apps.kubeblocks.io/component-name": "mysql"}
	pre := map[string]string{"app.kubernetes.io/instance": "db", "apps.kubeblocks.io/component-name": "mysql", "apps.kubeblocks.io/release-phase": "pre-provision"}
	cs := k8sfake.NewSimpleClientset(
		svc("db-mysql", 0, base),
		svc("db-mysql-np", 32360, base),
		svc("db-mysql-headless", 0, base), // headless 不算连接端点
		svc("db-mysql-pp", 0, pre),        // 发布期临时 Service 不算
		svc("other-mysql", 0, map[string]string{"app.kubernetes.io/instance": "other"}),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-mysql-account-root", Namespace: "app", Labels: base},
			Data: map[string][]byte{"username": []byte("root"), "password": []byte("topsecret")}},
	)
	conn, err := KBConnectionInfo(context.Background(), &kube.Client{Clientset: cs}, "app", "db")
	if err != nil {
		t.Fatalf("KBConnectionInfo: %v", err)
	}
	if len(conn.Services) != 2 {
		t.Fatalf("应只列 2 个对外 Service（排除 headless/发布期/他集群），实际 %+v", conn.Services)
	}
	if conn.Services[0].Dns != "db-mysql.app.svc:3306" {
		t.Errorf("内网地址不对: %s", conn.Services[0].Dns)
	}
	if conn.Services[1].Ports[0].NodePort != 32360 {
		t.Errorf("NodePort 没带上: %+v", conn.Services[1].Ports[0])
	}
	if len(conn.Accounts) != 1 || conn.Accounts[0].Username != "root" {
		t.Fatalf("账号应只有用户名（密码永不回显）: %+v", conn.Accounts)
	}
}

func TestKBEventsFilteredByCluster(t *testing.T) {
	mk := func(name, msg string) *corev1.Event {
		ev := &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name + "-evt", Namespace: "app"},
			InvolvedObject: corev1.ObjectReference{Name: name, Kind: "Pod"},
			Reason:         "BackOff", Message: msg, Type: "Warning", Count: 3,
		}
		ev.LastTimestamp = metav1.Now()
		return ev
	}
	cs := k8sfake.NewSimpleClientset(mk("db-mysql-0", "Back-off restarting"), mk("other-x-0", "nope"))
	events, err := KBEvents(context.Background(), &kube.Client{Clientset: cs}, "app", "db")
	if err != nil {
		t.Fatalf("KBEvents: %v", err)
	}
	if len(events) != 1 || events[0].Object != "db-mysql-0" {
		t.Fatalf("应只含本集群事件: %+v", events)
	}
}

func TestKBShardingParse(t *testing.T) {
	cluster := kbUD("mg", kbClusterGVR, "Cluster", "app", map[string]any{
		"spec": map[string]any{
			"componentSpecs": []any{map[string]any{"name": "configsvr", "componentDef": "mongo-config-server-1.0.2", "replicas": int64(3)}},
			"shardings":      []any{map[string]any{"name": "shard", "shardingDef": "mongodb-sharding-1.0.2", "shards": int64(2)}},
		},
		"status": map[string]any{
			"phase":     "Running",
			"shardings": map[string]any{"shard": map[string]any{"phase": "Running", "upToDate": true}},
		},
	})
	shardComp := kbUD("mg-shard-abc", kbCompGVR, "Component", "app", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{
			"app.kubernetes.io/instance":             "mg",
			"apps.kubeblocks.io/component-name":      "shard-abc",
			"componentdefinition.kubeblocks.io/name": "mongodb-1.0.2",
		}},
		"status": map[string]any{"phase": "Running"},
	})
	iset := kbUD("mg-shard-abc", kbInstanceSetGVR, "InstanceSet", "app", map[string]any{
		"status": map[string]any{"replicas": int64(3), "readyReplicas": int64(3)},
	})
	// KBGetCluster 会查 Service（headless 标签），给个空 fake Clientset
	v, err := KBGetCluster(context.Background(), &kube.Client{Dynamic: kbFakeDyn(cluster, shardComp, iset), Clientset: k8sfake.NewSimpleClientset()}, "app", "mg")
	if err != nil {
		t.Fatalf("KBGetCluster: %v", err)
	}
	if len(v.Shardings) != 1 || v.Shardings[0].Shards != 2 || v.Shardings[0].Phase != "Running" {
		t.Fatalf("分片拓扑没解析出来: %+v", v.Shardings)
	}
	members := v.Shardings[0].Components
	if len(members) != 1 || members[0].Name != "shard-abc" || members[0].Replicas != 3 || members[0].ReadyReplicas != 3 {
		t.Fatalf("分片成员/就绪数不对: %+v", members)
	}
}
