// Nacos Pod 注入 Webhook 的 mutate 逻辑单测
package service

import (
	"encoding/json"
	"fmt"
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

func newNacosTestService(t *testing.T) *NacosService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.NacosNamespace{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := db.Create(&model.NacosNamespace{
		ClusterName: "c1", K8sNamespace: "prod", NacosNamespaceId: "prod",
		Username: "prod", Password: "secret-pwd", Status: "synced",
	}).Error; err != nil {
		t.Fatalf("建映射失败: %v", err)
	}
	return NewNacosService(db, nil, config.NacosConfigYaml{})
}

func nacosReview(t *testing.T, ns string, pod corev1.Pod) *admissionv1.AdmissionReview {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "test-uid",
			Namespace: ns,
			Resource:  metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// 已同步 ns：两个容器分别需要 add envFrom / append secret
func TestNacosMutateInjectsEnvFrom(t *testing.T) {
	s := newNacosTestService(t)
	pod := corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app"},
			{Name: "sidecar", EnvFrom: []corev1.EnvFromSource{{
				ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: NacosConfigMapName}},
			}}},
		}},
	}
	resp := s.mutate("c1", nacosReview(t, "prod", pod))
	if !resp.Response.Allowed {
		t.Fatal("应允许 Pod 创建")
	}
	if resp.Response.PatchType == nil || len(resp.Response.Patch) == 0 {
		t.Fatal("应产生 JSON Patch")
	}
	var ops []map[string]interface{}
	if err := json.Unmarshal(resp.Response.Patch, &ops); err != nil {
		t.Fatalf("Patch 不是合法 JSON: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("期望 2 个 patch 操作，实际 %d: %s", len(ops), resp.Response.Patch)
	}
	if ops[0]["path"] != "/spec/containers/0/envFrom" || ops[0]["op"] != "add" {
		t.Errorf("容器 0 应为 add envFrom 整体: %v", ops[0])
	}
	if ops[1]["path"] != "/spec/containers/1/envFrom/-" {
		t.Errorf("容器 1 应为 append secretRef: %v", ops[1])
	}
	if *resp.Response.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Errorf("patchType 应为 JSONPatch")
	}
}

// 未同步的 ns / 非 Pod 资源：允许但不注入
func TestNacosMutateSkipsUnmanaged(t *testing.T) {
	s := newNacosTestService(t)
	pod := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}

	resp := s.mutate("c1", nacosReview(t, "other-ns", pod))
	if !resp.Response.Allowed || len(resp.Response.Patch) != 0 {
		t.Fatal("未同步 ns 应放行且不注入")
	}

	review := nacosReview(t, "prod", pod)
	review.Request.Resource.Resource = "deployments"
	if resp := s.mutate("c1", review); len(resp.Response.Patch) != 0 {
		t.Fatal("非 Pod 资源不应注入")
	}
}

// genPassword 基本可用性
func TestNacosGenPassword(t *testing.T) {
	p1, err := genPassword()
	if err != nil || len(p1) != 24 {
		t.Fatalf("密码长度应为 24，实际 %q", p1)
	}
	p2, _ := genPassword()
	if p1 == p2 {
		t.Fatal("两次生成的密码不应相同")
	}
}

