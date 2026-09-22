package tekton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DeployRoleName 项目 ns 内允许 CI 部署工作负载的 Role/RoleBinding 名。
// 沿用存量集群约定（目标 ns 里已有的就是这个名），自动写入的结果与人工准备的一致。
const DeployRoleName = "ci-deploy"

// ErrInfraSAUnavailable 运行 ServiceAccount 不存在且未能创建。
// 提交 run 前据此快速失败：缺 SA 时 TaskRun 会以 PodCreationFailed 收场，
// 而没有 Pod 就没有日志、失败原因只能去集群里翻 TaskRun 事件。
var ErrInfraSAUnavailable = errors.New("运行 ServiceAccount 不可用")

// rwVerbs 常规读写动词（读 + 写 + 删），用于工作负载类权限。
var rwVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

// EnsureCIInfra 确保项目 ns 具备 CI 运行所需的基础设施（幂等，可反复调用）。
// 写入三类对象：
//
//  1. ServiceAccount <saName>：PipelineRun 的 Pod 以它运行
//     （spec.taskRunTemplate.serviceAccountName）。缺失时 TaskRun 直接
//     PodCreationFailed（serviceaccounts "ci-bot" not found），连 Pod 都不会创建。
//  2. Role/RoleBinding <saName>：运行期自身权限（审批节点读 TaskRun、事件与
//     日志读取、ns 内 Secret/PVC）。
//  3. Role/RoleBinding ci-deploy：任务内脚本用 in-cluster 身份部署到本项目 ns
//     时的工作负载权限（k8s-deploy / helm-deploy 节点 credential 留空时走它）。
//
// 只补缺：已存在的 Role/RoleBinding 不覆盖（保留人工调整），RoleBinding 只追加
// 缺失的 subject。返回本次实际写入的对象名（全部已存在时为空），供调用方记日志。
// saName 为空表示平台未指定运行 SA（Tekton 退回 ns 内 default），此时不做任何写入。
func (c *Client) EnsureCIInfra(ctx context.Context, ns, saName string) ([]string, error) {
	if ns == "" || saName == "" {
		return nil, nil
	}
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		return nil, fmt.Errorf("确保命名空间 %s: %w", ns, err)
	}
	sa := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: ns}
	var created []string

	// 1) 运行 SA —— 这一条缺了，run 必然以 PodCreationFailed 失败
	changed, err := c.ensureServiceAccount(ctx, ns, saName)
	if err != nil {
		return created, fmt.Errorf("%w: 创建 ServiceAccount %s/%s 失败: %v", ErrInfraSAUnavailable, ns, saName, err)
	}
	if changed {
		created = append(created, "serviceaccount/"+saName)
	}

	// 2) 运行期权限 + 绑定
	changed, err = c.ensureRole(ctx, ns, saName, ciRunRoleRules())
	if err != nil {
		return created, fmt.Errorf("创建 Role %s/%s 失败: %v", ns, saName, err)
	}
	if changed {
		created = append(created, "role/"+saName)
	}
	changed, err = c.ensureRoleBinding(ctx, ns, saName, saName, []rbacv1.Subject{sa})
	if err != nil {
		return created, fmt.Errorf("创建 RoleBinding %s/%s 失败: %v", ns, saName, err)
	}
	if changed {
		created = append(created, "rolebinding/"+saName)
	}

	// 3) 部署权限 + 绑定（subject 指向本 ns 的运行 SA：Pod 里 kubectl 的
	//    in-cluster 身份就是 <saName>@<ns>，跨 ns 的 ci-bot@ci-projects 那种
	//    绑定只对「在 ci-projects 里运行、部署到别的 ns」的老布局有效）
	changed, err = c.ensureRole(ctx, ns, DeployRoleName, ciDeployRoleRules())
	if err != nil {
		return created, fmt.Errorf("创建 Role %s/%s 失败: %v", ns, DeployRoleName, err)
	}
	if changed {
		created = append(created, "role/"+DeployRoleName)
	}
	changed, err = c.ensureRoleBinding(ctx, ns, DeployRoleName, DeployRoleName, []rbacv1.Subject{sa})
	if err != nil {
		return created, fmt.Errorf("创建 RoleBinding %s/%s 失败: %v", ns, DeployRoleName, err)
	}
	if changed {
		created = append(created, "rolebinding/"+DeployRoleName)
	}
	return created, nil
}

