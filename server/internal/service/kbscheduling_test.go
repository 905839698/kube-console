package service

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// 建集群时的调度设置：转成 corev1 的形状要对（tolerations / nodeAffinity / podAntiAffinity），
// 且所有取值都得先校验——CRD 的 pruning 与调度器都不会替用户兜住拼错的 operator
func TestKBSchedulingSpecShape(t *testing.T) {
	s := &KBScheduling{
		NodeSelector: map[string]string{"node-role.kubernetes.io/db": "true"},
		Tolerations: []KBToleration{
			{Key: "dedicated", Value: "db", Operator: "Equal", Effect: "NoSchedule"},
			{Key: "disk-pressure", Effect: "NoExecute"}, // operator 空 = Exists
		},
		NodeRequired:  []KBNodeMatch{{Key: "kubernetes.io/arch", Operator: "In", Values: []string{"amd64"}}},
		NodePreferred: []KBNodeMatch{{Key: "node-role.kubernetes.io/db", Operator: "Exists"}},
		Spread:        "preferred",
	}
	spec, err := kbSchedulingSpec(s, "mysql-cluster")
	if err != nil {
		t.Fatalf("kbSchedulingSpec: %v", err)
	}
	sel, ok, _ := unstructured.NestedStringMap(spec, "nodeSelector")
	if !ok || sel["node-role.kubernetes.io/db"] != "true" {
		t.Errorf("nodeSelector 没带出来: %+v", spec)
	}
	tols, _, _ := unstructured.NestedSlice(spec, "tolerations")
	if len(tols) != 2 {
		t.Fatalf("应有 2 条容忍，实际 %d", len(tols))
	}
	first := tols[0].(map[string]any)
	if first["key"] != "dedicated" || first["operator"] != "Equal" || first["effect"] != "NoSchedule" || first["value"] != "db" {
		t.Errorf("Equal 容忍形状不对: %+v", first)
	}
	second := tols[1].(map[string]any)
	if second["operator"] != "Exists" || second["effect"] != "NoExecute" {
		t.Errorf("operator 空应按 Exists: %+v", second)
	}
	if _, has := second["value"]; has {
		t.Errorf("没填的 value 不该下发: %+v", second)
	}
	reqTerms, _, _ := unstructured.NestedSlice(spec, "affinity", "nodeAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	if len(reqTerms) != 1 {
		t.Fatalf("必须有 1 个 required term，实际 %d", len(reqTerms))
	}
	exprs, _, _ := unstructured.NestedSlice(reqTerms[0].(map[string]any), "matchExpressions")
	if len(exprs) != 1 || exprs[0].(map[string]any)["operator"] != "In" {
		t.Errorf("required 表达式不对: %+v", exprs)
	}
	pref, _, _ := unstructured.NestedSlice(spec, "affinity", "nodeAffinity",
		"preferredDuringSchedulingIgnoredDuringExecution")
	if len(pref) != 1 {
		t.Fatalf("应有 1 个 preferred 项，实际 %d", len(pref))
	}
	// Exists 不看值：preferred 的表达式里不该出现 values 键
	pe, _, _ := unstructured.NestedSlice(pref[0].(map[string]any), "preference", "matchExpressions")
	if _, has := pe[0].(map[string]any)["values"]; has {
		t.Errorf("Exists 不该带 values: %+v", pe[0])
	}
	anti, _, _ := unstructured.NestedSlice(spec, "affinity", "podAntiAffinity",
		"preferredDuringSchedulingIgnoredDuringExecution")
	if len(anti) != 1 {
		t.Fatalf("preferred 打散应生成 1 条 podAntiAffinity，实际 %d", len(anti))
	}
	term, _, _ := unstructured.NestedMap(anti[0].(map[string]any), "podAffinityTerm")
	if term["topologyKey"] != "kubernetes.io/hostname" {
		t.Errorf("打散必须按主机拓扑: %+v", term)
	}
	labels, _, _ := unstructured.NestedStringMap(term, "labelSelector", "matchLabels")
	if labels["app.kubernetes.io/instance"] != "mysql-cluster" {
		t.Errorf("反亲和选择器应按实例标签圈定本集群: %+v", labels)
	}

	// required 打散走 requiredDuringScheduling
	s.Spread = "required"
	spec2, err := kbSchedulingSpec(s, "mysql-cluster")
	if err != nil {
		t.Fatalf("required 打散: %v", err)
	}
	if _, ok, _ := unstructured.NestedSlice(spec2, "affinity", "podAntiAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution"); !ok {
		t.Errorf("required 打散应写 requiredDuringScheduling: %+v", spec2)
	}
}

