package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"kube-console/server/internal/kube"
)

// K8sService 提供所有 Kubernetes 资源操作（无状态，直接调用 client-go）
type K8sService struct {
	debugImage string
}

func NewK8sService(debugImage string) *K8sService {
	if debugImage == "" {
		debugImage = "busybox:1.36"
	}
	return &K8sService{debugImage: debugImage}
}

// ------------------- 通用辅助 -------------------

// ageOf 返回运行时长（从创建时间到现在），中文格式
func ageOf(t metav1.Time) string {
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d天", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%d个月", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%d年", int(d.Hours()/24/365))
	}
}

func int32p(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func searchMatch(name, search string) bool {
	return search == "" || strings.Contains(strings.ToLower(name), strings.ToLower(search))
}

// splitNamespaces 拆分命名空间参数（空值或 * = 全部命名空间；支持逗号分隔多选）
func splitNamespaces(ns string) []string {
	if ns == "" || ns == "*" {
		return []string{""}
	}
	parts := strings.Split(ns, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && p != "*" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// ------------------- 集群总览 -------------------

type NodeSummary struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Roles      string `json:"roles"`
	InternalIP string `json:"internalIP"`
	Version    string `json:"version"`
	CPUCores   string `json:"cpuCores"`
	MemGi      string `json:"memGi"`
	Age        string `json:"age"`
}

type OverviewStats struct {
	Nodes        int           `json:"nodes"`
	NodesReady   int           `json:"nodesReady"`
	Namespaces   int           `json:"namespaces"`
	Pods         int           `json:"pods"`
	PodsRunning  int           `json:"podsRunning"`
	Deployments  int           `json:"deployments"`
	StatefulSets int           `json:"statefulSets"`
	DaemonSets   int           `json:"daemonSets"`
	Services     int           `json:"services"`
	Ingresses    int           `json:"ingresses"`
	CPUCapacity  string        `json:"cpuCapacity"`  // 核
	MemCapacity  string        `json:"memCapacity"`  // Gi
	NodesList    []NodeSummary `json:"nodesList"`
}

// Overview 汇总集群统计信息
func (s *K8sService) Overview(ctx context.Context, c *kube.Client) (*OverviewStats, error) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	namespaces, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	pods, err := c.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	deployments, err := c.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	sts, err := c.Clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	dss, err := c.Clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	svcs, err := c.Clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	ings, err := c.Clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	stats := &OverviewStats{
		Namespaces:   len(namespaces.Items),
		Pods:         len(pods.Items),
		Deployments:  len(deployments.Items),
		StatefulSets: len(sts.Items),
		DaemonSets:   len(dss.Items),
		Services:     len(svcs.Items),
		Ingresses:    len(ings.Items),
	}
	var cpuMilli, memMi int64
	for _, n := range nodes.Items {
		stats.Nodes++
		ns := nodeSummary(&n)
		if ns.Status == "Ready" {
			stats.NodesReady++
		}
		stats.NodesList = append(stats.NodesList, ns)
		if cap, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
			cpuMilli += cap.MilliValue()
		}
		if mem, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
			memMi += mem.Value() / (1024 * 1024)
		}
	}
	for _, p := range pods.Items {
		if kube.PodStatus(&p).Status == "Running" {
			stats.PodsRunning++
		}
	}
	stats.CPUCapacity = fmt.Sprintf("%.1f", float64(cpuMilli)/1000)
	stats.MemCapacity = fmt.Sprintf("%.1f", float64(memMi)/1024)
	return stats, nil
}

func nodeSummary(n *corev1.Node) NodeSummary {
	ready := "NotReady"
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				ready = "Ready"
			}
			break
		}
	}
	var roles []string
	for k := range n.Labels {
		if v, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && v != "" {
			roles = append(roles, v)
		}
	}
	if len(roles) == 0 {
		roles = []string{"worker"}
	}
	sort.Strings(roles)
	ip := ""
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			ip = a.Address
			break
		}
	}
	cpu := "?"
	if cap, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
		cpu = strconv.FormatInt(cap.Value(), 10)
	}
	mem := "?"
	if m, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
		mem = fmt.Sprintf("%.1f", float64(m.Value())/(1024*1024*1024))
	}
	return NodeSummary{
		Name: n.Name, Status: ready, Roles: strings.Join(roles, ","), InternalIP: ip,
		Version: n.Status.NodeInfo.KubeletVersion, CPUCores: cpu, MemGi: mem,
		Age: ageOf(n.CreationTimestamp),
	}
}

// ------------------- 命名空间 -------------------

type NamespaceItem struct {
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
	Age    string            `json:"age"`
}

func (s *K8sService) ListNamespaces(ctx context.Context, c *kube.Client, search string) ([]NamespaceItem, error) {
	list, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := make([]NamespaceItem, 0, len(list.Items))
	for _, ns := range list.Items {
		if !searchMatch(ns.Name, search) {
			continue
		}
		items = append(items, NamespaceItem{
			Name: ns.Name, Status: string(ns.Status.Phase), Labels: ns.Labels, Age: ageOf(ns.CreationTimestamp),
		})
	}
	return items, nil
}

func (s *K8sService) CreateNamespace(ctx context.Context, c *kube.Client, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := c.Clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

func (s *K8sService) DeleteNamespace(ctx context.Context, c *kube.Client, name string) error {
	return c.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

// ------------------- 节点 -------------------

type NodeDetail struct {
	NodeSummary
	Labels        map[string]string `json:"labels"`
	Capacity      map[string]string `json:"capacity"`
	Allocatable   map[string]string `json:"allocatable"`
	Conditions    []CondItem        `json:"conditions"`
	Taints        []string          `json:"taints"`
	TaintItems    []TaintItem       `json:"taintItems"`
	Schedulable   bool              `json:"schedulable"`
	Pods          []PodItem         `json:"pods"`
	ContainerRuntime string         `json:"containerRuntime"`
	OSImage       string            `json:"osImage"`
	KernelVersion string            `json:"kernelVersion"`
}

// TaintItem 结构化污点（供可视化编辑）
type TaintItem struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

type CondItem struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Updated string `json:"updated"`
}

func (s *K8sService) ListNodeDetails(ctx context.Context, c *kube.Client) ([]NodeDetail, error) {
	list, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	details := make([]NodeDetail, 0, len(list.Items))
	for i := range list.Items {
		d, err := s.buildNodeDetail(ctx, c, &list.Items[i])
		if err != nil {
			return nil, err
		}
		details = append(details, *d)
	}
	return details, nil
}

func (s *K8sService) GetNodeDetail(ctx context.Context, c *kube.Client, name string) (*NodeDetail, error) {
	node, err := c.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return s.buildNodeDetail(ctx, c, node)
}

func (s *K8sService) buildNodeDetail(ctx context.Context, c *kube.Client, n *corev1.Node) (*NodeDetail, error) {
	d := &NodeDetail{
		NodeSummary: nodeSummary(n),
		Labels:      n.Labels,
		Capacity:    formatResourceMap(n.Status.Capacity),
		Allocatable: formatResourceMap(n.Status.Allocatable),
		ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
		OSImage: n.Status.NodeInfo.OSImage,
		KernelVersion: n.Status.NodeInfo.KernelVersion,
		Schedulable: !n.Spec.Unschedulable,
	}
	for _, cond := range n.Status.Conditions {
		d.Conditions = append(d.Conditions, CondItem{
			Type: string(cond.Type), Status: string(cond.Status),
			Reason: cond.Reason, Message: cond.Message,
			Updated: cond.LastTransitionTime.Format("2006-01-02 15:04:05"),
		})
	}
	for _, t := range n.Spec.Taints {
		d.Taints = append(d.Taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
		d.TaintItems = append(d.TaintItems, TaintItem{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
	}
	// 该节点上的 Pod
	pods, err := c.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", n.Name).String(),
	})
	if err == nil {
		for i := range pods.Items {
			d.Pods = append(d.Pods, s.podItem(&pods.Items[i]))
		}
	}
	return d, nil
}

func formatResourceMap(m corev1.ResourceList) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = v.String()
	}
	return out
}

// ------------------- Pod -------------------

