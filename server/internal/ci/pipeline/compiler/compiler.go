package compiler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/pipeline/validator"
)

// Compiler 把 DSL Graph 编译成 Tekton 资源（Pipeline + 所需 Task/PipelineRun）。
//
// 流程：
//
//	Graph → 拓扑排序（validator.Topological）
//	      → 每个节点调用 Node.RenderTask 生成 Task 片段
//	      → 按 edges 计算 runAfter
//	      → 组装 Tekton Pipeline YAML
//	      → （运行时）生成 PipelineRun + Workspace/Secret/SA
//
// 输出 YAML 存入 pipeline_versions.compiled_yaml，与 graph_json 并存。
type Compiler struct {
	Nodes nodetype.Registry
}

func New(reg nodetype.Registry) *Compiler { return &Compiler{Nodes: reg} }

// PipelineSpec 是编译产物的内存表示，再序列化为 YAML。
type PipelineSpec struct {
	APIVersion string       `json:"apiVersion" yaml:"apiVersion"`
	Kind       string       `json:"kind" yaml:"kind"`
	Metadata   Metadata     `json:"metadata" yaml:"metadata"`
	Spec       PipelineBody `json:"spec" yaml:"spec"`
}

type Metadata struct {
	Name      string            `json:"name" yaml:"name"`
	Namespace string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type PipelineBody struct {
	Params     []PipelineParam   `json:"params,omitempty" yaml:"params,omitempty"`
	Workspaces []TektonWorkspace `json:"workspaces,omitempty" yaml:"workspaces,omitempty"`
	Tasks      []TektonPTask     `json:"tasks" yaml:"tasks"`
}

// PipelineParam 是 Pipeline 级参数声明（spec.params 是 ParamSpec，不是赋值；
// 取值由 PipelineRun.spec.params 提供）。
type PipelineParam struct {
	Name string `json:"name" yaml:"name"`
	Type string `json:"type,omitempty" yaml:"type,omitempty"`
}

type TektonPTask struct {
	Name       string             `json:"name" yaml:"name"`
	TaskRef    *TaskRef           `json:"taskRef,omitempty" yaml:"taskRef,omitempty"`
	TaskSpec   *nodetype.TaskSpec `json:"taskSpec,omitempty" yaml:"taskSpec,omitempty"`
	RunAfter   []string           `json:"runAfter,omitempty" yaml:"runAfter,omitempty"`
	When       []WhenExpr         `json:"when,omitempty" yaml:"when,omitempty"` // 条件分支 when 表达式（结构化，对齐 Tekton PipelineWhen）
	Params     []TektonParam      `json:"params,omitempty" yaml:"params,omitempty"`
	Workspaces []PWorkspace       `json:"workspaces,omitempty" yaml:"workspaces,omitempty"`
}

// WhenExpr 是 Tekton v1 Pipeline 任务级 when 表达式。
// 字段名与 Tekton 官方 schema 对齐：input/operator/values 三选一组合，
// 或 cel（CEL 表达式，与 input/operator/values 互斥）。
// Tekton 不接受整串表达式字符串（如 `$(params.X)=="main"`），必须结构化。
type WhenExpr struct {
	Input    string   `json:"input,omitempty" yaml:"input,omitempty"`
	Operator string   `json:"operator,omitempty" yaml:"operator,omitempty"` // in / notin
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
	CEL      string   `json:"cel,omitempty" yaml:"cel,omitempty"`
}

type TaskRef struct {
	Name string `json:"name" yaml:"name"`
}

type PWorkspace struct {
	Name      string `json:"name" yaml:"name"`
	Workspace string `json:"workspace,omitempty" yaml:"workspace,omitempty"`
}

type TektonParam struct {
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value" yaml:"value"`
}

type TektonWorkspace struct {
	Name     string `json:"name" yaml:"name"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// workspaceBindName task workspace → Pipeline workspace 的映射规则：
// "cache" 映射到项目级依赖缓存 workspace（Pipeline 上声明为 optional，
// 运行时按节点的 cache 参数决定是否绑定 PVC）；其余一律映射到共享工作区 shared。
func workspaceBindName(name string) string {
	if name == "cache" {
		return "cache"
	}
	return "shared"
}

// Compile 生成 Pipeline 定义。
//
// 条件分支：condition 节点不生成 Task，而是把 yes/no 出边传播为下游 Task 的
// `when` 表达式（引用 Pipeline 参数 $(params.X)）。约束沿 DAG 归纳传播：
// 同一变量在多条入路上取值冲突（既有 true 又有 false，或一条路不带约束）时，
// 该变量对该 Task 不加约束（汇合 / 旁路场景正确放行）。
func (c *Compiler) Compile(g *model.Graph, namespace string) (*PipelineSpec, error) {
	order := validator.Topological(g)
	if order == nil {
		return nil, fmt.Errorf("无法拓扑排序：图中存在环")
	}

	byID := map[string]model.Node{}
	isCond := map[string]bool{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
		isCond[n.ID] = n.Type == model.NodeTypeCondition
	}

	// 前驱（按边），condition 节点在 runAfter 中会被穿透
	predEdges := map[string][]model.Edge{}
	for _, e := range g.Edges {
		predEdges[e.Target] = append(predEdges[e.Target], e)
	}

	// 收集条件 → 变量。Pipeline 参数只声明被 condition 实际引用的（含 GIT_*）：
	// 无 git/条件步骤的流水线不应背一堆空参数（PipelineRun 传参必须与声明一致）。
	// 变量两种来源：param（触发参数，需声明 Pipeline 参数）/ taskResult（上游任务
	// 结果，Tekton 直接以 $(tasks.T.results.R) 引用，无需 Pipeline 参数）。
	condOf := map[string]condExpr{} // conditionID -> expr
	paramSet := map[string]bool{}   // 需声明的 Pipeline 参数
	varSeen := map[string]bool{}    // B12 唯一性（按变量 key）
	for _, n := range g.Nodes {
		if n.Type != model.NodeTypeCondition {
			continue
		}
		source := strParam(n.Params, "source", "param")
		o := strParam(n.Params, "op", "==")
		v := strParam(n.Params, "value", "")
		var varKey, input, celRef string
		switch source {
		case "param":
			p := strParam(n.Params, "param", "GIT_BRANCH")
			varKey = p
			input = "$(params." + p + ")"
			celRef = `params["` + p + `"]`
		case "taskResult":
			task := strParam(n.Params, "task", "")
			result := strParam(n.Params, "result", "")
			if task == "" || result == "" {
				return nil, fmt.Errorf("条件节点 %s 引用任务结果缺少 task/result（source=taskResult 时必填）", n.ID)
			}
			varKey = "result:" + task + "." + result
			input = "$(tasks." + task + ".results." + result + ")"
			celRef = fmt.Sprintf(`tasks["%s"].results["%s"]`, task, result)
		default:
			return nil, fmt.Errorf("条件节点 %s 的 source %q 不支持（可选 param/taskResult）", n.ID, source)
		}
		// B12：同一变量被多个 condition 约束是用户错误（取值语义无法归纳），直接报错
		if varSeen[varKey] {
			return nil, fmt.Errorf("多个条件节点引用同一变量 %s（同一变量不允许被多个 condition 约束）", varKey)
		}
		varSeen[varKey] = true
		if source == "param" {
			paramSet[varKey] = true
		}
		condOf[n.ID] = condExpr{varKey: varKey, input: input, celRef: celRef, op: o, value: v}
	}

	// 约束归纳：constraints[node] = var -> {literal}，literal ∈ {"true","false"}
	// 空集合 = 无约束。
	constraints := map[string]map[string]map[string]bool{}
	for _, id := range order {
		in := predEdges[id]
		cset := map[string]map[string]bool{}
		if len(in) == 0 {
			constraints[id] = cset
			continue
		}
		for _, e := range in {
			up := constraints[e.Source]
			for varKey, lits := range up {
				mergeLit(cset, varKey, lits)
			}
			if isCond[e.Source] {
				expr, ok := condOf[e.Source]
				if !ok {
					continue
				}
				lit := "true"
				if e.Branch == model.BranchNo {
					lit = "false"
				}
				mergeLit(cset, expr.varKey, map[string]bool{lit: true})
			}
		}
		constraints[id] = cset
	}

	spec := &PipelineSpec{
		APIVersion: "tekton.dev/v1",
		Kind:       "Pipeline",
		Metadata: Metadata{
			Name:      g.Name,
			Namespace: namespace,
			// label 值必须满足 K8s 约束（≤63 字符、字母数字开头结尾），
			// 流水线名可含中文/空格，直接透传会让 apply 在运行期被拒。
			// 实际资源名由 StartRun 用 pl-<id>-v<version> 覆盖，这里只保证值合法。
			Labels: map[string]string{"ci-platform.io/name": labelSafe(g.Name)},
		},
		Spec: PipelineBody{
			// shared = 每次运行的独立工作区；cache = 项目级依赖缓存（optional：
			// 运行时未绑定时不影响 PipelineRun 创建，任务内脚本按空串跳过缓存逻辑）
			Workspaces: []TektonWorkspace{
				{Name: "shared"},
				{Name: "cache", Optional: true},
			},
		},
	}
	// Pipeline 参数（运行时由 PipelineRun 填值），按名排序保证稳定输出
	paramNames := make([]string, 0, len(paramSet))
	for name := range paramSet {
		paramNames = append(paramNames, name)
	}
	sort.Strings(paramNames)
	for _, name := range paramNames {
		spec.Spec.Params = append(spec.Spec.Params, PipelineParam{Name: name, Type: "string"})
	}

	// 任务名集合（condition 控制节点不生成 Task）；供 runAfter 编译期自检
	taskSet := map[string]bool{}
	for _, id := range order {
		if !isCond[id] {
			taskSet[id] = true
		}
	}

	// git-clone 节点 ID（首个）：build-image 等节点的默认 tag 引用其 git_commit
	// 结果。历史默认值有两种坏写法，统一改写为实际节点 ID 的引用：
	//   $(params.git_commit)                       —— 误把任务结果当 Pipeline 参数
	//   $(tasks.git-clone.results.git_commit)      —— git-clone 节点被改名后引用悬空
	// 没有该类型节点时交由 validateTaskRefs 报错（引用注定解析不出）。
	gitCloneID := ""
	for _, id := range order {
		if !isCond[id] && byID[id].Type == "git-clone" {
			gitCloneID = id
			break
		}
	}

	for _, id := range order {
		n := byID[id]
		if isCond[id] {
			continue // 控制节点不生成 Task
		}
		plugin, ok := c.Nodes.Get(n.Type)
		if !ok {
			return nil, fmt.Errorf("节点 %s 类型 %s 未注册", id, n.Type)
		}
		taskSpec, err := plugin.RenderTask(n.Params)
		if err != nil {
			return nil, fmt.Errorf("渲染节点 %s: %w", id, err)
		}
		injectPreShell(&taskSpec, n.Params)
		rewriteGitCloneRefs(&taskSpec, id, gitCloneID)

		pt := TektonPTask{Name: id, TaskSpec: &taskSpec}
		for _, ws := range taskSpec.Workspaces {
			pt.Workspaces = append(pt.Workspaces, PWorkspace{Name: ws.Name, Workspace: workspaceBindName(ws.Name)})
		}

		// runAfter：前驱若是 condition，则穿透到 condition 的前驱 task
		pt.RunAfter = runAfterThrough(id, byID, isCond, predEdges)

		// 编译期自检：runAfter 必须全部指向已生成的 task（condition 穿透后
		// 若仍引用不存在的节点，直接报平台错误，而不是让 Tekton webhook 报
		// 一句 "depends on X but X wasn't present" 的笼统拒绝）
		for _, ref := range pt.RunAfter {
			if !taskSet[ref] {
				return nil, fmt.Errorf("节点 %s 的依赖 %s 不存在于流水线任务中（可能是残留的边引用了已删除节点，请检查画布连线）", id, ref)
			}
		}

		// when 表达式（结构化 WhenExpr，对齐 Tekton PipelineWhen 字段）
		for varName, lits := range constraints[id] {
			expr, ok := condVarExpr(varName, condOf)
			if !ok {
				continue
			}
			for lit := range lits {
				if lit == "true" {
					pt.When = append(pt.When, expr.positive)
				} else {
					pt.When = append(pt.When, expr.negative)
				}
			}
		}
		sort.Slice(pt.When, func(i, j int) bool { return whenKey(pt.When[i]) < whenKey(pt.When[j]) })

		spec.Spec.Tasks = append(spec.Spec.Tasks, pt)
	}

	// 编译期校验 Tekton 变量引用，把 webhook 的笼统 "non-existent variable"
	// 拒绝提前为指明节点与写法的平台错误
	if err := validateTaskRefs(spec); err != nil {
		return nil, err
	}

	return spec, nil
}

// rewriteGitCloneRefs 把任务 spec 内对 git-clone 结果的历史坏引用改写为实际
// git-clone 节点的 $(tasks.<id>.results.<结果名>)（无 git-clone 节点时不改写，
// 交给 validateTaskRefs 报错）。按 $(tasks.git-clone.results. 前缀统一改写，
// 覆盖 git_commit / build_tag / git_branch 等全部结果；经 JSON 序列化做全文替换，
// 覆盖 script/env/args 等任意字段。
func rewriteGitCloneRefs(spec *nodetype.TaskSpec, taskID, gitCloneID string) {
	if gitCloneID == "" || taskID == gitCloneID {
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return
	}
	s := string(raw)
	const legacy = "$(params.git_commit)"
	const fixedPrefix = "$(tasks.git-clone.results."
	if !strings.Contains(s, legacy) && !strings.Contains(s, fixedPrefix) {
		return
	}
	s = strings.ReplaceAll(s, legacy, "$(tasks."+gitCloneID+".results.git_commit)")
	s = strings.ReplaceAll(s, fixedPrefix, "$(tasks."+gitCloneID+".results.")
	var out nodetype.TaskSpec
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return // 改写失败保持原样，由 validateTaskRefs 给出明确错误
	}
	*spec = out
}

// tektonRefRe 匹配 taskSpec 内的 Tekton 变量引用：$(params.X) / $(tasks.T.results.R)。
// $(workspaces.*), $(results.*), $(context.*) 等按 Tekton 语义固定可用，不在校验范围。
var tektonRefRe = regexp.MustCompile(`\$\((params|tasks)\.([a-zA-Z0-9._-]+)\)`)

// validateTaskRefs 校验任务 spec 里的 Tekton 变量引用，替代 webhook 的
// "non-existent variable in ..." 笼统拒绝：
//   - $(params.NAME)：嵌入 taskSpec 内 $(params.X) 解析的是该 Task 自身声明的
//     参数，不是 Pipeline 参数——历史默认值曾误用后者导致 apply 被 webhook 拒绝；
//   - $(tasks.T.results.R)：T 必须是本 Pipeline 生成的任务且声明了结果 R，
//     且不允许引用自身结果（结果只能被下游任务消费）。
func validateTaskRefs(spec *PipelineSpec) error {
	taskParams := map[string]map[string]bool{}
	taskResults := map[string]map[string]bool{}
	for i := range spec.Spec.Tasks {
		t := &spec.Spec.Tasks[i]
		params := map[string]bool{}
		for _, p := range t.TaskSpec.Params {
			params[p.Name] = true
		}
		results := map[string]bool{}
		for _, r := range t.TaskSpec.Results {
			results[r.Name] = true
		}
		taskParams[t.Name] = params
		taskResults[t.Name] = results
	}
	for i := range spec.Spec.Tasks {
		t := &spec.Spec.Tasks[i]
		if t.TaskSpec == nil {
			continue
		}
		raw, err := json.Marshal(t.TaskSpec)
		if err != nil {
			continue
		}
		for _, m := range tektonRefRe.FindAllStringSubmatch(string(raw), -1) {
			switch m[1] {
			case "params":
				if !taskParams[t.Name][m[2]] {
					return fmt.Errorf("任务 %s 引用了未声明的参数 $(params.%s)：嵌入 taskSpec 内 $(params.X) 只能引用该任务自身的参数；引用上游任务结果请写 $(tasks.<任务名>.results.<结果名>)", t.Name, m[2])
				}
			case "tasks":
				rest := m[2]
				dot := strings.Index(rest, ".results.")
				if dot < 0 {
					return fmt.Errorf("任务 %s 的引用 $(tasks.%s) 格式非法（应为 $(tasks.<任务名>.results.<结果名>)）", t.Name, rest)
				}
				tname, rname := rest[:dot], rest[dot+len(".results."):]
				if tname == t.Name {
					return fmt.Errorf("任务 %s 引用了自身的结果 $(tasks.%s.results.%s)：结果只能被下游任务引用", t.Name, tname, rname)
				}
				if _, ok := taskResults[tname]; !ok {
					return fmt.Errorf("任务 %s 引用了不存在的任务结果 $(tasks.%s.results.%s)：流水线中没有名为 %s 的任务节点（可能是节点被改名/删除，请重新在参数里选择上游结果）", t.Name, tname, rname, tname)
				}
				if !taskResults[tname][rname] {
					return fmt.Errorf("任务 %s 引用的结果 $(tasks.%s.results.%s) 不存在：%s 未声明该结果", t.Name, tname, rname, tname)
				}
			}
		}
	}
	return nil
}

// condExpr 一个条件节点对某个变量的比较表达式。
// varKey 是约束归纳用的不透明 key：param 变量为参数名，taskResult 变量为
// "result:<task>.<result>"；input/celRef 是同一变量在 Tekton 表达式与 CEL
// 两种上下文里的写法。
type condExpr struct {
	varKey  string
	input   string
	celRef  string
	op      string
	value   string
}

// condVarExpr 由变量 key 反查条件表达式（一个变量至多一个条件，Compile 阶段 B12 保证）。
// positive = 变量满足条件（yes 分支）；negative = 其否定（no 分支）。
func condVarExpr(varKey string, condOf map[string]condExpr) (ce struct{ positive, negative WhenExpr }, ok bool) {
	for _, e := range condOf {
		if e.varKey == varKey {
			return struct{ positive, negative WhenExpr }{
				positive: buildWhenExpr(e, e.op),
				negative: buildWhenExpr(e, negateOp(e.op)),
			}, true
		}
	}
	return ce, false
}

// buildWhenExpr 把一个比较表达式编译成结构化 WhenExpr：
//   - == / !=：Tekton 原生 operator（in / notin）
//   - contains / notcontains：Tekton when 无原生包含运算符，走 cel 字段
func buildWhenExpr(e condExpr, op string) WhenExpr {
	switch op {
	case "==":
		return WhenExpr{Input: e.input, Operator: "in", Values: []string{e.value}}
	case "!=":
		return WhenExpr{Input: e.input, Operator: "notin", Values: []string{e.value}}
	case "contains":
		return WhenExpr{CEL: e.celRef + " contains " + celString(e.value)}
	case "notcontains":
		return WhenExpr{CEL: e.celRef + " !contains " + celString(e.value)}
	default:
		// 兜底按相等处理（validator 已限制 op ∈ {==,!=,contains,notcontains}）
		return WhenExpr{Input: e.input, Operator: "in", Values: []string{e.value}}
	}
}

// celString 生成 CEL 字符串字面量（转义反斜杠与双引号）。
func celString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// whenKey 给 WhenExpr 一个稳定的排序键（保证编译输出确定）。
func whenKey(w WhenExpr) string {
	if w.CEL != "" {
		return "cel:" + w.CEL
	}
	return w.Input + " " + w.Operator + " " + strings.Join(w.Values, ",")
}

// ConditionVarNames 返回所有 condition 节点引用的 Pipeline 参数名集合
// （仅 source=param / 缺省的条件；taskResult 条件引用的是任务结果，
// 不需要 Pipeline 参数）。
// 运行时（StartRun）据此决定 PipelineRun 是否要传 GIT_* 参数——
// PipelineRun 传参必须与 Pipeline 声明一致，未声明的参数会导致 apply 失败。
func ConditionVarNames(g *model.Graph) map[string]bool {
	out := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Type != model.NodeTypeCondition {
			continue
		}
		if strParam(n.Params, "source", "param") != "param" {
			continue
		}
		out[strParam(n.Params, "param", "GIT_BRANCH")] = true
	}
	return out
}

func negateOp(op string) string {
	switch op {
	case "==":
		return "!="
	case "!=":
		return "=="
	case "contains":
		return "notcontains"
	case "notcontains":
		return "contains"
	default:
		return "!="
	}
}

// injectPreShell 依据节点通用参数 preShell 在 Task 首部注入 pre-shell step
// （所有节点的"执行 shell"统一入口）：与首个主 step 同镜像，cd 到首个 task 级
// workspace（Tekton 把 task 级 workspace 默认挂进全部 step），set -e 下执行
// 用户命令。preShell 为空或模板无 step 时不注入。
func injectPreShell(spec *nodetype.TaskSpec, params map[string]interface{}) {
	pre := strings.TrimSpace(strParam(params, "preShell", ""))
	if pre == "" || len(spec.Steps) == 0 {
		return
	}
	for _, s := range spec.Steps {
		if s.Name == "pre-shell" {
			return // 模板已含同名 step，避免重名
		}
	}
	script := "#!/bin/sh\nset -e\n"
	if len(spec.Workspaces) > 0 {
		script += "cd $(workspaces." + spec.Workspaces[0].Name + ".path)\n"
	}
	script += pre + "\n"
	spec.Steps = append([]nodetype.TektonStep{{
		Name:   "pre-shell",
		Image:  spec.Steps[0].Image,
		Script: script,
	}}, spec.Steps...)
}

// runAfterThrough 计算 task 的 runAfter：前驱是 condition 时穿透到其前驱 task。
func runAfterThrough(id string, byID map[string]model.Node, isCond map[string]bool, predEdges map[string][]model.Edge) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(node string)
	walk = func(node string) {
		for _, e := range predEdges[node] {
			src := e.Source
			if isCond[src] {
				walk(src) // 穿透 condition
			} else {
				if !seen[src] {
					seen[src] = true
					out = append(out, src)
				}
			}
		}
	}
	walk(id)
	sort.Strings(out)
	return out
}

// mergeLit 把 lits 合并进 set[varName]；与已有取值冲突时清空该变量（视为无约束）。
func mergeLit(set map[string]map[string]bool, varName string, lits map[string]bool) {
	cur, ok := set[varName]
	if !ok {
		m := map[string]bool{}
		for l := range lits {
			m[l] = true
		}
		set[varName] = m
		return
	}
	for l := range lits {
		if cur[l] {
			continue
		}
		// 新取值与已有不同 → 冲突 → 清空（无约束）
		delete(set, varName)
		return
	}
}

func strParam(params map[string]interface{}, key, def string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

// ToYAML 序列化 Pipeline 为 YAML 文本（存 compiled_yaml，供审计与人工排查）。
func ToYAML(spec *PipelineSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("nil pipeline spec")
	}
	b, err := yaml.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("yaml 序列化 Pipeline: %w", err)
	}
	return string(b), nil
}

// labelSafe 把任意字符串净化成合法 K8s label 值：
// 非法字符折叠为 '-'，掐头尾的 '-'/_/'.'，超长截断到 63，空结果兜底 "pipeline"。
func labelSafe(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	// 折叠连续 '-'
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-_.")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-_.")
	}
	if out == "" {
		out = "pipeline"
	}
	return out
}
