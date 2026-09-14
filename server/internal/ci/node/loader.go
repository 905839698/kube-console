// Package node 文件型节点插件加载器：扫描 nodes/<type>/ 目录，
// 从内嵌 FS（go:embed）加载全部插件并注册到 Registry。
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"kube-console/server/internal/ci/nodetype"
)

// 节点插件规范：每个节点是 nodes/<type>/ 目录，包含
//
//	node.yaml    元数据（type/name/category/icon/version/description）
//	schema.json  属性 JSON Schema（驱动前端表单 + 后端校验 + 参数默认值）
//	task.yaml    Tekton Task 模板（带 {{param}} 占位 / params / workspaces / steps / results）
//	result.yaml  产出映射（artifacts / outputs）
//
// 设计约束：schema 是唯一事实来源——前端表单、后端校验、Task 参数渲染三方
// 都读它，避免“三处漂移”。

// Loader 扫描节点目录，加载全部节点插件并注册。
type Loader struct {
	reg nodetype.Registry
	// globals 平台级全局模板变量（如 imageRegistry），渲染时注入且优先于节点参数
	globals map[string]interface{}
}

// NewLoader 创建加载器。reg 通常是 CI 依赖集里的节点注册表。
func NewLoader(reg nodetype.Registry) *Loader { return &Loader{reg: reg} }

// WithGlobals 设置全局模板变量（编译期 {{key}} 替换的最高优先级来源）。
func (l *Loader) WithGlobals(m map[string]interface{}) *Loader {
	l.globals = m
	return l
}

// LoadEmbedded 从内嵌 FS 加载全部节点插件（fsys 须以 nodes 目录为根，
// 即 fs.Sub(node.NodesFS, "nodes")）。
func (l *Loader) LoadEmbedded(fsys fs.FS) error {
	return l.loadRoot(fsys, ".")
}

// LoadDir 从磁盘目录加载（测试/自定义插件目录用）。
func (l *Loader) LoadDir(dir string) error {
	return l.loadRoot(os.DirFS(dir), ".")
}

func (l *Loader) loadRoot(fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // 允许节点目录不存在（测试环境）
		}
		return fmt.Errorf("read nodes dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := l.loadOne(fsys, path.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("load node %q: %w", e.Name(), err)
		}
	}
	return nil
}

