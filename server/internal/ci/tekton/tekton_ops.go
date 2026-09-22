package tekton

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Tekton v1 GVK。
var (
	gvkPipeline    = schema.GroupVersionKind{Group: "tekton.dev", Version: "v1", Kind: "Pipeline"}
	gvkPipelineRun = schema.GroupVersionKind{Group: "tekton.dev", Version: "v1", Kind: "PipelineRun"}
	gvkTaskRun     = schema.GroupVersionKind{Group: "tekton.dev", Version: "v1", Kind: "TaskRun"}
)

// RunStatus 是 PipelineRun / TaskRun 归一化后的状态。
type RunStatus struct {
	Status         string // pending / running / success / failed / cancelled
	Reason         string // Tekton reason（如 PipelineRunCancelled）
	StartTime      *time.Time
	CompletionTime *time.Time
}

// TaskRunInfo 是一个 TaskRun 的摘要（供回写 task_runs 表）。
type TaskRunInfo struct {
	Name     string // k8s TaskRun 名
	TaskName string // 对应 DSL 节点 id（label tekton.dev/pipelineTaskName）
	PodName  string
	Status   RunStatus
}

// ApplyPipeline 创建或替换一个 Pipeline CR。
// 全量替换 spec 必须走 Update（merge-patch 无法删除 spec 里被移除的 task），
// 但 Get→Update 之间对象可能被并发修改产生 409 Conflict——对 Conflict 做
// 有限次重试（重新 Get 最新 resourceVersion 再 Update）。
func (c *Client) ApplyPipeline(ctx context.Context, namespace, name string, spec map[string]interface{}) error {
	cli := c.dyn.Resource(gvkPipeline.GroupVersion().WithResource("pipelines")).Namespace(namespace)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		obj := &unstructured.Unstructured{Object: spec}
		obj.SetAPIVersion("tekton.dev/v1")
		obj.SetKind("Pipeline")
		obj.SetNamespace(namespace)
		obj.SetName(name)

		existing, err := cli.Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			_, err = cli.Create(ctx, obj, metav1.CreateOptions{})
			if errors.IsConflict(err) {
				lastErr = err // 并发 create 撞名 → 下轮走 Update 路径
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		if _, err = cli.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			if errors.IsConflict(err) {
				lastErr = err
				continue
			}
			return err
		}
		return nil
	}
	return lastErr
}

// RunParam 是 PipelineRun 的参数（对应 Pipeline.spec.params）。
type RunParam struct {
	Name  string
	Value string
}

