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

// mongos 类模板（data 由 spec.runtime.volumes 自带 emptyDir）不能再下发同名
// volumeClaimTemplates：KB 合成 InstanceSet 时报 "duplicated volume data"，
// 集群永远停在 Creating 且一个 Pod 都没有（2026-09-29 用户在 kube-public/mongodb 上撞到）。
// 这里锁三件事：能认出来、目录里能给前端禁用那一格、下发前就报错而不是建出卡死的集群。

func kbMongosCompDef(name string) *unstructured.Unstructured {
	return kbUD(name, kbCompDefGVR, "ComponentDefinition", "", map[string]any{"spec": map[string]any{
		"serviceVersion": "8.0.17",
		"replicasLimit":  map[string]any{"min": int64(1), "max": int64(3)},
		"runtime": map[string]any{"volumes": []any{
			map[string]any{"name": "data", "emptyDir": map[string]any{}},
		}},
	}})
}

func kbMongoClusterDef() *unstructured.Unstructured {
	return kbUD("mongodb", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
		"topologies": []any{map[string]any{
			"name":    "sharding",
			"default": true,
			"components": []any{
				map[string]any{"name": "mongos", "compDef": "^mongo-mongos-"},
				map[string]any{"name": "config-server", "compDef": "^mongo-config-server-"},
			},
		}},
	}})
}

func kbMongoClient() *kube.Client {
	return &kube.Client{Dynamic: kbFakeDyn(
		kbMongoClusterDef(),
		kbMongosCompDef("mongo-mongos-1.0.2"),
		kbCompDef("mongo-config-server-1.0.2", "8.0.17"),
	)}
}

func TestKBOwnsVolumeNameOnlyCountsPodSpecVolume(t *testing.T) {
	if !kbOwnsVolumeName(kbMongosCompDef("x"), "data") {
		t.Error("spec.runtime.volumes 里的 data 要认出来（mongos 就是这一种）")
	}
	// spec.volumes 是"这组件需要哪些 PVC"的声明，mysql/pg/redis 的 data 都写在那儿——
	// 把它当"自带卷"会让该建盘的组件建不出盘
	specVols := kbUD("y", kbCompDefGVR, "ComponentDefinition", "", map[string]any{"spec": map[string]any{
		"volumes": []any{map[string]any{"name": "data"}},
	}})
	if kbOwnsVolumeName(specVols, "data") {
		t.Error("spec.volumes 的 data 是要求下发 PVC，不是自带卷")
	}
	if kbOwnsVolumeName(kbCompDef("mongo-config-server-1.0.2", "8.0.17"), "data") {
		t.Error("config-server 没自带 data 卷，不该判成自带")
	}
	if kbOwnsVolumeName(nil, "data") || kbOwnsVolumeName(kbMongosCompDef("x"), "") {
		t.Error("空对象 / 空卷名不该判成自带")
	}
	// 别的卷名（dshm、log、bin 之类）不能误伤：那些与我们的 data 不冲突
	esLike := kbUD("es", kbCompDefGVR, "ComponentDefinition", "", map[string]any{"spec": map[string]any{
		"runtime": map[string]any{"volumes": []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}}},
	}})
	if kbOwnsVolumeName(esLike, "data") {
		t.Error("只有 data 才算冲突，log 之类的自带卷不该挡下发")
	}
}

// 目录要把这个信息带给前端，向导才能把那一格禁用（而不是让用户填了再被他报错）
func TestKBEnginesMarksStatelessComponent(t *testing.T) {
	engines, err := KBEngines(context.Background(), kbMongoClient())
	if err != nil {
		t.Fatalf("KBEngines: %v", err)
	}
	var mongos, cs *KBTemplateComponent
	for _, tc := range engines[0].Topologies[0].Components {
		c := tc
		switch c.Name {
		case "mongos":
			mongos = &c
		case "config-server":
			cs = &c
		}
	}
	if mongos == nil || cs == nil {
		t.Fatalf("目录里没给出两个组件: %+v", engines[0].Topologies[0].Components)
	}
	if !mongos.OwnsDataVolume {
		t.Error("mongos 默认模板该标成自带 data 卷")
	}
	if len(mongos.CompDefs) == 0 || !mongos.CompDefs[0].OwnsDataVolume {
		t.Errorf("候选模板也要带这个标记（用户可能换模板）: %+v", mongos.CompDefs)
	}
	if cs.OwnsDataVolume || (len(cs.CompDefs) > 0 && cs.CompDefs[0].OwnsDataVolume) {
		t.Error("config-server 需要 PVC，不该被标成自带卷")
	}
}

