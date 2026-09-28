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

// redis 的 cluster 拓扑 / mongodb 的 sharding 拓扑不写 components，而是写
// shardings[{name, shardingDef}]，模板在 ShardingDefinition 里（compDef 同样是正则）。
// 早先目录只读 components，这类拓扑显示成"没有组件"，前端新建向导因此一片空白。
func kbRedisClusterDef() *unstructured.Unstructured {
	return kbUD("redis", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
		"topologies": []any{
			map[string]any{"name": "standalone", "default": true, "components": []any{
				map[string]any{"name": "redis", "compDef": `^redis-\d+`},
			}},
			map[string]any{"name": "cluster", "shardings": []any{
				map[string]any{"name": "shard", "shardingDef": "redis-cluster"},
			}},
		},
	}})
}

func kbShardingDefObj(name, compDefPattern string, minS, maxS int64) *unstructured.Unstructured {
	return kbUD(name, kbShardingDefGVR, "ShardingDefinition", "", map[string]any{"spec": map[string]any{
		"shardsLimit": map[string]any{"minShards": minS, "maxShards": maxS},
		"template":    map[string]any{"compDef": compDefPattern},
	}})
}

func kbShardingClient() *kube.Client {
	return &kube.Client{Dynamic: kbFakeDyn(
		kbRedisClusterDef(),
		kbShardingDefObj("redis-cluster", `^redis-cluster-\d+`, 3, 64),
		kbCompDef("redis-7-1.0.2", "7.2.11"),
		kbCompDef("redis-8-1.0.2", "8.2.2"),
		kbCompDef("redis-cluster-7-1.0.2", "7.2.11"),
		kbCompDef("redis-cluster-8-1.0.2", "8.2.2"),
	)}
}

// 目录要把分片拓扑的分片数范围与解析后的模板一起给出去
func TestKBEnginesExposesShardingTopology(t *testing.T) {
	engines, err := KBEngines(context.Background(), kbShardingClient())
	if err != nil {
		t.Fatalf("KBEngines: %v", err)
	}
	if len(engines) != 1 {
		t.Fatalf("引擎数不对: %+v", engines)
	}
	var cluster *KBTopology
	for i := range engines[0].Topologies {
		if engines[0].Topologies[i].Name == "cluster" {
			cluster = &engines[0].Topologies[i]
		}
	}
	if cluster == nil {
		t.Fatalf("没拿到 cluster 拓扑: %+v", engines[0].Topologies)
	}
	if len(cluster.Shardings) != 1 {
		t.Fatalf("应有 1 个分片组，实际 %d", len(cluster.Shardings))
	}
	sh := cluster.Shardings[0]
	if sh.Name != "shard" || sh.ShardingDef != "redis-cluster" {
		t.Errorf("分片组不对: %+v", sh)
	}
	if sh.ShardsMin != 3 || sh.ShardsMax != 64 {
		t.Errorf("分片数范围应为 3-64，实际 %d-%d", sh.ShardsMin, sh.ShardsMax)
	}
	if len(sh.Components) != 1 || sh.Components[0].CompDef != "redis-cluster-8-1.0.2" {
		t.Fatalf("分片模板应默认取最高版本: %+v", sh.Components)
	}
	if got := len(sh.Components[0].CompDefs); got != 2 {
		t.Errorf("候选应有 2 个 redis-cluster 模板，实际 %d", got)
	}
	for _, tp := range engines[0].Topologies {
		if tp.Name == "standalone" && len(tp.Components) != 1 {
			t.Errorf("普通拓扑组件受影响: %+v", tp)
		}
	}
}

func TestKBCreateClusterWithShardings(t *testing.T) {
	c := kbShardingClient()
	in := &KBCreateInput{Name: "rc", ClusterDef: "redis", Topology: "cluster", TerminationPolicy: "Delete",
		Shardings: []KBShardingInput{{Name: "shard", Shards: 3, Replicas: 2, Storage: "5Gi", CPULimit: "1", MemLimit: "2Gi"}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "rc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回失败: %v", err)
	}
	if _, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs"); ok {
		t.Error("分片拓扑不该同时下发 componentSpecs")
	}
	shs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "shardings")
	if len(shs) != 1 {
		t.Fatalf("spec.shardings = %+v", shs)
	}
	item := shs[0].(map[string]any)
	if item["name"] != "shard" || item["shardingDef"] != "redis-cluster" || item["shards"] != int64(3) {
		t.Errorf("分片组头不对: %+v", item)
	}
	tpl := item["template"].(map[string]any)
	if tpl["componentDef"] != "redis-cluster-8-1.0.2" || tpl["replicas"] != int64(2) || tpl["serviceVersion"] != "8.2.2" {
		t.Errorf("模板不对: %+v", tpl)
	}
	if _, ok := tpl["volumeClaimTemplates"]; !ok {
		t.Error("存储没带进 volumeClaimTemplates")
	}
	if _, ok := tpl["resources"]; !ok {
		t.Error("资源限制没带进 template.resources")
	}
}

