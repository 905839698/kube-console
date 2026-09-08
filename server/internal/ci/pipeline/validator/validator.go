package validator

import (
	"fmt"
	"strings"

	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/model"
)

// Result 图校验结果。Valid=false 时 Errors 非空。
type Result struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
	Warns  []string `json:"warns,omitempty"`
}

// Validator 校验 Pipeline DSL。
type Validator struct {
	Nodes nodetype.Registry
}

func New(reg nodetype.Registry) *Validator { return &Validator{Nodes: reg} }

// Validate 依次执行：引用完整性 → 环检测 → 孤立节点 → 节点类型存在 → 参数必填。
func (v *Validator) Validate(g *model.Graph) Result {
	var errs []string

	errs = append(errs, g.ValidateIDs()...)

	if hasCycle(g) {
		errs = append(errs, "流水线存在环（DAG 必须无环）")
	}

	errs = append(errs, orphanNodes(g)...)

	for _, n := range g.Nodes {
		node, ok := v.Nodes.Get(n.Type)
		if !ok {
			errs = append(errs, fmt.Sprintf("未知节点类型: %s", n.Type))
			continue
		}
		for _, e := range node.Validate(n.Params) {
			errs = append(errs, fmt.Sprintf("节点 %s(%s): %s", n.ID, n.Type, e))
		}
	}

	errs = append(errs, validateConditions(g, v.Nodes)...)

	if len(errs) == 0 {
		return Result{Valid: true}
	}
	return Result{Valid: false, Errors: errs}
}

// conditionOps 是 DSL 支持的比较运算符集合（编译器按此映射到 Tekton when）。
var conditionOps = map[string]bool{"==": true, "!=": true, "contains": true, "notcontains": true}

// condSource 条件变量来源：param（触发参数，缺省）| taskResult（上游任务结果）。
const (
	condSourceParam      = "param"
	condSourceTaskResult = "taskResult"
)

// validateConditions 校验条件分支：
//   - condition 节点的 op/value 必须非空，且 op 在支持集合内
//   - source=param：param 必须非空
//   - source=taskResult：task/result 必须非空；task 必须是该条件的严格上游、
//     非 condition 节点，且其插件声明了对应 result
//   - condition 节点的出边必须带 branch（yes/no），且至少一条
//   - 非 condition 节点的出边不能带 branch
func validateConditions(g *model.Graph, reg nodetype.Registry) []string {
	isCond := map[string]bool{}
	byID := map[string]model.Node{}
	var errs []string
	for _, n := range g.Nodes {
		isCond[n.ID] = n.Type == model.NodeTypeCondition
		byID[n.ID] = n
		// 参数完整性：op/value 非空 + op 合法 + 变量来源合法
		if isCond[n.ID] {
			o, _ := n.Params["op"].(string)
			v, _ := n.Params["value"].(string)
			source, _ := n.Params["source"].(string)
			if source == "" {
				source = condSourceParam
			}
			if o == "" {
				errs = append(errs, fmt.Sprintf("条件节点 %s 缺少 op（比较运算符）", n.ID))
			} else if !conditionOps[o] {
				errs = append(errs, fmt.Sprintf("条件节点 %s 的 op %q 不支持（可选 ==/!=/contains/notcontains）", n.ID, o))
			}
			if v == "" {
				errs = append(errs, fmt.Sprintf("条件节点 %s 缺少 value（比较值）", n.ID))
			}
			switch source {
			case condSourceParam:
				if p, _ := n.Params["param"].(string); p == "" {
					errs = append(errs, fmt.Sprintf("条件节点 %s 缺少 param（被比较的参数名）", n.ID))
				}
			case condSourceTaskResult:
				task, _ := n.Params["task"].(string)
				result, _ := n.Params["result"].(string)
				if task == "" {
					errs = append(errs, fmt.Sprintf("条件节点 %s 缺少 task（被比较结果所属的上游节点 id）", n.ID))
				}
				if result == "" {
					errs = append(errs, fmt.Sprintf("条件节点 %s 缺少 result（被比较的结果名）", n.ID))
				}
				if task != "" && result != "" {
					errs = append(errs, validateTaskResultRef(g, n.ID, isCond, byID, reg, task, result)...)
				}
			default:
				errs = append(errs, fmt.Sprintf("条件节点 %s 的 source %q 不支持（可选 param/taskResult）", n.ID, source))
			}
		}
	}
	condOut := map[string]int{}
	for _, e := range g.Edges {
		if isCond[e.Source] {
			if e.Branch != model.BranchYes && e.Branch != model.BranchNo {
				errs = append(errs, fmt.Sprintf("条件节点 %s 的出边必须指定 yes/no 分支", e.Source))
			} else {
				condOut[e.Source]++
			}
		} else if e.Branch != "" {
			errs = append(errs, fmt.Sprintf("普通节点 %s 的出边不能带分支标记（%s）", e.Source, e.Branch))
		}
	}
	for id := range isCond {
		if isCond[id] && condOut[id] == 0 {
			errs = append(errs, fmt.Sprintf("条件节点 %s 至少需要一条 yes/no 出边", id))
		}
	}
	return errs
}

