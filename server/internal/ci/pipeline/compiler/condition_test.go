package compiler

import (
	"reflect"
	"strings"
	"testing"

	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/model"
)

// stub 节点 + 注册表（复用主测试里的实现若可用；此处独立一份避免跨包）。
type stubNode2 struct{ t string }

func (s stubNode2) Meta() nodetype.Meta                      { return nodetype.Meta{Type: s.t, Properties: nil} }
func (s stubNode2) Validate(map[string]interface{}) []string { return nil }
func (s stubNode2) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	return nodetype.TaskSpec{Name: s.t, Steps: []nodetype.TektonStep{{Name: s.t, Image: "alpine"}}}, nil
}
func (s stubNode2) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

type stubReg2 struct{ nodes map[string]nodetype.Node }

func (r *stubReg2) Register(n nodetype.Node) { r.nodes[n.Meta().Type] = n }
func (r *stubReg2) Get(t string) (nodetype.Node, bool) {
	n, ok := r.nodes[t]
	return n, ok
}
func (r *stubReg2) List() []nodetype.Meta { return nil }

func condReg2() *stubReg2 {
	m := map[string]nodetype.Node{}
	for _, t := range []string{"git", "build", "upload", model.NodeTypeCondition} {
		m[t] = stubNode2{t}
	}
	return &stubReg2{nodes: m}
}

func taskByName(spec *PipelineSpec, name string) *TektonPTask {
	for i := range spec.Spec.Tasks {
		if spec.Spec.Tasks[i].Name == name {
			return &spec.Spec.Tasks[i]
		}
	}
	return nil
}

// TestIfElse 简单 IF/ELSE：yes 分支 build，no 分支 upload。
func TestIfElse(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "ifelse", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "build", Type: "build"},
		{ID: "upload", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "build", Branch: model.BranchYes},
		{Source: "cond", Target: "upload", Branch: model.BranchNo},
	}

	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// condition 不生成 task
	if taskByName(spec, "cond") != nil {
		t.Error("condition 不应生成 task")
	}
	wantIn := WhenExpr{Input: "$(params.GIT_BRANCH)", Operator: "in", Values: []string{"main"}}
	b := taskByName(spec, "build")
	if b == nil || len(b.When) != 1 || !reflect.DeepEqual(b.When[0], wantIn) {
		t.Errorf("build when = %+v, want [%+v]", b.When, wantIn)
	}
	wantNotIn := WhenExpr{Input: "$(params.GIT_BRANCH)", Operator: "notin", Values: []string{"main"}}
	u := taskByName(spec, "upload")
	if u == nil || len(u.When) != 1 || !reflect.DeepEqual(u.When[0], wantNotIn) {
		t.Errorf("upload when = %+v, want [%+v]", u.When, wantNotIn)
	}
	// build/upload 的 runAfter 穿透 cond 到 git
	if len(b.RunAfter) != 1 || b.RunAfter[0] != "git" {
		t.Errorf("build runAfter = %v, want [git]", b.RunAfter)
	}
	// Pipeline 含 GIT_BRANCH 参数
	found := false
	for _, p := range spec.Spec.Params {
		if p.Name == "GIT_BRANCH" {
			found = true
		}
	}
	if !found {
		t.Error("Pipeline 应声明 GIT_BRANCH 参数")
	}
}

// TestBranchMerge yes/no 分支汇合后无约束。
func TestBranchMerge(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "merge", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "a", Type: "build"},
		{ID: "b", Type: "build"},
		{ID: "merge", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "a", Branch: model.BranchYes},
		{Source: "cond", Target: "b", Branch: model.BranchNo},
		{Source: "a", Target: "merge"},
		{Source: "b", Target: "merge"},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if a := taskByName(spec, "a"); len(a.When) != 1 {
		t.Errorf("a when = %v, want 1", a.When)
	}
	if b := taskByName(spec, "b"); len(b.When) != 1 {
		t.Errorf("b when = %v, want 1", b.When)
	}
	if m := taskByName(spec, "merge"); len(m.When) != 0 {
		t.Errorf("merge when = %v, want 无约束（两分支汇合）", m.When)
	}
}

// TestBypass 旁路（不经过 condition 的路径）无约束。
func TestBypass(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "bypass", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "a", Type: "build"},
		{ID: "b", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "a", Branch: model.BranchYes},
		{Source: "git", Target: "b"}, // 旁路，不经 cond
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if a := taskByName(spec, "a"); len(a.When) != 1 {
		t.Errorf("a when = %v, want 1", a.When)
	}
	if b := taskByName(spec, "b"); len(b.When) != 0 {
		t.Errorf("b when = %v, want 无约束（旁路）", b.When)
	}
}

