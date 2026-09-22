package service

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// rolePermOf 只看动词/资源，不看角色名：名字叫 admin 的只读 Role 不能算可写。
func TestRolePermOf(t *testing.T) {
	cases := []struct {
		name                   string
		rules                  []rbacv1.PolicyRule
		wantWrite, wantCluster bool
	}{
		{"view（只读）", []rbacv1.PolicyRule{{Verbs: []string{"get", "list", "watch"}, Resources: []string{"*"}}}, false, false},
		{"edit（ns 写）", []rbacv1.PolicyRule{{Verbs: []string{"get", "create", "update", "patch", "delete"}, Resources: []string{"pods", "deployments"}}}, true, false},
		{"cluster-admin（*/*）", []rbacv1.PolicyRule{{Verbs: []string{"*"}, Resources: []string{"*"}}}, true, true},
		{"只写 nodes", []rbacv1.PolicyRule{{Verbs: []string{"patch"}, Resources: []string{"nodes"}}}, true, true},
		{"名字叫 admin 但只读", []rbacv1.PolicyRule{{Verbs: []string{"get"}, Resources: []string{"pods"}}}, false, false},
		{"只写 pods/log 子资源", []rbacv1.PolicyRule{{Verbs: []string{"get"}, Resources: []string{"pods/log"}}}, false, false},
	}
	for _, c := range cases {
		got := rolePermOf(c.rules)
		if got.Write != c.wantWrite || got.ClusterWrite != c.wantCluster {
			t.Errorf("%s: rolePermOf = %+v, want write=%v cluster=%v", c.name, got, c.wantWrite, c.wantCluster)
		}
	}
}

func crb(name string, subjects []rbacv1.Subject) rbacv1.ClusterRoleBinding {
	return rbacv1.ClusterRoleBinding{
		RoleRef:  rbacv1.RoleRef{Kind: "ClusterRole", Name: name},
		Subjects: subjects,
	}
}

func rb(ns, name string, subjects []rbacv1.Subject) rbacv1.RoleBinding {
	return rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: name},
		Subjects:   subjects,
	}
}

func user(n string) []rbacv1.Subject {
	return []rbacv1.Subject{{Kind: "User", Name: n}}
}

// 授权页三种角色的判定：view 只读、edit 单 ns 可写、admin（ns 级）可写。
func TestBuildAccessNamespaceLevels(t *testing.T) {
	users := user("zhang.san")
	rules := map[string][]rbacv1.PolicyRule{
		"view":  {{Verbs: []string{"get", "list", "watch"}, Resources: []string{"*"}}},
		"edit":  {{Verbs: []string{"get", "create", "update", "patch", "delete"}, Resources: []string{"pods", "deployments"}}},
		"admin": {{Verbs: []string{"get", "create", "delete"}, Resources: []string{"roles", "rolebindings", "pods"}}},
	}
	resolve := func(ref rbacv1.RoleRef, _ string) rolePerm { return rolePermOf(rules[ref.Name]) }

	// view：不可写
	acc := buildAccess(nil, []rbacv1.RoleBinding{rb("dev", "view", users)}, "zhang.san", nil, false, resolve)
	if acc.CanWriteNS("dev") || acc.AllNSWrite || acc.CanWriteCluster() {
		t.Errorf("view 用户不应有写权限: %+v", acc)
	}
	// edit：dev 可写，其它 ns 不可写
	acc = buildAccess(nil, []rbacv1.RoleBinding{rb("dev", "edit", users)}, "zhang.san", nil, false, resolve)
	if !acc.CanWriteNS("dev") {
		t.Error("edit 用户应能写 dev")
	}
	if acc.CanWriteNS("prod") || acc.CanWriteNS("") {
		t.Error("edit 用户不应能写未授权的 ns（含 default）")
	}
	if acc.AllNSWrite || acc.CanWriteCluster() {
		t.Error("ns 级 edit 不应变成全集群可写")
	}
	// admin（ns 级）：与 edit 同权可写，但仍不是集群级
	acc = buildAccess(nil, []rbacv1.RoleBinding{rb("dev", "admin", users)}, "zhang.san", nil, false, resolve)
	if !acc.CanWriteNS("dev") || acc.CanWriteCluster() {
		t.Errorf("ns admin 判定错误: %+v", acc)
	}
}

