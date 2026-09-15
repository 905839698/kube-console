package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func promRuleCR(ns, name string, groups []interface{}) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PrometheusRule",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"groups": groups},
	}}
}

func TestPromRuleSources(t *testing.T) {
	items := []unstructured.Unstructured{
		promRuleCR("monitor", "kube-prom", []interface{}{
			map[string]interface{}{
				"name": "kube-system",
				"rules": []interface{}{
					map[string]interface{}{"alert": "KubeAPIServerDown", "expr": "up == 0"},
					map[string]interface{}{"record": "node:cpu:avg", "expr": "avg(x)"}, // 记录规则应被忽略
				},
			},
			map[string]interface{}{
				"name": "kubeadm",
				"rules": []interface{}{
					map[string]interface{}{"alert": "KubeadmControlPlaneNotHealthy"},
				},
			},
		}),
		// 同组名的另一个 CR：先出现者胜出
		promRuleCR("monitor", "other", []interface{}{
			map[string]interface{}{
				"name": "kubeadm",
				"rules": []interface{}{
					map[string]interface{}{"alert": "KubeadmControlPlaneNotHealthy", "expr": "y"},
					map[string]interface{}{"alert": "OnlyInOther"},
				},
			},
		}),
	}

	idx := promRuleSources(items)
	if got := idx["kube-system"]["KubeAPIServerDown"]; got != "monitor/kube-prom" {
		t.Errorf("KubeAPIServerDown 源 = %q，期望 monitor/kube-prom", got)
	}
	if _, ok := idx["kube-system"]["node:cpu:avg"]; ok {
		t.Error("记录型规则（无 alert 字段）不应进入索引")
	}
	if got := idx["kubeadm"]["KubeadmControlPlaneNotHealthy"]; got != "monitor/kube-prom" {
		t.Errorf("重名分组应取先出现的 CR，实际 = %q", got)
	}
	if got := idx["kubeadm"]["OnlyInOther"]; got != "monitor/other" {
		t.Errorf("OnlyInOther 源 = %q，期望 monitor/other", got)
	}
	if _, ok := idx["not-exist"]; ok {
		t.Error("不存在的分组不应有索引项")
	}
}

func TestPromRuleSourcesEmpty(t *testing.T) {
	idx := promRuleSources(nil)
	if len(idx) != 0 {
		t.Errorf("空列表应得到空索引，实际 %d 项", len(idx))
	}
}

// sanitizeProm：无 swap 节点的 swap 指标（0/0 → "NaN"）等 NaN/Inf 样本必须剔除，
// 否则 encoding/json 无法序列化 NaN 导致整个监控接口 500
func TestSanitizePromRemovesNaNSamples(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"__name__":"cpu"},"value":[1757500000,"12.3"]},
		{"metric":{"__name__":"swap"},"value":[1757500000,"NaN"]},
		{"metric":{"__name__":"disk"},"value":[1757500000,"+Inf"]}
	]}}`
	var pr promResponse
	if err := json.Unmarshal([]byte(body), &pr); err != nil {
		t.Fatal(err)
	}
	sanitizeProm(&pr)
	if len(pr.Data.Result) != 1 || pr.Data.Result[0].Metric["__name__"] != "cpu" {
		t.Fatalf("NaN/Inf 瞬时样本应整条剔除，剩余 %d 条", len(pr.Data.Result))
	}

	matrix := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"nodename":"n1"},"values":[[1757500000,"1"],[1757500100,"NaN"],[1757500200,"+Inf"],[1757500300,"3"]]}
	]}}`
	var pm promResponse
	if err := json.Unmarshal([]byte(matrix), &pm); err != nil {
		t.Fatal(err)
	}
	sanitizeProm(&pm)
	got := pm.Data.Result[0].Values
	if len(got) != 2 || fmt.Sprint(got[0][1]) != "1" || fmt.Sprint(got[1][1]) != "3" {
		t.Fatalf("NaN/Inf 区间点应逐点剔除，实际 %v", got)
	}

	// 全 NaN 的序列应整条消失（避免前端拿到空 values 的残缺 series）
	allNaN := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"nodename":"n1"},"values":[[1757500000,"NaN"]]}
	]}}`
	var pa promResponse
	_ = json.Unmarshal([]byte(allNaN), &pa)
	sanitizeProm(&pa)
	if len(pa.Data.Result) != 0 {
		t.Fatalf("全 NaN 序列应整条剔除，剩余 %d 条", len(pa.Data.Result))
	}
}

// nodeAgg：聚合必须先 by(instance) 再 join（裸 sum 剥掉 instance 标签导致 on(instance) 恒为空），
// 外层按 nodename 汇总，节点过滤落到 node_uname_info
func TestNodeAggShape(t *testing.T) {
	got := nodeAgg(`sum by(instance)(rate(node_disk_reads_completed_total[5m]))`, "sum", "node1.example.com")
	want := `sum by(nodename)((sum by(instance)(rate(node_disk_reads_completed_total[5m]))) * on(instance) group_left(nodename) node_uname_info{nodename="node1.example.com"})`
	if got != want {
		t.Fatalf("nodeAgg 生成不符：\n got: %s\nwant: %s", got, want)
	}
	// 内层聚合缺 by(instance) 属于历史 bug（剥标签 → join 恒空），这里守住形状
	if !contains(got, "sum by(instance)") || !contains(got, "on(instance)") || !contains(got, `nodename="node1.example.com"`) {
		t.Fatalf("缺少 by(instance)/on(instance)/节点过滤要素: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