type PodItem struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	NodeName  string            `json:"nodeName"`
	Status    string            `json:"status"`
	Ready     string            `json:"ready"` // "1/2"
	Restarts  int32             `json:"restarts"`
	IP        string            `json:"ip"`
	Age       string            `json:"age"`
	Labels    map[string]string `json:"labels"`
}

func (s *K8sService) podItem(p *corev1.Pod) PodItem {
	ready, total := 0, len(p.Spec.Containers)
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	info := kube.PodStatus(p)
	return PodItem{
		Name: p.Name, Namespace: p.Namespace, NodeName: p.Spec.NodeName,
		Status: info.Status, Ready: fmt.Sprintf("%d/%d", ready, total),
		Restarts: info.RestartCount, IP: p.Status.PodIP, Age: ageOf(p.CreationTimestamp),
		Labels: p.Labels,
	}
}

func (s *K8sService) ListPods(ctx context.Context, c *kube.Client, namespace, search string) ([]PodItem, error) {
	items := make([]PodItem, 0)
	for _, ns := range splitNamespaces(namespace) {
		list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			if !searchMatch(list.Items[i].Name, search) {
				continue
			}
			items = append(items, s.podItem(&list.Items[i]))
		}
	}
	return items, nil
}

type PodDetail struct {
	PodItem
	Phase          string                     `json:"phase"`
	Containers     []kube.ContainerStatusInfo `json:"containers"`
	InitContainers []kube.ContainerStatusInfo `json:"initContainers"`
	Conditions     []CondItem                 `json:"conditions"`
	Events         []EventItem                `json:"events"`
	ServiceAccount string                     `json:"serviceAccount"`
	Tolerations    []string                   `json:"tolerations"`
	StartTime      string                     `json:"startTime"`
}

func (s *K8sService) GetPodDetail(ctx context.Context, c *kube.Client, namespace, name string) (*PodDetail, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	item := s.podItem(pod)
	d := &PodDetail{
		PodItem: item, Phase: string(pod.Status.Phase),
		Containers: kube.ContainerStatuses(pod), ServiceAccount: pod.Spec.ServiceAccountName,
	}
	if pod.Status.StartTime != nil {
		d.StartTime = pod.Status.StartTime.Format("2006-01-02 15:04:05")
	}
	for _, ic := range pod.Status.InitContainerStatuses {
		init := kube.ContainerStatusInfo{Name: ic.Name, Image: ic.Image, Ready: ic.Ready, RestartCount: ic.RestartCount}
		switch {
		case ic.State.Running != nil:
			init.State = "Running"
		case ic.State.Waiting != nil:
			init.State = "Waiting"
			init.Reason = ic.State.Waiting.Reason
		case ic.State.Terminated != nil:
			init.State = "Terminated"
			init.Reason = ic.State.Terminated.Reason
		}
		d.InitContainers = append(d.InitContainers, init)
	}
	for _, cond := range pod.Status.Conditions {
		d.Conditions = append(d.Conditions, CondItem{
			Type: string(cond.Type), Status: string(cond.Status),
			Reason: cond.Reason, Message: cond.Message,
			Updated: cond.LastTransitionTime.Format("2006-01-02 15:04:05"),
		})
	}
	for _, t := range pod.Spec.Tolerations {
		if t.Key != "" {
			d.Tolerations = append(d.Tolerations, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
		}
	}
	d.Events, _ = s.listEvents(ctx, c, namespace, "Pod", name)
	return d, nil
}

func (s *K8sService) DeletePod(ctx context.Context, c *kube.Client, namespace, name string) error {
	return c.Clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// flushWriter 包装 io.Writer，每次写入后强制 Flush（http 响应有缓冲，流式场景必须逐段刷新）
type flushWriter struct {
	w io.Writer
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if f, ok := fw.w.(interface{ Flush() }); ok {
		f.Flush()
	}
	return n, err
}

// StreamPodLogs 流式输出 Pod 日志
func (s *K8sService) StreamPodLogs(ctx context.Context, c *kube.Client, namespace, name, container string, tailLines int64, follow bool, out io.Writer) error {
	if container == "" {
		pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(pod.Spec.Containers) == 0 {
			return fmt.Errorf("Pod 没有容器")
		}
		container = pod.Spec.Containers[0].Name
	}
	opts := &corev1.PodLogOptions{Container: container, TailLines: &tailLines, Follow: follow}
	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	_, err = io.Copy(flushWriter{w: out}, stream)
	return err
}

// ------------------- 事件 -------------------

type EventItem struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

func (s *K8sService) listEvents(ctx context.Context, c *kube.Client, namespace, kind, name string) ([]EventItem, error) {
	list, err := c.Clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.AndSelectors(
			fields.OneTermEqualSelector("involvedObject.kind", kind),
			fields.OneTermEqualSelector("involvedObject.name", name),
		).String(),
		Limit: 100,
	})
	if err != nil {
		return nil, err
	}
	items := make([]EventItem, 0, len(list.Items))
	for _, e := range list.Items {
		items = append(items, EventItem{
			Type: e.Type, Reason: e.Reason, Message: e.Message,
			Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			Count:  e.Count,
			FirstSeen: e.FirstTimestamp.Format("2006-01-02 15:04:05"),
			LastSeen:  e.LastTimestamp.Format("2006-01-02 15:04:05"),
		})
	}
	return items, nil
}

// ------------------- 工作负载 -------------------

type WorkloadItem struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Replicas  int32             `json:"replicas"` // 期望副本
	Ready     int32             `json:"ready"`
	Updated   int32             `json:"updated"`
	Available int32             `json:"available"`
	Images    []string          `json:"images"`
	Extra     string            `json:"extra"` // 类型特有的摘要（schedule/completions 等）
	Age       string            `json:"age"`
	Labels    map[string]string `json:"labels"`
	// Pod 专用扩展（控制器类型不填）
	Status   string `json:"status,omitempty"`   // Pending/Running/...
	ReadyStr string `json:"readyStr,omitempty"` // "1/2"
	Restarts int32  `json:"restarts,omitempty"`
	IP       string `json:"ip,omitempty"`
	NodeName string `json:"nodeName,omitempty"`
}

type WorkloadDetail struct {
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Namespace  string           `json:"namespace"`
	Replicas   kube.WorkloadReplicaStatus `json:"replicas"`
	Selector   map[string]string `json:"selector"`
	Strategy   string           `json:"strategy"`
	Images     []string         `json:"images"`
	Labels     map[string]string `json:"labels"`
	Age        string           `json:"age"`
	Containers []ContainerInfo  `json:"containers"`
	Pods       []PodItem        `json:"pods"`
	Events     []EventItem      `json:"events"`
}

type ContainerInfo struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Command      string `json:"command"`
	Ports        string `json:"ports"`
	Requests     string `json:"requests"`
	Limits       string `json:"limits"`
	ReadyProbe   string `json:"readyProbe"`
	LiveProbe    string `json:"liveProbe"`
}

var workloadKinds = map[string]bool{
	"deployments": true, "statefulsets": true, "daemonsets": true,
	"replicasets": true, "replicationcontrollers": true, "jobs": true, "cronjobs": true,
}

// ListWorkloads 列出指定类型的工作负载（namespace 支持逗号分隔多选，* 或空=全部）
func (s *K8sService) ListWorkloads(ctx context.Context, c *kube.Client, kind, namespace, search string) ([]WorkloadItem, error) {
	items := make([]WorkloadItem, 0)
	for _, ns := range splitNamespaces(namespace) {
		part, err := s.listWorkloadsIn(ctx, c, kind, ns, search)
		if err != nil {
			return nil, err
		}
		items = append(items, part...)
	}
	return items, nil
}

