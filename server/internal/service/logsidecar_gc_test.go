// 采集关闭后的共享凭据 Secret 回收判定：只要该 ns 里还有任何带 fluent-bit 的工作负载，
// 就不能回收（误删会让其它 Sidecar 起不来）。
package service

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

func podWith(names ...string) corev1.PodSpec {
	spec := corev1.PodSpec{}
	for _, n := range names {
		spec.Containers = append(spec.Containers, corev1.Container{Name: n})
	}
	return spec
}

func TestCollectorsLeftIn(t *testing.T) {
	newClient := func(objs ...runtime.Object) *kube.Client {
		return &kube.Client{Clientset: fake.NewSimpleClientset(objs...)}
	}

	// 同 ns 另一个 Deployment 仍在采集
	c := newClient(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "a"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podWith("app")}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "b"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podWith("app", LogSidecarName)}}},
	)
	if left, err := collectorsLeftIn(context.Background(), c, "app"); err != nil || !left {
		t.Errorf("仍有采集者时应返回 true，得到 left=%v err=%v", left, err)
	}

	// StatefulSet / DaemonSet 里的采集者同样要算数
	c = newClient(
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: podWith("db", LogSidecarName)}}},
	)
	if left, _ := collectorsLeftIn(context.Background(), c, "app"); !left {
		t.Error("StatefulSet 里的采集者不应被忽略")
	}
	c = newClient(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "agent"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: podWith("agent", LogSidecarName)}}},
	)
	if left, _ := collectorsLeftIn(context.Background(), c, "app"); !left {
		t.Error("DaemonSet 里的采集者不应被忽略")
	}

	// 全部关闭：可回收
	c = newClient(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "a"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podWith("app")}}},
	)
	if left, err := collectorsLeftIn(context.Background(), c, "app"); err != nil || left {
		t.Errorf("无采集者时应返回 true 之外的 false，得到 left=%v err=%v", left, err)
	}
}