// TestBypassMixedWithBranch 分支 + 旁路混合：同一任务既有条件分支入边又有无条件旁路入边时，
// 该任务存在无条件到达路径，不能加 when（否则非 main 分支上该任务被错误跳过）。
// 回归：旧实现只在「取值冲突」时清约束，旁路入边不产生冲突 → 误保留 when。
func TestBypassMixedWithBranch(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "bypass-mixed", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "deploy", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "deploy", Branch: model.BranchYes},
		{Source: "git", Target: "deploy"}, // 旁路：画布上是无条件依赖
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if d := taskByName(spec, "deploy"); len(d.When) != 0 {
		t.Errorf("deploy when = %+v, want 无约束（存在旁路 = 无条件路径）", d.When)
	}
}

// TestBypassThroughChainedTask 旁路经由中间任务传递：git → a → deploy 旁路 +
// cond → deploy 分支，a 本身无约束 → deploy 同样不得加 when。
func TestBypassThroughChainedTask(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "bypass-chain", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "a", Type: "build"},
		{ID: "deploy", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "deploy", Branch: model.BranchYes},
		{Source: "git", Target: "a"},
		{Source: "a", Target: "deploy"},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if d := taskByName(spec, "deploy"); len(d.When) != 0 {
		t.Errorf("deploy when = %+v, want 无约束（经 a 的旁路是无条件路径）", d.When)
	}
}

// TestNestedBranch yes 分支内再分支。
func TestNestedBranch(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "nested", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "c2", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_REPO", "op": "contains", "value": "core"}},
		{ID: "x", Type: "build"},
		{ID: "y", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "c1"},
		{Source: "c1", Target: "c2", Branch: model.BranchYes},
		{Source: "c1", Target: "y", Branch: model.BranchNo},
		{Source: "c2", Target: "x", Branch: model.BranchYes},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// x: 需 branch==main 且 repo contains core
	x := taskByName(spec, "x")
	if len(x.When) != 2 {
		t.Fatalf("x when = %+v, want 2 条", x.When)
	}
	// y: 需 branch!=main（结构化 notin）
	wantNotIn := WhenExpr{Input: "$(params.GIT_BRANCH)", Operator: "notin", Values: []string{"main"}}
	y := taskByName(spec, "y")
	if len(y.When) != 1 || !reflect.DeepEqual(y.When[0], wantNotIn) {
		t.Errorf("y when = %+v, want [%+v]", y.When, wantNotIn)
	}
}

// TestTaskResultCondition 按上游任务结果分支：when 引用 $(tasks.T.results.R)，
// 且 taskResult 条件不产生 Pipeline 参数声明。
func TestTaskResultCondition(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "tr", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{
			"source": "taskResult", "task": "git", "result": "r1", "op": "==", "value": "ok",
		}},
		{ID: "build", Type: "build"},
		{ID: "upload", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "build", Branch: model.BranchYes},
		{Source: "cond", Target: "upload", Branch: model.BranchNo},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	wantIn := WhenExpr{Input: "$(tasks.git.results.r1)", Operator: "in", Values: []string{"ok"}}
	if b := taskByName(spec, "build"); len(b.When) != 1 || !reflect.DeepEqual(b.When[0], wantIn) {
		t.Errorf("build when = %+v, want [%+v]", b.When, wantIn)
	}
	wantNotIn := WhenExpr{Input: "$(tasks.git.results.r1)", Operator: "notin", Values: []string{"ok"}}
	if u := taskByName(spec, "upload"); len(u.When) != 1 || !reflect.DeepEqual(u.When[0], wantNotIn) {
		t.Errorf("upload when = %+v, want [%+v]", u.When, wantNotIn)
	}
	// taskResult 条件不应声明任何 Pipeline 参数
	if len(spec.Spec.Params) != 0 {
		t.Errorf("taskResult 条件无需 Pipeline 参数, got %v", spec.Spec.Params)
	}
}

// TestTaskResultConditionContains contains 走 CEL 方法形式（a.contains(b)；
// 「a contains b」不是合法 CEL），引用 tasks[T].results[R]。
func TestTaskResultConditionContains(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "trc", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{
			"source": "taskResult", "task": "git", "result": "r1", "op": "contains", "value": "rele",
		}},
		{ID: "build", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "build", Branch: model.BranchYes},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	want := WhenExpr{CEL: `tasks["git"].results["r1"].contains("rele")`}
	if b := taskByName(spec, "build"); len(b.When) != 1 || !reflect.DeepEqual(b.When[0], want) {
		t.Errorf("build when = %+v, want [%+v]", b.When, want)
	}
}