// listWorkloadsIn 在单个命名空间中列出指定类型的工作负载
func (s *K8sService) listWorkloadsIn(ctx context.Context, c *kube.Client, kind, namespace, search string) ([]WorkloadItem, error) {
	items := make([]WorkloadItem, 0)
	switch kind {
	case "deployments":
		list, err := c.Clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			d := &list.Items[i]
			if !searchMatch(d.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "Deployment", Name: d.Name, Namespace: d.Namespace,
				Replicas: int32p(d.Spec.Replicas), Ready: d.Status.ReadyReplicas,
				Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas,
				Images: imagesOf(d.Spec.Template.Spec.Containers), Age: ageOf(d.CreationTimestamp),
				Labels: d.Labels,
			})
		}
	case "statefulsets":
		list, err := c.Clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			s := &list.Items[i]
			if !searchMatch(s.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "StatefulSet", Name: s.Name, Namespace: s.Namespace,
				Replicas: int32p(s.Spec.Replicas), Ready: s.Status.ReadyReplicas,
				Updated: s.Status.UpdatedReplicas, Available: s.Status.ReadyReplicas,
				Images: imagesOf(s.Spec.Template.Spec.Containers), Age: ageOf(s.CreationTimestamp),
				Labels: s.Labels,
			})
		}
	case "daemonsets":
		list, err := c.Clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			d := &list.Items[i]
			if !searchMatch(d.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "DaemonSet", Name: d.Name, Namespace: d.Namespace,
				Replicas: d.Status.DesiredNumberScheduled, Ready: d.Status.NumberReady,
				Updated: d.Status.UpdatedNumberScheduled, Available: d.Status.NumberAvailable,
				Images: imagesOf(d.Spec.Template.Spec.Containers), Age: ageOf(d.CreationTimestamp),
				Labels: d.Labels,
			})
		}
	case "replicasets":
		list, err := c.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			rs := &list.Items[i]
			if !searchMatch(rs.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "ReplicaSet", Name: rs.Name, Namespace: rs.Namespace,
				Replicas: int32p(rs.Spec.Replicas), Ready: rs.Status.ReadyReplicas,
				Updated: rs.Status.ReadyReplicas, Available: rs.Status.AvailableReplicas,
				Images: imagesOf(rs.Spec.Template.Spec.Containers), Age: ageOf(rs.CreationTimestamp),
				Labels: rs.Labels,
			})
		}
	case "replicationcontrollers":
		list, err := c.Clientset.CoreV1().ReplicationControllers(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			rc := &list.Items[i]
			if !searchMatch(rc.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "ReplicationController", Name: rc.Name, Namespace: rc.Namespace,
				Replicas: int32p(rc.Spec.Replicas), Ready: rc.Status.ReadyReplicas,
				Updated: rc.Status.ReadyReplicas, Available: rc.Status.ReadyReplicas,
				Images: imagesOf(rc.Spec.Template.Spec.Containers), Age: ageOf(rc.CreationTimestamp),
				Labels: rc.Labels,
			})
		}
	case "cronjobs":
		list, err := c.Clientset.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			cj := &list.Items[i]
			if !searchMatch(cj.Name, search) {
				continue
			}
			active := int32(len(cj.Status.Active))
			items = append(items, WorkloadItem{
				Kind: "CronJob", Name: cj.Name, Namespace: cj.Namespace,
				Replicas: active, Ready: active, Updated: active, Available: active,
				Images: imagesOf(cj.Spec.JobTemplate.Spec.Template.Spec.Containers),
				Extra:  fmt.Sprintf("%s · 最近 %s", cj.Spec.Schedule, lastSchedule(cj)),
				Age:    ageOf(cj.CreationTimestamp), Labels: cj.Labels,
			})
		}
	case "jobs":
		list, err := c.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			j := &list.Items[i]
			if !searchMatch(j.Name, search) {
				continue
			}
			items = append(items, WorkloadItem{
				Kind: "Job", Name: j.Name, Namespace: j.Namespace,
				Replicas: int32p(j.Spec.Parallelism), Ready: j.Status.Active,
				Updated: j.Status.Succeeded, Available: j.Status.Active,
				Images: imagesOf(j.Spec.Template.Spec.Containers),
				Extra:  fmt.Sprintf("成功 %d/%d · 失败 %d", j.Status.Succeeded, jobCompletions(j), j.Status.Failed),
				Age:    ageOf(j.CreationTimestamp), Labels: j.Labels,
			})
		}
	case "pods":
		// Pod 与工作负载同列表展示：状态/就绪/重启/IP/节点走扩展字段
		list, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			p := &list.Items[i]
			if !searchMatch(p.Name, search) {
				continue
			}
			pi := s.podItem(p)
			items = append(items, WorkloadItem{
				Kind: "Pod", Name: pi.Name, Namespace: pi.Namespace,
				Status: pi.Status, ReadyStr: pi.Ready, Restarts: pi.Restarts,
				IP: pi.IP, NodeName: pi.NodeName,
				Images: imagesOf(p.Spec.Containers), Age: pi.Age, Labels: pi.Labels,
			})
		}
	default:
		return nil, fmt.Errorf("不支持的工作负载类型: %s", kind)
	}
	return items, nil
}

func lastSchedule(cj *batchv1.CronJob) string {
	if cj.Status.LastScheduleTime == nil {
		return "从未"
	}
	return cj.Status.LastScheduleTime.Format("01-02 15:04")
}

func jobCompletions(j *batchv1.Job) int32 {
	if j.Spec.Completions != nil {
		return *j.Spec.Completions
	}
	return 1
}

// GetWorkloadDetail 获取工作负载详情（含关联 Pod 与事件）
func (s *K8sService) GetWorkloadDetail(ctx context.Context, c *kube.Client, kind, namespace, name string) (*WorkloadDetail, error) {
	d := &WorkloadDetail{Kind: kind, Name: name, Namespace: namespace}
	var selector map[string]string
	var ownerName string
	var containerList []corev1.Container

	switch kind {
	case "deployments":
		obj, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: int32p(obj.Spec.Replicas), ReadyReplicas: obj.Status.ReadyReplicas,
			Updated: obj.Status.UpdatedReplicas, Available: obj.Status.AvailableReplicas,
		}
		selector = obj.Spec.Selector.MatchLabels
		d.Strategy = string(obj.Spec.Strategy.Type)
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	case "statefulsets":
		obj, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: int32p(obj.Spec.Replicas), ReadyReplicas: obj.Status.ReadyReplicas,
			Updated: obj.Status.UpdatedReplicas, Available: obj.Status.ReadyReplicas,
		}
		selector = obj.Spec.Selector.MatchLabels
		d.Strategy = string(obj.Spec.UpdateStrategy.Type)
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	case "daemonsets":
		obj, err := c.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: obj.Status.DesiredNumberScheduled, ReadyReplicas: obj.Status.NumberReady,
			Updated: obj.Status.UpdatedNumberScheduled, Available: obj.Status.NumberAvailable,
		}
		selector = obj.Spec.Selector.MatchLabels
		d.Strategy = string(obj.Spec.UpdateStrategy.Type)
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	case "replicasets":
		obj, err := c.Clientset.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: int32p(obj.Spec.Replicas), ReadyReplicas: obj.Status.ReadyReplicas,
			Updated: obj.Status.ReadyReplicas, Available: obj.Status.AvailableReplicas,
		}
		selector = obj.Spec.Selector.MatchLabels
		d.Strategy = "RollingUpdate"
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	case "replicationcontrollers":
		obj, err := c.Clientset.CoreV1().ReplicationControllers(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: int32p(obj.Spec.Replicas), ReadyReplicas: obj.Status.ReadyReplicas,
			Updated: obj.Status.ReadyReplicas, Available: obj.Status.ReadyReplicas,
		}
		selector = obj.Spec.Selector
		d.Strategy = "RollingUpdate"
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	case "cronjobs":
		obj, err := c.Clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		active := int32(len(obj.Status.Active))
		d.Replicas = kube.WorkloadReplicaStatus{Replicas: active, ReadyReplicas: active}
		d.Strategy = string(obj.Spec.ConcurrencyPolicy)
		d.Images = imagesOf(obj.Spec.JobTemplate.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		d.Selector = obj.Spec.JobTemplate.Spec.Template.Labels
		containerList = obj.Spec.JobTemplate.Spec.Template.Spec.Containers
		ownerName = name // CronJob -> Job -> Pod 两层 ownerRef
	case "jobs":
		obj, err := c.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		d.Replicas = kube.WorkloadReplicaStatus{
			Replicas: int32p(obj.Spec.Parallelism), ReadyReplicas: obj.Status.Active,
			Updated: obj.Status.Succeeded, Available: obj.Status.Active,
		}
		selector = obj.Spec.Selector.MatchLabels
		d.Strategy = string(obj.Spec.Template.Spec.RestartPolicy)
		d.Images = imagesOf(obj.Spec.Template.Spec.Containers)
		d.Labels, d.Age = obj.Labels, ageOf(obj.CreationTimestamp)
		containerList = obj.Spec.Template.Spec.Containers
	default:
		return nil, fmt.Errorf("不支持的工作负载类型: %s", kind)
	}

	for _, ct := range containerList {
		d.Containers = append(d.Containers, containerInfo(ct))
	}
	var err error
	d.Selector = selector
	d.Pods, err = s.workloadPods(ctx, c, namespace, selector, ownerName)
	if err != nil {
		return nil, fmt.Errorf("查询工作负载 Pod 失败: %w", err)
	}
	d.Events, _ = s.listEvents(ctx, c, namespace, kindTitle(kind), name)
	return d, nil
}

