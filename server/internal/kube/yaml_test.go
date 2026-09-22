package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var (
	clusterrolesGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	deploymentsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func newFakeDyn() *dynamicfake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		clusterrolesGVR:  "ClusterRoleList",
		deploymentsGVR:   "DeploymentList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
}

// 集群级资源不带 namespace：必须落在集群级路径上，
// 旧实现强塞 default 会打到 /namespaces/default/clusterroles/... 真实集群返回 404
func TestApplyYAMLClearScoped(t *testing.T) {
	dyn := newFakeDyn()
	ctx := context.Background()
	yamlStr := `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus-k8s
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, watch]
`
	created, err := ApplyYAML(ctx, dyn, yamlStr)
	if err != nil {
		t.Fatalf("ApplyYAML: %v", err)
	}
	if !created {
		t.Fatal("want created=true")
	}
	got, err := dyn.Resource(clusterrolesGVR).Get(ctx, "prometheus-k8s", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("集群级路径读不到对象（可能被误存到命名空间下）: %v", err)
	}
	if got.GetNamespace() != "" {
		t.Errorf("集群级对象不应带 namespace，got %q", got.GetNamespace())
	}

	// 二次提交走更新路径
	created, err = ApplyYAML(ctx, dyn, yamlStr)
	if err != nil {
		t.Fatalf("第二次 ApplyYAML: %v", err)
	}
	if created {
		t.Error("已存在时应返回 created=false")
	}
}

// 命名空间资源不带 namespace：维持 default 兜底
func TestApplyYAMLNamespacedDefault(t *testing.T) {
	dyn := newFakeDyn()
	ctx := context.Background()
	yamlStr := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: d1
spec:
  replicas: 1
`
	if _, err := ApplyYAML(ctx, dyn, yamlStr); err != nil {
		t.Fatalf("ApplyYAML: %v", err)
	}
	got, err := dyn.Resource(deploymentsGVR).Namespace("default").Get(ctx, "d1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("default 命名空间读不到对象: %v", err)
	}
	if got.GetNamespace() != "default" {
		t.Errorf("want namespace=default, got %q", got.GetNamespace())
	}
}

// 命名空间资源显式指定 namespace：不被 default 覆盖
func TestApplyYAMLNamespacedExplicit(t *testing.T) {
	dyn := newFakeDyn()
	ctx := context.Background()
	yamlStr := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: d1
  namespace: prod
spec:
  replicas: 1
`
	if _, err := ApplyYAML(ctx, dyn, yamlStr); err != nil {
		t.Fatalf("ApplyYAML: %v", err)
	}
	if _, err := dyn.Resource(deploymentsGVR).Namespace("prod").Get(ctx, "d1", metav1.GetOptions{}); err != nil {
		t.Fatalf("prod 命名空间读不到对象: %v", err)
	}
	if _, err := dyn.Resource(deploymentsGVR).Namespace("default").Get(ctx, "d1", metav1.GetOptions{}); err == nil {
		t.Error("对象不应同时出现在 default 命名空间")
	}
}

// 集群级 YAML 中即使误带 metadata.namespace 也应忽略（集群级路径无 ns 语义）
func TestApplyYAMLClusterScopedIgnoresNamespaceField(t *testing.T) {
	dyn := newFakeDyn()
	ctx := context.Background()
	yamlStr := `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cr
  namespace: default
rules: []
`
	if _, err := ApplyYAML(ctx, dyn, yamlStr); err != nil {
		t.Fatalf("ApplyYAML: %v", err)
	}
	got, err := dyn.Resource(clusterrolesGVR).Get(ctx, "cr", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("集群级路径读不到对象: %v", err)
	}
	if got.GetNamespace() != "" {
		t.Errorf("误带的 metadata.namespace 应被清空，got %q", got.GetNamespace())
	}
}
