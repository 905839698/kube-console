// kbscheduling.go 建集群时的调度设置（亲和性 + 容忍污点），下发到 KubeBlocks 1.0.2 的
// ClusterSpec.schedulingPolicy——它对全部组件生效，组件级 schedulingPolicy 会覆盖它
// （向导先只做集群级：专用 DB 节点带污点这种诉求，一套策略就够）。
//
// 表单只收"人话"结构（键值行 + 匹配表达式），转 corev1 的那层在这里做；
// 所有取值都先校验再下发，因为 CRD 的 pruning 会**静默丢掉**它不认识的字段，
// 一个拼错的 operator 不会报错，只会让 Pod 永远 Pending。
package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kube-console/server/internal/kube"
)

// 标签键：可选 DNS 前缀 + 名字；标签值：RFC 1123 标签或空
var kbLabelKeyRe = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[a-z0-9]([-a-z0-9_.]*[a-z0-9])?$`)
var kbLabelValueRe = regexp.MustCompile(`^$|^[a-z0-9A-Z]([-a-z0-9A-Z_.]*[a-z0-9A-Z])?$`)

const kbHostTopology = "kubernetes.io/hostname"

// KBToleration 一条污点容忍。Operator=Exists 时不看 Value。
type KBToleration struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Operator string `json:"operator"` // Exists | Equal（空按 Exists）
	Effect   string `json:"effect"`   // NoSchedule | PreferNoSchedule | NoExecute（空 = 全部）
}

// KBNodeMatch 一条节点标签匹配表达式
type KBNodeMatch struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"` // In | NotIn | Exists | DoesNotExist | Gt | Lt
	Values   []string `json:"values"`
}

type KBScheduling struct {
	NodeSelector  map[string]string `json:"nodeSelector,omitempty"`
	Tolerations   []KBToleration    `json:"tolerations,omitempty"`
	NodeRequired  []KBNodeMatch     `json:"nodeRequired,omitempty"`  // 节点亲和：必须满足
	NodePreferred []KBNodeMatch     `json:"nodePreferred,omitempty"` // 节点亲和：尽量满足
	Spread        string            `json:"spread,omitempty"`        // 同组实例打散：preferred | required（空=不设置）
}

func (s *KBScheduling) empty() bool {
	if s == nil {
		return true
	}
	return len(s.NodeSelector) == 0 && len(s.Tolerations) == 0 &&
		len(s.NodeRequired) == 0 && len(s.NodePreferred) == 0 && s.Spread == ""
}