// workloadPods 列出工作负载的 Pod：
// 1. 有标签选择器时按 selector 查询（Deployment/STS/DS/Job/RC）
// 2. 否则按 ownerRef 过滤（CronJob -> Job -> Pod 两层）
func (s *K8sService) workloadPods(ctx context.Context, c *kube.Client, namespace string, selector map[string]string, ownerName string) ([]PodItem, error) {
	listOpts := metav1.ListOptions{}
	if len(selector) > 0 {
		listOpts.LabelSelector = labels.Set(selector).String()
	}
	list, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, listOpts)
	if err != nil {
		return nil, err
	}

	// ownerRef 过滤时：先找出 ownerName 名下的 Job（CronJob -> Job 一层）
	jobNames := map[string]bool{}
	if ownerName != "" {
		jobs, err := c.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range jobs.Items {
				for _, ref := range jobs.Items[i].OwnerReferences {
					if ref.Name == ownerName {
						jobNames[jobs.Items[i].Name] = true
					}
				}
			}
		}
	}

	items := make([]PodItem, 0, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		if ownerName != "" && !ownedBy(pod, ownerName, jobNames) {
			continue
		}
		items = append(items, s.podItem(pod))
	}
	return items, nil
}

// ownedBy 判断 Pod 是否直接（Job/ReplicaSet 等）或间接（CronJob -> Job）属于 ownerName
func ownedBy(pod *corev1.Pod, ownerName string, jobNames map[string]bool) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Name == ownerName || jobNames[ref.Name] {
			return true
		}
	}
	return false
}

// ScaleWorkload 调整副本数（DaemonSet 不支持）
func (s *K8sService) ScaleWorkload(ctx context.Context, c *kube.Client, kind, namespace, name string, replicas int32) error {
	if kind == "daemonsets" {
		return fmt.Errorf("DaemonSet 不支持手动扩缩容")
	}
	gvr, ok := kube.GVRFor(kind)
	if !ok {
		return fmt.Errorf("不支持的工作负载类型: %s", kind)
	}
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := c.Dynamic.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// RestartWorkload 滚动重启（注入 restartedAt 注解触发模板变更）
func (s *K8sService) RestartWorkload(ctx context.Context, c *kube.Client, kind, namespace, name string) error {
	gvr, ok := kube.GVRFor(kind)
	if !ok {
		return fmt.Errorf("不支持的工作负载类型: %s", kind)
	}
	ts := time.Now().Format(time.RFC3339)
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, ts))
	_, err := c.Dynamic.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// DeleteWorkload 删除工作负载
func (s *K8sService) DeleteWorkload(ctx context.Context, c *kube.Client, kind, namespace, name string) error {
	gvr, ok := kube.GVRFor(kind)
	if !ok {
		return fmt.Errorf("不支持的工作负载类型: %s", kind)
	}
	err := c.Dynamic.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	// 删除工作负载时同步清理同名 PodMonitor
	if namespace != "" {
		pmGVR := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "podmonitors"}
		_ = c.Dynamic.Resource(pmGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	return nil
}

func imagesOf(containers []corev1.Container) []string {
	var images []string
	for _, c := range containers {
		images = append(images, c.Image)
	}
	return images
}

func containerInfo(ct corev1.Container) ContainerInfo {
	info := ContainerInfo{Name: ct.Name, Image: ct.Image}
	if len(ct.Command) > 0 {
		info.Command = strings.Join(ct.Command, " ")
	}
	for _, p := range ct.Ports {
		info.Ports += fmt.Sprintf("%d/%s ", p.ContainerPort, p.Protocol)
	}
	if ct.Resources.Requests != nil {
		info.Requests = formatResourceMap(ct.Resources.Requests)["cpu"] + " / " + formatResourceMap(ct.Resources.Requests)["memory"]
	}
	if ct.Resources.Limits != nil {
		info.Limits = formatResourceMap(ct.Resources.Limits)["cpu"] + " / " + formatResourceMap(ct.Resources.Limits)["memory"]
	}
	if ct.ReadinessProbe != nil {
		info.ReadyProbe = probeDesc(ct.ReadinessProbe)
	}
	if ct.LivenessProbe != nil {
		info.LiveProbe = probeDesc(ct.LivenessProbe)
	}
	return info
}

func probeDesc(p *corev1.Probe) string {
	switch {
	case p.HTTPGet != nil:
		return fmt.Sprintf("http-get %s:%d%s", p.HTTPGet.Host, p.HTTPGet.Port.IntValue(), p.HTTPGet.Path)
	case p.TCPSocket != nil:
		return fmt.Sprintf("tcp-socket :%d", p.TCPSocket.Port.IntValue())
	case p.Exec != nil:
		return "exec " + strings.Join(p.Exec.Command, " ")
	default:
		return ""
	}
}

func kindTitle(kind string) string {
	return kube.KindTitle(kind)
}

// ------------------- 通用资源（Service/Ingress/ConfigMap/Secret） -------------------

type GenericItem struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Summary   string            `json:"summary"`
	Age       string            `json:"age"`
	Labels    map[string]string `json:"labels"`
	Addresses []string          `json:"addresses,omitempty"` // Service 访问地址（每端口一条，供一键复制）
}

// ListGeneric 列出通用资源（namespace 支持逗号分隔多选，空值=全部）
func (s *K8sService) ListGeneric(ctx context.Context, c *kube.Client, kind, namespace, search string) ([]GenericItem, error) {
	gvr, ok, err := kube.ResolveGVR(ctx, c, kind)
	if !ok {
		// routes：应用层路由（存于 ConfigMap annotation），非真实 GVR
		if kind == "routes" {
			gvr = configmapGVR()
		} else if err != nil {
			return nil, err // Gateway API 资源当前集群不可用，返回带说明的错误
		} else {
			return nil, fmt.Errorf("不支持的资源类型: %s", kind)
		}
	}
	items := make([]GenericItem, 0)
	for _, ns := range splitNamespaces(namespace) {
		if kube.IsClusterScoped(kind) {
			// 集群级资源与命名空间无关：只列一次，避免多选命名空间时重复
			ns = ""
			list, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			for _, u := range list.Items {
				if kind == "routes" && !hasRoutesAnnotation(&u) {
					continue
				}
				name := u.GetName()
				if !searchMatch(name, search) {
					continue
				}
				items = append(items, GenericItem{
					Kind: kind, Name: name, Namespace: u.GetNamespace(),
					Summary: s.genericSummary(kind, &u), Age: ageOf(u.GetCreationTimestamp()),
					Addresses: serviceAddresses(&u),
					Labels: u.GetLabels(),
				})
			}
			break
		}
		list, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, u := range list.Items {
			if kind == "routes" && !hasRoutesAnnotation(&u) {
				continue // 仅列出带路由 annotation 的 ConfigMap
			}
			name := u.GetName()
			if !searchMatch(name, search) {
				continue
			}
			items = append(items, GenericItem{
				Kind: kind, Name: name, Namespace: u.GetNamespace(),
				Summary: s.genericSummary(kind, &u), Age: ageOf(u.GetCreationTimestamp()),
				Labels: u.GetLabels(), Addresses: serviceAddresses(&u),
			})
		}
	}
	return items, nil
}

