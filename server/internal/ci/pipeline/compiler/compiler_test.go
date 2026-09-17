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
// paramDefaults 给参数配默认值（模拟 loader 把节点字段渲染进 params[].default）。
type specNode struct {
	t             string
	params        []string
	paramDefaults map[string]string
	results       []string
	script        string
}

func (s specNode) Meta() nodetype.Meta                      { return nodetype.Meta{Type: s.t} }
func (s specNode) Validate(map[string]interface{}) []string { return nil }
func (s specNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	ts := nodetype.TaskSpec{
		Name:  s.t,
		Steps: []nodetype.TektonStep{{Name: "main", Image: "alpine", Script: s.script}},
	}
	for _, p := range s.params {
		param := nodetype.TektonParam{Name: p, Type: "string"}
		if v, ok := s.paramDefaults[p]; ok {
			param.Default = v
		}
		ts.Params = append(ts.Params, param)
	}
	for _, r := range s.results {
		ts.Results = append(ts.Results, nodetype.TektonResult{Name: r})
	}
	return ts, nil
}
func (s specNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

func taskScriptOf(spec *PipelineSpec, name string) string {
	t := taskOf(spec, name)
	if t == nil {
		return ""
	}
	return t.TaskSpec.Steps[0].Script
}

func taskOf(spec *PipelineSpec, name string) *TektonPTask {
	for i := range spec.Spec.Tasks {
		if spec.Spec.Tasks[i].Name == name {
			return &spec.Spec.Tasks[i]
		}
	}
	return nil
}

func paramValueOf(t *TektonPTask, name string) (string, bool) {
	for _, p := range t.Params {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
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

// 引用了非上游任务的结果（画布缺连线）→ 编译期报错。运行期两任务并行，
// $(tasks...) 占位符解析不出会原样留在脚本里被 shell 当命令执行
// （"tasks.xxx.results.yyy: not found"）
func TestCompileResultRefWithoutDependency(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(tasks.gc.results.git_commit)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "gc", Type: "git-clone"},
		{ID: "build", Type: "build-image"},
	}
	// 无连线：build 引用 gc 的结果但两者无依赖路径

	_, err := c.Compile(g, "ns")
	if err == nil {
		t.Fatal("引用非上游任务结果（缺连线）应编译失败")
	}
	if !strings.Contains(err.Error(), "没有依赖路径") {
		t.Errorf("错误应提示缺少连线: %v", err)
	}
}

// 经 condition 节点传递的依赖也算上游（gc → cond → build 引用 gc 结果合法）
func TestCompileResultRefThroughCondition(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{t: model.NodeTypeCondition})
	reg.Register(specNode{t: "build-image", script: "IMG=repo:$(tasks.gc.results.git_commit)"})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "gc", Type: "git-clone"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "x", "op": "==", "value": "1"}},
		{ID: "build", Type: "build-image"},
	}
	g.Edges = []model.Edge{
		{Source: "gc", Target: "c1"},
		{Source: "c1", Target: "build", Branch: model.BranchYes},
	}

	if _, err := c.Compile(g, "ns"); err != nil {
		t.Fatalf("经 condition 传递的依赖应合法: %v", err)
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

// 参数默认值里的上游结果引用必须提升为 PipelineTask 级 params 赋值：
// Tekton 的结果替换不覆盖内嵌 taskSpec 的 params[].default，留在默认值里会被
// 下游 $(params.X) 原样注入脚本、被 shell 当命令执行
// （"tasks.build-image-xxx.results.imageRef: not found"，报错行正是 $(params.X) 那行）。
func TestCompileLiftResultRefParamDefault(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{
		t:             "gitops-bump",
		params:        []string{"images", "files"},
		paramDefaults: map[string]string{"images": "$(tasks.gc.results.git_commit)", "files": "apps/demo/deployment.yaml"},
		script:        `IMAGES="$(params.images)"`,
	})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "gc", Type: "git-clone"},
		{ID: "bump", Type: "gitops-bump"},
	}
	g.Edges = []model.Edge{{Source: "gc", Target: "bump"}}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	bump := taskOf(spec, "bump")
	if bump == nil {
		t.Fatal("缺少 bump 任务")
	}
	v, ok := paramValueOf(bump, "images")
	if !ok {
		t.Fatal("含上游结果引用的参数 images 应提升为 PipelineTask params（值原样）")
	}
	if v != "$(tasks.gc.results.git_commit)" {
		t.Errorf("提升的值应为原引用文本，实际 %q", v)
	}
	if _, ok := paramValueOf(bump, "files"); ok {
		t.Error("不含结果引用的参数不应提升（继续用 taskSpec 默认值）")
	}
	// 默认值保留：编译产物自解释，人工排查时能看到原始引用
	for _, p := range bump.TaskSpec.Params {
		if p.Name == "images" && p.Default != "$(tasks.gc.results.git_commit)" {
			t.Errorf("taskSpec 默认值应保持原样，实际 %v", p.Default)
		}
	}
}

// 提升不能把引用搬出编译期校验范围：缺连线时仍须报「没有依赖路径」。
func TestCompileLiftResultRefParamDefaultWithoutDependency(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{
		t:             "gitops-bump",
		params:        []string{"images"},
		paramDefaults: map[string]string{"images": "$(tasks.gc.results.git_commit)"},
		script:        `IMAGES="$(params.images)"`,
	})
	c := New(reg)

	g := &model.Graph{Name: "p", Version: 1}
	g.Nodes = []model.Node{
		{ID: "gc", Type: "git-clone"},
		{ID: "bump", Type: "gitops-bump"},
	}
	// 无连线

	_, err := c.Compile(g, "ns")
	if err == nil {
		t.Fatal("参数默认值引用非上游任务结果（缺连线）应编译失败")
	}
	if !strings.Contains(err.Error(), "没有依赖路径") {
		t.Errorf("错误应提示缺少连线: %v", err)
	}
}

// 提升发生在 git-clone 引用改写之后：形如 $(params.git_commit) 的历史默认值
// 先改写成实际节点 ID 的结果引用，再被提升。
func TestCompileLiftResultRefParamAfterGitCloneRewrite(t *testing.T) {
	reg := newStubRegistry()
	reg.Register(specNode{t: "git-clone", results: []string{"git_commit"}})
	reg.Register(specNode{
		t:             "build-image",
		params:        []string{"tag"},
		paramDefaults: map[string]string{"tag": "$(params.git_commit)"},
		script:        "IMG=repo:$(params.tag)",
	})
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
	v, ok := paramValueOf(taskOf(spec, "build"), "tag")
	if !ok {
		t.Fatal("改写后的结果引用应提升为 PipelineTask params")
	}
	if v != "$(tasks.src.results.git_commit)" {
		t.Errorf("提升的值应为改写后的引用，实际 %q", v)
	}
}