// CreatePipelineRun 创建一个引用指定 Pipeline 的 PipelineRun，绑定共享 PVC workspace。
// imagePullSecrets 非空时挂到 podTemplate.spec.imagePullSecrets（registry 类型凭证，
// 镜像拉取走 Secret 而非 env）；cacheClaim 非空时额外绑定 optional 的 cache workspace
//（项目级依赖缓存，任务未声明该 workspace 时无副作用）。
func (c *Client) CreatePipelineRun(ctx context.Context, namespace, runName, pipelineName, serviceAccount, pvcClaim, cacheClaim string, params []RunParam, imagePullSecrets []string) error {
	podTemplate := map[string]interface{}{
		// 保留终态 Pod，任务结束后日志仍可回看
		"preserveLogs": true,
	}
	if len(imagePullSecrets) > 0 {
		ses := make([]interface{}, 0, len(imagePullSecrets))
		for _, s := range imagePullSecrets {
			ses = append(ses, map[string]interface{}{"name": s})
		}
		podTemplate["imagePullSecrets"] = ses
	}
	spec := map[string]interface{}{
		"pipelineRef": map[string]interface{}{"name": pipelineName},
		"podTemplate": podTemplate,
	}
	// Tekton v1：执行 Pod 的 SA 走 spec.taskRunTemplate.serviceAccountName
	// （顶层 serviceAccountName 是 v1beta1 字段，v1 会忽略并告警）。
	if serviceAccount != "" {
		spec["taskRunTemplate"] = map[string]interface{}{"serviceAccountName": serviceAccount}
	}
	workspaces := []interface{}{
		map[string]interface{}{
			"name":                  "shared",
			"persistentVolumeClaim": map[string]interface{}{"claimName": pvcClaim},
		},
	}
	if cacheClaim != "" {
		workspaces = append(workspaces, map[string]interface{}{
			"name":                  "cache",
			"persistentVolumeClaim": map[string]interface{}{"claimName": cacheClaim},
		})
	}
	spec["workspaces"] = workspaces
	if len(params) > 0 {
		ps := make([]interface{}, 0, len(params))
		for _, p := range params {
			ps = append(ps, map[string]interface{}{"name": p.Name, "value": p.Value})
		}
		spec["params"] = ps
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       "PipelineRun",
		"metadata": map[string]interface{}{
			"name":      runName,
			"namespace": namespace,
			// 平台标记 label：syncer 的 ListPipelineRunStatuses 按它过滤，
			// 缺失会导致所有 run 在宽限期后被误判为「集群中不存在」而置 failed。
			// Tekton 不会把 Pipeline CR 的 label 传播到 PipelineRun，必须显式打。
			"labels": map[string]interface{}{"ci-platform.io/name": pipelineName},
		},
		"spec": spec,
	}}
	cli := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace)
	_, err := cli.Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// CancelPipelineRun 取消一个运行中的 PipelineRun：
// 置 spec.status=PipelineRunCancelled（Tekton v1 标准取消方式），
// controller 停掉未开始的 TaskRun 并终止进行中的 Pod。
// 已处于终态的 PipelineRun 再次 patch 无副作用（幂等）。
func (c *Client) CancelPipelineRun(ctx context.Context, namespace, name string) error {
	cli := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace)
	patch, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"status": "PipelineRunCancelled"},
	})
	if err != nil {
		return err
	}
	_, err = cli.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// ListPipelineRunStatuses 一次拉取平台全部 PipelineRun 的状态
// （syncer 批量同步用，替代每 run 一次 Get，API 调用与并发 run 数解耦）。
// 返回 name → RunStatus。
// 平台 run 名固定带 "run-" 前缀（StartRun 命名约定），namespace 为平台专用，
// 按前缀过滤而不是 label——修复前创建的 run 没有 ci-platform.io/name label，
// 按 label 过滤会把它们全部漏掉，导致 syncer 误判「集群中不存在」而置 failed。
func (c *Client) ListPipelineRunStatuses(ctx context.Context, namespace string) (map[string]RunStatus, error) {
	cli := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace)
	list, err := cli.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]RunStatus, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		if !strings.HasPrefix(it.GetName(), "run-") {
			continue
		}
		out[it.GetName()] = parseStatusFromUnstructured(it)
	}
	return out, nil
}

// ListTaskRunsAll 一次拉取 namespace 内全部平台 TaskRun，按 pipelineRun 分组
// （syncer 批量同步用，替代每 run 一次 ListTaskRuns）。
func (c *Client) ListTaskRunsAll(ctx context.Context, namespace string) (map[string][]TaskRunInfo, error) {
	cli := c.dyn.Resource(gvkTaskRun.GroupVersion().WithResource("taskruns")).Namespace(namespace)
	list, err := cli.List(ctx, metav1.ListOptions{LabelSelector: "tekton.dev/pipelineRun"})
	if err != nil {
		return nil, err
	}
	out := map[string][]TaskRunInfo{}
	for i := range list.Items {
		it := &list.Items[i]
		labels := it.GetLabels()
		pr := labels["tekton.dev/pipelineRun"]
		if pr == "" {
			continue
		}
		taskName := labels["tekton.dev/pipelineTask"]
		if taskName == "" {
			taskName = labels["tekton.dev/pipelineTaskName"]
		}
		info := TaskRunInfo{Name: it.GetName(), TaskName: taskName}
		if pod, found, _ := unstructured.NestedString(it.Object, "status", "podName"); found {
			info.PodName = pod
		}
		info.Status = parseStatusFromUnstructured(it)
		out[pr] = append(out[pr], info)
	}
	return out, nil
}

