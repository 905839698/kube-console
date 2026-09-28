package service

import (
	"context"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

func kbMatrixInstanceSet(ns, name string, replicas, ready int64) *unstructured.Unstructured {
	o := map[string]any{"spec": map[string]any{}, "status": map[string]any{}}
	if replicas >= 0 {
		o["spec"].(map[string]any)["replicas"] = replicas
	}
	if ready >= 0 {
		o["status"].(map[string]any)["readyReplicas"] = ready
	}
	return kbUD(name, kbInstanceSetGVR, "InstanceSet", ns, o)
}

func i32p(v int32) *int32 { return &v }

func matrixPod(name, ns string, ready bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	if ready {
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true}}
	} else {
		p.Status.Phase = corev1.PodPending
	}
	return p
}

// 矩阵要按实际的运行态展开：Pod 列看容器就绪，InstanceSet 列是 KubeBlocks 数据库副本的
// 实际载体（早先只有三类内置控制器，数据库命名空间在矩阵里是空的）。
func TestWorkloadMatrixCountsPodsAndInstanceSets(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32p(2)},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	cs := fake.NewSimpleClientset(dep, matrixPod("app-ready", "app", true), matrixPod("app-pending", "app", false))
	dyn := kbFakeDyn(
		kbMatrixInstanceSet("app", "db-mysql", 3, 3),
		kbMatrixInstanceSet("app", "db-es", 3, 2),
		kbMatrixInstanceSet("other", "x-postgresql", -1, 1), // spec.replicas 缺省按 1
	)

	m, err := (&K8sService{}).WorkloadMatrixData(context.Background(), &kube.Client{Clientset: cs, Dynamic: dyn})
	if err != nil {
		t.Fatalf("WorkloadMatrixData: %v", err)
	}
	want := []string{"Deployment", "StatefulSet", "DaemonSet", "InstanceSet", "Pod"}
	if len(m.Kinds) != len(want) {
		t.Fatalf("列顺序应为 %v，实际 %v", want, m.Kinds)
	}
	for i, k := range want {
		if m.Kinds[i] != k {
			t.Fatalf("列顺序应为 %v，实际 %v", want, m.Kinds)
		}
	}
	app := m.Cells["app"]
	if app["Deployment"].Total != 1 || app["Deployment"].Ready != 1 {
		t.Errorf("Deployment 应 1/1，实际 %+v", app["Deployment"])
	}
	if app["Pod"].Total != 2 || app["Pod"].Ready != 1 {
		t.Errorf("Pod 应按容器就绪计数（1/2），实际 %+v", app["Pod"])
	}
	if app["InstanceSet"].Total != 2 || app["InstanceSet"].Ready != 1 {
		t.Errorf("InstanceSet 应 1/2（3/3 就绪、3/2 未齐），实际 %+v", app["InstanceSet"])
	}
	other := m.Cells["other"]
	if other["InstanceSet"].Total != 1 || other["InstanceSet"].Ready != 1 {
		t.Errorf("缺 spec.replicas 应默认 1 副本判就绪，实际 %+v", other["InstanceSet"])
	}
	if other["Pod"].Total != 0 {
		t.Errorf("other 命名空间没有 Pod，实际 %+v", other["Pod"])
	}
	// 命名空间按 Pod/InstanceSet 也要出现在行里
	if len(m.Namespaces) != 2 || m.Namespaces[0] != "app" || m.Namespaces[1] != "other" {
		t.Errorf("行应覆盖 app/other，实际 %v", m.Namespaces)
	}
}

// addon 没装（列不到 InstanceSet）时不给整列 "-"，否则每个矩阵都多一列噪音
func TestWorkloadMatrixHidesInstanceSetColumnWithoutCRD(t *testing.T) {
	cs := fake.NewSimpleClientset(matrixPod("app-ready", "app", true))
	m, err := (&K8sService{}).WorkloadMatrixData(context.Background(), &kube.Client{Clientset: cs})
	if err != nil {
		t.Fatalf("WorkloadMatrixData: %v", err)
	}
	for _, k := range m.Kinds {
		if k == "InstanceSet" {
			t.Fatalf("拿不到 CRD 时不该有 InstanceSet 列: %v", m.Kinds)
		}
	}
	if m.Kinds[len(m.Kinds)-1] != "Pod" {
		t.Errorf("Pod 列应保留，实际 %v", m.Kinds)
	}
	if m.Cells["app"]["Pod"].Ready != 1 {
		t.Errorf("Pod 计数丢失: %+v", m.Cells["app"])
	}
}

// 真实集群核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveMatrix
// 断言的是字段口径：InstanceSet 的期望副本读 spec.replicas、就绪读 status.readyReplicas。
// KubeBlocks 改字段名时矩阵会静默变成全 0/未就绪，这里会先响。
func TestWorkloadMatrixLive(t *testing.T) {
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
	m, err := (&K8sService{}).WorkloadMatrixData(context.Background(), c)
	if err != nil {
		t.Fatalf("WorkloadMatrixData: %v", err)
	}
	has := map[string]bool{}
	for _, k := range m.Kinds {
		has[k] = true
	}
	if !has["Pod"] {
		t.Errorf("Pod 列应在，实际 %v", m.Kinds)
	}
	if !has["InstanceSet"] {
		t.Logf("该集群未装 KubeBlocks，跳过 InstanceSet 断言（kinds=%v）", m.Kinds)
		return
	}
	sets, err := c.Dynamic.Resource(kbInstanceSetGVR).Namespace("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("列 InstanceSet: %v", err)
	}
	// 逐个 InstanceSet 对账：矩阵格子必须与 kubectl 的 spec.replicas / status.readyReplicas 口径一致
	type cnt struct{ total, ready int }
	want := map[string]cnt{}
	for i := range sets.Items {
		it := &sets.Items[i]
		replicas, ok, _ := unstructured.NestedInt64(it.Object, "spec", "replicas")
		if !ok {
			t.Errorf("%s/%s 没有 spec.replicas（CRD 默认值没生效？矩阵会按 1 判）", it.GetNamespace(), it.GetName())
			replicas = 1
		}
		ready, _, _ := unstructured.NestedInt64(it.Object, "status", "readyReplicas")
		c := want[it.GetNamespace()]
		c.total++
		if ready >= replicas {
			c.ready++
		}
		want[it.GetNamespace()] = c
	}
	for ns, c := range want {
		cell := m.Cells[ns]["InstanceSet"]
		if cell != (MatrixCell{Total: c.total, Ready: c.ready}) {
			t.Errorf("%s 的 InstanceSet 格子应 %d 齐/%d 个，实际 %d 齐/%d 个", ns, c.ready, c.total, cell.Ready, cell.Total)
		}
		t.Logf("OK %s: InstanceSet %d/%d，Pod %+v", ns, cell.Ready, cell.Total, m.Cells[ns]["Pod"])
	}
	for ns, cells := range m.Cells {
		if p := cells["Pod"]; p.Ready > p.Total {
			t.Errorf("%s 的 Pod 就绪数超过总数: %+v", ns, p)
		}
	}
}