// ExportGeneric 导出筛选后的资源为多文档 YAML（kind 模式，与列表页同参：namespace 逗号分隔多选、search 名称过滤）。
// 每个对象做 CleanForExport 深度清洗（去 uid/resourceVersion/managedFields/status/last-applied 注解），
// 导出文件可直接再导入。
func (s *K8sService) ExportGeneric(ctx context.Context, c *kube.Client, kind, namespace, search string) (string, error) {
	gvr, ok, err := kube.ResolveGVR(ctx, c, kind)
	if !ok {
		if kind == "routes" {
			gvr = configmapGVR()
		} else if err != nil {
			return "", err
		} else {
			return "", fmt.Errorf("不支持的资源类型: %s", kind)
		}
	}
	var b strings.Builder
	for _, ns := range splitNamespaces(namespace) {
		if kube.IsClusterScoped(kind) {
			ns = ""
		}
		list, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		for i := range list.Items {
			u := &list.Items[i]
			if kind == "routes" && !hasRoutesAnnotation(u) {
				continue
			}
			if !searchMatch(u.GetName(), search) {
				continue
			}
			obj := u.DeepCopy()
			kube.CleanForExport(obj)
			data, err := yaml.Marshal(obj.Object)
			if err != nil {
				return "", err
			}
			if b.Len() > 0 {
				b.WriteString("---\n")
			}
			b.Write(data)
		}
		if kube.IsClusterScoped(kind) {
			break
		}
	}
	return b.String(), nil
}

// routesGVR 应用层路由使用的底层 GVR（ConfigMap）
func configmapGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
}

// hasRoutesAnnotation 判断 ConfigMap 是否携带应用层路由 annotation
func hasRoutesAnnotation(u *unstructured.Unstructured) bool {
	ann := u.GetAnnotations()
	_, ok := ann["console.kube.io/routes"]
	return ok
}

// routesSummary 解析路由 annotation，展示 path → service 概览
func routesSummary(u *unstructured.Unstructured) string {
	raw, _ := u.GetAnnotations()["console.kube.io/routes"]
	if raw == "" {
		return "-"
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "-"
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		path, _ := e["path"].(string)
		svc, _ := e["service"].(string)
		port, _ := e["port"].(float64)
		parts = append(parts, fmt.Sprintf("%s→%s:%d", path, svc, int(port)))
	}
	if len(parts) == 0 {
		return "-"
	}
	if len(parts) > 3 {
		return strings.Join(parts[:3], "  ") + fmt.Sprintf("  …共 %d 条", len(parts))
	}
	return strings.Join(parts, "  ")
}

func (s *K8sService) genericSummary(kind string, u *unstructured.Unstructured) string {
	switch kind {
	case "services":
		svcType, _ := nestedString(u, "spec", "type")
		clusterIP, _ := nestedString(u, "spec", "clusterIP")
		ports := portList(u)
		if clusterIP == "" {
			clusterIP = "None"
		}
		return fmt.Sprintf("%s / %s / %s", svcType, clusterIP, ports)
	case "ingresses":
		if hosts := ingressHosts(u); hosts != "" {
			return hosts
		}
		return "-"
	case "routes":
		return routesSummary(u)
	case "configmaps", "secrets":
		return fmt.Sprintf("%d 个键", len(dataKeys(u)))
	case "endpoints":
		subsets, _, _ := unstructured.NestedSlice(u.Object, "subsets")
		return fmt.Sprintf("%d 个子集", len(subsets))
	case "endpointslices":
		endpoints, _, _ := unstructured.NestedSlice(u.Object, "endpoints")
		ports, _, _ := unstructured.NestedSlice(u.Object, "ports")
		// 展示前 3 个端点 IP，便于快速识别
		addrs := make([]string, 0, 3)
		for _, ep := range endpoints {
			epMap, _ := ep.(map[string]interface{})
			if epMap == nil {
				continue
			}
			epAddrs, _, _ := unstructured.NestedStringSlice(epMap, "addresses")
			for _, a := range epAddrs {
				if len(addrs) >= 3 {
					break
				}
				addrs = append(addrs, a)
			}
			if len(addrs) >= 3 {
				break
			}
		}
		addrStr := strings.Join(addrs, ", ")
		if addrStr == "" {
			addrStr = "-"
		}
		return fmt.Sprintf("%d 个端点: %s / %d 端口", len(endpoints), addrStr, len(ports))
	case "serviceaccounts":
		secrets, _, _ := unstructured.NestedSlice(u.Object, "secrets")
		return fmt.Sprintf("%d 个 Secret", len(secrets))
	case "persistentvolumeclaims":
		phase, _ := nestedString(u, "status", "phase")
		size, _ := nestedString(u, "spec", "resources", "requests", "storage")
		sc, _ := nestedString(u, "spec", "storageClassName")
		return fmt.Sprintf("%s / %s / %s", phase, size, sc)
	case "persistentvolumes":
		phase, _ := nestedString(u, "status", "phase")
		size, _ := nestedString(u, "spec", "capacity", "storage")
		reclaim, _ := nestedString(u, "spec", "persistentVolumeReclaimPolicy")
		return fmt.Sprintf("%s / %s / %s", phase, size, reclaim)
	case "storageclasses":
		provisioner, _ := nestedString(u, "provisioner")
		reclaim, _ := nestedString(u, "reclaimPolicy")
		return fmt.Sprintf("%s / %s", provisioner, reclaim)
	case "networkpolicies":
		ing, _, _ := unstructured.NestedSlice(u.Object, "spec", "ingress")
		eg, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
		return fmt.Sprintf("入站 %d 条 · 出站 %d 条", len(ing), len(eg))
	case "roles", "clusterroles":
		rules, _, _ := unstructured.NestedSlice(u.Object, "rules")
		return fmt.Sprintf("%d 条规则", len(rules))
	case "rolebindings", "clusterrolebindings":
		roleKind, _ := nestedString(u, "roleRef", "kind")
		roleName, _ := nestedString(u, "roleRef", "name")
		return fmt.Sprintf("%s/%s", roleKind, roleName)
	case "horizontalpodautoscalers":
		min, _ := nestedInt64(u, "spec", "minReplicas")
		max, _ := nestedInt64(u, "spec", "maxReplicas")
		target, _ := nestedString(u, "spec", "scaleTargetRef", "kind")
		targetName, _ := nestedString(u, "spec", "scaleTargetRef", "name")
		return fmt.Sprintf("%s/%s · %d→%d 副本", target, targetName, min, max)
	case "resourcequotas", "limitranges":
		hard, _, _ := unstructured.NestedMap(u.Object, "spec", "hard")
		return fmt.Sprintf("%d 项", len(hard))
	case "gatewayclasses":
		controller, _ := nestedString(u, "spec", "controllerName")
		return controller
	case "gateways":
		cls, _ := nestedString(u, "spec", "gatewayClassName")
		listeners, _, _ := unstructured.NestedSlice(u.Object, "spec", "listeners")
		return fmt.Sprintf("class=%s · %d 个监听器", cls, len(listeners))
	case "httproutes", "grpcroutes", "tlsroutes":
		hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
		return strings.Join(hosts, ", ")
	case "referencegrants":
		from, _, _ := unstructured.NestedSlice(u.Object, "spec", "from")
		return fmt.Sprintf("%d 条授权", len(from))
	case "events":
		typ, _ := nestedString(u, "type")
		reason, _ := nestedString(u, "reason")
		return fmt.Sprintf("%s / %s", typ, reason)
	}
	return ""
}

// GetGenericYAML 获取通用资源 YAML（详情展示用）
func (s *K8sService) GetGenericYAML(ctx context.Context, c *kube.Client, kind, namespace, name string) (string, error) {
	gvr, ok, err := kube.ResolveGVR(ctx, c, kind)
	if !ok {
		if kind == "routes" {
			gvr = configmapGVR()
		} else if err != nil {
			return "", err // Gateway API 资源当前集群不可用
		} else {
			return "", fmt.Errorf("不支持的资源类型: %s", kind)
		}
	}
	if kube.IsClusterScoped(kind) {
		namespace = ""
	}
	return kube.GetYAML(ctx, c.Dynamic, gvr, namespace, name)
}