// GetPipelineRunStatus 读取 PipelineRun 状态并归一化。
// 不存在时返回 apierrors NotFound（调用方据此判断 PipelineRun 是否被删），
// 不再需要单独 Exists 探测（省一次 Get）。
func (c *Client) GetPipelineRunStatus(ctx context.Context, namespace, name string) (RunStatus, error) {
	var out RunStatus
	cli := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace)
	obj, err := cli.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	return parseStatusFromUnstructured(obj), nil
}

// ListTaskRuns 列出某 PipelineRun 下的全部 TaskRun。
// 用 LabelSelector 在 API server 侧过滤（tekton.dev/pipelineRun 标签由 Tekton 打），
// 不再全量 list 整个 namespace 的 TaskRun。
func (c *Client) ListTaskRuns(ctx context.Context, namespace, pipelineRunName string) ([]TaskRunInfo, error) {
	cli := c.dyn.Resource(gvkTaskRun.GroupVersion().WithResource("taskruns")).Namespace(namespace)
	list, err := cli.List(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + pipelineRunName,
	})
	if err != nil {
		return nil, err
	}
	var out []TaskRunInfo
	for i := range list.Items {
		it := &list.Items[i]
		labels := it.GetLabels()
		// 任务名标签：v1 为 tekton.dev/pipelineTask（beta 旧版是 pipelineTaskName，两者都认）
		taskName := labels["tekton.dev/pipelineTask"]
		if taskName == "" {
			taskName = labels["tekton.dev/pipelineTaskName"]
		}
		info := TaskRunInfo{
			Name:     it.GetName(),
			TaskName: taskName,
		}
		// podName
		if pod, found, _ := unstructured.NestedString(it.Object, "status", "podName"); found {
			info.PodName = pod
		}
		info.Status = parseStatusFromUnstructured(it)
		out = append(out, info)
	}
	return out, nil
}