// kbSchedulingSpec 校验并转成 spec.schedulingPolicy 的形状；clusterName 用于打散的反亲和选择器
func kbSchedulingSpec(s *KBScheduling, clusterName string) (map[string]any, error) {
	if s == nil {
		return nil, nil
	}
	out := map[string]any{}

	if len(s.NodeSelector) > 0 {
		sel := map[string]any{}
		for k, v := range s.NodeSelector {
			if !kbLabelKeyRe.MatchString(k) {
				return nil, fmt.Errorf("节点标签键 %q 不合法（小写字母数字与 -_.，可带 DNS 前缀）", k)
			}
			if !kbLabelValueRe.MatchString(v) {
				return nil, fmt.Errorf("节点标签 %s 的值 %q 不合法（不能有空格或大写以外的特殊字符）", k, v)
			}
			sel[k] = v
		}
		out["nodeSelector"] = sel
	}

	if len(s.Tolerations) > 0 {
		list := make([]any, 0, len(s.Tolerations))
		for i, t := range s.Tolerations {
			key := strings.TrimSpace(t.Key)
			op := strings.TrimSpace(t.Operator)
			if op == "" {
				op = "Exists"
			}
			if op != "Exists" && op != "Equal" {
				return nil, fmt.Errorf("第 %d 条容忍的 operator 只能是 Exists 或 Equal，实际 %q", i+1, t.Operator)
			}
			eff := strings.TrimSpace(t.Effect)
			switch eff {
			case "", "NoSchedule", "PreferNoSchedule", "NoExecute":
			default:
				return nil, fmt.Errorf("第 %d 条容忍的 effect 不合法: %q（可为空 = 匹配全部效果）", i+1, t.Effect)
			}
			val := t.Value
			if op == "Equal" && val == "" && key != "" {
				return nil, fmt.Errorf("第 %d 条容忍用 Equal 必须填 value（Exists 才允许留空）", i+1)
			}
			if key != "" && !kbLabelKeyRe.MatchString(key) {
				return nil, fmt.Errorf("第 %d 条容忍的污点键 %q 不合法", i+1, key)
			}
			item := map[string]any{"operator": op}
			if key != "" {
				item["key"] = key
			}
			if val != "" {
				item["value"] = val
			}
			if eff != "" {
				item["effect"] = eff
			}
			list = append(list, item)
		}
		out["tolerations"] = list
	}

	nodeAffinity := map[string]any{}
	if req, err := kbNodeSelectorTerms(s.NodeRequired, "必须满足"); err != nil {
		return nil, err
	} else if req != nil {
		nodeAffinity["requiredDuringSchedulingIgnoredDuringExecution"] = req
	}
	if pref, err := kbPreferredTerms(s.NodePreferred, "尽量满足"); err != nil {
		return nil, err
	} else if pref != nil {
		nodeAffinity["preferredDuringSchedulingIgnoredDuringExecution"] = pref
	}

	anti := map[string]any{}
	if s.Spread != "" {
		if s.Spread != "preferred" && s.Spread != "required" {
			return nil, fmt.Errorf("打散方式只能是 preferred 或 required，实际 %q", s.Spread)
		}
		if clusterName == "" {
			return nil, fmt.Errorf("打散设置需要集群名才能生成反亲和选择器")
		}
		term := map[string]any{
			"topologyKey": kbHostTopology,
			"labelSelector": map[string]any{
				"matchLabels": map[string]any{"app.kubernetes.io/instance": clusterName},
			},
		}
		if s.Spread == "required" {
			anti["requiredDuringSchedulingIgnoredDuringExecution"] = []any{term}
		} else {
			anti["preferredDuringSchedulingIgnoredDuringExecution"] = []any{
				map[string]any{"weight": int64(100), "podAffinityTerm": term},
			}
		}
	}

	if len(nodeAffinity) > 0 || len(anti) > 0 {
		aff := map[string]any{}
		if len(nodeAffinity) > 0 {
			aff["nodeAffinity"] = nodeAffinity
		}
		if len(anti) > 0 {
			aff["podAntiAffinity"] = anti
		}
		out["affinity"] = aff
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func kbMatchExprs(items []KBNodeMatch, what string) ([]any, error) {
	exprs := make([]any, 0, len(items))
	for i, m := range items {
		key := strings.TrimSpace(m.Key)
		if key == "" || !kbLabelKeyRe.MatchString(key) {
			return nil, fmt.Errorf("节点亲和（%s）第 %d 条的标签键不合法: %q", what, i+1, m.Key)
		}
		op := strings.TrimSpace(m.Operator)
		if op == "" {
			op = "In"
		}
		switch op {
		case "In", "NotIn", "Gt", "Lt":
			if len(m.Values) == 0 {
				return nil, fmt.Errorf("节点亲和（%s）第 %d 条：operator %s 必须至少一个值", what, i+1, op)
			}
			for _, v := range m.Values {
				if !kbLabelValueRe.MatchString(v) {
					return nil, fmt.Errorf("节点亲和（%s）第 %d 条的值 %q 不合法", what, i+1, v)
				}
			}
		case "Exists", "DoesNotExist":
			if len(m.Values) > 0 {
				return nil, fmt.Errorf("节点亲和（%s）第 %d 条：operator %s 不看值，请清空", what, i+1, op)
			}
		default:
			return nil, fmt.Errorf("节点亲和（%s）第 %d 条的 operator 不合法: %q（In/NotIn/Exists/DoesNotExist/Gt/Lt）", what, i+1, m.Operator)
		}
		expr := map[string]any{"key": key, "operator": op}
		if len(m.Values) > 0 {
			vals := make([]any, 0, len(m.Values))
			for _, v := range m.Values {
				vals = append(vals, v)
			}
			expr["values"] = vals
		}
		exprs = append(exprs, expr)
	}
	return exprs, nil
}

// kbNodeSelectorTerms 多个条目是 OR 关系；这里每条表达式各成一个 term（比塞进一个 term 的
// AND 更贴近"这些节点里任选其一"的直觉）
func kbNodeSelectorTerms(items []KBNodeMatch, what string) (any, error) {
	if len(items) == 0 {
		return nil, nil
	}
	terms := make([]any, 0, len(items))
	for _, m := range items {
		exprs, err := kbMatchExprs([]KBNodeMatch{m}, what)
		if err != nil {
			return nil, err
		}
		terms = append(terms, map[string]any{"matchExpressions": exprs})
	}
	return map[string]any{"nodeSelectorTerms": terms}, nil
}

func kbPreferredTerms(items []KBNodeMatch, what string) (any, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(items))
	for _, m := range items {
		exprs, err := kbMatchExprs([]KBNodeMatch{m}, what)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"weight":     int64(100),
			"preference": map[string]any{"matchExpressions": exprs},
		})
	}
	return out, nil
}

