package service

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

// "一键接入 exporter 抓取"点了之后没数据的原因（2026-09-29 真机）：
// prometheus-operator 只加载 Prometheus CR 的 spec.serviceMonitorSelector 匹配得上的
// ServiceMonitor。这套集群的 selector 是 {release: prometheus}，而我们建的对象只带
// managed-by 标签 → 11 条 kb-*-exporter 一条都没被加载，Prometheus 不报错、事件里也没有，
// 界面就是"接入成功 + 永远没有曲线"。所以建之前必须先问 Prometheus 认哪些标签。

// kbPromCR 造一个 Prometheus CR；matchLabels 为 nil 表示不写 serviceMonitorSelector（= 匹配全部）
func kbPromCR(name, ns string, matchLabels map[string]string) *unstructured.Unstructured {
	spec := map[string]any{}
	if matchLabels != nil {
		ml := map[string]any{}
		for k, v := range matchLabels {
			ml[k] = v
		}
		spec["serviceMonitorSelector"] = map[string]any{"matchLabels": ml}
	}
	return kbUD(name, kbPrometheusGVR, "Prometheus", ns, map[string]any{"spec": spec})
}

// 客户端里塞一个 mysql 集群的 headless Service（带 metrics 端口），供 KBEnsureServiceMonitor 用
func kbMetricsClient(t *testing.T, proms ...*unstructured.Unstructured) *kube.Client {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql-mysql-headless",
			Namespace: "app",
			Labels:    map[string]string{KBEssentialLabel: "mysql-mysql"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     []corev1.ServicePort{{Name: "http-metrics", Port: 9104}},
		},
	}
	objs := append([]*unstructured.Unstructured{}, proms...)
	return &kube.Client{Clientset: fake.NewSimpleClientset(svc), Dynamic: kbFakeDyn(objs...)}
}

func kbGetSM(t *testing.T, c *kube.Client, ns, name string) map[string]any {
	t.Helper()
	o, err := c.Dynamic.Resource(kbSMGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取 ServiceMonitor 失败: %v", err)
	}
	return o.Object
}

// selector 有 matchLabels：建出来的对象必须带上它，否则 prometheus-operator 直接忽略
func TestKBMonitorAccessAppliesPrometheusSelectorLabels(t *testing.T) {
	c := kbMetricsClient(t, kbPromCR("prometheus-kube-prometheus-prometheus", "monitor",
		map[string]string{"release": "prometheus"}))
	res, err := KBEnsureServiceMonitor(context.Background(), c, "app", "mysql")
	if err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("应接入 1 条: %+v", res)
	}
	sm := kbGetSM(t, c, "app", res.Created[0])
	labels, _, _ := unstructured.NestedStringMap(sm, "metadata", "labels")
	if labels["release"] != "prometheus" {
		t.Errorf("没带上 Prometheus 的抓取标签，这条配置会永远不出数据: %+v", labels)
	}
	if labels["app.kubernetes.io/managed-by"] != "kube-console" {
		t.Errorf("自己的归属标签被弄丢了: %+v", labels)
	}
	if !strings.Contains(res.Note, "release=prometheus") {
		t.Errorf("要在回执里说清贴了什么标签: %+v", res)
	}
}

// selector 是空的（匹配全部）时不乱贴标签，也不要在回执里冒充做了事
func TestKBMonitorAccessNoLabelsWhenSelectorMatchesAll(t *testing.T) {
	c := kbMetricsClient(t, kbUD("prom", kbPrometheusGVR, "Prometheus", "monitor", map[string]any{"spec": map[string]any{}}))
	res, err := KBEnsureServiceMonitor(context.Background(), c, "app", "mysql")
	if err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	sm := kbGetSM(t, c, "app", res.Created[0])
	labels, _, _ := unstructured.NestedStringMap(sm, "metadata", "labels")
	if _, has := labels["release"]; has {
		t.Errorf("该集群不挑标签，别乱加: %+v", labels)
	}
	if res.Note != "" || res.Warning != "" {
		t.Errorf("什么都没做就不该有提示: %+v", res)
	}
}

// matchExpressions 挑不出标签能满足的形式：必须显式 warning，不能说"已接入"就完事
func TestKBMonitorAccessWarnsOnMatchExpressions(t *testing.T) {
	sel := map[string]any{"matchExpressions": []any{
		map[string]any{"key": "release", "operator": "In", "values": []any{"prometheus"}},
	}}
	prom := kbUD("prom", kbPrometheusGVR, "Prometheus", "monitor", map[string]any{
		"spec": map[string]any{"serviceMonitorSelector": sel}})
	c := kbMetricsClient(t, prom)
	ml := KBMonitorLabelsFor(context.Background(), c, kbSelServiceMonitor)
	if len(ml.Labels) != 0 {
		t.Errorf("matchExpressions 不该被当成可贴的标签: %+v", ml.Labels)
	}
	if ml.Warning == "" {
		t.Error("这种情况必须给 warning，否则用户以为接入成功了")
	}
}

// 历史遗留：早先建的 ServiceMonitor 没有抓取标签，再点一次按钮要把它修好
func TestKBMonitorAccessRepairsExisting(t *testing.T) {
	existing := kbUD("kb-mysql-mysql-exporter", kbSMGVR, "ServiceMonitor", "app", map[string]any{
		"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{KBEssentialLabel: "mysql-mysql"}}},
	})
	existing.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "kube-console"})
	c := kbMetricsClient(t, append([]*unstructured.Unstructured{existing},
		kbPromCR("prom", "monitor", map[string]string{"release": "prometheus"}))...)
	res, err := KBEnsureServiceMonitor(context.Background(), c, "app", "mysql")
	if err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	labels, _, _ := unstructured.NestedStringMap(kbGetSM(t, c, "app", "kb-mysql-mysql-exporter"), "metadata", "labels")
	if labels["release"] != "prometheus" {
		t.Errorf("既有的那条没被修好: %+v", labels)
	}
	if !strings.Contains(res.Note, "1 条") {
		t.Errorf("回执要说明修了几条: %+v", res)
	}
}

