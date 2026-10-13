// 日志采集 webhook 注入的单测：策略命中→注入合法 Sidecar；不命中→放行不注入。
// 注入条目必须是类型化对象（nacos 注入曾栽在扁平结构被 apiserver 剪成空条目上）。
package service

import (
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

func newLogInjectTestService(t *testing.T) (*LogInjectService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.LogCollectionPolicy{}, &model.NacosConfig{}, &model.Cluster{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	nacos := NewNacosService(db, nil, config.NacosConfigYaml{CertDir: t.TempDir()})
	svc := &LogInjectService{db: db, nacos: nacos}
	return svc, db
}

func logInjectPolicy(t *testing.T, db *gorm.DB, ns, workload string) {
	t.Helper()
	if err := db.Create(&model.LogCollectionPolicy{
		ClusterName: "c1", Namespace: ns, Workload: workload,
		Container: "app", LogPath: "/var/log/app/*.log", Format: "json", Image: "", ESUser: "elastic",
	}).Error; err != nil {
		t.Fatalf("建策略失败: %v", err)
	}
}

func logInjectReview(t *testing.T, pod corev1.Pod) *admissionv1.AdmissionReview {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "test-uid",
			Namespace: "prod",
			Resource:  metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func logInjectPatchOps(t *testing.T, resp *admissionv1.AdmissionReview) []map[string]interface{} {
	t.Helper()
	if !resp.Response.Allowed || resp.Response.PatchType == nil || len(resp.Response.Patch) == 0 {
		t.Fatalf("应产生注入 patch: %+v", resp.Response)
	}
	var ops []map[string]interface{}
	if err := json.Unmarshal(resp.Response.Patch, &ops); err != nil {
		t.Fatalf("Patch 不是合法 JSON: %v", err)
	}
	return ops
}

// 策略命中：Pod 被注入 fluent-bit Sidecar、共享卷/配置卷、应用容器挂载
func TestLogInjectMutatesPod(t *testing.T) {
	svc, db := newLogInjectTestService(t)
	logInjectPolicy(t, db, "prod", "orders")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-0", Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "orders"}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	ops := logInjectPatchOps(t, svc.mutatePod("c1", logInjectReview(t, pod)))
	if len(ops) != 3 {
		t.Fatalf("期望 3 个 patch 操作（sidecar/卷/挂载），实际 %d: %+v", len(ops), ops)
	}
	// sidecar 容器（/- 追加，值是单个对象）
	raw, _ := json.Marshal(ops[0]["value"])
	var sc corev1.Container
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("容器注入值非法: %s", raw)
	}
	if sc.Name != LogSidecarName || sc.Image != defaultFluentImage {
		t.Errorf("Sidecar 名/镜像不对: %+v", sc)
	}
	if len(sc.VolumeMounts) != 2 || sc.VolumeMounts[0].Name != LogShareVolumeName {
		t.Errorf("Sidecar 卷挂载不对: %+v", sc.VolumeMounts)
	}
	// 卷（Pod 原本没有 volumes 字段：应整体 add 两个卷）
	raw, _ = json.Marshal(ops[1]["value"])
	var volumes []corev1.Volume
	if err := json.Unmarshal(raw, &volumes); err != nil || len(volumes) != 2 {
		t.Fatalf("卷注入值非法: %s", raw)
	}
	if volumes[0].Name != LogShareVolumeName || volumes[1].Name != LogConfVolumeName ||
		volumes[1].ConfigMap == nil || volumes[1].ConfigMap.Name != "orders-fluent-bit" {
		t.Errorf("卷内容不对: %+v", volumes)
	}
	// 应用容器挂载（原本无 volumeMounts 字段：值是单个对象）
	raw, _ = json.Marshal(ops[2]["value"])
	var mount corev1.VolumeMount
	if err := json.Unmarshal(raw, &mount); err != nil || mount.Name != LogShareVolumeName {
		t.Fatalf("应用容器挂载注入值非法: %s", raw)
	}
}

// 策略未命中（裸 Pod / 无策略）→ 放行不注入
func TestLogInjectSkipsUnmanaged(t *testing.T) {
	svc, db := newLogInjectTestService(t)
	logInjectPolicy(t, db, "prod", "orders")
	bare := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	if resp := svc.mutatePod("c1", logInjectReview(t, bare)); resp.Response.PatchType != nil {
		t.Error("裸 Pod 不该被注入")
	}
	owned := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "other"}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	if resp := svc.mutatePod("c1", logInjectReview(t, owned)); resp.Response.PatchType != nil {
		t.Error("无策略的工作负载不该被注入")
	}
}

// 模板模式遗留（Pod 模板自带 fluent-bit）→ 不重复注入
func TestLogInjectSkipsWhenSidecarPresent(t *testing.T) {
	svc, db := newLogInjectTestService(t)
	logInjectPolicy(t, db, "prod", "orders")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "orders"}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app"}, {Name: LogSidecarName},
		}},
	}
	if resp := svc.mutatePod("c1", logInjectReview(t, pod)); resp.Response.PatchType != nil {
		t.Error("已有 Sidecar 的 Pod 不该被重复注入")
	}
}
