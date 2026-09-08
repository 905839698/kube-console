package validator

import (
	"testing"

	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/model"
)

func graph(nodes ...string) *model.Graph {
	g := &model.Graph{Name: "t", Version: 1}
	for _, id := range nodes {
		g.Nodes = append(g.Nodes, model.Node{ID: id, Type: "x", Params: map[string]interface{}{}})
	}
	return g
}

func link(g *model.Graph, src, dst string) {
	g.Edges = append(g.Edges, model.Edge{Source: src, Target: dst})
}

func TestNoCycleLinear(t *testing.T) {
	g := graph("a", "b", "c")
	link(g, "a", "b")
	link(g, "b", "c")
	if hasCycle(g) {
		t.Fatal("linear graph should have no cycle")
	}
	order := Topological(g)
	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Fatalf("unexpected topo order: %v", order)
	}
}

func TestCycleDetected(t *testing.T) {
	g := graph("a", "b", "c")
	link(g, "a", "b")
	link(g, "b", "c")
	link(g, "c", "a")
	if !hasCycle(g) {
		t.Fatal("cycle should be detected")
	}
	if Topological(g) != nil {
		t.Fatal("topological order should be nil for cyclic graph")
	}
}

func TestParallelFanInFanOut(t *testing.T) {
	// git → trivy, sonar, test → build
	g := graph("git", "trivy", "sonar", "test", "build")
	link(g, "git", "trivy")
	link(g, "git", "sonar")
	link(g, "git", "test")
	link(g, "trivy", "build")
	link(g, "sonar", "build")
	link(g, "test", "build")
	if hasCycle(g) {
		t.Fatal("fan-in/fan-out should be acyclic")
	}
	order := Topological(g)
	if len(order) != 5 {
		t.Fatalf("expected 5 nodes in order, got %v", order)
	}
	// git 必须最先，build 必须最后
	if order[0] != "git" || order[4] != "build" {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestOrphanNodes(t *testing.T) {
	g := graph("a", "b", "c")
	link(g, "a", "b")
	errs := orphanNodes(g)
	if len(errs) != 1 || len(errs[0]) == 0 {
		t.Fatalf("expected 1 orphan (c), got %v", errs)
	}
}

func TestSingleNodeNoOrphan(t *testing.T) {
	g := graph("a")
	if errs := orphanNodes(g); len(errs) != 0 {
		t.Fatalf("single node should not be orphan, got %v", errs)
	}
}

// ===== taskResult 条件校验 =====

type stubResultNode struct {
	t       string
	results []string
}

func (s stubResultNode) Meta() nodetype.Meta {
	var rs []nodetype.PropOpt
	for _, r := range s.results {
		rs = append(rs, nodetype.PropOpt{Value: r, Label: r})
	}
	return nodetype.Meta{Type: s.t, Properties: nil, Results: rs}
}
func (s stubResultNode) Validate(map[string]interface{}) []string { return nil }
func (s stubResultNode) RenderTask(map[string]interface{}) (nodetype.TaskSpec, error) {
	return nodetype.TaskSpec{}, nil
}
func (s stubResultNode) ResultMapping() nodetype.ResultMap { return nodetype.ResultMap{} }

type stubReg struct{ m map[string]nodetype.Node }

func (r stubReg) List() []nodetype.Meta {
	var out []nodetype.Meta
	for _, n := range r.m {
		out = append(out, n.Meta())
	}
	return out
}
func (r stubReg) Get(t string) (nodetype.Node, bool) { n, ok := r.m[t]; return n, ok }
func (r stubReg) Register(n nodetype.Node)           { r.m[n.Meta().Type] = n }

// taskResultGraph 构造 a(x, results=[r1]) → c(condition) --yes--> b(x)。
func taskResultGraph(condParams map[string]interface{}) (*model.Graph, nodetype.Registry) {
	g := &model.Graph{Name: "t", Version: 1}
	g.Nodes = []model.Node{
		{ID: "a", Type: "x", Params: map[string]interface{}{}},
		{ID: "c", Type: model.NodeTypeCondition, Params: condParams},
		{ID: "b", Type: "x", Params: map[string]interface{}{}},
	}
	g.Edges = []model.Edge{
		{Source: "a", Target: "c"},
		{Source: "c", Target: "b", Branch: model.BranchYes},
	}
	reg := stubReg{m: map[string]nodetype.Node{
		"x":         stubResultNode{t: "x", results: []string{"r1"}},
		"condition": stubResultNode{t: "condition"},
	}}
	return g, reg
}

func TestTaskResultConditionValid(t *testing.T) {
	g, reg := taskResultGraph(map[string]interface{}{
		"source": "taskResult", "task": "a", "result": "r1", "op": "==", "value": "ok",
	})
	if errs := validateConditions(g, reg); len(errs) != 0 {
		t.Fatalf("合法 taskResult 条件不应报错: %v", errs)
	}
}

func TestTaskResultConditionRefErrors(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"引用不存在节点": {"source": "taskResult", "task": "ghost", "result": "r1", "op": "==", "value": "x"},
		"引用下游节点":   {"source": "taskResult", "task": "b", "result": "r1", "op": "==", "value": "x"},
		"未声明result":  {"source": "taskResult", "task": "a", "result": "nope", "op": "==", "value": "x"},
		"缺task":        {"source": "taskResult", "result": "r1", "op": "==", "value": "x"},
		"缺result":      {"source": "taskResult", "task": "a", "op": "==", "value": "x"},
		"非法source":    {"source": "weird", "op": "==", "value": "x"},
	}
	for name, params := range cases {
		g, reg := taskResultGraph(params)
		errs := validateConditions(g, reg)
		if len(errs) == 0 {
			t.Errorf("%s: 应报错，实际通过", name)
		}
	}
}

// 回归：不写 source 时按 param 处理，param 缺失照旧报错。
func TestConditionSourceDefaultParam(t *testing.T) {
	g, reg := taskResultGraph(map[string]interface{}{"op": "==", "value": "x"})
	errs := validateConditions(g, reg)
	if len(errs) == 0 {
		t.Fatal("缺 param（source 缺省=param）应报错")
	}
}