// ciRunRoleRules 项目 ns 内 CI 运行自身需要的权限，逐条对齐存量 ci-projects/ci-bot
// （与线上 Role 直接对照即可 review）。
func ciRunRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"tekton.dev"},
			Resources: []string{"pipelines", "pipelineruns", "tasks", "taskruns"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
		{
			APIGroups: []string{"tekton.dev"},
			Resources: []string{"pipelineruns/finalizers", "taskruns/finalizers"},
			Verbs:     []string{"update"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/log"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"},
			Verbs:     []string{"get", "list", "watch", "create", "delete"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts"},
			Verbs:     []string{"get", "list", "watch", "create"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"namespaces"},
			Verbs:     []string{"get", "create"},
		},
	}
}

// ciDeployRoleRules 任务内 kubectl/helm 部署到本项目 ns 需要的权限，
// 逐条对齐存量目标 ns 里的 ci-deploy Role。
func ciDeployRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"apps"},
			Resources: []string{"deployments", "statefulsets", "daemonsets", "replicasets"},
			Verbs:     rwVerbs,
		},
		{
			APIGroups: []string{""},
			Resources: []string{"services", "configmaps", "secrets", "persistentvolumeclaims"},
			Verbs:     rwVerbs,
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/log"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"networking.k8s.io"},
			Resources: []string{"ingresses"},
			Verbs:     rwVerbs,
		},
		{
			APIGroups: []string{"batch"},
			Resources: []string{"jobs", "cronjobs"},
			Verbs:     rwVerbs,
		},
		{
			APIGroups: []string{"autoscaling"},
			Resources: []string{"horizontalpodautoscalers"},
			Verbs:     rwVerbs,
		},
	}
}

// ensureServiceAccount 确保 SA 存在，返回本次是否创建。
func (c *Client) ensureServiceAccount(ctx context.Context, ns, name string) (bool, error) {
	_, err := c.clientset.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	_, err = c.clientset.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureRole 确保 Role 存在，返回本次是否创建。
// 已存在时不做覆盖：存量集群里的 ci-bot / ci-deploy 是人工维护的，
// 平台按「补缺」语义写入，避免把运维手工放宽/收紧的规则改回去。
func (c *Client) ensureRole(ctx context.Context, ns, name string, rules []rbacv1.PolicyRule) (bool, error) {
	_, err := c.clientset.RbacV1().Roles(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	_, err = c.clientset.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Rules:      rules,
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureRoleBinding 确保 RoleBinding 存在且包含给定 subjects，返回本次是否有写入。
//   - 不存在：按 roleName + subjects 创建；
//   - 已存在：只合并缺失的 subject（roleRef 在 K8s 中不可变，不去动它）。
//
// 用 MergePatch 提交合并结果，避免 Get→Update 的 409（与 EnsureSecret 同一考虑）。
func (c *Client) ensureRoleBinding(ctx context.Context, ns, name, roleName string, subjects []rbacv1.Subject) (bool, error) {
	rb := c.clientset.RbacV1().RoleBindings(ns)
	existing, err := rb.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = rb.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName,
			},
			Subjects: subjects,
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	merged := existing.Subjects
	missing := false
	for _, want := range subjects {
		if !containsSubject(merged, want) {
			merged = append(merged, want)
			missing = true
		}
	}
	if !missing {
		return false, nil
	}
	b, err := json.Marshal(map[string]interface{}{"subjects": merged})
	if err != nil {
		return false, err
	}
	if _, err := rb.Patch(ctx, name, types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

// containsSubject 判断 subject 是否已在绑定里（kind/name/namespace 三元组）。
func containsSubject(list []rbacv1.Subject, want rbacv1.Subject) bool {
	for _, s := range list {
		if s.Kind == want.Kind && s.Name == want.Name && s.Namespace == want.Namespace {
			return true
		}
	}
	return false
}