// validateTaskResultRef 校验 condition 引用上游任务结果的合法性：
//   - task 必须存在、非 condition 节点
//   - task 必须是 cond 的严格上游（沿入边可达），保证结果在判断时已产出
//   - task 的插件必须声明了该 result（Meta.Results 来自 task.yaml）
func validateTaskResultRef(g *model.Graph, condID string, isCond map[string]bool, byID map[string]model.Node, reg nodetype.Registry, task, result string) []string {
	var errs []string
	upstream := byID[task]
	if upstream.ID == "" {
		errs = append(errs, fmt.Sprintf("条件节点 %s 引用的上游节点 %s 不存在", condID, task))
		return errs
	}
	if isCond[task] {
		errs = append(errs, fmt.Sprintf("条件节点 %s 不能引用 condition 节点 %s 的结果（控制节点不产出结果）", condID, task))
		return errs
	}
	// 严格上游：沿入边从 condID 回溯可达 task
	pred := map[string][]string{}
	for _, e := range g.Edges {
		pred[e.Target] = append(pred[e.Target], e.Source)
	}
	reach := map[string]bool{}
	stack := []string{condID}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range pred[u] {
			if !reach[p] {
				reach[p] = true
				stack = append(stack, p)
			}
		}
	}
	if !reach[task] {
		errs = append(errs, fmt.Sprintf("条件节点 %s 引用的节点 %s 不是其上游（无法保证判断时结果已产出）", condID, task))
		return errs
	}
	plugin, ok := reg.Get(upstream.Type)
	if !ok {
		return errs // 未知类型已在其他校验中报错
	}
	for _, r := range plugin.Meta().Results {
		if r.Value == result {
			return nil
		}
	}
	errs = append(errs, fmt.Sprintf("节点 %s(%s) 未声明 result %q（可选：%s）", task, upstream.Type, result, resultNames(plugin)))
	return errs
}

// resultNames 拼接插件 result 名列表，供错误提示。
func resultNames(plugin nodetype.Node) string {
	var out []string
	for _, r := range plugin.Meta().Results {
		out = append(out, r.Value)
	}
	if len(out) == 0 {
		return "（无）"
	}
	return strings.Join(out, ", ")
}

// hasCycle 基于 DFS 三色标记检测有向环。
func hasCycle(g *model.Graph) bool {
	adj := map[string][]string{}
	for _, e := range g.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(u string) bool
	visit = func(u string) bool {
		color[u] = gray
		for _, w := range adj[u] {
			switch color[w] {
			case gray:
				return true // 回边 → 有环
			case white:
				if visit(w) {
					return true
				}
			}
		}
		color[u] = black
		return false
	}
	for _, n := range g.Nodes {
		if color[n.ID] == white && visit(n.ID) {
			return true
		}
	}
	return false
}

// orphanNodes 找出既无入边也无出边的节点（孤立）。
// 注：单节点流水线是合法的（允许 0 条边）。
func orphanNodes(g *model.Graph) []string {
	if len(g.Nodes) <= 1 {
		return nil
	}
	deg := map[string]int{}
	for _, e := range g.Edges {
		deg[e.Source]++
		deg[e.Target]++
	}
	var out []string
	for _, n := range g.Nodes {
		if deg[n.ID] == 0 {
			out = append(out, fmt.Sprintf("节点 %s 是孤立节点（无连接）", n.ID))
		}
	}
	return out
}

// Topological 返回一个拓扑序（用于编译器排 runAfter）。
// 若仍有环，返回 nil（调用方应先 Validate）。
func Topological(g *model.Graph) []string {
	indeg := map[string]int{}
	adj := map[string][]string{}
	ids := []string{}
	for _, n := range g.Nodes {
		indeg[n.ID] = 0
		ids = append(ids, n.ID)
	}
	for _, e := range g.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
		indeg[e.Target]++
	}
	queue := []string{}
	for _, id := range ids {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	order := []string{}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		order = append(order, u)
		for _, w := range adj[u] {
			indeg[w]--
			if indeg[w] == 0 {
				queue = append(queue, w)
			}
		}
	}
	if len(order) != len(ids) {
		return nil // 有环
	}
	return order
}

// Format 便于日志。
func (r Result) Format() string {
	return strings.Join(r.Errors, "; ")
}