func TestKBSchedulingValidation(t *testing.T) {
	cases := []struct {
		name string
		in   *KBScheduling
		want string
	}{
		{"标签键大写", &KBScheduling{NodeSelector: map[string]string{"DB": "1"}}, "不合法"},
		{"Equal 缺值", &KBScheduling{Tolerations: []KBToleration{{Key: "a", Operator: "Equal"}}}, "必须填 value"},
		{"effect 拼错", &KBScheduling{Tolerations: []KBToleration{{Key: "a", Effect: "NoScheduleee"}}}, "effect"},
		{"operator 拼错", &KBScheduling{Tolerations: []KBToleration{{Key: "a", Operator: "Exist"}}}, "Exists 或 Equal"},
		{"In 缺值", &KBScheduling{NodeRequired: []KBNodeMatch{{Key: "a", Operator: "In"}}}, "至少一个值"},
		{"Exists 多值", &KBScheduling{NodeRequired: []KBNodeMatch{{Key: "a", Operator: "Exists", Values: []string{"x"}}}}, "请清空"},
		{"亲和 operator 拼错", &KBScheduling{NodePreferred: []KBNodeMatch{{Key: "a", Operator: "InA"}}}, "operator 不合法"},
		{"打散方式拼错", &KBScheduling{Spread: "force"}, "preferred 或 required"},
	}
	for _, tc := range cases {
		_, err := kbSchedulingSpec(tc.in, "c1")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 应报「%s」，实际 %v", tc.name, tc.want, err)
		}
	}
}

// 全空就不该下发 schedulingPolicy（看着像配了空策略的集群最容易误导人）
func TestKBSchedulingEmpty(t *testing.T) {
	if !(*KBScheduling)(nil).empty() || !(&KBScheduling{}).empty() {
		t.Error("nil 与零值都应判空")
	}
	if !(&KBScheduling{Tolerations: []KBToleration{}}).empty() {
		t.Error("只有空数组也应判空")
	}
	if (&KBScheduling{Spread: "preferred"}).empty() {
		t.Error("有打散设置不算空")
	}
}

// 向导提交后，调度设置必须出现在下发的 Cluster 对象里
func TestKBCreateClusterAppliesScheduling(t *testing.T) {
	c := kbShardingClient()
	in := &KBCreateInput{Name: "rc", ClusterDef: "redis", Topology: "cluster", TerminationPolicy: "Delete",
		Shardings: []KBShardingInput{{Name: "shard", Shards: 3, Replicas: 2}},
		Scheduling: &KBScheduling{
			Tolerations: []KBToleration{{Key: "dedicated", Value: "db", Operator: "Equal", Effect: "NoSchedule"}},
			Spread:      "required",
		}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "rc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回失败: %v", err)
	}
	tols, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "schedulingPolicy", "tolerations")
	if !ok || len(tols) != 1 {
		t.Fatalf("tolerations 没下发: %+v", obj.Object["spec"])
	}
	if got := tols[0].(map[string]any)["key"]; got != "dedicated" {
		t.Errorf("容忍内容不对: %+v", tols[0])
	}
	if _, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "schedulingPolicy", "affinity",
		"podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution"); !ok {
		t.Errorf("required 打散没生成反亲和: %+v", obj.Object["spec"])
	}
}

// 详情头部的"调度：…"摘要：从集群对象反向读出来，用来确认设置真的落上了
func TestKBSchedulingSummaryOf(t *testing.T) {
	spec, err := kbSchedulingSpec(&KBScheduling{
		NodeSelector: map[string]string{"k1": "v1", "k2": "v2"},
		Tolerations:  []KBToleration{{Key: "dedicated", Value: "db", Operator: "Equal", Effect: "NoSchedule"}},
		Spread:       "required",
	}, "c1")
	if err != nil {
		t.Fatalf("kbSchedulingSpec: %v", err)
	}
	obj := kbUD("c1", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{"schedulingPolicy": spec}})
	got := kbSchedulingSummaryOf(obj)
	for _, want := range []string{"节点标签 k1=v1 k2=v2", "容忍 dedicated=db(NoSchedule)", "必须分散到不同节点"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要应含 %q，实际 %q", want, got)
		}
	}

	// 没配调度 / 配了空对象都不该显示
	plain := kbUD("c2", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{}})
	if s := kbSchedulingSummaryOf(plain); s != "" {
		t.Errorf("没配调度应有空摘要，实际 %q", s)
	}
	empty := kbUD("c3", kbClusterGVR, "Cluster", "app", map[string]any{
		"spec": map[string]any{"schedulingPolicy": map[string]any{}},
	})
	if s := kbSchedulingSummaryOf(empty); s != "" {
		t.Errorf("空 policy 不该出摘要，实际 %q", s)
	}
}

// 没配调度设置时不该在集群对象上留 schedulingPolicy 键
func TestKBCreateClusterWithoutScheduling(t *testing.T) {
	c := kbShardingClient()
	in := &KBCreateInput{Name: "rc", ClusterDef: "redis", Topology: "cluster",
		Shardings: []KBShardingInput{{Name: "shard", Shards: 3, Replicas: 2}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "rc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回失败: %v", err)
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "schedulingPolicy"); found {
		t.Error("没配调度设置却下发了 schedulingPolicy")
	}
}
