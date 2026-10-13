package service

import (
	"context"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kube-console/server/internal/kube"
)

// Elasticsearch 的 single-node / mdit 拓扑（单个组件、没有专职 master）必须显式拿到
// 模板变量 mode=single-node，否则 elasticsearch.yml 模板按 multi-node 渲染，
// 从 ALL_CMP_REPLICA_LIST 里找不到 "master:" 段 → 产出 "cluster.initial_master_nodes:"
// 这种有键无值的行，ES 启动即报 null-valued setting 退出（2026-09-29 用户在建 ES 时撞到）。

func kbESCompDef(name string) *unstructured.Unstructured {
	o := map[string]any{"spec": map[string]any{
		"serviceVersion": "8.8.2",
		"replicasLimit":  map[string]any{"min": int64(1), "max": int64(3)},
		"configs":        []any{map[string]any{"name": "es-cm"}},
	}}
	return kbUD(name, kbCompDefGVR, "ComponentDefinition", "", o)
}

func kbESClusterDef() *unstructured.Unstructured {
	return kbUD("elasticsearch", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
		"topologies": []any{
			map[string]any{"name": "single-node", "default": true, "components": []any{
				map[string]any{"name": "mdit", "compDef": "^elasticsearch-"},
			}},
			map[string]any{"name": "multi-node", "components": []any{
				map[string]any{"name": "master", "compDef": "^elasticsearch-master-"},
				map[string]any{"name": "dit", "compDef": "^elasticsearch-data-"},
			}},
		},
	}})
}

func kbESClient() *kube.Client {
	return &kube.Client{Dynamic: kbFakeDyn(
		kbESClusterDef(),
		kbESCompDef("elasticsearch-8-1.0.5"),
		kbESCompDef("elasticsearch-master-8-1.0.5"),
		kbESCompDef("elasticsearch-data-8-1.0.5"),
	)}
}

func kbGetCluster(t *testing.T, c *kube.Client, ns, name string) map[string]any {
	t.Helper()
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回集群失败: %v", err)
	}
	return obj.Object
}

