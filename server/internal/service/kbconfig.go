// kbconfig.go 数据库参数管理（KubeBlocks 1.0 的 parameters.kubeblocks.io 契约，1.0.2 实测）：
//   - 1.0 把配置体系从 ConfigConstraint/Configuration 迁到了 parameters 组——实测该版本
//     ConfigConstraint 里是 0 个对象，别再走老接口
//   - 参数 schema 在 ParametersDefinition（集群级）：fileName + parametersSchema.schemaInJSON
//     （JSON Schema，参数在 topLevelKey 包裹下）+ dynamic/static/immutableParameters 三张清单
//   - 与组件的关联：ParamConfigRenderer.spec.componentDef == compDef（精确匹配）→ spec.parametersDefs[]
//   - 当前生效值在 ComponentParameter（<cluster>-<component>）spec.configItemDetails[].configFileParams
//   - 修改走 Reconfiguring OpsRequest（KBSubmitOps），operator 负责热载或滚动重启
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kube-console/server/internal/kube"
)

// KBParam 一个可调参数：schema 元数据 + 当前值
type KBParam struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"` // integer|number|boolean|string
	Default     string   `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Min         string   `json:"min,omitempty"`
	Max         string   `json:"max,omitempty"`
	Description string   `json:"description,omitempty"`
	Dynamic     bool     `json:"dynamic"`    // 动态参数：改完热载，不用重启
	Immutable   bool     `json:"immutable"`  // 不可修改（提交前直接拦）
	Current     string   `json:"current"`    // 当前生效值（ComponentParameter）
	InSchema    bool     `json:"inSchema"`   // false = 当前值里有但 schema 没声明的键
}

type KBConfigFile struct {
	Name   string    `json:"name"` // 配置文件名（my.cnf 等）
	PDName string    `json:"pdName,omitempty"`
	Reload string    `json:"reload,omitempty"` // Dynamic|Script|Restart（动态参数的生效方式）
	Params []KBParam `json:"params"`
}

type KBConfigSchema struct {
	Cluster   string          `json:"cluster"`
	Component string          `json:"component"`
	CompDef   string          `json:"compDef"`
	Files     []KBConfigFile  `json:"files"`
	Note      string          `json:"note,omitempty"`
}

// KBGetComponentConfig 组件的参数表单数据源：schema（ParametersDefinition）+ 当前值（ComponentParameter）
func KBGetComponentConfig(ctx context.Context, c *kube.Client, ns, cluster, component string) (*KBConfigSchema, error) {
	cl, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	comps, _, _ := unstructured.NestedSlice(cl.Object, "spec", "componentSpecs")
	compDef := ""
	for _, sp := range comps {
		if sm, ok := sp.(map[string]any); ok && sm["name"] == component {
			compDef, _, _ = unstructured.NestedString(sm, "componentDef")
			break
		}
	}
	if compDef == "" {
		return nil, fmt.Errorf("集群 %s 里没有组件 %s", cluster, component)
	}
	out := &KBConfigSchema{Cluster: cluster, Component: component, CompDef: compDef, Files: []KBConfigFile{}}

	// compDef → ParamConfigRenderer（精确匹配）→ ParametersDefinition 列表
	pcrs, err := c.Dynamic.Resource(kbPCRGVR).List(ctx, metav1.ListOptions{})
	if err != nil && !metaNoMatch(err) {
		return nil, err
	}
	pdNames := []string{}
	for i := range pcrs.Items {
		it := &pcrs.Items[i]
		if cd, _, _ := unstructured.NestedString(it.Object, "spec", "componentDef"); cd != compDef {
			continue
		}
		defs, _, _ := unstructured.NestedStringSlice(it.Object, "spec", "parametersDefs")
		pdNames = append(pdNames, defs...)
	}
	if len(pdNames) == 0 {
		out.Note = "该组件模板没有参数定义（ParametersDefinition），只支持编辑原始配置文件"
		return out, nil
	}
	current := kbCurrentParams(ctx, c, ns, cluster, component)
	for _, pdName := range pdNames {
		pd, err := c.Dynamic.Resource(kbPDGVR).Get(ctx, pdName, metav1.GetOptions{})
		if err != nil {
			continue // 单个 PD 取不到不阻断整个表单
		}
		f := kbParsePD(pd, current)
		if len(f.Params) > 0 || f.Name != "" {
			out.Files = append(out.Files, f)
		}
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	return out, nil
}

// kbCurrentParams 组件当前生效的参数（按文件归并多个 configItemDetails）
func kbCurrentParams(ctx context.Context, c *kube.Client, ns, cluster, component string) map[string]map[string]string {
	out := map[string]map[string]string{}
	cp, err := c.Dynamic.Resource(kbCPGVR).Namespace(ns).Get(ctx, cluster+"-"+component, metav1.GetOptions{})
	if err != nil {
		return out
	}
	details, _, _ := unstructured.NestedSlice(cp.Object, "spec", "configItemDetails")
	for _, d := range details {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		files, ok, _ := unstructured.NestedMap(dm, "configFileParams")
		if !ok {
			continue
		}
		for fileName, fv := range files {
			fm, ok := fv.(map[string]any)
			if !ok {
				continue
			}
			params, _, _ := unstructured.NestedMap(fm, "parameters")
			if out[fileName] == nil {
				out[fileName] = map[string]string{}
			}
			for k, v := range params {
				out[fileName][k] = kbScalar(v)
			}
		}
	}
	return out
}

// kbParsePD 把一个 ParametersDefinition 解析成参数表（schema 元数据 + 三张清单归类 + 当前值）
func kbParsePD(pd *unstructured.Unstructured, current map[string]map[string]string) KBConfigFile {
	f := KBConfigFile{}
	f.Name, _, _ = unstructured.NestedString(pd.Object, "spec", "fileName")
	f.PDName = pd.GetName()
	if f.Name == "" {
		f.Name = pd.GetName()
	}
	// 动态参数的生效方式：autoTrigger/unixSignalTrigger=热载；shell/tplScriptTrigger=脚本热载；
	// 都没有则改参数要滚动重启
	switch {
	case hasAny(pd, "spec", "reloadAction", "autoTrigger"), hasAny(pd, "spec", "reloadAction", "unixSignalTrigger"):
		f.Reload = "Dynamic"
	case hasAny(pd, "spec", "reloadAction", "shellTrigger"), hasAny(pd, "spec", "reloadAction", "tplScriptTrigger"):
		f.Reload = "Script"
	default:
		f.Reload = "Restart"
	}
	dyn := setOf(pd, "spec", "dynamicParameters")
	immutable := setOf(pd, "spec", "immutableParameters")

	props := kbSchemaParams(pd, f.Name)
	for name, raw := range props {
		p := KBParam{Name: name, InSchema: true, Current: current[f.Name][name]}
		if pm, ok := raw.(map[string]any); ok {
			p.Type, _ = pm["type"].(string)
			p.Default = kbScalar(pm["default"])
			p.Description, _ = pm["description"].(string)
			p.Min = kbScalar(pm["minimum"])
			p.Max = kbScalar(pm["maximum"])
			if enums, ok, _ := unstructured.NestedSlice(pm, "enum"); ok && len(enums) > 0 {
				p.Enum = make([]string, 0, len(enums)) // enum 值可能是数字/布尔，统一转字符串
				for _, e := range enums {
					p.Enum = append(p.Enum, kbScalar(e))
				}
			}
		}
		_, p.Dynamic = dyn[name]
		_, p.Immutable = immutable[name]
		f.Params = append(f.Params, p)
	}
	// schema 没声明但当前值里有的键（用户手工加的）：保留可编辑，标记 InSchema=false
	schemaNames := map[string]bool{}
	for _, p := range f.Params {
		schemaNames[p.Name] = true
	}
	for k := range current[f.Name] {
		if !schemaNames[k] {
			f.Params = append(f.Params, KBParam{Name: k, Current: current[f.Name][k], InSchema: false})
		}
	}
	sort.Slice(f.Params, func(i, j int) bool { return f.Params[i].Name < f.Params[j].Name })
	return f
}

// kbSchemaParams 从 parametersSchema.schemaInJSON 取参数属性表。
// 实测有两种形态：内嵌对象（mysql-8.0-pd，properties.spec.properties.<param>）与
// JSON 字符串（apecloud 的 PD，properties.<topLevelKey>.properties.<param>）。
// 解析顺序：topLevelKey 包裹 → 唯一键包裹 → 直接 properties；只有 cue 的 PD 返回空（降级为无元数据编辑）。
func kbSchemaParams(pd *unstructured.Unstructured, fileName string) map[string]any {
	var root map[string]any
	if m, ok, _ := unstructured.NestedMap(pd.Object, "spec", "parametersSchema", "schemaInJSON"); ok {
		root = m
	} else if js, ok, _ := unstructured.NestedString(pd.Object, "spec", "parametersSchema", "schemaInJSON"); ok && js != "" {
		if err := json.Unmarshal([]byte(js), &root); err != nil {
			return map[string]any{}
		}
	} else {
		return map[string]any{}
	}
	props, _, _ := unstructured.NestedMap(root, "properties")
	if len(props) == 0 {
		return map[string]any{}
	}
	topKey, _, _ := unstructured.NestedString(pd.Object, "spec", "parametersSchema", "topLevelKey")
	if topKey != "" {
		if top, ok := props[topKey].(map[string]any); ok {
			if sub, ok2, _ := unstructured.NestedMap(top, "properties"); ok2 {
				return sub
			}
		}
	}
	if len(props) == 1 {
		for _, v := range props {
			if vm, ok := v.(map[string]any); ok {
				if sub, ok2, _ := unstructured.NestedMap(vm, "properties"); ok2 {
					return sub
				}
			}
		}
	}
	return props
}

func setOf(pd *unstructured.Unstructured, fields ...string) map[string]bool {
	out := map[string]bool{}
	if lst, ok, _ := unstructured.NestedStringSlice(pd.Object, fields...); ok {
		for _, v := range lst {
			out[v] = true
		}
	}
	return out
}

func hasAny(pd *unstructured.Unstructured, fields ...string) bool {
	_, ok, _ := unstructured.NestedMap(pd.Object, fields...)
	if ok {
		return true
	}
	// unixSignalTrigger 等可能被编码为空对象以外的形态，存在即算
	_, ok, _ = unstructured.NestedFieldNoCopy(pd.Object, fields...)
	return ok
}

// kbScalar schema 值转展示字符串（JSON number 是 float64，整型别渲染成 8.0）
func kbScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// KBValidateParams Reconfiguring 提交前的本地校验：拒不可变参数与未知键（schema 有声明时）。
// operator 侧还有一层 CUE 校验兜底，这里拦的是能给用户讲清楚的错误。
func KBValidateParams(schema *KBConfigSchema, params map[string]string) error {
	if len(params) == 0 {
		return fmt.Errorf("改配置需至少一个参数")
	}
	if schema == nil || len(schema.Files) == 0 {
		return nil // 无 schema（ParametersDefinition 缺失）：不拦，交给 operator
	}
	known := map[string]KBParam{}
	for _, f := range schema.Files {
		for _, p := range f.Params {
			known[p.Name] = p
		}
	}
	for k, v := range params {
		p, ok := known[k]
		if !ok {
			return fmt.Errorf("参数 %s 不在该组件的参数定义里（可改参数以接口返回的 schema 为准）", k)
		}
		if p.Immutable {
			return fmt.Errorf("参数 %s 是不可变参数，不允许修改", k)
		}
		if p.Enum != nil && len(p.Enum) > 0 {
			hit := false
			for _, e := range p.Enum {
				if e == v {
					hit = true
				}
			}
			if !hit {
				return fmt.Errorf("参数 %s 取值须为 %s 之一", k, strings.Join(p.Enum, "/"))
			}
		}
		if p.Type == "boolean" && v != "true" && v != "false" && v != "on" && v != "off" && v != "1" && v != "0" {
			return fmt.Errorf("参数 %s 是布尔型，取值 true/false", k)
		}
	}
	return nil
}
