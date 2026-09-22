// 集群事件中心与节点运维接口（cordon / drain / 污点 / 标签）
package handler

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/pkg/response"
)


// namespaceList 解析命名空间参数（空或 * = 全部）
func namespaceList(q string) []string {
	if q == "" || q == "*" {
		return []string{""}
	}
	out := []string{}
	for _, p := range strings.Split(q, ",") {
		if p = strings.TrimSpace(p); p != "" && p != "*" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// ------------------- 事件中心 -------------------

// Events GET /events?namespace=&type=&search=&limit=
// type: "" 全部 / Warning / Normal
func (h *K8sHandler) Events(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	limit := 300
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	evType := c.Query("type")
	search := strings.ToLower(c.Query("search"))

	items := make([]gin.H, 0)
	for _, ns := range namespaceList(c.Query("namespace")) {
		list, err := client.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			response.Fail(c, 400, 400, "读取事件失败: "+err.Error())
			return
		}
		for i := range list.Items {
			ev := &list.Items[i]
			if evType != "" && ev.Type != evType {
				continue
			}
			if search != "" {
				hay := strings.ToLower(ev.Name + " " + ev.Reason + " " + ev.Message + " " + ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name)
				if !strings.Contains(hay, search) {
					continue
				}
			}
			last := ev.LastTimestamp
			if last.IsZero() {
				last = ev.FirstTimestamp
			}
			if last.IsZero() {
				last = metav1.NewTime(ev.CreationTimestamp.Time)
			}
			items = append(items, gin.H{
				"name":      ev.Name,
				"namespace": ev.Namespace,
				"type":      ev.Type,
				"reason":    ev.Reason,
				"message":   ev.Message,
				"object":    ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name,
				"count":     ev.Count,
				"lastAt":    last.Time.Format("2006-01-02 15:04:05"),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["lastAt"].(string) > items[j]["lastAt"].(string) })
	if len(items) > limit {
		items = items[:limit]
	}
	response.OK(c, items)
}

// ------------------- 节点运维 -------------------

// CordonNode POST /nodes/:name/cordon {cordon: true|false}
func (h *K8sHandler) CordonNode(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Cordon *bool `json:"cordon" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误：需要 cordon 字段")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	node, err := client.Clientset.CoreV1().Nodes().Get(ctx, c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	node.Spec.Unschedulable = *req.Cordon
	if _, err := client.Clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		response.Fail(c, 400, 400, "更新节点失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"cordon": *req.Cordon})
}

// UpdateNodeTaints PUT /nodes/:name/taints {taints:[{key,value,effect}]}
func (h *K8sHandler) UpdateNodeTaints(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Taints []corev1.Taint `json:"taints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "污点格式错误")
		return
	}
	for _, t := range req.Taints {
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectPreferNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
			response.Fail(c, 400, 400, "effect 只能是 NoSchedule / PreferNoSchedule / NoExecute")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	node, err := client.Clientset.CoreV1().Nodes().Get(ctx, c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	node.Spec.Taints = req.Taints
	if _, err := client.Clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		response.Fail(c, 400, 400, "更新污点失败: "+err.Error())
		return
	}
	response.OK(c, nil)
}

// UpdateNodeLabels PUT /nodes/:name/labels {labels: {k: v|null}}（value 为 null 表示删除该标签）
func (h *K8sHandler) UpdateNodeLabels(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Labels map[string]*string `json:"labels"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "标签格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	node, err := client.Clientset.CoreV1().Nodes().Get(ctx, c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	for k, v := range req.Labels {
		if v == nil || *v == "" {
			delete(node.Labels, k)
		} else {
			node.Labels[k] = *v
		}
	}
	if _, err := client.Clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		response.Fail(c, 400, 400, "更新标签失败: "+err.Error())
		return
	}
	response.OK(c, nil)
}

type drainReq struct {
	Force             bool `json:"force"`
	IgnoreDaemonsets  bool `json:"ignoreDaemonsets"`
	DeleteEmptydirData bool `json:"deleteEmptydirData"`
	GracePeriodSeconds int64 `json:"gracePeriodSeconds"`
	TimeoutSeconds    int64 `json:"timeoutSeconds"`
}

// DrainNode POST /nodes/:name/drain —— 封锁 + 逐个驱逐（Eviction API，尊重 PDB）
func (h *K8sHandler) DrainNode(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req drainReq
	if err := c.ShouldBindJSON(&req); err != nil {
		req = drainReq{IgnoreDaemonsets: true}
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 180
	}
	if req.IgnoreDaemonsets {
		// kubectl drain 默认忽略 DaemonSet
		req.IgnoreDaemonsets = true
	}
	// drain 是长时操作：不挂在请求 ctx 上——浏览器超时/关页不能把节点留在
	// 「已 cordon + 部分 Pod 残留」的半截状态，按自身超时跑完
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutSeconds)*time.Second+30*time.Second)
	defer cancel()
	nodeName := c.Param("name")

	// 1) 封锁
	node, err := client.Clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	node.Spec.Unschedulable = true
	if _, err := client.Clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		response.Fail(c, 400, 400, "封锁节点失败: "+err.Error())
		return
	}

	// 2) 列出节点上的 Pod
	pods, err := client.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		response.Fail(c, 400, 400, "读取节点 Pod 失败: "+err.Error())
		return
	}

	deadline := time.Now().Add(time.Duration(req.TimeoutSeconds) * time.Second)
	evicted, skipped := []string{}, []string{}
	type failItem struct{ Name, Error string }
	failed := []failItem{}

	for i := range pods.Items {
		if ctx.Err() != nil {
			break
		}
		pod := &pods.Items[i]
		display := pod.Namespace + "/" + pod.Name

		// 已结束的 Pod 无需驱逐
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			skipped = append(skipped, display+"（已结束）")
			continue
		}
		// DaemonSet Pod
		if ownerKind(pod) == "DaemonSet" {
			if req.IgnoreDaemonsets {
				skipped = append(skipped, display+"（DaemonSet）")
				continue
			}
			failed = append(failed, failItem{display, "DaemonSet Pod 需忽略或先删除 DaemonSet"})
			continue
		}
		// 静态镜像 Pod（mirror annotation）不可驱逐
		if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
			skipped = append(skipped, display+"（静态 Pod）")
			continue
		}
		// 无属主的裸 Pod
		if ownerKind(pod) == "" && !req.Force {
			skipped = append(skipped, display+"（无控制器的裸 Pod，需 force）")
			continue
		}
		// emptyDir 且未允许删除
		if hasEmptyDir(pod) && !req.DeleteEmptydirData {
			skipped = append(skipped, display+"（含 emptyDir 数据卷，需允许删除）")
			continue
		}

		// 3) 驱逐（PDB 拦截时 429，重试到超时）
		if err := evictDrain(ctx, client, pod, req.GracePeriodSeconds, deadline); err != nil {
			failed = append(failed, failItem{display, err.Error()})
			continue
		}
		evicted = append(evicted, display)
	}
	response.OK(c, gin.H{"evicted": evicted, "skipped": skipped, "failed": failed, "cordon": true})
}

func ownerKind(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind
		}
	}
	for _, ref := range pod.OwnerReferences {
		return ref.Kind
	}
	return ""
}

func hasEmptyDir(pod *corev1.Pod) bool {
	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir != nil {
			return true
		}
	}
	return false
}

// evictDrain 调用 Eviction API；被 PDB 拒绝（429 Too Many Requests）时按 3s 重试直到超时
func evictDrain(ctx context.Context, client *kube.Client, pod *corev1.Pod, grace int64, deadline time.Time) error {
	for {
		gracePeriod := grace
		ev := &policyv1.Eviction{
			ObjectMeta:    metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
			DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: &gracePeriod},
		}
		err := client.Clientset.PolicyV1().Evictions(pod.Namespace).Evict(ctx, ev)
		if err == nil {
			return nil
		}
		// PDB 拦截是 StatusError reason=TooManyRequests（message 里并没有
		// "Too Many Requests"/"429" 字样，按字符串匹配永不命中、重试逻辑形同虚设）
		if !apierrors.IsTooManyRequests(err) {
			return fmt.Errorf("%v", err)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("被 PodDisruptionBudget 拦截，等待超时")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ------------------- Deployment 回滚 -------------------

// Rollouts GET /workloads/:kind/:name/rollouts
func (h *K8sHandler) Rollouts(c *gin.Context) {
	if c.Param("kind") != "deployments" {
		response.Fail(c, 400, 400, "仅 Deployment 支持回滚")
		return
	}
	client := h.client(c)
	if client == nil {
		return
	}
	items, err := h.k8s.ListRollouts(c.Request.Context(), client, c.Query("namespace"), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, items)
}

// Rollback POST /workloads/:kind/:name/rollback {revision}
func (h *K8sHandler) Rollback(c *gin.Context) {
	if c.Param("kind") != "deployments" {
		response.Fail(c, 400, 400, "仅 Deployment 支持回滚")
		return
	}
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Revision int    `json:"revision" binding:"required"`
		Namespace string `json:"namespace" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误：需要 namespace / revision")
		return
	}
	if err := h.k8s.RollbackDeployment(c.Request.Context(), client, req.Namespace, c.Param("name"), req.Revision); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, nil)
}

// EvictPod POST /pods/:name/evict?namespace= —— 通过 Eviction API 驱逐 Pod（尊重 PDB）
func (h *K8sHandler) EvictPod(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	ns, name := c.Query("namespace"), c.Param("name")
	pod, err := client.Clientset.CoreV1().Pods(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.K8sError(c, err)
		return
	}
	if k := ownerKind(pod); k == "DaemonSet" {
		response.Fail(c, 400, 400, "DaemonSet Pod 不支持驱逐（请先封锁节点或修改 DaemonSet 容忍度）")
		return
	}
	if k := ownerKind(pod); k == "" {
		response.Fail(c, 400, 400, "静态/独立 Pod 不支持驱逐（请用删除）")
		return
	}
	ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if err := client.Clientset.PolicyV1().Evictions(ns).Evict(c.Request.Context(), ev); err != nil {
		if apierrors.IsTooManyRequests(err) {
			response.Fail(c, 429, 429, "被 PodDisruptionBudget 拒绝驱逐（最小可用副本约束），请稍后重试")
			return
		}
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"evicted": name, "namespace": ns})
}