func (l *Loader) loadOne(fsys fs.FS, dir string) error {
	var meta nodeMetaYAML
	raw, err := fs.ReadFile(fsys, path.Join(dir, "node.yaml"))
	if err != nil {
		return fmt.Errorf("read node.yaml: %w", err)
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return err
	}
	// type 以目录名为准，避免 node.yaml 与目录不一致
	typeName := path.Base(dir)
	meta.Type = typeName

	schemaRaw, err := fs.ReadFile(fsys, path.Join(dir, "schema.json"))
	if err != nil {
		return fmt.Errorf("missing schema.json: %w", err)
	}
	// schema.json 是属性定义的【唯一事实来源】：
	// 若存在则覆盖 node.yaml 中的内联 properties（node.yaml 仅兜底）。
	if props, err := parseSchema(schemaRaw); err != nil {
		return fmt.Errorf("parse schema.json: %w", err)
	} else if len(props) > 0 {
		meta.Properties = props
	}

	taskRaw, err := fs.ReadFile(fsys, path.Join(dir, "task.yaml"))
	if err != nil {
		return fmt.Errorf("missing task.yaml: %w", err)
	}
	// results 段是 Tekton results 的唯一事实来源，提取到 Meta 供前端选择
	// （条件分支按上游结果比较）。模板含 {{placeholder}} 时个别写法可能不是
	// 合法 YAML，解析失败不阻塞加载——RenderTask 渲染后仍会严格解析报错。
	var taskMeta struct {
		Results []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"results"`
	}
	if err := yaml.Unmarshal(taskRaw, &taskMeta); err == nil {
		for _, r := range taskMeta.Results {
			if r.Name == "" {
				continue
			}
			meta.Results = append(meta.Results, nodetype.PropOpt{Value: r.Name, Label: r.Name})
		}
	}

	resultRaw := []byte("{}")
	if b, err := fs.ReadFile(fsys, path.Join(dir, "result.yaml")); err == nil {
		resultRaw = b
	}

	n := &fileNode{
		meta:      meta,
		schemaRaw: schemaRaw,
		taskRaw:   string(taskRaw),
		resultRaw: string(resultRaw),
		globals:   l.globals,
	}
	l.reg.Register(n)
	return nil
}

// parseSchema 解析 schema.json 的 properties 数组。
// 结构与前端属性面板消费的一致（name/type/required/default/options/description）。
func parseSchema(raw []byte) ([]nodetype.PropSchema, error) {
	var s struct {
		Type       string                `json:"type"`
		Properties []nodetype.PropSchema `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return s.Properties, nil
}

// ============ 文件型节点实现 ============

// nodeMetaYAML 对应 node.yaml。
type nodeMetaYAML struct {
	Type        string `yaml:"type"`
	Name        string `yaml:"name"`
	Category    string `yaml:"category"`
	Icon        string `yaml:"icon"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
	// Properties 可选：若 schema.json 缺失，可在此内联声明。
	Properties []nodetype.PropSchema `yaml:"properties"`
	// Results 由 loader 从 task.yaml 的 results 段提取（非 node.yaml 字段）。
	Results []nodetype.PropOpt `yaml:"-"`
}

// fileNode 用“文件”实现 nodetype.Node 接口。
// RenderTask 用 task.yaml 模板 + 简单 {{param}} 替换生成 Task 片段。
type fileNode struct {
	meta      nodeMetaYAML
	schemaRaw []byte
	taskRaw   string
	resultRaw string
	globals   map[string]interface{}
}

func (f *fileNode) Meta() nodetype.Meta {
	return nodetype.Meta{
		Type:        f.meta.Type,
		Name:        f.meta.Name,
		Category:    f.meta.Category,
		Icon:        f.meta.Icon,
		Version:     f.meta.Version,
		Description: f.meta.Description,
		Properties:  f.meta.Properties,
		Results:     f.meta.Results,
	}
}

// Validate 基于 schema 做节点级校验：
//  1. required 字段必须存在且非空
//  2. select 类型的值必须落在 options 内
//  3. 类型校验：number 参数必须是数字、boolean 参数必须是布尔
//     （DSL 经 JSON 反序列化后数字为 float64、布尔为 bool，传字符串应报错）
func (f *fileNode) Validate(params map[string]interface{}) []string {
	var errs []string
	for _, p := range f.meta.Properties {
		v, ok := params[p.Name]
		if ok && v != nil {
			switch p.Type {
			case "number":
				if _, isNum := v.(float64); !isNum {
					errs = append(errs, fmt.Sprintf("参数 %s 必须是数字（实际 %v）", p.Name, v))
				}
			case "boolean":
				if _, isBool := v.(bool); !isBool {
					errs = append(errs, fmt.Sprintf("参数 %s 必须是布尔（实际 %v）", p.Name, v))
				}
			}
		}
		if !p.Required {
			if ok && v != nil && p.Type == "select" {
				if err := checkSelect(p, v); err != "" {
					errs = append(errs, err)
				}
			}
			continue
		}
		if !ok || empty(v) {
			errs = append(errs, fmt.Sprintf("缺少必填参数 %s", p.Name))
			continue
		}
		if p.Type == "select" {
			if err := checkSelect(p, v); err != "" {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

func checkSelect(p nodetype.PropSchema, v interface{}) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return ""
	}
	// 支持 "A,B" 多选（如 trivy severity）
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		found := false
		for _, o := range p.Options {
			if o.Value == part {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("参数 %s 的值 %q 不在可选项中", p.Name, part)
		}
	}
	return ""
}

// RenderTask 返回渲染后的 Task 片段（供 compiler 组装成 Pipeline）。
// 渲染前先以 schema 默认值补齐缺失参数，避免模板里残留 {{placeholder}}；
// 用户显式传的 nil 值不覆盖 schema 默认值。
func (f *fileNode) RenderTask(params map[string]interface{}) (nodetype.TaskSpec, error) {
	merged := map[string]interface{}{}
	for _, p := range f.Meta().Properties {
		if p.Default != nil {
			merged[p.Name] = p.Default
		}
	}
	for k, v := range params {
		if v == nil {
			continue // nil 不当有效值：不覆盖默认值，也不参与渲染
		}
		merged[k] = v
	}
	// 全局变量（imageRegistry 等）最后注入：平台级配置优先于节点参数，
	// 防止流水线图里误填同名参数把镜像仓库改坏
	for k, v := range f.globals {
		merged[k] = v
	}
	rendered := renderTemplate(f.taskRaw, merged)
	var spec nodetype.TaskSpec
	if err := yaml.Unmarshal([]byte(rendered), &spec); err != nil {
		// 渲染后解析失败直接报错：绝不把解析不了的模板当 shell 脚本塞进容器执行
		return nodetype.TaskSpec{}, fmt.Errorf("节点 %s 渲染后的 Task 模板不是合法 YAML: %w", f.meta.Type, err)
	}
	return spec, nil
}

// 模板占位符 {{key}}。
var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// renderTemplate 做 {{key}} 替换，按上下文分三档处理，保证渲染产物仍是合法 YAML：
//
//  1. 双引号段内（YAML "..." 值 / shell 双引号）：转义 \ 与 " 后替换。
//     YAML 双引号与 POSIX sh 双引号对这两个字符的转义语义一致，
//     值里含引号时既不会截断外层引号，也不会注入新的 YAML 结构。
//  2. 独占一个 YAML 标量（`key: {{k}}` 整行）：字符串值加双引号（YAML 安全），
//     数字/布尔不加引号。
//  3. 其余（嵌在大 token 里、shell 脚本块内）：原样替换（脚本上下文需要原样）。
func renderTemplate(tmpl string, merged map[string]interface{}) string {
	// 1) 双引号段内：转义替换
	tmpl = quotedSegRe.ReplaceAllStringFunc(tmpl, func(seg string) string {
		inner := seg[1 : len(seg)-1]
		if !strings.Contains(inner, "{{") {
			return seg
		}
		inner = placeholderRe.ReplaceAllStringFunc(inner, func(ph string) string {
			v, ok := merged[ph[2:len(ph)-2]]
			if !ok {
				return ph
			}
			return renderValue(v)
		})
		return `"` + inner + `"`
	})
	// 2) 独占标量：key: {{k}}（行首 key + 冒号 + 仅占位符）
	tmpl = standaloneRe.ReplaceAllStringFunc(tmpl, func(line string) string {
		m := standaloneRe.FindStringSubmatch(line)
		if len(m) != 4 {
			return line
		}
		v, ok := merged[m[2]]
		if !ok {
			return line
		}
		if _, isStr := v.(string); isStr {
			// 字符串加引号（含特殊字符时是 YAML 必需）；数字/布尔原样
			return m[1] + `"` + renderValue(v) + `"` + m[3]
		}
		return m[1] + renderValue(v) + m[3]
	})
	// 3) 其余位置：原样替换（脚本块 / 嵌入 token）
	for k, v := range merged {
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", fmt.Sprint(v))
	}
	// 兜底：无默认值且用户未填的参数，占位符替换为空串。
	// 若残留字面量，脚本里的 [ -n "{{x}}" ] 会恒真，把占位符当合法值拼进命令。
	tmpl = placeholderRe.ReplaceAllString(tmpl, "")
	return tmpl
}

var (
	// 双引号段（不含嵌套引号；模板里不存在跨行 YAML 双引号字符串）。
	quotedSegRe = regexp.MustCompile(`"[^"\n]*"`)
	// `key: {{k}}` 独占标量行（允许行尾注释）。
	standaloneRe = regexp.MustCompile(`(?m)^(\s*[\w.-]+\s*:\s*)\{\{(\w+)\}\}(\s*(?:#.*)?)$`)
)

// renderValue 按类型生成替换文本：字符串做 YAML 转义，数字/布尔直接打印。
func renderValue(v interface{}) string {
	if s, ok := v.(string); ok {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	return fmt.Sprint(v)
}

func (f *fileNode) ResultMapping() nodetype.ResultMap {
	var rm nodetype.ResultMap
	_ = yaml.Unmarshal([]byte(f.resultRaw), &rm)
	return rm
}

// ============ helpers ============

func empty(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	default:
		return false
	}
}
