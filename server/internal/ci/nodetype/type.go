// Package nodetype 定义节点插件的领域类型（叶子包，无内部依赖）。
// app / node / pipeline 等包都引用这里，避免 import cycle。
package nodetype

// Registry 是节点插件注册表接口。
type Registry interface {
	// List 返回所有已注册节点类型（供 GET /api/v1/node-types）。
	List() []Meta
	// Get 按 type 取节点定义。
	Get(nodeType string) (Node, bool)
	// Register 注册一个节点插件（启动时调用）。
	Register(n Node)
}

// Meta 是节点类型对外暴露的元数据（前端据此渲染节点面板）。
type Meta struct {
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	Category    string       `json:"category"`
	Icon        string       `json:"icon"`
	Version     string       `json:"version"`
	Description string       `json:"description,omitempty"`
	Properties  []PropSchema `json:"properties"`
	// Results 是节点产出的 Tekton result 名（来自 task.yaml 的 results 段），
	// 供前端"按上游任务结果分支"时选择可比较的变量。
	Results []PropOpt `json:"results,omitempty"`
}

// PropSchema 描述一个节点属性，驱动前端属性面板表单与后端校验。
type PropSchema struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"` // string/number/boolean/credential/select/text
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
	Options     []PropOpt   `json:"options,omitempty"`
	Description string      `json:"description,omitempty"`
	// OptionsSource 前端下拉选项的动态数据源标记（后端忽略，仅 UI 提示）：
	// git-branch/git-tag = 按同表单的 url+credential 调 /repo/refs 动态加载。
	OptionsSource string `json:"optionsSource,omitempty"`
}

type PropOpt struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Node 是一个节点插件的运行时表示。
type Node interface {
	Meta() Meta
	// Validate 校验节点参数是否合法。
	Validate(params map[string]interface{}) []string
	// RenderTask 把参数渲染成 Tekton Task 片段。
	RenderTask(params map[string]interface{}) (TaskSpec, error)
	// ResultMapping 返回节点产出（artifact / result）到平台的映射。
	ResultMapping() ResultMap
}

// TaskSpec 是渲染出的 Tekton Task 结构，可直接内嵌进 Pipeline 的 tasks[].taskSpec。
// 注意：Tekton TaskSpec 没有 name 字段（name 属于 Task CR 的 metadata），Name 仅供平台内部使用。
type TaskSpec struct {
	Name       string         `json:"-" yaml:"-"` // 内部用（节点 id），不进 CR
	Params     []TektonParam  `json:"params,omitempty" yaml:"params,omitempty"`
	Workspaces []TektonWS     `json:"workspaces,omitempty" yaml:"workspaces,omitempty"`
	Steps      []TektonStep   `json:"steps" yaml:"steps"`
	Results    []TektonResult `json:"results,omitempty" yaml:"results,omitempty"`
	Volumes    []Volume       `json:"volumes,omitempty" yaml:"volumes,omitempty"` // Task 级卷（凭证文件挂载用）
}

// TektonParam 是 Task 级参数声明（ParamSpec：name + type + default，不含 value）。
type TektonParam struct {
	Name        string      `json:"name" yaml:"name"`
	Type        string      `json:"type,omitempty" yaml:"type,omitempty"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Default     interface{} `json:"default,omitempty" yaml:"default,omitempty"`
}

type TektonWS struct {
	Name string `json:"name" yaml:"name"`
	// Optional 对应 Tekton WorkspaceDeclaration.optional。Pipeline 上声明为
	// optional 的 workspace（如 cache），引用它的 Task 必须同样声明 optional，
	// 否则 PipelineRun 会被 Tekton 校验拒绝（RequiredWorkspaceMarkedOptional）。
	Optional bool `json:"optional,omitempty" yaml:"optional,omitempty"`
}

type TektonStep struct {
	Name            string           `json:"name" yaml:"name"`
	Image           string           `json:"image" yaml:"image"`
	Script          string           `json:"script,omitempty" yaml:"script,omitempty"`
	Command         []string         `json:"command,omitempty" yaml:"command,omitempty"`
	Args            []string         `json:"args,omitempty" yaml:"args,omitempty"`
	Env             []EnvVar         `json:"env,omitempty" yaml:"env,omitempty"`
	EnvFrom         []StepEnvFrom    `json:"envFrom,omitempty" yaml:"envFrom,omitempty"` // 凭证 Secret 注入（runtime 按 credential 参数填充）
	VolumeMounts    []VolumeMount    `json:"volumeMounts,omitempty" yaml:"volumeMounts,omitempty"` // 凭证文件挂载（kubeconfig/dockerconfig）
	SecurityContext *SecurityContext `json:"securityContext,omitempty" yaml:"securityContext,omitempty"`
}

// EnvVar Step 环境变量。
type EnvVar struct {
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
}

// SecurityContext 简化的容器安全上下文（Buildah 需要 privileged）。
type SecurityContext struct {
	Privileged bool   `json:"privileged,omitempty" yaml:"privileged,omitempty"`
	RunAsUser  *int64 `json:"runAsUser,omitempty" yaml:"runAsUser,omitempty"`
}

// StepEnvFrom step 环境变量来源。
type StepEnvFrom struct {
	SecretRef *SecretRef `json:"secretRef,omitempty" yaml:"secretRef,omitempty"`
}

// SecretRef 引用 K8s Secret（凭证）。
type SecretRef struct {
	Name string `json:"name" yaml:"name"`
}

// VolumeMount Step 级卷挂载（kubeconfig/dockerconfig 凭证文件用）。
type VolumeMount struct {
	Name      string `json:"name" yaml:"name"`
	MountPath string `json:"mountPath" yaml:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
}

// Volume Task 级卷声明，承载凭证 Secret 的文件挂载。
type Volume struct {
	Name   string             `json:"name" yaml:"name"`
	Secret *SecretVolumeSource `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// SecretVolumeSource 把 K8s Secret 的指定 key 挂成文件。
type SecretVolumeSource struct {
	SecretName string     `json:"secretName" yaml:"secretName"`
	Items      []KeyToPath `json:"items,omitempty" yaml:"items,omitempty"`
}

// KeyToPath 把 Secret 的某 key 映射为某文件名。
type KeyToPath struct {
	Key  string `json:"key" yaml:"key"`
	Path string `json:"path" yaml:"path"`
}

type TektonResult struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	ValueFrom   string `json:"valueFrom,omitempty" yaml:"valueFrom,omitempty"`
}

// ResultMap 描述节点产出如何映射为平台 artifact / 变量。
type ResultMap struct {
	Artifacts []string `json:"artifacts,omitempty"`
	Outputs   []string `json:"outputs,omitempty"`
}