// kbSchedulingSummaryOf 把集群上的调度设置读成一行话，用于详情展示。
// 建完集群后能在界面上看到"到底有没有生效"，比让人去 kubectl 复查 spec 更省事。
func kbSchedulingSummaryOf(obj *unstructured.Unstructured) string {
	sp, ok, _ := unstructured.NestedMap(obj.Object, "spec", "schedulingPolicy")
	if !ok || len(sp) == 0 {
		return ""
	}
	bits := []string{}
	if sel, _, _ := unstructured.NestedStringMap(sp, "nodeSelector"); len(sel) > 0 {
		keys := make([]string, 0, len(sel))
		for k := range sel {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys[:min(2, len(keys))] {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, sel[k]))
		}
		if len(keys) > 2 {
			pairs = append(pairs, fmt.Sprintf("另 %d 项", len(keys)-2))
		}
		bits = append(bits, "节点标签 "+strings.Join(pairs, " "))
	}
	if tols, _, _ := unstructured.NestedSlice(sp, "tolerations"); len(tols) > 0 {
		parts := make([]string, 0, len(tols))
		for _, raw := range tols {
			t, isMap := raw.(map[string]any)
			if !isMap {
				continue
			}
			key, _ := t["key"].(string)
			eff, _ := t["effect"].(string)
			val, _ := t["value"].(string)
			name := key
			if name == "" {
				name = "全部污点"
			}
			if val != "" {
				name += "=" + val
			}
			if eff != "" {
				name += "(" + eff + ")"
			}
			parts = append(parts, name)
		}
		if len(parts) > 0 {
			bits = append(bits, "容忍 "+strings.Join(parts, " "))
		}
	}
	nodeTerms, _, _ := unstructured.NestedSlice(sp, "affinity", "nodeAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	if len(nodeTerms) > 0 {
		bits = append(bits, fmt.Sprintf("节点亲和(必须) %d 条", len(nodeTerms)))
	}
	if pref, _, _ := unstructured.NestedSlice(sp, "affinity", "nodeAffinity",
		"preferredDuringSchedulingIgnoredDuringExecution"); len(pref) > 0 {
		bits = append(bits, fmt.Sprintf("节点亲和(尽量) %d 条", len(pref)))
	}
	if req, _, _ := unstructured.NestedSlice(sp, "affinity", "podAntiAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution"); len(req) > 0 {
		bits = append(bits, "必须分散到不同节点")
	} else if pr, _, _ := unstructured.NestedSlice(sp, "affinity", "podAntiAffinity",
		"preferredDuringSchedulingIgnoredDuringExecution"); len(pr) > 0 {
		bits = append(bits, "尽量分散到不同节点")
	}
	return strings.Join(bits, " · ")
}

// kbAssertSchedulingSupported 用 dry-run 建一份确认这套 addon 的 CRD 真的认这个字段。
// structural schema 会**静默剪掉**未知字段：老版本 addon 没有 spec.schedulingPolicy，
// 直接 Create 会得到一个"配了但没生效"的集群，比报错难查得多。
// 探测用的假名字可能因为别的原因被 webhook 拒——那种情况不下结论，交给真 Create 报错。
func kbAssertSchedulingSupported(ctx context.Context, c *kube.Client, ns string, obj *unstructured.Unstructured) error {
	probe := obj.DeepCopy()
	probe.SetName("kc-sched-probe")
	res, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Create(ctx, probe, metav1.CreateOptions{DryRun: []string{"All"}})
	if err != nil {
		return nil
	}
	if _, ok, _ := unstructured.NestedMap(res.Object, "spec", "schedulingPolicy"); !ok {
		return fmt.Errorf("该集群的 KubeBlocks 不认 spec.schedulingPolicy（下发后被 apiserver 剪掉），请升级 addon，或建完集群后用 YAML 编辑加调度设置")
	}
	return nil
}