// DeleteGeneric 删除通用资源
func (s *K8sService) DeleteGeneric(ctx context.Context, c *kube.Client, kind, namespace, name string) error {
	gvr, ok, err := kube.ResolveGVR(ctx, c, kind)
	if !ok {
		if kind == "routes" {
			gvr = configmapGVR()
		} else if err != nil {
			return err // Gateway API 资源当前集群不可用
		} else {
			return fmt.Errorf("不支持的资源类型: %s", kind)
		}
	}
	if kube.IsClusterScoped(kind) {
		namespace = ""
	}
	err = c.Dynamic.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	// 删除 Service 时同步清理同名 ServiceMonitor
	if kind == "services" && namespace != "" {
		smGVR := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
		_ = c.Dynamic.Resource(smGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	// 删除工作负载时同步清理同名 PodMonitor
	if workloadKinds[kind] && namespace != "" {
		pmGVR := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "podmonitors"}
		_ = c.Dynamic.Resource(pmGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	return nil
}

// ------------------- YAML 应用 -------------------

// ApplyYAML 应用 YAML（create-or-update），返回是否新建
func (s *K8sService) ApplyYAML(ctx context.Context, c *kube.Client, yamlStr string) (bool, error) {
	// Gateway API 资源：版本随渠道变化（v1/v1beta1/v1alphaN），需通过 discovery
	// 解析集群实际提供的版本，避免硬编码 apiVersion 导致 404。
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &m); err == nil {
		yamlKind, _ := m["kind"].(string)
		if resource, isGW := kube.IsGatewayKindFromYAML(yamlKind); isGW {
			gvr, ok, err := kube.ResolveGVR(ctx, c, resource)
			if !ok {
				return false, err
			}
			expected := gvr.Group + "/" + gvr.Version
			if apiVersion, _ := m["apiVersion"].(string); apiVersion != expected {
				m["apiVersion"] = expected
				data, err := yaml.Marshal(m)
				if err != nil {
					return false, err
				}
				yamlStr = string(data)
			}
		}
	}
	return kube.ApplyYAML(ctx, c.Dynamic, yamlStr)
}

// routeKinds 属于 Gateway API 的路由资源（支持 parentRefs 跨命名空间引用）
var routeKinds = map[string]bool{
	"HTTPRoute": true, "GRPCRoute": true, "TLSRoute": true, "TCPRoute": true, "UDPRoute": true,
}

// SyncReferenceGrant 当 Route 的 parentRefs 引用了其他命名空间的 Gateway 时，
// 自动在 Gateway 所在命名空间创建 ReferenceGrant 授权（静默失败，不影响主流程）。
func (s *K8sService) SyncReferenceGrant(ctx context.Context, c *kube.Client, yamlStr string) error {
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &m); err != nil {
		return nil
	}
	obj := &unstructured.Unstructured{Object: m}
	kind := obj.GetKind()
	if !routeKinds[kind] {
		return nil
	}
	routeNS := obj.GetNamespace()
	if routeNS == "" {
		routeNS = "default"
	}
	// 取 parentRefs[0].namespace 和 name
	parentRefs, found, _ := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	if !found || len(parentRefs) == 0 {
		return nil
	}
	ref, ok := parentRefs[0].(map[string]interface{})
	if !ok {
		return nil
	}
	gwNS, _ := ref["namespace"].(string)
	gwName, _ := ref["name"].(string)
	if gwName == "" || gwNS == "" || gwNS == routeNS {
		return nil // 同命名空间或无引用，不需要授权
	}
	// 构建 ReferenceGrant（放在 Gateway 所在命名空间）
	grantName := fmt.Sprintf("allow-%s-%s", strings.ToLower(kind), routeNS)
	grant := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "ReferenceGrant",
		"metadata": map[string]interface{}{
			"name":      grantName,
			"namespace": gwNS,
			"labels": map[string]interface{}{
				"kube-console.io/managed-by": "auto",
			},
		},
		"spec": map[string]interface{}{
			"from": []interface{}{map[string]interface{}{
				"group":     "gateway.networking.k8s.io",
				"kind":      kind,
				"namespace": routeNS,
			}},
			"to": []interface{}{map[string]interface{}{
				"group": "gateway.networking.k8s.io",
				"kind":  "Gateway",
				"name":  gwName,
			}},
		},
	}}
	// create-or-update
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "referencegrants"}
	existing, err := c.Dynamic.Resource(gvr).Namespace(gwNS).Get(ctx, grantName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err = c.Dynamic.Resource(gvr).Namespace(gwNS).Create(ctx, grant, metav1.CreateOptions{})
			return err
		}
		return err
	}
	grant.SetResourceVersion(existing.GetResourceVersion())
	_, err = c.Dynamic.Resource(gvr).Namespace(gwNS).Update(ctx, grant, metav1.UpdateOptions{})
	return err
}

// SyncServiceMonitor 根据 Service 的监控 annotation 自动创建/更新/删除 ServiceMonitor
// annotation: monitoring.coreos.com/servicemonitor=true 启用；port/interval 可选
func (s *K8sService) SyncServiceMonitor(ctx context.Context, c *kube.Client, yamlStr string) error {
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &m); err != nil {
		return nil // 非 YAML 场景静默
	}
	obj := &unstructured.Unstructured{Object: m}
	if obj.GetKind() != "Service" {
		return nil
	}
	ns := obj.GetNamespace()
	if ns == "" {
		ns = "default"
	}
	name := obj.GetName()
	if name == "" {
		return nil
	}
	ann := obj.GetAnnotations()
	enabled := ann["monitoring.coreos.com/servicemonitor"] == "true"

	smGVR := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
	if !enabled {
		// 关闭监控：删除同名 ServiceMonitor（幂等）
		_ = c.Dynamic.Resource(smGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		return nil
	}

	// 端口：annotation 指定或取第一个端口名
	port := ann["monitoring.coreos.com/port"]
	if port == "" {
		ports, _, _ := unstructured.NestedSlice(obj.Object, "spec", "ports")
		if len(ports) > 0 {
			if pm, ok := ports[0].(map[string]interface{}); ok {
				port, _ = pm["name"].(string)
			}
		}
	}
	if port == "" {
		port = "http"
	}
	interval := ann["monitoring.coreos.com/interval"]
	if interval == "" {
		interval = "30s"
	}
	path := ann["monitoring.coreos.com/path"]
	if path == "" {
		path = "/metrics"
	}

	// selector：匹配 Service 的 spec.selector（即后端 Pod 标签）
	selector, _, _ := unstructured.NestedMap(obj.Object, "spec", "selector")
	if len(selector) == 0 {
		return nil // 无 selector 的 Service（如 ExternalName）不创建
	}

	sm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "ServiceMonitor",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{"matchLabels": selector},
			"endpoints": []interface{}{
				map[string]interface{}{
					"port":     port,
					"interval": interval,
					"path":     path,
				},
			},
		},
	}}
	_, err := c.Dynamic.Resource(smGVR).Namespace(ns).Create(ctx, sm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// 已存在则更新（补 resourceVersion）
	if existing, gerr := c.Dynamic.Resource(smGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
		sm.SetResourceVersion(existing.GetResourceVersion())
	}
	_, err = c.Dynamic.Resource(smGVR).Namespace(ns).Update(ctx, sm, metav1.UpdateOptions{})
	return err
}

