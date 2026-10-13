package service

// 真实集群核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveMenu
//
// 菜单能力表是按"内置 view 到底能读哪些资源"写的，这句话在集群里，不在我这脑子里。
// 这里把真机 clusterrole/view 的规则喂进同一条链路（rolePermOf → VisibleMenus → APICapFor），
// 并顺手验一遍"只授了 view 的真实用户"能不能被推出合理的一套菜单——上游角色一改，这里先响。
import (
	"context"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
)

func liveMenuClient(t *testing.T) *kube.Client {
	t.Helper()
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
	return c
}

// 内置 view 角色的真实规则 → 能力集合 → 菜单，必须与 menu_test.go 的 fixture 口径一致
func TestLiveMenuViewRole(t *testing.T) {
	c := liveMenuClient(t)
	ctx := context.Background()
	cr, err := c.Clientset.RbacV1().ClusterRoles().Get(ctx, "view", metav1.GetOptions{})
	if err != nil {
		t.Skipf("读不到 clusterrole/view: %v", err)
	}
	rp := rolePermOf(cr.Rules)
	for _, want := range []string{"core/configmaps", "core/services", "core/pods", "apps/deployments", "core/namespaces", "core/resourcequotas"} {
		if !rp.readRes[want] {
			t.Errorf("内置 view 应能读 %s（能力表按这条写）", want)
		}
	}
	for _, no := range []string{"core/secrets", "core/nodes", "core/persistentvolumes", "rbac.authorization.k8s.io/roles", "apiextensions.k8s.io/customresourcedefinitions"} {
		if rp.readRes[no] {
			t.Errorf("内置 view 不该能读 %s：能力表里据此隐藏对应菜单的判断要重看", no)
		}
	}
	// fixture 与真机一致性：菜单结论必须完全相同
	real := &Access{NS: map[string]bool{}, NSRead: map[string]bool{"app": true}, readRes: rp.readRes}
	fake := viewAccess()
	got, want := map[string]bool{}, map[string]bool{}
	for _, m := range VisibleMenus(real) {
		got[m] = true
	}
	for _, m := range VisibleMenus(fake) {
		want[m] = true
	}
	for _, k := range []string{MenuMatrix, MenuPods, "/resources/services", "/resources/secrets", MenuNodes, MenuCRD, MenuQuotas, MenuMonitor} {
		if got[k] != want[k] {
			t.Errorf("%s 可见性在真机 view 与测试 fixture 之间不一致：真机=%v fixture=%v", k, got[k], want[k])
		}
	}
	// 接口收口的结论也要跟着走
	if _, need := APICapFor("/api/resources/secrets"); !need {
		t.Error("/api/resources/secrets 未纳入收口")
	}
	if c2, need := APICapFor("/api/resources/configmaps"); !need || !real.Meets(c2) {
		t.Error("按真机 view 应通过 configmaps 的收口")
	}
	if c2, need := APICapFor("/api/resources/secrets"); !need || real.Meets(c2) {
		t.Error("按真机 view 不该通过 secrets 的收口")
	}
	// 项目层"admin"用的就是上游 ClusterRole/admin，而它是**命名空间级**角色（不含 nodes、
	// CRD、namespaces、PV）。所以节点/自定义资源页对项目 admin 隐藏是正确的——集群级
	// 页面要靠集群层角色或平台角色。这条把该结论钉住，并确认它比 view 多开了 secrets。
	adm, err := c.Clientset.RbacV1().ClusterRoles().Get(ctx, "admin", metav1.GetOptions{})
	if err != nil {
		t.Logf("读不到 clusterrole/admin，跳过: %v", err)
		return
	}
	arp := rolePermOf(adm.Rules)
	aacc := &Access{NS: map[string]bool{}, NSRead: map[string]bool{"app": true},
		readRes: arp.readRes, allRead: arp.allRead}
	if !aacc.MenuVisible("/resources/secrets") {
		t.Error("项目 admin 能读 secrets，保密字典该给它")
	}
	if !aacc.MenuVisible(MenuMatrix) || !aacc.MenuVisible(MenuMonitor) {
		t.Error("项目 admin 该看得见工作负载与监控")
	}
	if !aacc.MenuVisible(MenuNamespaces) {
		t.Error("命名空间页是导航入口：上游 admin 不含 namespaces 读权限，仍要按\"有任意角色\"给它")
	}
	for _, k := range []string{MenuNodes, MenuCRD, "/resources/persistentvolumes"} {
		if aacc.MenuVisible(k) {
			t.Errorf("上游 admin 角色不含该资源读权限，%s 不该给它（集群级页面走集群层/平台角色）", k)
		}
	}
}

// 真机里"只在某 ns 绑了 view 的账号"：DB 侧一条绑定都没有，菜单必须完全由 K8s 侧推出来
// （这就是 mergeAccess 里 readRes 并集那一步存在的理由）
func TestLiveMenuRealViewUser(t *testing.T) {
	c := liveMenuClient(t)
	ctx := context.Background()
	var username, ns string
	rbs, err := c.Clientset.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Skipf("列 RoleBinding 失败: %v", err)
	}
	// 挑一个非 system: 前缀的真实用户主体，且它在整个集群里只有 view 这一类只读绑定
	for i := range rbs.Items {
		b := &rbs.Items[i]
		if b.RoleRef.Name != "view" && b.RoleRef.Name != "namespace-user-view" {
			continue
		}
		for _, s := range b.Subjects {
			if s.Kind == "User" && s.Name != "" && len(s.Name) > 4 {
				username, ns = s.Name, b.Namespace
			}
		}
	}
	if username == "" {
		t.Skip("该集群没有绑 view 的真实用户，跳过（可用 kubectl create rolebinding 造一个再跑）")
	}
	svc := NewPermissionService(nil, nil, "")
	kacc := svc.fromK8sRBAC(ctx, c, "live", username)
	if len(kacc.readRes) == 0 {
		t.Fatalf("用户 %s 在 ns %s 有 view 绑定，却没推出任何读能力", username, ns)
	}
	if !kacc.NSRead[ns] {
		t.Errorf("NSRead 没含 %s: %+v", ns, kacc.NSRead)
	}
	// DB 侧空的 Access（模拟"权限只在 kubectl 里授过、没落库"）
	dbAcc := &Access{NS: map[string]bool{}, NSRead: map[string]bool{}, readRes: map[string]bool{}}
	mergeAccess(dbAcc, kacc)
	if len(dbAcc.Menus) == 0 {
		t.Fatalf("并完 K8s 侧权限后菜单仍为空——收口会把 %s 挡成白屏", username)
	}
	has := map[string]bool{}
	for _, m := range dbAcc.Menus {
		has[m] = true
	}
	if !has[MenuOverview] {
		t.Errorf("只读用户至少该看得见集群总览: %v", dbAcc.Menus)
	}
	t.Logf("用户 %s（ns %s）菜单 %d 项：工作负载=%v Pod=%v 保密字典=%v 节点=%v",
		username, ns, len(dbAcc.Menus), has[MenuMatrix], has[MenuPods], has["/resources/secrets"], has[MenuNodes])
}
