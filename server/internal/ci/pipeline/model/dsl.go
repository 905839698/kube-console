package model

import "encoding/json"

// Graph 是 CI Pipeline DSL——平台自有的流水线描述，与 Tekton 解耦。
// 前端画布的 nodes/edges 直接序列化为 Graph 存库（pipeline_versions.graph_json）。
type Graph struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

// Node 画布上的一个节点实例。
type Node struct {
	ID     string                 `json:"id"`     // 画布内唯一（如 "git"、"scan"）
	Type   string                 `json:"type"`   // 节点类型（如 "git-clone"）
	Params map[string]interface{} `json:"params"` // 属性值，对应节点 schema
	// Position 前端布局用，后端编译可忽略。
	Position *Position `json:"position,omitempty"`
}

// Position 节点坐标（前端 Dagre/拖拽产生）。
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge 有向边：source 完成后 target 才能开始。
// Branch 仅在 source 为 condition 节点时有效："yes"/"no"，
// 编译为 Tekton `when` 表达式（true/false 分支）。
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Branch string `json:"branch,omitempty"` // "" | "yes" | "no"
}

// 控制节点类型常量。
const (
	NodeTypeCondition = "condition"

	BranchYes = "yes"
	BranchNo  = "no"
)

// ValidateIDs 做最基础的引用完整性检查（edges 引用的节点必须存在）。
// 完整图校验（环、孤立、参数）见 validator。
func (g *Graph) ValidateIDs() []string {
	var errs []string
	ids := make(map[string]bool, len(g.Nodes))
	seen := make(map[string]bool)
	for _, n := range g.Nodes {
		if seen[n.ID] {
			errs = append(errs, "节点 id 重复: "+n.ID)
		}
		seen[n.ID] = true
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if !ids[e.Source] {
			errs = append(errs, "边引用了不存在的源节点: "+e.Source)
		}
		if !ids[e.Target] {
			errs = append(errs, "边引用了不存在的目标节点: "+e.Target)
		}
	}
	return errs
}

// MustMarshal 便于测试/调试。
func (g *Graph) MustMarshal() string {
	b, _ := json.MarshalIndent(g, "", "  ")
	return string(b)
}