// 单组件形态：注入 configs[].variables.mode，模板才会走单机分支
func TestKBCreateESSingleNodeInjectsMode(t *testing.T) {
	c := kbESClient()
	in := &KBCreateInput{Name: "logs", ClusterDef: "elasticsearch", Topology: "single-node",
		Components: []KBComponentInput{{Name: "mdit", CompDef: "^elasticsearch-", Replicas: 1, Storage: "20Gi"}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(kbGetCluster(t, c, "app", "logs"), "spec", "componentSpecs")
	if len(specs) != 1 {
		t.Fatalf("组件数不对: %+v", specs)
	}
	cfgs, ok, _ := unstructured.NestedSlice(specs[0].(map[string]any), "configs")
	if !ok || len(cfgs) != 1 {
		t.Fatalf("没注入 configs: %+v", specs[0])
	}
	cm := cfgs[0].(map[string]any)
	if cm["name"] != "es-cm" {
		t.Errorf("配置项名要取模板自己声明的那个（addon 写的是 es-cm）: %+v", cm)
	}
	vars, ok, _ := unstructured.NestedStringMap(cm, "variables")
	if !ok || vars["mode"] != "single-node" {
		t.Errorf("variables.mode 没落上: %+v", cm)
	}
}

// 多副本在单机模式下是"3 个互不相干的单节点集群"，必须在提交前就拒
func TestKBCreateESSingleNodeRejectsMultiReplica(t *testing.T) {
	c := kbESClient()
	in := &KBCreateInput{Name: "logs", ClusterDef: "elasticsearch", Topology: "single-node",
		Components: []KBComponentInput{{Name: "mdit", CompDef: "^elasticsearch-", Replicas: 3}}}
	err := KBCreateCluster(context.Background(), c, "app", in)
	if err == nil || !strings.Contains(err.Error(), "只能是 1") {
		t.Fatalf("多副本应被拒并说明原因: %v", err)
	}
	if _, e := c.Dynamic.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "logs", metav1.GetOptions{}); e == nil {
		t.Error("报错前就该拦住：集群对象不该已经建出来")
	}
}

// multi-node（master + dit）由模板自己算主节点列表，不该被注入单机开关
func TestKBCreateESMultiNodeNotInjected(t *testing.T) {
	c := kbESClient()
	in := &KBCreateInput{Name: "es-ha", ClusterDef: "elasticsearch", Topology: "multi-node",
		Components: []KBComponentInput{
			{Name: "master", CompDef: "^elasticsearch-master-", Replicas: 3},
			{Name: "dit", CompDef: "^elasticsearch-data-", Replicas: 3},
		}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(kbGetCluster(t, c, "app", "es-ha"), "spec", "componentSpecs")
	for _, s := range specs {
		sm, _ := s.(map[string]any)
		if _, ok, _ := unstructured.NestedSlice(sm, "configs"); ok {
			t.Errorf("multi-node 的组件 %v 被注入了 configs（会把集群降级成单机）: %+v", sm["name"], sm)
		}
	}
}

// 多组件但没有单组件形态（真机 addon 的 m-d-i-t 拓扑：m/d/i/t 四个组件）不注入：
// 把单机开关塞给四个角色会得到四台互不相干的单节点，比报错更糟
func TestKBCreateESMultiComponentNoMasterNotInjected(t *testing.T) {
	c := &kube.Client{Dynamic: kbFakeDyn(
		kbUD("elasticsearch", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
			"topologies": []any{map[string]any{"name": "m-d-i-t", "default": true, "components": []any{
				map[string]any{"name": "m", "compDef": "^elasticsearch-master-"},
				map[string]any{"name": "d", "compDef": "^elasticsearch-data-"},
			}}},
		}}),
		kbESCompDef("elasticsearch-master-8-1.0.5"),
		kbESCompDef("elasticsearch-data-8-1.0.5"),
	)}
	in := &KBCreateInput{Name: "es-mdit", ClusterDef: "elasticsearch", Topology: "m-d-i-t",
		Components: []KBComponentInput{
			{Name: "m", CompDef: "^elasticsearch-master-", Replicas: 3},
			{Name: "d", CompDef: "^elasticsearch-data-", Replicas: 3},
		}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(kbGetCluster(t, c, "app", "es-mdit"), "spec", "componentSpecs")
	if len(specs) != 2 {
		t.Fatalf("组件数不对: %+v", specs)
	}
	for _, s := range specs {
		sm, _ := s.(map[string]any)
		if _, ok, _ := unstructured.NestedSlice(sm, "configs"); ok {
			t.Errorf("多组件形态 %v 不该被注入单机开关: %+v", sm["name"], sm)
		}
	}
}

// 非 ES 引擎不受影响（mysql 的单组件形态不该被注入 mode）
func TestKBCreateMySQLUnaffectedByESMode(t *testing.T) {
	c := &kube.Client{Dynamic: kbFakeDyn(
		kbClusterDef("mysql", "replica", "mysql", `^mysql-\d+`),
		kbCompDef("mysql-8.0-1.0.2", "8.0.33"),
	)}
	in := &KBCreateInput{Name: "db", ClusterDef: "mysql", Topology: "replica",
		Components: []KBComponentInput{{Name: "mysql", CompDef: "^mysql-", Replicas: 2, Storage: "20Gi"}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(kbGetCluster(t, c, "app", "db"), "spec", "componentSpecs")
	sm, _ := specs[0].(map[string]any)
	if _, ok, _ := unstructured.NestedSlice(sm, "configs"); ok {
		t.Errorf("mysql 不该被注入 ES 的 configs: %+v", sm)
	}
	if _, ok, _ := unstructured.NestedSlice(sm, "volumeClaimTemplates"); !ok {
		t.Error("mysql 的存储下发不能被 ES 那段逻辑带跑")
	}
}

// 模板没声明配置项名时不猜：明确拒，并告诉用户改走 multi-node
func TestKBCreateESWithoutConfigName(t *testing.T) {
	c := &kube.Client{Dynamic: kbFakeDyn(
		kbESClusterDef(),
		kbUD("elasticsearch-8-1.0.5", kbCompDefGVR, "ComponentDefinition", "", map[string]any{"spec": map[string]any{
			"serviceVersion": "8.8.2",
		}}),
	)}
	in := &KBCreateInput{Name: "logs", ClusterDef: "elasticsearch", Topology: "single-node",
		Components: []KBComponentInput{{Name: "mdit", CompDef: "^elasticsearch-", Replicas: 1}}}
	err := KBCreateCluster(context.Background(), c, "app", in)
	if err == nil || !strings.Contains(err.Error(), "multi-node") {
		t.Fatalf("应明确拒绝并给出路: %v", err)
	}
}

// 真机核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveESMode
// 认两件事：addon 的配置项名（我们靠它挂 variables，改名了这里先响），以及
// ES 的 ClusterDefinition 拓扑确实没有 mode 变量的兜底（那才需要控制台注入）。
func TestKBEsModeLiveCheck(t *testing.T) {
	path := os.Getenv("KC_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("未设 KC_TEST_KUBECONFIG，跳过真实集群核对")
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 kubeconfig: %v", err)
	}
	c, err := kube.NewClient(string(bs))
	if err != nil {
		t.Skipf("连不上集群: %v", err)
	}
	idx := kbLoadCompDefs(context.Background(), c)
	ctx := context.Background()
	cd, err := c.Dynamic.Resource(kbClusterDefGVR).Get(ctx, "elasticsearch", metav1.GetOptions{})
	if err != nil {
		t.Skipf("该集群没装 elasticsearch ClusterDefinition: %v", err)
	}
	singleComps := 0
	for _, raw := range topologiesOf(cd) {
		tp, _ := raw.(map[string]any)
		if name, _ := tp["name"].(string); name != "single-node" {
			continue
		}
		comps, _, _ := unstructured.NestedSlice(tp, "components")
		singleComps = len(comps)
		if vars, ok, _ := unstructured.NestedSlice(tp, "vars"); ok && len(vars) > 0 {
			t.Errorf("single-node 拓扑已经自带 vars 兜底了，控制台的注入要跟着退让: %+v", vars)
		}
	}
	if singleComps == 0 {
		t.Skip("该 addon 的 elasticsearch ClusterDefinition 没有 single-node 拓扑")
	}
	if singleComps != 1 {
		t.Errorf("single-node 拓扑现在是 %d 个组件：控制台的注入条件（单组件形态）不再成立，集群会回到那个空 initial_master_nodes 的坑", singleComps)
	}
	for _, probe := range []string{"^elasticsearch-8-", "^elasticsearch-\\d+-"} {
		e, err := kbPickCompDef(idx, probe, "")
		if err != nil {
			continue
		}
		if n := kbFirstConfigName(e.obj); n == "" {
			t.Errorf("模板 %s 读不到配置项名（spec.configs[].name 改了？注入会失败）", e.name)
		} else {
			t.Logf("模板 %s 的配置项名 = %s", e.name, n)
		}
		break
	}
}

// topologiesOf 取 ClusterDefinition 的拓扑列表（live 检查用）
func topologiesOf(cd *unstructured.Unstructured) []any {
	tps, _, _ := unstructured.NestedSlice(cd.Object, "spec", "topologies")
	return tps
}