// mongodb 的 exporter 端口在 headless Service 上叫 "exporter"（9216），
// 不是 http-metrics/metrics——只认那两个名字会把 mongodb 判成"无需接入"
func TestKBMonitorAccessRecognizesMongodbExporterPort(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mongodb-mongos-headless",
			Namespace: "app",
			Labels:    map[string]string{KBEssentialLabel: "mongodb-mongos"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{Name: "mongos", Port: 27017},
				{Name: "exporter", Port: 9216},
			},
		},
	}
	c := &kube.Client{Clientset: fake.NewSimpleClientset(svc), Dynamic: kbFakeDyn()}
	res, err := KBEnsureServiceMonitor(context.Background(), c, "app", "mongodb")
	if err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("exporter 端口应被识别并接入: %+v", res)
	}
	eps, _, _ := unstructured.NestedSlice(kbGetSM(t, c, "app", res.Created[0]), "spec", "endpoints")
	if len(eps) != 1 {
		t.Fatalf("应有 1 条抓取端点: %v", eps)
	}
	port, _, _ := unstructured.NestedString(eps[0].(map[string]any), "port")
	if port != "exporter" {
		t.Errorf("ServiceMonitor 应按 exporter 端口名抓取: %v", eps[0])
	}
}

// 多个 Prometheus 在同名键上冲突时取并集会两边都不匹配，所以按 ns/name 排序取第一个
func TestKBMonitorLabelsFirstPrometheusWins(t *testing.T) {
	a := kbPromCR("a-prom", "monitor", map[string]string{"release": "aaa"})
	b := kbPromCR("b-prom", "monitor", map[string]string{"release": "bbb"})
	c := kbMetricsClient(t, a, b)
	ml := KBMonitorLabelsFor(context.Background(), c, kbSelServiceMonitor)
	if ml.Labels["release"] != "aaa" {
		t.Errorf("应当只取排序后的第一个 selector，实际 %+v", ml.Labels)
	}
}

// 读不到 Prometheus CR（没装 operator / 无权限）时保持原样，不猜默认标签
func TestKBMonitorLabelsReadUnavailableKeepsBehavior(t *testing.T) {
	c := kbMetricsClient(t) // 一个 Prometheus 对象都没有
	ml := KBMonitorLabelsFor(context.Background(), c, kbSelServiceMonitor)
	if len(ml.Labels) != 0 || ml.Warning != "" {
		t.Errorf("列不到 prometheuses 时不该有动作: %+v", ml)
	}
	res, err := KBEnsureServiceMonitor(context.Background(), c, "app", "mysql")
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("读不到 selector 也要能建: %v %+v", err, res)
	}
	labels, _, _ := unstructured.NestedStringMap(kbGetSM(t, c, "app", res.Created[0]), "metadata", "labels")
	if len(labels) != 2 {
		t.Errorf("只该有自己的两个归属标签: %+v", labels)
	}
}

// PodMonitor 走的是另一个 selector 字段，别把两边搞混
func TestKBMonitorLabelsUsesPerKindSelector(t *testing.T) {
	prom := kbUD("prom", kbPrometheusGVR, "Prometheus", "monitor", map[string]any{"spec": map[string]any{
		"serviceMonitorSelector": map[string]any{"matchLabels": map[string]any{"release": "svc-side"}},
		"podMonitorSelector":     map[string]any{"matchLabels": map[string]any{"release": "pod-side"}},
	}})
	c := kbMetricsClient(t, prom)
	if got := KBMonitorLabelsFor(context.Background(), c, kbSelServiceMonitor).Labels["release"]; got != "svc-side" {
		t.Errorf("ServiceMonitor 取错字段: %s", got)
	}
	if got := KBMonitorLabelsFor(context.Background(), c, kbSelPodMonitor).Labels["release"]; got != "pod-side" {
		t.Errorf("PodMonitor 取错字段: %s", got)
	}
}

// 真机核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LivePromSel
// 这里认的是"这个集群的 Prometheus 到底挑不挑标签"——挑而我们对不上，就是那条"接入成功但没数据"。
func TestKBPromSelLiveCheck(t *testing.T) {
	path := os.Getenv("KC_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("未设 KC_TEST_KUBECONFIG，跳过真实集群核对")
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 kubeconfig: %v", err)
	}
	c, err := kube.NewClient(string(bs))
	if err != nil {
		t.Skipf("连不上集群: %v", err)
	}
	ctx := context.Background()
	list, err := c.Dynamic.Resource(kbPrometheusGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Skipf("该集群没有 Prometheus CR（没装 prometheus-operator？）: %v", err)
	}
	ml := KBMonitorLabelsFor(ctx, c, kbSelServiceMonitor)
	t.Logf("Prometheus CR %d 个；需要的抓取标签=%v；warning=%q", len(list.Items), ml.Labels, ml.Warning)
	for i := range list.Items {
		sel, found, _ := unstructured.NestedMap(list.Items[i].Object, "spec", "serviceMonitorSelector")
		t.Logf("  %s/%s serviceMonitorSelector=%v", list.Items[i].GetNamespace(), list.Items[i].GetName(), sel)
		if !found {
			continue
		}
		if _, has := sel["matchExpressions"]; has && ml.Labels == nil && ml.Warning == "" {
			t.Errorf("%s 用 matchExpressions 挑选，但没给出 warning", list.Items[i].GetName())
		}
	}
}
