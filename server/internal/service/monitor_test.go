package service

import (
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