// TestCheckNacosResult v1 成功 code=200、v3 成功 code=0、其余报错、非 JSON（v1 字面量）跳过
func TestCheckNacosResult(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"v3 success", `{"code":0,"message":"success","data":true}`, false},
		{"v1 success", `{"code":200,"message":null,"data":true}`, false},
		{"v1 literal", `true`, false},
		{"v1 page shape", `{"totalCount":2,"pageItems":[]}`, false},
		{"v3 error", `{"code":10001,"message":"namespaceId [foo] already exist"}`, true},
		{"v1 error", `{"code":500,"message":"server error"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNacosResult([]byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkNacosResult(%s) err=%v, wantErr=%v", tc.body, err, tc.wantErr)
			}
		})
	}
}

// TestNacosNsInfoID 命名空间标识按 namespace > namespaceId > customNamespaceId 优先级取值
func TestNacosNsInfoID(t *testing.T) {
	if got := (NacosNsInfo{Namespace: "dev", NamespaceId: "ignored"}).ID(); got != "dev" {
		t.Fatalf("ID() = %q, want dev", got)
	}
	if got := (NacosNsInfo{NamespaceId: "dev"}).ID(); got != "dev" {
		t.Fatalf("ID() = %q, want dev", got)
	}
	if got := (NacosNsInfo{CustomNamespaceId: "dev"}).ID(); got != "dev" {
		t.Fatalf("ID() = %q, want dev", got)
	}
	if got := (NacosNsInfo{}).ID(); got != "" {
		t.Fatalf("ID() = %q, want empty", got)
	}
}

// TestNacosNsParam v3 风格空命名空间归一为 public，v1 保持原样
func TestNacosNsParam(t *testing.T) {
	v3 := &NacosClient{v3: true}
	if got := v3.nsParam(""); got != "public" {
		t.Fatalf("v3 nsParam(\"\") = %q, want public", got)
	}
	if got := v3.nsParam("dev"); got != "dev" {
		t.Fatalf("v3 nsParam(dev) = %q, want dev", got)
	}
	v1 := &NacosClient{}
	if got := v1.nsParam(""); got != "" {
		t.Fatalf("v1 nsParam(\"\") = %q, want empty", got)
	}
}

// TestNacosAlreadyExists 幂等冲突容错：v1/v3 应用层文案 + 数据库唯一约束（MySQL/Derby）
func TestNacosAlreadyExists(t *testing.T) {
	tolerated := []string{
		`Nacos 返回 500: {"message":"user 'prod' already bound to the role 'ROLE_prod'!"}`,
		`Nacos 返回 500: {"message":"user 'prod' already exist!"}`,
		`Nacos 返回错误 10001: role 'prod' already exists`,
		`Nacos 返回 500: Duplicate entry 'ROLE_prod-prod-rw' for key 'uk_role_permission'`,
		`Nacos 返回 500: The statement was aborted because it would have caused a duplicate key value in a unique or primary key constraint`,
	}
	for _, msg := range tolerated {
		if !nacosAlreadyExists(fmt.Errorf("%s", msg)) {
			t.Fatalf("应视为幂等冲突被容忍: %s", msg)
		}
	}
	fatal := []string{
		`Nacos 返回 403: unknown user!`,
		`Nacos 返回 500: role 'prod' not found!`,
		`Nacos 不可达: connection refused`,
	}
	for _, msg := range fatal {
		if nacosAlreadyExists(fmt.Errorf("%s", msg)) {
			t.Fatalf("不应被容忍: %s", msg)
		}
	}
	if nacosAlreadyExists(nil) {
		t.Fatal("nil 错误不应被容忍")
	}
}

// TestNacosDuplicateMessageSurvivesTruncation 回归：真实 MySQL 幂等冲突的 Spring 错误体中
// "Duplicate entry" 出现在第 201 字符（JSON 前缀占位），错误信息截断必须放宽到 500 才能保留关键词
func TestNacosDuplicateMessageSurvivesTruncation(t *testing.T) {
	body := `{"timestamp":"2026-09-14T09:19:53.127+00:00","status":500,"error":"Internal Server Error","message":"PreparedStatementCallback; SQL [INSERT INTO permissions (role, resource, action) VALUES (?, ?, ?)]; Duplicate entry 'ROLE_agentgateway-system-agentgateway-system-rw' for key 'uk_role_permission'; nested exception is com.mysql.cj.jdbc.exceptions.SQLException","path":"/nacos/v3/auth/permission"}`
	err := fmt.Errorf("Nacos 返回 500: %s", truncateStr(body, 500))
	if !nacosAlreadyExists(err) {
		t.Fatalf("500 字符截断后幂等冲突关键词丢失: %s", err)
	}
}
