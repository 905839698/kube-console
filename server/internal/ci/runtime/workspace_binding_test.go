package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"kube-console/server/internal/ci/pipeline/compiler"
)

// bindWorkspaceSubPaths 回归：source 绑定必须落到流水线自己的工作子目录
// （同项目不同流水线并行互不踩），启用缓存时 cache 绑定落到项目级共享缓存
// kc-cache；且不得残留任何指向 Pipeline workspace「cache」的绑定——PipelineRun
// 只绑 shared 一条（约束见 tekton_ops_test 的单绑定测试），残留引用会让
// Tekton affinity-assistant 的助手名与任务 Pod 的亲和引用对不上，Pod
// 永远 Unschedulable（线上 run 卡满 1h 超时的根因）。
func TestBindWorkspaceSubPaths(t *testing.T) {
	spec := &compiler.PipelineSpec{Spec: compiler.PipelineBody{
		Tasks: []compiler.TektonPTask{
			{Name: "clone", Workspaces: []compiler.PWorkspace{{Name: "source", Workspace: "shared"}}},
			{Name: "web", Workspaces: []compiler.PWorkspace{
				{Name: "source", Workspace: "shared"},
				{Name: "cache", Workspace: "cache"},
			}},
			{Name: "go", Workspaces: []compiler.PWorkspace{
				{Name: "source", Workspace: "shared"},
				{Name: "cache", Workspace: "cache"},
			}},
		},
	}}

	bindWorkspaceSubPaths(spec, 7, "kc-cache")

	for _, task := range spec.Spec.Tasks {
		for _, ws := range task.Workspaces {
			if ws.Workspace == "cache" {
				t.Errorf("任务 %s 仍有指向 cache workspace 的绑定: %+v", task.Name, ws)
			}
		}
	}
	// source：每个任务都挂到流水线自己的子目录
	for _, task := range spec.Spec.Tasks {
		got := task.Workspaces[0]
		if got.Workspace != "shared" || got.SubPath != "pl-7" {
			t.Errorf("任务 %s 的 source 绑定应为 shared+pl-7，实际 %+v", task.Name, got)
		}
	}
	got := spec.Spec.Tasks[1].Workspaces[1]
	if got.Workspace != "shared" || got.SubPath != "kc-cache" {
		t.Errorf("cache 绑定应重写为 shared+kc-cache，实际 %+v", got)
	}
	// specToMap 经 JSON 中转进 unstructured，subPath 必须真实落进序列化产物
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"subPath":"kc-cache"`) || !strings.Contains(string(b), `"subPath":"pl-7"`) {
		t.Errorf("序列化产物缺 subPath: %s", b)
	}
}

// 未启用缓存：cache 绑定原样保留（PipelineRun 不绑 cache workspace，optional
// 语义让脚本拿到空路径跳过缓存），source 仍然要落流水线子目录。
func TestBindWorkspaceSubPathsWithoutCache(t *testing.T) {
	spec := &compiler.PipelineSpec{Spec: compiler.PipelineBody{
		Tasks: []compiler.TektonPTask{
			{Name: "clone", Workspaces: []compiler.PWorkspace{{Name: "source", Workspace: "shared"}}},
			{Name: "web", Workspaces: []compiler.PWorkspace{
				{Name: "source", Workspace: "shared"},
				{Name: "cache", Workspace: "cache"},
			}},
		},
	}}

	bindWorkspaceSubPaths(spec, 9, "")

	if ws := spec.Spec.Tasks[1].Workspaces[1]; ws.Workspace != "cache" || ws.SubPath != "" {
		t.Errorf("未启用缓存时 cache 绑定不该被动: %+v", ws)
	}
	if ws := spec.Spec.Tasks[0].Workspaces[0]; ws.Workspace != "shared" || ws.SubPath != "pl-9" {
		t.Errorf("source 绑定应为 shared+pl-9: %+v", ws)
	}
}
