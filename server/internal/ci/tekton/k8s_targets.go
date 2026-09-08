package tekton

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NamespaceNames 列出集群全部 namespace（k8s-deploy 属性面板 namespace 下拉）。
func (c *Client) NamespaceNames(ctx context.Context) ([]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Workload 是一个可 set image 的目标：deployment / statefulset / daemonset 及其容器列表。
type Workload struct {
	Kind       string   `json:"kind"` // deployment / statefulset / daemonset
	Name       string   `json:"name"` // workload 名
	Containers []string `json:"containers"`
}

// NamespaceWorkloads 列出指定 namespace 下全部 workload 及其容器
// （k8s-deploy 属性面板 deployment / container 下拉）。
// 权限：ci-server SA 需对集群 workloads 有 get/list
// （deployments/rbac-cluster.yaml 的 ClusterRole 已授）。
func (c *Client) NamespaceWorkloads(ctx context.Context, namespace string) ([]Workload, error) {
	var out []Workload
	// apps/v1 的 resource 名复数约定：deployments / statefulsets / daemonsets
	kinds := []struct{ kind, plural string }{
		{"deployment", "deployments"},
		{"statefulset", "statefulsets"},
		{"daemonset", "daemonsets"},
	}
	for _, k := range kinds {
		gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: k.plural}
		list, err := c.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list %s in %s: %w", k.plural, namespace, err)
		}
		for i := range list.Items {
			it := &list.Items[i]
			w := Workload{Kind: k.kind, Name: it.GetName()}
			// containers 是对象数组（不是 []string），须 NestedSlice 后取 name；
			// 用 NestedStringSlice 会类型断言失败静默返回 nil
			cs, _, _ := unstructured.NestedSlice(it.Object,
				"spec", "template", "spec", "containers")
			w.Containers = []string{}
			for _, c := range cs {
				if cm, ok := c.(map[string]interface{}); ok {
					if name, ok := cm["name"].(string); ok && name != "" {
						w.Containers = append(w.Containers, name)
					}
				}
			}
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