// SyncPodMonitor 根据工作负载的监控 annotation 自动创建/更新/删除 PodMonitor
// annotation: monitoring.coreos.com/podmonitor=true 启用；port/interval/path 可选
// PodMonitor 直接采集 Pod 的 /metrics（无需 Service），适用于 headless/无 Service 工作负载
func (s *K8sService) SyncPodMonitor(ctx context.Context, c *kube.Client, yamlStr string) error {
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &m); err != nil {
		return nil
	}
	obj := &unstructured.Unstructured{Object: m}
	kind := obj.GetKind()
	// 支持的工作负载 kind
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "ReplicationController", "Job", "CronJob":
	default:
		return nil
	}
	ns := obj.GetNamespace()
	if ns == "" {
		ns = "default"
	}
	name := obj.GetName()
	if name == "" {
		return nil
	}
	ann := obj.GetAnnotations()
	enabled := ann["monitoring.coreos.com/podmonitor"] == "true"

	pmGVR := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "podmonitors"}
	if !enabled {
		// 关闭监控：删除同名 PodMonitor（幂等）
		_ = c.Dynamic.Resource(pmGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		return nil
	}

	// Pod 标签选择器：spec.template.metadata.labels（CronJob 在 spec.jobTemplate.spec.template）
	labelsPath := []string{"spec", "template", "metadata", "labels"}
	if kind == "CronJob" {
		labelsPath = []string{"spec", "jobTemplate", "spec", "template", "metadata", "labels"}
	}
	selector, _, _ := unstructured.NestedMap(obj.Object, labelsPath...)
	if len(selector) == 0 {
		return nil // 无 Pod 标签选择器时不创建
	}

	// 端口：annotation 指定或取第一个容器端口名
	port := ann["monitoring.coreos.com/port"]
	if port == "" {
		containersPath := []string{"spec", "template", "spec", "containers"}
		if kind == "CronJob" {
			containersPath = []string{"spec", "jobTemplate", "spec", "template", "spec", "containers"}
		}
		if containers, _, _ := unstructured.NestedSlice(obj.Object, containersPath...); len(containers) > 0 {
			if cm, ok := containers[0].(map[string]interface{}); ok {
				if ports, _, _ := unstructured.NestedSlice(cm, "ports"); len(ports) > 0 {
					if pm, ok := ports[0].(map[string]interface{}); ok {
						port, _ = pm["name"].(string)
					}
				}
			}
		}
	}
	if port == "" {
		port = "metrics"
	}
	interval := ann["monitoring.coreos.com/interval"]
	if interval == "" {
		interval = "30s"
	}
	path := ann["monitoring.coreos.com/path"]
	if path == "" {
		path = "/metrics"
	}

	pm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PodMonitor",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{"matchLabels": selector},
			"podMetricsEndpoints": []interface{}{
				map[string]interface{}{
					"port":     port,
					"interval": interval,
					"path":     path,
				},
			},
		},
	}}
	_, err := c.Dynamic.Resource(pmGVR).Namespace(ns).Create(ctx, pm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// 已存在则更新（补 resourceVersion）
	if existing, gerr := c.Dynamic.Resource(pmGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
		pm.SetResourceVersion(existing.GetResourceVersion())
	}
	_, err = c.Dynamic.Resource(pmGVR).Namespace(ns).Update(ctx, pm, metav1.UpdateOptions{})
	return err
}

// GetResourceYAML 获取任意受支持资源的 YAML
func (s *K8sService) GetResourceYAML(ctx context.Context, c *kube.Client, kind, namespace, name string) (string, error) {
	gvr, ok := kube.GVRFor(kind)
	if !ok {
		return "", fmt.Errorf("不支持的资源类型: %s", kind)
	}
	if kube.IsClusterScoped(kind) {
		namespace = ""
	}
	return kube.GetYAML(ctx, c.Dynamic, gvr, namespace, name)
}

// ------------------- 任意 GVR（CRD 等） -------------------

// ListGenericGVR 按任意 GVR 列出资源（CRD 浏览用，无 summary；namespace 支持逗号分隔多选）
func (s *K8sService) ListGenericGVR(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource, namespace, search, labelSelector string) ([]GenericItem, error) {
	items := make([]GenericItem, 0)
	for _, ns := range splitNamespaces(namespace) {
		list, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return nil, err
		}
		for _, u := range list.Items {
			if !searchMatch(u.GetName(), search) {
				continue
			}
			items = append(items, GenericItem{
				Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(),
				Summary: gvrSummary(gvr.Resource, &u), Age: ageOf(u.GetCreationTimestamp()), Labels: u.GetLabels(),
			})
		}
	}
	return items, nil
}

// gvrSummary 对 GVR 列表生成概要（Tekton 流水线状态等）
func gvrSummary(resource string, u *unstructured.Unstructured) string {
	switch resource {
	case "pipelines":
		tasks, _, _ := unstructured.NestedSlice(u.Object, "spec", "tasks")
		return fmt.Sprintf("%d 个任务", len(tasks))
	case "pipelineruns":
		return tektonStatus(u)
	case "tasks":
		steps, _, _ := unstructured.NestedSlice(u.Object, "spec", "steps")
		return fmt.Sprintf("%d 个步骤", len(steps))
	case "taskruns":
		return tektonStatus(u)
	}
	return "-"
}

// tektonStatus 提取 Tekton 运行状态（status.conditions[0]，兼容 Succeeded 条件）
func tektonStatus(u *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, cond := range conds {
		cm, _ := cond.(map[string]interface{})
		if cm == nil {
			continue
		}
		typ, _ := cm["type"].(string)
		if typ != "Succeeded" {
			continue
		}
		status, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		switch {
		case status == "True":
			return "Succeeded"
		case status == "False":
			return reason // Failed / TimedOut / PipelineRunCancelled
		case reason == "Running" || reason == "Started":
			return "Running"
		default:
			return reason
		}
	}
	// 尚未生成条件
	if _, ok, _ := unstructured.NestedMap(u.Object, "status"); ok {
		return "Pending"
	}
	return "Pending"
}

// GetGenericYAMLByGVR 按任意 GVR 获取 YAML
func (s *K8sService) GetGenericYAMLByGVR(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource, namespace, name string) (string, error) {
	return kube.GetYAML(ctx, c.Dynamic, gvr, namespace, name)
}