// TestMixedParamAndResultConditions param 与 taskResult 变量各自独立归纳：
// 两个条件约束不同变量时下游同时携带两条 when。
func TestMixedParamAndResultConditions(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "mix", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "c2", Type: model.NodeTypeCondition, Params: map[string]interface{}{
			"source": "taskResult", "task": "git", "result": "r1", "op": "==", "value": "ok",
		}},
		{ID: "x", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "c1"},
		{Source: "c1", Target: "c2", Branch: model.BranchYes},
		{Source: "c2", Target: "x", Branch: model.BranchYes},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	x := taskByName(spec, "x")
	if len(x.When) != 2 {
		t.Fatalf("x when = %+v, want 2 条（param + result 各一）", x.When)
	}
	want1 := WhenExpr{Input: "$(params.GIT_BRANCH)", Operator: "in", Values: []string{"main"}}
	want2 := WhenExpr{Input: "$(tasks.git.results.r1)", Operator: "in", Values: []string{"ok"}}
	set := map[string]bool{whenKey(x.When[0]): true, whenKey(x.When[1]): true}
	if !set[whenKey(want1)] || !set[whenKey(want2)] {
		t.Errorf("x when = %+v, 应含 param 与 result 两条", x.When)
	}
}

// TestConditionParamWhitelist 条件引用的 param 只能是运行时注入的 GIT_BRANCH/GIT_COMMIT/GIT_REPO：
// 其它名字会声明为 Pipeline 必填参数却永远收不到值，Tekton webhook 拒绝整个 PipelineRun。
func TestConditionParamWhitelist(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "param-wl", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "ENV", "op": "==", "value": "prod"}},
		{ID: "a", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "c1"},
		{Source: "c1", Target: "a", Branch: model.BranchYes},
	}
	if _, err := c.Compile(g, "ns"); err == nil {
		t.Error("引用未注入的参数 ENV 应编译失败")
	} else if !strings.Contains(err.Error(), "GIT_BRANCH/GIT_COMMIT/GIT_REPO") {
		t.Errorf("错误应指明可用参数，实际: %v", err)
	}

	// 白名单内的参数正常通过
	g2 := &model.Graph{Name: "param-wl-ok", Version: 1}
	g2.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_COMMIT", "op": "==", "value": "abc"}},
		{ID: "a", Type: "build"},
	}
	g2.Edges = []model.Edge{
		{Source: "git", Target: "c1"},
		{Source: "c1", Target: "a", Branch: model.BranchYes},
	}
	if _, err := c.Compile(g2, "ns"); err != nil {
		t.Errorf("GIT_COMMIT 在白名单内，应编译通过: %v", err)
	}
}

// TestConditionDuplicateVar 同一变量被多个 condition 引用 → 编译报错（B12）。
func TestConditionDuplicateVar(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "dup", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "c1", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "==", "value": "main"}},
		{ID: "c2", Type: model.NodeTypeCondition, Params: map[string]interface{}{"param": "GIT_BRANCH", "op": "contains", "value": "re"}},
		{ID: "a", Type: "build"},
		{ID: "b", Type: "build"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "c1"},
		{Source: "c1", Target: "c2", Branch: model.BranchYes},
		{Source: "c2", Target: "a", Branch: model.BranchYes},
		{Source: "c1", Target: "b", Branch: model.BranchNo},
	}
	if _, err := c.Compile(g, "ns"); err == nil {
		t.Error("同一变量被两个 condition 引用应报错")
	}
}

// TestTaskResultConditionNotContains notcontains 的否定形式（!a.contains(b)）。
func TestTaskResultConditionNotContains(t *testing.T) {
	c := New(condReg2())
	g := &model.Graph{Name: "trnc", Version: 1}
	g.Nodes = []model.Node{
		{ID: "git", Type: "git"},
		{ID: "cond", Type: model.NodeTypeCondition, Params: map[string]interface{}{
			"source": "taskResult", "task": "git", "result": "r1", "op": "notcontains", "value": "rele",
		}},
		{ID: "build", Type: "build"},
		{ID: "upload", Type: "upload"},
	}
	g.Edges = []model.Edge{
		{Source: "git", Target: "cond"},
		{Source: "cond", Target: "build", Branch: model.BranchYes},
		{Source: "cond", Target: "upload", Branch: model.BranchNo},
	}
	spec, err := c.Compile(g, "ns")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	b := taskByName(spec, "build")
	want := WhenExpr{CEL: `!tasks["git"].results["r1"].contains("rele")`}
	if len(b.When) != 1 || !reflect.DeepEqual(b.When[0], want) {
		t.Errorf("build when = %+v, want [%+v]", b.When, want)
	}
}
