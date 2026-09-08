package compiler

import (
	"strings"
	"testing"

	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/model"
)

// stubNode 固定返回一个简单 taskSpec 的测试节点。
type stubNode struct{ t string }

func (s stubNode) Meta() nodetype.Meta {
	return nodetype.Meta{Type: s.t, Properties: nil}
}
func (s stubNode) Validate(map[string]interface{}) []string { return nil }
func (s stubNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	return nodetype.TaskSpec{Name: s.t, Steps: []nodetype.TektonStep{{Name: s.t, Image: "alpine"}}}, nil
}
func (s stubNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

// stubRegistry 内存注册表（实现 nodetype.Registry）。
type stubRegistry struct{ nodes map[string]nodetype.Node }

func newStubRegistry(types ...string) *stubRegistry {
	r := &stubRegistry{nodes: map[string]nodetype.Node{}}
	for _, t := range types {
		r.nodes[t] = stubNode{t}
	}
	return r
}
func (r *stubRegistry) Register(n nodetype.Node)           { r.nodes[n.Meta().Type] = n }
func (r *stubRegistry) Get(t string) (nodetype.Node, bool) { n, ok := r.nodes[t]; return n, ok }
func (r *stubRegistry) List() []nodetype.Meta              { return nil }

func TestCompileRunAfter(t *testing.T) {
	c := New(newStubRegistry("git", "scan", "build"))

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git", Params: map[string]interface{}{}},
		{ID: "scan", Type: "scan", Params: map[string]interface{}{}},
		{ID: "build", Type: "build", Params: map[string]interface{}{}},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "scan"},
		{Source: "scan", Target: "build"},
	}

	spec, err := c.Compile(g, "ci-projects")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(spec.Spec.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(spec.Spec.Tasks))
	}
	for _, task := range spec.Spec.Tasks {
		switch task.Name {
		case "git":
			if len(task.RunAfter) != 0 {
				t.Errorf("git should have no runAfter, got %v", task.RunAfter)
			}
		case "scan":
			if len(task.RunAfter) != 1 || task.RunAfter[0] != "git" {
				t.Errorf("scan runAfter should be [git], got %v", task.RunAfter)
			}
		case "build":
			if len(task.RunAfter) != 1 || task.RunAfter[0] != "scan" {
				t.Errorf("build runAfter should be [scan], got %v", task.RunAfter)
			}
		}
	}

	yamlText, err := ToYAML(spec)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if !strings.Contains(yamlText, "kind: Pipeline") || !strings.Contains(yamlText, "runAfter:") {
		t.Errorf("yaml missing expected content:\n%s", yamlText)
	}
}

func TestCompileParallelRunAfter(t *testing.T) {
	c := New(newStubRegistry("git", "trivy", "sonar", "build"))

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "trivy", Type: "trivy"},
		{ID: "sonar", Type: "sonar"},
		{ID: "build", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "trivy"},
		{Source: "git", Target: "sonar"},
		{Source: "trivy", Target: "build"},
		{Source: "sonar", Target: "build"},
	}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, task := range spec.Spec.Tasks {
		if task.Name == "build" && len(task.RunAfter) != 2 {
			t.Errorf("build should runAfter 2 tasks, got %v", task.RunAfter)
		}
	}
}

// wsStubNode 与 stubNode 相同，但 taskSpec 带 workspace（验证 pre-shell 的 cd 行）。
type wsStubNode struct{ t string }

func (s wsStubNode) Meta() nodetype.Meta {
	return nodetype.Meta{Type: s.t, Properties: nil}
}
func (s wsStubNode) Validate(map[string]interface{}) []string { return nil }
func (s wsStubNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	return nodetype.TaskSpec{
		Name:       s.t,
		Workspaces: []nodetype.TektonWS{{Name: "source"}},
		Steps:      []nodetype.TektonStep{{Name: "main", Image: "img:1"}},
	}, nil
}
func (s wsStubNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

func TestCompilePreShell(t *testing.T) {
	reg := newStubRegistry("build")
	reg.Register(wsStubNode{"build"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "build", Type: "build", Params: map[string]interface{}{
			"preShell": "rm -rf package-lock.json pnpm-lock.yaml",
		}},
	}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	steps := spec.Spec.Tasks[0].TaskSpec.Steps
	if len(steps) != 2 || steps[0].Name != "pre-shell" {
		t.Fatalf("steps[0] 应为 pre-shell，实际 %d 个 step: %+v", len(steps), steps)
	}
	if steps[0].Image != "img:1" {
		t.Errorf("pre-shell 应复用主 step 镜像，got %s", steps[0].Image)
	}
	for _, want := range []string{
		"#!/bin/sh", "set -e",
		"cd $(workspaces.source.path)",
		"rm -rf package-lock.json pnpm-lock.yaml",
	} {
		if !strings.Contains(steps[0].Script, want) {
			t.Errorf("pre-shell 脚本缺少 %q:\n%s", want, steps[0].Script)
		}
	}
	if steps[1].Name != "main" {
		t.Errorf("原主 step 应保持顺序，got %s", steps[1].Name)
	}

	// 空 preShell 不注入
	g2 := &model.Graph{Name: "p2", Version: 1}
	g2.Nodes = []model.Node{{ID: "build", Type: "build", Params: map[string]interface{}{"preShell": "  "}}}
	spec2, err := c.Compile(g2, "ns")
	if err != nil {
		t.Fatalf("compile2: %v", err)
	}
	if n := len(spec2.Spec.Tasks[0].TaskSpec.Steps); n != 1 {
		t.Errorf("空白 preShell 不应注入，got %d 个 step", n)
	}
}

// cacheStubNode taskSpec 带 optional 的 cache workspace（依赖缓存场景）。
type cacheStubNode struct{ t string }

func (s cacheStubNode) Meta() nodetype.Meta {
	return nodetype.Meta{Type: s.t, Properties: nil}
}
func (s cacheStubNode) Validate(map[string]interface{}) []string { return nil }
func (s cacheStubNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	return nodetype.TaskSpec{
		Name:       s.t,
		Workspaces: []nodetype.TektonWS{{Name: "source"}, {Name: "cache", Optional: true}},
		Steps:      []nodetype.TektonStep{{Name: "main", Image: "img:1"}},
	}, nil
}
func (s cacheStubNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

// TestCompileCacheWorkspaceOptional 回归：Pipeline 上 cache 是 optional，
// task 级 cache 必须同样声明 optional，否则 Tekton 拒绝 PipelineRun
// （RequiredWorkspaceMarkedOptional）。
func TestCompileCacheWorkspaceOptional(t *testing.T) {
	reg := newStubRegistry("build")
	reg.Register(cacheStubNode{"build"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{{ID: "build", Type: "build", Params: map[string]interface{}{}}}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	pw := map[string]TektonWorkspace{}
	for _, ws := range spec.Spec.Workspaces {
		pw[ws.Name] = ws
	}
	if !pw["cache"].Optional {
		t.Error("pipeline workspace cache 应为 optional")
	}

	var cache nodetype.TektonWS
	found := false
	for _, ws := range spec.Spec.Tasks[0].TaskSpec.Workspaces {
		if ws.Name == "cache" {
			cache, found = ws, true
		}
	}
	if !found {
		t.Fatal("taskSpec 缺少 cache workspace")
	}
	if !cache.Optional {
		t.Error("task 级 cache workspace 未保留 optional: true（会被 Tekton 拒绝）")
	}
}