// 提交前就挡住，并且真的没建出集群
func TestKBCreateRejectsStorageOnTemplateOwnedVolume(t *testing.T) {
	c := kbMongoClient()
	in := &KBCreateInput{Name: "mongo", ClusterDef: "mongodb", Topology: "sharding", TerminationPolicy: "Delete",
		Components: []KBComponentInput{
			{Name: "mongos", CompDef: "^mongo-mongos-", Replicas: 1, Storage: "10Gi"},
			{Name: "config-server", CompDef: "^mongo-config-server-", Replicas: 1, Storage: "10Gi"},
		}}
	err := KBCreateCluster(context.Background(), c, "kube-public", in)
	if err == nil {
		t.Fatal("mongos 带存储应该报错，实际建成功了")
	}
	if !strings.Contains(err.Error(), "mongos") || !strings.Contains(err.Error(), "留空") {
		t.Errorf("报错要指名是哪个组件、该怎么改: %v", err)
	}
	if _, e := c.Dynamic.Resource(kbClusterGVR).Namespace("kube-public").Get(context.Background(), "mongo", metav1.GetOptions{}); e == nil {
		t.Error("报错前就该拦住：集群对象不该已经建出来")
	}

	// mongos 存储留空就能建，且只有 config-server 下发 volumeClaimTemplates
	in.Components[0].Storage = ""
	if err := KBCreateCluster(context.Background(), c, "kube-public", in); err != nil {
		t.Fatalf("留空后应该能建: %v", err)
	}
	obj, err := c.Dynamic.Resource(kbClusterGVR).Namespace("kube-public").Get(context.Background(), "mongo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回失败: %v", err)
	}
	specs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "componentSpecs")
	if len(specs) != 2 {
		t.Fatalf("组件数不对: %+v", specs)
	}
	for _, s := range specs {
		sm, _ := s.(map[string]any)
		name, _ := sm["name"].(string)
		vct, _, _ := unstructured.NestedSlice(sm, "volumeClaimTemplates")
		switch name {
		case "mongos":
			if len(vct) != 0 {
				t.Errorf("mongos 不该下发 volumeClaimTemplates: %+v", vct)
			}
		case "config-server":
			if len(vct) != 1 {
				t.Errorf("config-server 该有一块 data 盘: %+v", vct)
			}
		}
	}
}

// 分片组同理：mongo 的 shard 若哪天改成模板自带 data，也不能让它带着存储下发
func TestKBShardingRejectsStorageOnTemplateOwnedVolume(t *testing.T) {
	c := &kube.Client{Dynamic: kbFakeDyn(
		kbUD("mongo", kbClusterDefGVR, "ClusterDefinition", "", map[string]any{"spec": map[string]any{
			"topologies": []any{map[string]any{"name": "sharding", "default": true,
				"shardings": []any{map[string]any{"name": "shard", "shardingDef": "^mongo-shard-"}}}},
		}}),
		kbShardingDefObj("mongo-shard-1.0.2", "^mongo-shard-", 1, 8),
		kbMongosCompDef("mongo-shard-1.0.2"), // 故意用一个自带 data 卷的模板
	)}
	in := &KBCreateInput{Name: "mongo", ClusterDef: "mongo", Topology: "sharding",
		Shardings: []KBShardingInput{{Name: "shard", ShardingDef: "^mongo-shard-", Shards: 1, Replicas: 2, Storage: "50Gi"}}}
	err := KBCreateCluster(context.Background(), c, "app", in)
	if err == nil || !strings.Contains(err.Error(), "分片组 shard") {
		t.Fatalf("分片组带同名卷应报错并指名分片组: %v", err)
	}
}

// 真机核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveOwnVolume
// 认的是 addon 声明卷的位置（自带卷在 spec.runtime.volumes，要求下发的 PVC 在 spec.volumes）——
// KB 或 addon 改了字段路径，上面那套判断就会静默失效，这里先响。
func TestKBOwnVolumeLiveCheck(t *testing.T) {
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
	mongos, err := kbPickCompDef(idx, "^mongo-mongos-", "")
	if err != nil {
		t.Skipf("该集群没装 mongodb sharding 的 mongos 模板: %v", err)
	}
	if !kbOwnsVolumeName(mongos.obj, "data") {
		t.Errorf("模板 %s 的 data 卷没从已知字段路径认出来（addon 结构变了要跟着改 kbOwnsVolumeName）", mongos.name)
	}
	cs, err := kbPickCompDef(idx, "^mongo-config-server-", "")
	if err == nil && kbOwnsVolumeName(cs.obj, "data") {
		t.Errorf("config-server %s 被判成自带 data 卷，会导致它建不出盘", cs.name)
	}
	// 反向：需要 PVC 的模板（mysql / postgresql / redis / config-server）绝不能被判成自带卷，
	// 否则它们的盘就不下发了。它们的 data 声明在 spec.volumes（不是 runtime），
	// 这条断言就是守住"只看 runtime"这个口径。
	for _, probe := range []string{"^mysql-8\\.0-", "^postgresql-16-", "^redis-8-", "^mongo-config-server-"} {
		e, err := kbPickCompDef(idx, probe, "")
		if err != nil {
			continue // 该引擎没装就跳过，别让 live 测试因为环境差异而假失败
		}
		if kbOwnsVolumeName(e.obj, "data") {
			t.Errorf("%s 被判成自带 data 卷：它会拿不到 PVC（spec.volumes 的 data 不该算）", e.name)
		}
	}
	// 反向：全集群扫一遍，凡是自带 data 的模板都打出来，便于发现新增的无状态组件
	list, err := c.Dynamic.Resource(kbCompDefGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Skipf("列 ComponentDefinition 失败: %v", err)
	}
	own := []string{}
	for i := range list.Items {
		if kbOwnsVolumeName(&list.Items[i], "data") {
			own = append(own, list.Items[i].GetName())
		}
	}
	t.Logf("自带 data 卷的模板：%v", own)
}
