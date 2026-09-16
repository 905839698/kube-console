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

// specNode 带固定 script 与声明参数/结果的测试节点（引用改写/校验场景用）。
type specNode struct {
	t       string
	params  []string
	results []string
	script  string
}

func (s specNode) Meta() nodetype.Meta               { return nodetype.Meta{Type: s.t} }
func (s specNode) Validate(map[string]interface{}) []string { return nil }
func (s specNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	ts := nodetype.TaskSpec{
		Name:  s.t,
		Steps: []nodetype.TektonStep{{Name: "main", Image: "alpine", Script: s.script}},
	}
	for _, p := range s.params {
		ts.Params = append(ts.Params, nodetype.TektonParam{Name: p, Type: "string"})
	}
	for _, r := range s.results {
		ts.Results = append(ts.Results, nodetype.TektonResult{Name: r})
	}
	return ts, nil
}
func (s specNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

func taskScriptOf(spec *PipelineSpec, name string) string {
	for i := range spec.Spec.Tasks {
		if spec.Spec.Tasks[i].Name == name {
			return spec.Spec.Tasks[i].TaskSpec.Steps[0].Script
		}
	}
	return ""
}

// 回归：build-image 等节点的默认 tag 曾误写 $(params.git_commit)——嵌入 taskSpec
// 的 $(params.X) 指任务自身参数，Tekton webhook 以 non-existent variable 拒绝 apply。
// 编译器应把历史写法改写为实际 git-clone 节点的结果引用。
func TestCompileRewriteGitCommitRefs(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(params.git_commit)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git-clone", Type: "git-clone"},
		{ID: "build", Type: "build-image"},
	}
	g.Edges = []model.Edge{{Source: "git-clone", Target: "build"}}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	script := taskScriptOf(spec, "build")
	if strings.Contains(script, "$(params.git_commit)") {
		t.Errorf("历史引用未改写: %s", script)
	}
	if !strings.Contains(script, "$(tasks.git-clone.results.git_commit)") {
		t.Errorf("应改写为 $(tasks.git-clone.results.git_commit): %s", script)
	}
}

// git-clone 节点被改名（如 src）后，默认值里的 $(tasks.git-clone....) 引用悬空，
// 应改写到实际节点 ID。
func TestCompileRewriteGitCommitRefsRenamedNode(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(tasks.git-clone.results.git_commit)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "src", Type: "git-clone"},
		{ID: "build", Type: "build-image"},
	}
	g.Edges = []model.Edge{{Source: "src", Target: "build"}}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if script := taskScriptOf(spec, "build"); !strings.Contains(script, "$(tasks.src.results.git_commit)") {
		t.Errorf("应改写为 $(tasks.src.results.git_commit): %s", script)
	}
}

// build-image 默认 tag 引用 build_tag 结果，git-clone 节点改名后前缀改写同样覆盖
func TestCompileRewriteBuildTagRefRenamedNode(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit", "build_tag"}})
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(tasks.git-clone.results.build_tag)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "src", Type: "git-clone"},
		{ID: "build", Type: "build-image"},
	}
	g.Edges = []model.Edge{{Source: "src", Target: "build"}}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if script := taskScriptOf(spec, "build"); !strings.Contains(script, "$(tasks.src.results.build_tag)") {
		t.Errorf("应改写为 $(tasks.src.results.build_tag): %s", script)
	}
}

// 流水线没有 git-clone 节点时，遗留引用无法解析，编译期给出可读错误
// （而不是等 Tekton webhook 报 non-existent variable）。
func TestCompileGitCommitRefWithoutGitClone(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(params.git_commit)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{{ID: "build", Type: "build-image"}}

	_, err := c.Compile(g, "ns")
	if err == nil {
		t.Fatal("$(params.git_commit) 且无 git-clone 节点应编译失败")
	}
	if !strings.Contains(err.Error(), "$(params.git_commit)") {
		t.Errorf("错误应指出非法引用: %v", err)
	}
}

// validateTaskRefs：引用上游结果合法通过；未知任务/未知结果/引用自身结果报可读错误。
func TestCompileValidateTaskRefs(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{t: "k8s-deploy", params: []string{"image"}, script: "kubectl apply -f manifest"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git-clone", Type: "git-clone"},
		{ID: "deploy", Type: "k8s-deploy"},
	}
	g.Edges = []model.Edge{{Source: "git-clone", Target: "deploy"}}
	if _, err := c.Compile(g, "ns"); err != nil {
		t.Fatalf("合法引用不应报错: %v", err)
	}

	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"未知任务", "echo $(tasks.ghost.results.git_commit)", "没有名为 ghost"},
		{"未知结果", "echo $(tasks.git-clone.results.nope)", "未声明该结果"},
		{"引用自身结果", "echo $(tasks.deploy.results.image)", "自身的结果"},
	}
	for _, tc := range cases {
		reg2 := newStubRegistry()
		reg2.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
		reg2.Register(specNode{t: "k8s-deploy", params: []string{"image"}, script: tc.script})
		c2 := New(reg2)
		g2 := &model.Graph{Name: "p2", Version: 1}
		g2.Nodes = []model.Node{
			{ID: "git-clone", Type: "git-clone"},
			{ID: "deploy", Type: "k8s-deploy"},
		}
		g2.Edges = []model.Edge{{Source: "git-clone", Target: "deploy"}}
		_, err := c2.Compile(g2, "ns")
		if err == nil {
			t.Errorf("%s: 应编译失败", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 错误应含 %q，实际 %v", tc.name, tc.want, err)
		}
	}
}