// GetTaskRunResults 读取某 TaskRun 的 results（status.results: [{name, value}]）。
// 任务模板把产出（imageRef / storage_path / sha256 / git_commit 等）写进 results，
// 平台据此注册制品记录。
func (c *Client) GetTaskRunResults(ctx context.Context, namespace, taskRunName string) (map[string]string, error) {
	out := map[string]string{}
	cli := c.dyn.Resource(gvkTaskRun.GroupVersion().WithResource("taskruns")).Namespace(namespace)
	obj, err := cli.Get(ctx, taskRunName, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	ress, found, _ := unstructured.NestedSlice(obj.Object, "status", "results")
	if !found {
		return out, nil
	}
	for _, r := range ress {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		value, _ := rm["value"].(string)
		if name != "" {
			out[name] = value
		}
	}
	return out, nil
}

// AnnotateTaskRun 给 TaskRun 打/更新单个注解（人工审批：ci-platform.io/approval）。
// 用 MergePatch（不带 resourceVersion）：只动 metadata.annotations，
// 避免与 Tekton controller 高频更新 status 产生的 409 Conflict 竞态。
func (c *Client) AnnotateTaskRun(ctx context.Context, namespace, taskRunName, key, value string) error {
	cli := c.dyn.Resource(gvkTaskRun.GroupVersion().WithResource("taskruns")).Namespace(namespace)
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{key: value},
		},
	})
	if err != nil {
		return err
	}
	_, err = cli.Patch(ctx, taskRunName, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// GetTaskRunAnnotation 读 TaskRun 的单个注解（不存在返回 ""）。
func (c *Client) GetTaskRunAnnotation(ctx context.Context, namespace, taskRunName, key string) (string, error) {
	cli := c.dyn.Resource(gvkTaskRun.GroupVersion().WithResource("taskruns")).Namespace(namespace)
	obj, err := cli.Get(ctx, taskRunName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return obj.GetAnnotations()[key], nil
}

// GetTaskRunPodContainers 返回某 TaskRun Pod 的 step 容器名列表（用于定位日志容器）。
func (c *Client) GetTaskRunPodContainers(ctx context.Context, namespace, podName string) ([]string, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ctr := range pod.Spec.Containers {
		names = append(names, ctr.Name)
	}
	return names, nil
}

// StreamPodLogs 流式输出 Pod 中指定容器的日志到 w（follow=true 时持续 tail）。
func (c *Client) StreamPodLogs(ctx context.Context, namespace, podName, container string, follow bool, w io.Writer) error {
	req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		Follow:    follow,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// CreatePVC 创建共享 workspace PVC。
func (c *Client) CreatePVC(ctx context.Context, namespace, name, storage string) error {
	return c.EnsurePVC(ctx, namespace, name, storage, "ReadWriteOnce")
}

// EnsurePVC 幂等创建 PVC（已存在视为成功）：
//   - workspace PVC（ci-ws-*）：每 run 一个，已存在即并发/脏数据异常
//   - 缓存 PVC（ci-cache-*）：项目级共享，长期存在，重复创建是正常路径
func (c *Client) EnsurePVC(ctx context.Context, namespace, name, storage, accessMode string) error {
	if accessMode == "" {
		accessMode = "ReadWriteOnce"
	}
	// 用户可配的存储量（CI_CACHE_SIZE）：非法值报错而不是 panic（MustParse 会炸）
	qty, err := resource.ParseQuantity(storage)
	if err != nil {
		return fmt.Errorf("非法存储量 %q: %w", storage, err)
	}
	spec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.PersistentVolumeAccessMode(accessMode)},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: qty,
			},
		},
	}
	// 无默认 StorageClass 的集群必须显式指定，否则 PVC 永远 Pending
	if sc := c.cfg.PVCStorageClass; sc != "" {
		spec.StorageClassName = &sc
	}
	_, err = c.clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       spec,
	}, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// DeletePVC 删除 PVC（不存在忽略）。
func (c *Client) DeletePVC(ctx context.Context, namespace, name string) error {
	err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// ============ workload 镜像快照 / 回滚（发布管理） ============

// GetDeploymentImages 读取某 deployment 的 容器名 → 镜像 快照。
func (c *Client) GetDeploymentImages(ctx context.Context, namespace, name string) (map[string]string, error) {
	d, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		out[ctr.Name] = ctr.Image
	}
	return out, nil
}

// ListDeploymentsByLabel 按 label selector 列出 deployment 名
//（helm release 的 workload 用 app.kubernetes.io/instance=<release> 定位）。
func (c *Client) ListDeploymentsByLabel(ctx context.Context, namespace, selector string) ([]string, error) {
	list, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out, nil
}

