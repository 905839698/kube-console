package tekton

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
)

// mapCondition：when 未命中的 TaskRun（reason=WhenExpressionsEvaluatedFalse，
// kubectl 显示 SKIPPED）必须映射为 skipped，否则节点在前端永远不收敛。
func TestMapConditionSkipped(t *testing.T) {
	cases := []struct {
		status, reason, want string
	}{
		{"True", "WhenExpressionsEvaluatedFalse", "skipped"},
		{"Unknown", "WhenExpressionsEvaluatedFalse", "skipped"},
		{"True", "Succeeded", "success"},
		{"False", "Failed", "failed"},
		{"Unknown", "Running", "running"},
		{"False", "TaskRunCancelled", "cancelled"},
		{"False", "PipelineRunCancelled", "cancelled"},
		{"Unknown", "PipelineRunCancelled", "cancelled"},
	}
	for _, c := range cases {
		if got := mapCondition(c.status, c.reason); got != c.want {
			t.Errorf("mapCondition(%q, %q) = %q, want %q", c.status, c.reason, got, c.want)
		}
	}
}

// PipelineRun 只允许有一条 workspace 绑定（shared）。缓存不走 PipelineRun 级
// 第二条绑定：Tekton affinity-assistant 对多条同 claim 绑定只建一个 StatefulSet，
// 任务 Pod 的亲和引用却按各自 workspace 算出助手实例名，两边不一致时 Pod
// 永远 Unschedulable（线上 run 卡满 1h 超时的根因）。缓存的同盘 subPath 约束
// 由 service 层的任务级绑定承担，这里守住「PipelineRun 永远单绑定」。
func TestCreatePipelineRunBindsSingleWorkspace(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PipelineRunList"})
	c := NewClient("c1", &kube.Client{Dynamic: dyn}, &config.CIConfig{})

	ctx := context.Background()
	if err := c.CreatePipelineRun(ctx, "ci-demo", "run-1", "pl-1", "ci-bot", "ci-ws-p3", nil, nil, nil); err != nil {
		t.Fatalf("CreatePipelineRun: %v", err)
	}
	obj, err := dyn.Resource(gvr).Namespace("ci-demo").Get(ctx, "run-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pipelinerun: %v", err)
	}
	ws, _, _ := unstructured.NestedSlice(obj.Object, "spec", "workspaces")
	if len(ws) != 1 {
		t.Fatalf("PipelineRun 必须只绑一个 workspace，实际 %d: %+v", len(ws), ws)
	}
	first := ws[0].(map[string]interface{})
	claim, _, _ := unstructured.NestedString(first, "persistentVolumeClaim", "claimName")
	if first["name"] != "shared" || claim != "ci-ws-p3" {
		t.Errorf("绑定不对: %+v", first)
	}
	// 节点固定：nodeSelector/tolerations 必须落进 taskRunTemplate.podTemplate（v1 的
	// 顶层 spec.podTemplate 会被 apiserver 静默剪枝），且与 SA 共存
	if err := c.CreatePipelineRun(ctx, "ci-demo", "run-2", "pl-1", "ci-bot", "ci-ws-p3", nil, nil, &RunScheduling{
		NodeSelector: map[string]string{"kubernetes.io/hostname": "node-a"},
		Tolerations:  []map[string]any{{"key": "type", "operator": "Exists"}},
	}); err != nil {
		t.Fatalf("CreatePipelineRun(sched): %v", err)
	}
	obj2, err := dyn.Resource(gvr).Namespace("ci-demo").Get(ctx, "run-2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get run-2: %v", err)
	}
	if _, exists, _ := unstructured.NestedMap(obj2.Object, "spec", "podTemplate"); exists {
		t.Error("顶层 spec.podTemplate 会被静默剪枝，不允许出现")
	}
	saName, _, _ := unstructured.NestedString(obj2.Object, "spec", "taskRunTemplate", "serviceAccountName")
	if saName != "ci-bot" {
		t.Errorf("serviceAccountName 应在 taskRunTemplate 下，实际 %q", saName)
	}
	tol, _, _ := unstructured.NestedSlice(obj2.Object, "spec", "taskRunTemplate", "podTemplate", "tolerations")
	if len(tol) != 1 {
		t.Errorf("tolerations 应有 1 条，实际 %+v", tol)
	}
	sel, _, _ := unstructured.NestedStringMap(obj2.Object, "spec", "taskRunTemplate", "podTemplate", "nodeSelector")
	if sel["kubernetes.io/hostname"] != "node-a" {
		t.Errorf("nodeSelector 不对: %+v", sel)
	}
	keep, _, _ := unstructured.NestedBool(obj2.Object, "spec", "taskRunTemplate", "podTemplate", "preserveLogs")
	if !keep {
		t.Error("preserveLogs 丢了")
	}
	// 工作区可写靠 fsGroup（组 = 工具镜像统一 gid=101），不能设 runAsUser：
	// Tekton 给内部容器注入 runAsNonRoot: true，pod 级 runAsUser=0 会被 kubelet 拒绝
	podSC, hasSC, _ := unstructured.NestedMap(obj2.Object, "spec", "taskRunTemplate", "podTemplate", "securityContext")
	if !hasSC {
		t.Fatal("podTemplate.securityContext 丢了")
	}
	if gid, ok, _ := unstructured.NestedInt64(podSC, "fsGroup"); !ok || gid != 101 {
		t.Errorf("podTemplate.securityContext.fsGroup 应为 101（found=%v 值=%d）", ok, gid)
	}
	if _, ok, _ := unstructured.NestedInt64(podSC, "runAsUser"); ok {
		t.Error("不能设 runAsUser：Tekton 内部容器是 runAsNonRoot，会被 kubelet 拒绝")
	}
}
