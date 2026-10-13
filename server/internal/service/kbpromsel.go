// kbpromsel.go 解决"ServiceMonitor / PodMonitor 建成功了却什么都没抓"这一类静默失败。
//
// prometheus-operator 不是"有 monitor 对象就抓"，它只加载每个 Prometheus CR 的
// spec.serviceMonitorSelector（PodMonitor 是 spec.podMonitorSelector）匹配得上的那些；
// 匹配不上时不报错、不告警、事件里也没有痕迹，现象就是控制台显示"接入成功"而指标页一条曲线都没有。
//
// 所以凡是我们替用户建 monitor 对象的地方，都必须先问一句"这个集群的 Prometheus 认哪些标签"，
// 把答案贴到 metadata.labels 上，并把历史上漏贴的那批修回来。
package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
)

// kbPrometheusGVR Prometheus CR（只为读它的 selector）
var kbPrometheusGVR = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheuses"}

// 两类 monitor 对象各自的挑选字段
const (
	kbSelServiceMonitor = "serviceMonitorSelector"
	kbSelPodMonitor     = "podMonitorSelector"
)

// KBMonitorLabels 建 monitor 对象时必须带的标签，以及给人看的说明。
//
// Labels 为空表示"不用补"：任一 Prometheus 的对应 selector 是空的（那就是匹配全部），
// 或根本读不到 Prometheus CR（没装 operator / 无 list 权限）——那种情况保持原样，
// 不去猜一个默认值，因为 kube-prometheus-stack 的 release 名随安装方式变
// （prometheus / kube-prometheus / 自定义），猜错等于白贴一堆标签。
//
// 多个 Prometheus 的 selector 在同名键上冲突时按 命名空间/名字 排序取第一个：
// 取并集会让两个都不匹配，比挑一个更糟。只用 matchExpressions（不是标签等值）的
// selector 没法靠贴标签满足，跳过并在 Note 里说清楚，别让人以为接入成功了。
type KBMonitorLabels struct {
	Labels  map[string]string
	Warning string
}

func KBMonitorLabelsFor(ctx context.Context, c *kube.Client, selectorField string) KBMonitorLabels {
	list, err := c.Dynamic.Resource(kbPrometheusGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return KBMonitorLabels{} // 读不到就不改行为，调用方照常建
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		if items[i].GetNamespace() != items[j].GetNamespace() {
			return items[i].GetNamespace() < items[j].GetNamespace()
		}
		return items[i].GetName() < items[j].GetName()
	})
	out := map[string]string{}
	exprOnly := 0
	for i := range items {
		sel, found, _ := unstructured.NestedMap(items[i].Object, "spec", selectorField)
		if !found || len(sel) == 0 {
			return KBMonitorLabels{} // 这个 Prometheus 匹配全部：任何 monitor 对象都会被加载
		}
		ml, ok, _ := unstructured.NestedStringMap(sel, "matchLabels")
		if !ok || len(ml) == 0 {
			exprOnly++
			continue
		}
		for k, v := range ml {
			if _, taken := out[k]; !taken {
				out[k] = v
			}
		}
	}
	if len(out) == 0 {
		if exprOnly > 0 {
			return KBMonitorLabels{Warning: fmt.Sprintf(
				"该集群的 Prometheus 用 %s 的 matchExpressions 挑选对象，贴标签满足不了它；"+
					"请把对象的标签改成能被选中的形式（或放宽该 selector）", selectorField)}
		}
		return KBMonitorLabels{}
	}
	pairs := make([]string, 0, len(out))
	for k, v := range out {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return KBMonitorLabels{Labels: out}
}

// AppliedNote 贴了哪些标签的人话版本。
//
// 值得单独说一句：用户之前点过"接入"却没数据，光看"已接入 N 条"分不出真接入还是被忽略。
func (m KBMonitorLabels) AppliedNote() string {
	if len(m.Labels) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(m.Labels))
	for k, v := range m.Labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return "已按该集群 Prometheus 的 serviceMonitorSelector 贴标签：" + strings.Join(pairs, ",")
}

// KBMonitorApplyLabels 把抓取标签并进一个已存在的对象，缺哪补哪。
//
// 为什么要修历史：一键接入这个按钮以前建的对象全都没带这些标签，用户点过一次没数据、
// 再点一次还是没有。只改新建逻辑等于让老数据永久坏着。
// 返回 true 表示真的改了（调用方据此在提示里说"修好了 N 条"）。
func KBMonitorApplyLabels(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource, ns, name string, want map[string]string) (bool, error) {
	if len(want) == 0 {
		return false, nil
	}
	cli := c.Dynamic.Resource(gvr).Namespace(ns)
	obj, err := cli.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	changed := false
	for k, v := range want {
		if labels[k] != v {
			labels[k] = v
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	obj.SetLabels(labels)
	if _, err := cli.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

// KBMergeMonitorLabels 把抓取标签并进一份 labels map（建对象时用），不覆盖调用方自己的标签
func KBMergeMonitorLabels(labels map[string]any, want map[string]string) map[string]any {
	if labels == nil {
		labels = map[string]any{}
	}
	for k, v := range want {
		if _, taken := labels[k]; !taken {
			labels[k] = v
		}
	}
	return labels
}