// SetDeploymentImages 把 deployment 的容器镜像恢复为快照值（一键回滚）：
// 只更新快照里存在且当前 spec 中也存在的容器（快照外的容器不动）。
// 返回是否发生了实际更新。
func (c *Client) SetDeploymentImages(ctx context.Context, namespace, name string, images map[string]string) (bool, error) {
	if len(images) == 0 {
		return false, fmt.Errorf("镜像快照为空")
	}
	d, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	changed := false
	for i := range d.Spec.Template.Spec.Containers {
		if img, ok := images[d.Spec.Template.Spec.Containers[i].Name]; ok &&
			d.Spec.Template.Spec.Containers[i].Image != img {
			d.Spec.Template.Spec.Containers[i].Image = img
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

// ============ 孤儿资源清理（reconcile） ============

// ListPipelineRuns 列出 namespace 内所有 PipelineRun 名。
func (c *Client) ListPipelineRuns(ctx context.Context, namespace string) ([]string, error) {
	list, err := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range list.Items {
		out = append(out, list.Items[i].GetName())
	}
	return out, nil
}

// DeletePipelineRun 删除 PipelineRun（不存在忽略）。
func (c *Client) DeletePipelineRun(ctx context.Context, namespace, name string) error {
	err := c.dyn.Resource(gvkPipelineRun.GroupVersion().WithResource("pipelineruns")).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// ListPipelines 列出 namespace 内所有 Pipeline CR 名。
func (c *Client) ListPipelines(ctx context.Context, namespace string) ([]string, error) {
	list, err := c.dyn.Resource(gvkPipeline.GroupVersion().WithResource("pipelines")).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range list.Items {
		out = append(out, list.Items[i].GetName())
	}
	return out, nil
}

// DeletePipeline 删除 Pipeline CR（不存在忽略）。
func (c *Client) DeletePipeline(ctx context.Context, namespace, name string) error {
	err := c.dyn.Resource(gvkPipeline.GroupVersion().WithResource("pipelines")).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// ListPVCs 列出 namespace 内前缀匹配的 PVC 名。
func (c *Client) ListPVCs(ctx context.Context, namespace, prefix string) ([]string, error) {
	list, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range list.Items {
		if strings.HasPrefix(list.Items[i].GetName(), prefix) {
			out = append(out, list.Items[i].GetName())
		}
	}
	return out, nil
}

// DeletePod 删除 Pod（不存在忽略）。preserveLogs 保留的终态 Pod 由此清理。
func (c *Client) DeletePod(ctx context.Context, namespace, name string) error {
	err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// ListTaskRunPods 列出 namespace 内平台创建的 TaskRun Pod（带 tekton.dev/pipelineRun 标签）。
func (c *Client) ListTaskRunPods(ctx context.Context, namespace string) (map[string]string, error) {
	// podName -> pipelineRunName
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun",
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i := range list.Items {
		out[list.Items[i].GetName()] = list.Items[i].GetLabels()["tekton.dev/pipelineRun"]
	}
	return out, nil
}

// ============ helpers ============

// parseStatusFromUnstructured 从 status.conditions 里取 Succeeded 条件，归一化为平台状态。
func parseStatusFromUnstructured(obj *unstructured.Unstructured) RunStatus {
	var out RunStatus
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		out.Status = "pending"
		return out
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] != "Succeeded" {
			continue
		}
		status, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		out.Reason = reason
		out.Status = mapCondition(status, reason)
		break
	}
	if out.Status == "" {
		out.Status = "pending"
	}
	if st, found, _ := unstructured.NestedString(obj.Object, "status", "startTime"); found {
		if t, err := time.Parse(time.RFC3339, st); err == nil {
			out.StartTime = &t
		}
	}
	if ct, found, _ := unstructured.NestedString(obj.Object, "status", "completionTime"); found {
		if t, err := time.Parse(time.RFC3339, ct); err == nil {
			out.CompletionTime = &t
		}
	}
	return out
}

func mapCondition(status, reason string) string {
	// when 表达式未命中：TaskRun 被 Tekton 立即终结（Succeeded/True，
	// reason=WhenExpressionsEvaluatedFalse，即 kubectl 显示的 SKIPPED）。
	// 不识别该 reason 会映射成 success/running，节点在前端永远不收敛。
	if reason == "WhenExpressionsEvaluatedFalse" {
		return "skipped"
	}
	switch status {
	case "True":
		return "success"
	case "False":
		if reason == "PipelineRunCancelled" || reason == "TaskRunCancelled" {
			return "cancelled"
		}
		return "failed"
	default: // Unknown
		if reason == "PipelineRunCancelled" || reason == "TaskRunCancelled" {
			return "cancelled"
		}
		return "running"
	}
}