// 分片数越界（实测 dryRun 传 shards=2 而 minShards=3 时 apiserver 也不拦）
// 与模板不属于该分片定义，都要在这里拦下
func TestKBCreateClusterShardingValidation(t *testing.T) {
	c := kbShardingClient()
	cases := []struct {
		name string
		in   KBShardingInput
		want string
	}{
		{"分片数低于下限", KBShardingInput{Name: "shard", Shards: 2, Replicas: 2}, "至少 3"},
		{"分片数超上限", KBShardingInput{Name: "shard", Shards: 99, Replicas: 2}, "最多 64"},
		{"模板不属于该分片", KBShardingInput{Name: "shard", Shards: 3, Replicas: 2, CompDef: "redis-8-1.0.2"}, "不属于"},
		{"分片组名非法", KBShardingInput{Name: "Sh ard", Shards: 3, Replicas: 2}, "不合法"},
		{"未知分片组", KBShardingInput{Name: "other", Shards: 3, Replicas: 2}, "ShardingDefinition"},
	}
	for _, tc := range cases {
		in := &KBCreateInput{Name: "rc", ClusterDef: "redis", Topology: "cluster", Shardings: []KBShardingInput{tc.in}}
		err := KBCreateCluster(context.Background(), c, "app", in)
		if err == nil {
			t.Errorf("%s: 应报错", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 报错里应含 %q，实际 %v", tc.name, tc.want, err)
		}
	}
}

// components 与 shardings 都不给时必须拒（否则会下发一个没有组件的集群）
func TestKBCreateClusterNeedsComponentsOrShardings(t *testing.T) {
	err := KBCreateCluster(context.Background(), kbShardingClient(), "app", &KBCreateInput{Name: "x", ClusterDef: "redis"})
	if err == nil || !strings.Contains(err.Error(), "至少需要一个组件") {
		t.Errorf("应提示至少一个组件，实际 %v", err)
	}
}

// 请求里没写 shardingDef 时，要从 ClusterDefinition 的拓扑兜底找出来
func TestKBCreateClusterInfersShardingDef(t *testing.T) {
	c := kbShardingClient()
	in := &KBCreateInput{Name: "rc2", ClusterDef: "redis", Topology: "cluster",
		Shardings: []KBShardingInput{{Name: "shard", Shards: 3, Replicas: 3}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("应能推断 shardingDef: %v", err)
	}
	obj, _ := c.Dynamic.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "rc2", metav1.GetOptions{})
	shs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "shardings")
	if len(shs) != 1 || shs[0].(map[string]any)["shardingDef"] != "redis-cluster" {
		t.Errorf("shardingDef 没推断出来: %+v", shs)
	}
}

// 真实集群核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveSharding
// 断言 redis 的 cluster 拓扑能拿到分片定义与解析后的模板——addon 换了写法这里会先响。
func TestKBEnginesLiveSharding(t *testing.T) {
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
	engines, err := KBEngines(context.Background(), c)
	if err != nil {
		t.Fatalf("KBEngines: %v", err)
	}
	for _, e := range engines {
		for _, tp := range e.Topologies {
			for _, sh := range tp.Shardings {
				if sh.ShardsMin < 1 {
					t.Errorf("%s/%s 分片组 %s 没有下限", e.Name, tp.Name, sh.Name)
				}
				if len(sh.Components) == 0 || sh.Components[0].CompDef == "" {
					t.Errorf("%s/%s 分片组 %s 没解析出组件模板（ShardingDefinition 缺失？）", e.Name, tp.Name, sh.Name)
					continue
				}
				if !kbCompDefNameRe.MatchString(sh.Components[0].CompDef) {
					t.Errorf("%s/%s 分片模板还是模式: %s", e.Name, tp.Name, sh.Components[0].CompDef)
				}
				t.Logf("OK %s/%s 分片组 %s: %d-%d 片，模板 %s v%s（候选 %d）", e.Name, tp.Name, sh.Name,
					sh.ShardsMin, sh.ShardsMax, sh.Components[0].CompDef, sh.Components[0].ServiceVersion, len(sh.Components[0].CompDefs))
			}
		}
	}
}

// mongodb 的拓扑把 shardingDef 写成前缀 "mongo-shard-"（真实对象是 mongo-shard-1.0.2），
// 与 compDef 同口径要能解析；否则分片模板永远查不到，创建时只能说"不知道用哪个分片定义"
func TestKBShardingDefPrefixResolves(t *testing.T) {
	c := &kube.Client{Dynamic: kbFakeDyn(
		kbUD("mongodb", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
			"topologies": []any{map[string]any{"name": "sharding", "shardings": []any{
				map[string]any{"name": "shard", "shardingDef": "mongo-shard-"},
			}}},
		}}),
		kbShardingDefObj("mongo-shard-1.0.2", "mongo-shard-1.0.2", 1, 128),
		kbShardingDefObj("mongo-shard-1.0.5", "mongo-shard-1.0.5", 1, 128),
		kbCompDef("mongo-shard-1.0.2", "8.0.17"),
		kbCompDef("mongo-shard-1.0.5", "8.0.18"),
	)}
	engines, err := KBEngines(context.Background(), c)
	if err != nil {
		t.Fatalf("KBEngines: %v", err)
	}
	sh := engines[0].Topologies[0].Shardings[0]
	if sh.ShardingDef != "mongo-shard-1.0.5" || sh.ShardingDefPattern != "mongo-shard-" {
		t.Errorf("应解析到版本最高的分片定义，实际 %+v", sh)
	}
	if len(sh.Components) != 1 || sh.Components[0].CompDef != "mongo-shard-1.0.5" {
		t.Errorf("分片模板没解析出来: %+v", sh.Components)
	}
	// 创建时前端只回传解析后的名字，也要能建
	in := &KBCreateInput{Name: "mg", ClusterDef: "mongodb", Topology: "sharding",
		Shardings: []KBShardingInput{{Name: "shard", ShardingDef: sh.ShardingDef, Shards: 3, Replicas: 3}}}
	if err := KBCreateCluster(context.Background(), c, "app", in); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
}