// DeleteGenericByGVR 按任意 GVR 删除
func (s *K8sService) DeleteGenericByGVR(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource, namespace, name string) error {
	return c.Dynamic.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// ------------------- 全局搜索 -------------------

// SearchItem 搜索结果
type SearchItem struct {
	Type      string `json:"type"` // namespaces|pods|deployments|statefulsets|daemonsets|services|configmaps|nodes
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Search 全局搜索（按名称包含匹配，各类型上限 limit 条）
func (s *K8sService) Search(ctx context.Context, c *kube.Client, q string, limit int) []SearchItem {
	if q == "" || limit <= 0 {
		return nil
	}
	if limit > 10 {
		limit = 10
	}
	var (
		mu     sync.Mutex
		items  []SearchItem
		wg     sync.WaitGroup
		search = strings.ToLower(q)
	)
	add := func(typ, ns, name string) {
		mu.Lock()
		items = append(items, SearchItem{Type: typ, Namespace: ns, Name: name})
		mu.Unlock()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 200}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("namespaces", "", list.Items[i].Name)
					if len(items) >= limit { // 粗粒度限流
						break
					}
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("pods", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("deployments", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("statefulsets", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("daemonsets", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("services", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("configmaps", list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if list, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200}); err == nil {
			for i := range list.Items {
				if strings.Contains(strings.ToLower(list.Items[i].Name), search) {
					add("nodes", "", list.Items[i].Name)
				}
			}
		}
	}()
	wg.Wait()

	// 按类型分组截断
	byType := map[string][]SearchItem{}
	for _, it := range items {
		byType[it.Type] = append(byType[it.Type], it)
	}
	out := make([]SearchItem, 0, len(items))
	for _, typ := range []string{"namespaces", "pods", "deployments", "statefulsets", "daemonsets", "services", "configmaps", "nodes"} {
		for _, it := range byType[typ] {
			if len(out) >= 50 {
				return out
			}
			out = append(out, it)
		}
	}
	return out
}

// ------------------- 工作负载矩阵 -------------------

// MatrixCell 矩阵单元格（某命名空间某类型工作负载的汇总）
type MatrixCell struct {
	Total int `json:"total"`
	Ready int `json:"ready"`
}

// WorkloadMatrix 工作负载矩阵（命名空间 × 类型）
type WorkloadMatrix struct {
	Namespaces []string                        `json:"namespaces"`
	Kinds      []string                        `json:"kinds"`
	Cells      map[string]map[string]MatrixCell `json:"cells"`
}

// WorkloadMatrixData 汇总全部命名空间的工作负载矩阵
func (s *K8sService) WorkloadMatrixData(ctx context.Context, c *kube.Client) (*WorkloadMatrix, error) {
	out := &WorkloadMatrix{
		Kinds: []string{"Deployment", "StatefulSet", "DaemonSet"},
		Cells: map[string]map[string]MatrixCell{},
	}
	nsSet := map[string]bool{}

	fill := func(kind string, items []WorkloadItem) {
		for _, it := range items {
			nsSet[it.Namespace] = true
			if out.Cells[it.Namespace] == nil {
				out.Cells[it.Namespace] = map[string]MatrixCell{}
			}
			cell := out.Cells[it.Namespace][kind]
			cell.Total++
			if it.Ready >= it.Replicas && it.Replicas > 0 {
				cell.Ready++
			} else if kind == "DaemonSet" && it.Ready >= it.Replicas {
				cell.Ready++
			}
			out.Cells[it.Namespace][kind] = cell
		}
	}

	// 并行拉取三类工作负载
	type result struct {
		kind  string
		items []WorkloadItem
		err   error
	}
	ch := make(chan result, 3)
	for _, kind := range []string{"deployments", "statefulsets", "daemonsets"} {
		go func(k string) {
			items, err := s.ListWorkloads(ctx, c, k, "", "")
			ch <- result{kind: k, items: items, err: err}
		}(kind)
	}
	for i := 0; i < 3; i++ {
		r := <-ch
		if r.err != nil {
			continue
		}
		fill(kindTitle(r.kind), r.items)
	}

	// 命名空间排序
	for ns := range nsSet {
		out.Namespaces = append(out.Namespaces, ns)
	}
	sort.Strings(out.Namespaces)
	return out, nil
}

// ------------------- 配额总览 -------------------

// QuotaOverviewItem 命名空间配额状态
type QuotaOverviewItem struct {
	Namespace string            `json:"namespace"`
	QuotaName string            `json:"quotaName"` // 空 = 未配置配额
	Hard      map[string]string `json:"hard"`      // spec.hard（如 cpu/requests.cpu/memory）
	Used      map[string]string `json:"used"`      // status.used
}

// QuotaOverview 列出全部命名空间的 ResourceQuota 配置状态
func (s *K8sService) QuotaOverview(ctx context.Context, c *kube.Client) ([]QuotaOverviewItem, error) {
	nsList, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// 全部命名空间的配额（dynamic 读取 spec.hard / status.used）
	quotaList, err := c.Dynamic.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	byNs := map[string]*QuotaOverviewItem{}
	for i := range quotaList.Items {
		u := &quotaList.Items[i]
		item := &QuotaOverviewItem{
			Namespace: u.GetNamespace(),
			QuotaName: u.GetName(),
			Hard:      map[string]string{},
			Used:      map[string]string{},
		}
		if hard, found, _ := unstructured.NestedMap(u.Object, "spec", "hard"); found {
			for k, v := range hard {
				item.Hard[k] = fmt.Sprintf("%v", v)
			}
		}
		if used, found, _ := unstructured.NestedMap(u.Object, "status", "used"); found {
			for k, v := range used {
				item.Used[k] = fmt.Sprintf("%v", v)
			}
		}
		byNs[u.GetNamespace()] = item
	}
	out := make([]QuotaOverviewItem, 0, len(nsList.Items))
	for i := range nsList.Items {
		ns := nsList.Items[i].Name
		if item, ok := byNs[ns]; ok {
			out = append(out, *item)
		} else {
			out = append(out, QuotaOverviewItem{Namespace: ns, Hard: map[string]string{}, Used: map[string]string{}})
		}
	}
	return out, nil
}

// ------------------- unstructured 辅助 -------------------

func nestedString(u *unstructured.Unstructured, fields ...string) (string, bool) {
	v, found, err := unstructured.NestedString(u.Object, fields...)
	if err != nil || !found {
		return "", false
	}
	return v, true
}

func nestedInt64(u *unstructured.Unstructured, fields ...string) (int64, bool) {
	v, found, err := unstructured.NestedInt64(u.Object, fields...)
	if err != nil || !found {
		return 0, false
	}
	return v, true
}

func portList(u *unstructured.Unstructured) string {
	ports, found, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
	if !found {
		return ""
	}
	var parts []string
	for _, p := range ports {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		port := fmt.Sprintf("%v", m["port"])
		if nodePort, ok := m["nodePort"]; ok && nodePort != nil {
			port += ":" + fmt.Sprintf("%v", nodePort)
		}
		proto, _ := m["protocol"].(string)
		if proto == "" {
			proto = "TCP"
		}
		parts = append(parts, fmt.Sprintf("%s/%s", port, proto))
	}
	return strings.Join(parts, ", ")
}

// serviceAddresses 计算 Service 的访问地址（前端一键复制用）：
// LoadBalancer 优先外部 IP/域名，ExternalName 用其目标名，其余用集群内 DNS；
// 每个端口各生成一条 host:port（多端口 Service 不丢端口）
func serviceAddresses(u *unstructured.Unstructured) []string {
	if u.GetKind() != "Service" {
		return nil
	}
	svcType, _ := nestedString(u, "spec", "type")
	var host string
	switch {
	case svcType == "ExternalName":
		name, _ := nestedString(u, "spec", "externalName")
		host = name
	case svcType == "LoadBalancer":
		if ip, ok := nestedString(u, "status", "loadBalancer", "ingress", "0", "ip"); ok && ip != "" {
			host = ip
		} else if hn, ok := nestedString(u, "status", "loadBalancer", "ingress", "0", "hostname"); ok && hn != "" {
			host = hn
		}
	}
	if host == "" {
		host = u.GetName()
		if ns := u.GetNamespace(); ns != "" {
			host = fmt.Sprintf("%s.%s.svc.cluster.local", u.GetName(), ns)
		}
	}
	ports, found, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
	if !found {
		return []string{host}
	}
	// 同一端口可能同时声明 UDP/TCP（如 kube-dns 的 53），按 host:port 去重
	out := make([]string, 0, len(ports))
	seen := make(map[string]bool, len(ports))
	for _, p := range ports {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if port, ok := m["port"].(int64); ok && port > 0 {
			addr := fmt.Sprintf("%s:%d", host, port)
			if !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, host)
	}
	return out
}

func ingressHosts(u *unstructured.Unstructured) string {
	rules, found, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	if !found {
		return ""
	}
	var hosts []string
	for _, r := range rules {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		host, _ := m["host"].(string)
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return strings.Join(hosts, ", ")
}

func dataKeys(u *unstructured.Unstructured) []string {
	keys := make([]string, 0, 8)
	if data, found, _ := unstructured.NestedMap(u.Object, "data"); found {
		for k := range data {
			keys = append(keys, k)
		}
	}
	if data, found, _ := unstructured.NestedMap(u.Object, "stringData"); found {
		for k := range data {
			keys = append(keys, k)
		}
	}
	return keys
}

// ------------------- Deployment 回滚（rollout undo 语义） -------------------

// RolloutItem Deployment 的一个历史版本（由 ReplicaSet 呈现）
type RolloutItem struct {
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Replicas int32  `json:"replicas"`
	Age      string `json:"age"`
	Current  bool   `json:"current"` // 模板与 Deployment 当前一致
	ChangeCause string `json:"changeCause"`
}

const (
	revisionAnn  = "deployment.kubernetes.io/revision"
	changeCauseAnn = "deployment.kubernetes.io/change-cause"
)

// ListRollouts 列出 Deployment 的历史版本（按 revision 降序）
func (s *K8sService) ListRollouts(ctx context.Context, c *kube.Client, namespace, name string) ([]RolloutItem, error) {
	dep, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	depTpl, _ := json.Marshal(dep.Spec.Template)
	owned := func(rs *appsv1.ReplicaSet) bool {
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == name && ref.UID == dep.UID {
				return true
			}
		}
		return false
	}
	rsList, err := c.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := make([]RolloutItem, 0, 8)
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !owned(rs) {
			continue
		}
		rev, _ := strconv.Atoi(rs.Annotations[revisionAnn])
		image := ""
		if len(rs.Spec.Template.Spec.Containers) > 0 {
			image = rs.Spec.Template.Spec.Containers[0].Image
		}
		rsTpl, _ := json.Marshal(rs.Spec.Template)
		items = append(items, RolloutItem{
			Revision: rev, Name: rs.Name, Image: image,
			Replicas: rs.Status.Replicas, Age: ageOf(rs.CreationTimestamp),
			Current: bytes.Equal(depTpl, rsTpl), ChangeCause: rs.Annotations[changeCauseAnn],
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Revision > items[j].Revision })
	return items, nil
}

// RollbackDeployment 回滚到指定 revision（将该 ReplicaSet 的 Pod 模板写回 Deployment）
func (s *K8sService) RollbackDeployment(ctx context.Context, c *kube.Client, namespace, name string, revision int) error {
	dep, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	rsList, err := c.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var target *appsv1.ReplicaSet
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		owned := false
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == name && ref.UID == dep.UID {
				owned = true
				break
			}
		}
		if owned && rs.Annotations[revisionAnn] == strconv.Itoa(revision) {
			target = rs
			break
		}
	}
	if target == nil {
		return fmt.Errorf("未找到 revision %d 对应的历史版本", revision)
	}
	dep.Spec.Template = *target.Spec.Template.DeepCopy()
	if _, err := c.Clientset.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}
