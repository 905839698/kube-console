package tekton

import (
	"context"
	"errors"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
)

const testNS = "qa-demo"

func newTestClient() *Client {
	// EnsureCIInfra 只走 clientset，dynamic 留空即可
	return NewClient("c1", &kube.Client{Clientset: fake.NewSimpleClientset()}, &config.CIConfig{})
}

// 新项目 ns：三件套 + 部署 Role/RoleBinding 全部写入，且运行 SA 是部署绑定的 subject。
func TestEnsureCIInfraCreatesAll(t *testing.T) {
	c := newTestClient()
	created, err := c.EnsureCIInfra(context.Background(), testNS, "ci-bot")
	if err != nil {
		t.Fatalf("EnsureCIInfra: %v", err)
	}
	if len(created) != 5 {
		t.Fatalf("created = %v, want 5 个对象", created)
	}

	ctx := context.Background()
	if _, err := c.clientset.CoreV1().ServiceAccounts(testNS).Get(ctx, "ci-bot", metav1.GetOptions{}); err != nil {
		t.Errorf("ServiceAccount 未创建: %v", err)
	}
	for _, name := range []string{"ci-bot", DeployRoleName} {
		if _, err := c.clientset.RbacV1().Roles(testNS).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Errorf("Role %s 未创建: %v", name, err)
		}
		rb, err := c.clientset.RbacV1().RoleBindings(testNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("RoleBinding %s 未创建: %v", name, err)
			continue
		}
		if rb.RoleRef.Name != name {
			t.Errorf("RoleBinding %s 的 roleRef = %s, want %s", name, rb.RoleRef.Name, name)
		}
		if !containsSubject(rb.Subjects, rbacv1.Subject{
			Kind: rbacv1.ServiceAccountKind, Name: "ci-bot", Namespace: testNS,
		}) {
			t.Errorf("RoleBinding %s 缺少运行 SA subject: %v", name, rb.Subjects)
		}
	}

	// 缺了这条权限，k8s-deploy/helm-deploy 用 in-cluster 身份部署会 forbidden
	deploy, err := c.clientset.RbacV1().Roles(testNS).Get(ctx, DeployRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("读 ci-deploy Role: %v", err)
	}
	if !hasRule(deploy.Rules, "apps", "deployments") {
		t.Errorf("ci-deploy Role 缺少 apps/deployments 权限: %v", deploy.Rules)
	}
}

// 幂等：重复调用不再写任何对象（run 每次提交都会调一次）。
func TestEnsureCIInfraIdempotent(t *testing.T) {
	c := newTestClient()
	ctx := context.Background()
	if _, err := c.EnsureCIInfra(ctx, testNS, "ci-bot"); err != nil {
		t.Fatalf("第一次: %v", err)
	}
	created, err := c.EnsureCIInfra(ctx, testNS, "ci-bot")
	if err != nil {
		t.Fatalf("第二次: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("第二次 created = %v, want 空", created)
	}
}

// 存量集群：ci-deploy 的 Role/RoleBinding 已存在，绑定指向别处的 SA
// （老布局 ci-bot@ci-projects，如 qa-service-manager），只追加本 ns 的 SA，
// 原有 subject 与 roleRef 不动、既有 Role 不覆盖。
func TestEnsureCIInfraAppendsSubjectToExistingBinding(t *testing.T) {
	c := newTestClient()
	ctx := context.Background()
	if _, err := c.clientset.RbacV1().Roles(testNS).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: DeployRoleName, Namespace: testNS},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("预置 Role: %v", err)
	}
	legacy := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "ci-bot", Namespace: "ci-projects"}
	if _, err := c.clientset.RbacV1().RoleBindings(testNS).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: DeployRoleName, Namespace: testNS},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: DeployRoleName},
		Subjects:   []rbacv1.Subject{legacy},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("预置绑定: %v", err)
	}

	created, err := c.EnsureCIInfra(ctx, testNS, "ci-bot")
	if err != nil {
		t.Fatalf("EnsureCIInfra: %v", err)
	}
	// 既有 Role 不覆盖、绑定只追加 subject：SA + 运行 Role/RoleBinding + 追加 = 4 个写入
	if len(created) != 4 {
		t.Errorf("created = %v, want 4 个对象", created)
	}
	// 人工维护的 ci-deploy Role 不被平台改写（这里故意只给了 get deployments）
	role, err := c.clientset.RbacV1().Roles(testNS).Get(ctx, DeployRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("读 Role: %v", err)
	}
	if len(role.Rules) != 1 || !hasRule(role.Rules, "apps", "deployments") {
		t.Errorf("既有 ci-deploy Role 被覆盖: %v", role.Rules)
	}
	rb, err := c.clientset.RbacV1().RoleBindings(testNS).Get(ctx, DeployRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("读绑定: %v", err)
	}
	if len(rb.Subjects) != 2 || !containsSubject(rb.Subjects, legacy) {
		t.Errorf("subjects = %v, want 保留 %v 并追加本 ns SA", rb.Subjects, legacy)
	}
	if !containsSubject(rb.Subjects, rbacv1.Subject{
		Kind: rbacv1.ServiceAccountKind, Name: "ci-bot", Namespace: testNS,
	}) {
		t.Errorf("subjects 缺少本 ns SA: %v", rb.Subjects)
	}
}

// 未配置运行 SA：不做任何写入（Tekton 退回 ns 内 default，不是平台管理的身份）。
func TestEnsureCIInfraEmptySANoOp(t *testing.T) {
	c := newTestClient()
	created, err := c.EnsureCIInfra(context.Background(), testNS, "")
	if err != nil {
		t.Fatalf("EnsureCIInfra: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("created = %v, want 空", created)
	}
	if _, err := c.clientset.RbacV1().Roles(testNS).Get(context.Background(), DeployRoleName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("saName 为空时不应写入 Role，err = %v", err)
	}
}

// SA 写不进去时要能被调用方识别（提交 run 前快速失败，而不是等 PodCreationFailed）。
func TestEnsureCIInfraSAFailureSentinel(t *testing.T) {
	c := newTestClient()
	c.clientset.(*fake.Clientset).PrependReactor("create", "serviceaccounts",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
	if _, err := c.EnsureCIInfra(context.Background(), testNS, "ci-bot"); !errors.Is(err, ErrInfraSAUnavailable) {
		t.Errorf("err = %v, want ErrInfraSAUnavailable", err)
	}
}

// hasRule 判断规则集里是否存在某 apiGroup/resource 的授权。
func hasRule(rules []rbacv1.PolicyRule, group, resource string) bool {
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				if g == group && res == resource {
					return true
				}
			}
		}
	}
	return false
}