// 全集群授权：edit 级 → 所有 ns 可写但不能动集群级资源；cluster-admin 级 → 集群级也可写。
func TestBuildAccessClusterWide(t *testing.T) {
	users := user("li.si")
	rules := map[string][]rbacv1.PolicyRule{
		"edit":          {{Verbs: []string{"create", "update"}, Resources: []string{"pods"}}},
		"cluster-admin": {{Verbs: []string{"*"}, Resources: []string{"*"}}},
	}
	resolve := func(ref rbacv1.RoleRef, _ string) rolePerm { return rolePermOf(rules[ref.Name]) }

	acc := buildAccess([]rbacv1.ClusterRoleBinding{crb("edit", users)}, nil, "li.si", nil, false, resolve)
	if !acc.CanWriteNS("any-ns") || acc.CanWriteCluster() {
		t.Errorf("全集群 edit：应所有 ns 可写、集群级不可写: %+v", acc)
	}
	acc = buildAccess([]rbacv1.ClusterRoleBinding{crb("cluster-admin", users)}, nil, "li.si", nil, false, resolve)
	if !acc.CanWriteNS("any-ns") || !acc.CanWriteCluster() {
		t.Errorf("cluster-admin：集群级应可写: %+v", acc)
	}
}

// 主体匹配：用户名、组名命中才授权；未命中一律无权限。
func TestBuildAccessSubjectMatch(t *testing.T) {
	resolve := func(rbacv1.RoleRef, string) rolePerm { return rolePerm{Write: true} }

	byUser := []rbacv1.RoleBinding{rb("dev", "edit", user("zhang.san"))}
	if acc := buildAccess(nil, byUser, "wang.wu", nil, false, resolve); acc.CanWriteNS("dev") {
		t.Error("非绑定主体不应有权限")
	}
	if acc := buildAccess(nil, byUser, "zhang.san", nil, false, resolve); !acc.CanWriteNS("dev") {
		t.Error("绑定主体应可写")
	}
	byGroup := []rbacv1.RoleBinding{rb("dev", "edit", []rbacv1.Subject{{Kind: "Group", Name: "devs"}})}
	if acc := buildAccess(nil, byGroup, "anyone", []string{"devs"}, false, resolve); !acc.CanWriteNS("dev") {
		t.Error("组主体命中应可写")
	}
	if acc := buildAccess(nil, byGroup, "anyone", []string{"others"}, false, resolve); acc.CanWriteNS("dev") {
		t.Error("组主体未命中不应有权限")
	}
}

// 多 ns 参数：每一段都要有授权；"*" 只在全集群授权时成立。
func TestCanWriteNSMulti(t *testing.T) {
	acc := &Access{NS: map[string]bool{"dev": true, "qa": true}}
	if !acc.CanWriteNS("dev,qa") {
		t.Error("dev,qa 都已授权应放行")
	}
	if acc.CanWriteNS("dev,prod") {
		t.Error("含未授权 ns 应拒绝")
	}
	if acc.CanWriteNS("*") {
		t.Error("通配只在全集群授权时成立")
	}
	all := &Access{AllNSWrite: true, NS: map[string]bool{}}
	if !all.CanWriteNS("*") || !all.CanWriteNS("") || !all.CanWriteNS("whatever") {
		t.Error("全集群可写应覆盖一切 ns 形式")
	}
	admin := &Access{PlatformAdmin: true}
	if !admin.CanWriteCluster() || !admin.CanWriteNS("prod") {
		t.Error("平台管理员应全部放行")
	}
	var nilAcc *Access
	if nilAcc.CanWriteNS("dev") || nilAcc.CanWriteCluster() {
		t.Error("nil Access 应一律拒绝")
	}
}
