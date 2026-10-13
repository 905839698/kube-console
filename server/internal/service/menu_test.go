package service

import (
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

// 内置/自定义角色的规则集直接喂给 rolePermOf：这里断的是"能力集合"这一层，
// 菜单表与接口收口都建立在它之上，判错一处就会整块菜单看不见或挡不住。
func TestRolePermOfReadCapabilities(t *testing.T) {
	view := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{""}, Resources: []string{"pods"}},
		{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{"apps"}, Resources: []string{"deployments"}},
	})
	if !view.readRes["core/pods"] || !view.readRes["apps/deployments"] {
		t.Errorf("只读角色没收集到读能力: %+v", view.readRes)
	}
	if view.Write || view.allRead {
		t.Errorf("只读角色不该有写能力或通配: %+v", view)
	}
	// 写角色天然可读，否则"能改却看不见"
	edit := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"create", "update"}, APIGroups: []string{"batch"}, Resources: []string{"jobs"}},
	})
	if !edit.readRes["batch/jobs"] || !edit.Write {
		t.Errorf("写角色应同时算可读: %+v", edit)
	}
	star := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
	})
	if !star.allRead || !star.Write || !star.ClusterWrite {
		t.Errorf("通配规则应判 allRead + 集群写: %+v", star)
	}
	// 只有写动词、资源是 nodes：集群级写
	cluster := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"update"}, APIGroups: []string{""}, Resources: []string{"nodes"}},
	})
	if !cluster.ClusterWrite {
		t.Errorf("nodes 写应算集群级写: %+v", cluster)
	}
	// * 子资源（pods/log 之类）不该把整组资源放大成通配
	sub := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods/log"}},
	})
	if sub.allRead || sub.readRes["core/pods/log"] != true {
		t.Errorf("子资源应只登记自己这一条: %+v", sub)
	}
}

func accWith(reads []string, nsRead map[string]bool) *Access {
	rs := map[string]bool{}
	for _, r := range reads {
		rs[r] = true
	}
	if nsRead == nil {
		nsRead = map[string]bool{}
	}
	return &Access{NSRead: nsRead, NS: map[string]bool{}, readRes: rs}
}

// upstreamViewRules 是真机 clusterrole/view 的规则（kubectl get clusterrole view 抄下来的）。
// 用它当"项目只读用户"的输入，而不是我自己想象的一套：这张表判对判错，
// 直接决定普通用户在侧边栏看见什么、哪些接口会 403。
func upstreamViewRules() []rbacv1.PolicyRule {
	get := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Verbs: get, Resources: []string{
			"configmaps", "endpoints", "persistentvolumeclaims", "persistentvolumeclaims/status",
			"pods", "replicationcontrollers", "replicationcontrollers/scale", "serviceaccounts",
			"services", "services/status"}},
		{APIGroups: []string{""}, Verbs: get, Resources: []string{
			"bindings", "limitranges", "namespaces/status", "pods/log", "pods/status",
			"resourcequotas", "resourcequotas/status", "namespaces", "events"}},
		{APIGroups: []string{"", "events.k8s.io"}, Verbs: get, Resources: []string{"events"}},
		{APIGroups: []string{"discovery.k8s.io"}, Verbs: get, Resources: []string{"endpointslices"}},
		{APIGroups: []string{"apps"}, Verbs: get, Resources: []string{
			"controllerrevisions", "daemonsets", "deployments", "deployments/scale", "replicasets", "statefulsets"}},
		{APIGroups: []string{"autoscaling"}, Verbs: get, Resources: []string{"horizontalpodautoscalers"}},
		{APIGroups: []string{"batch"}, Verbs: get, Resources: []string{"cronjobs", "jobs"}},
		{APIGroups: []string{"networking.k8s.io"}, Verbs: get, Resources: []string{"ingresses", "networkpolicies"}},
		{APIGroups: []string{"policy"}, Verbs: get, Resources: []string{"poddisruptionbudgets"}},
		// metrics.k8s.io 的 nodes 也可读，但节点页要看的是 core/nodes：不能混为一谈
		{APIGroups: []string{"metrics.k8s.io"}, Verbs: get, Resources: []string{"pods", "nodes"}},
	}
}

func viewAccess() *Access {
	a := &Access{NS: map[string]bool{}, NSRead: map[string]bool{"app": true}}
	a.readRes = rolePermOf(upstreamViewRules()).readRes
	return a
}

// 项目层只读用户：命名空间级资源页可见，集群级（节点/存储类/CRD）与平台页不可见
func TestVisibleMenusForNsViewer(t *testing.T) {
	a := viewAccess()
	has := map[string]bool{}
	for _, m := range VisibleMenus(a) {
		has[m] = true
	}
	for _, want := range []string{
		MenuOverview, MenuMatrix, MenuPods, "/workloads/deployments", "/resources/services",
		"/resources/persistentvolumeclaims", "/resources/limitranges", MenuEvents, MenuCI,
		MenuMonitor, MenuAlerts, MenuQuotas, MenuNamespaces, MenuHelm, "/kb/mysql", MenuNacosSvc,
		"group:workload-group", "group:storage-group", "group:observe-group", "group:kb-group",
	} {
		if !has[want] {
			t.Errorf("view 用户应看见 %s，实际菜单 %v", want, has)
		}
	}
	// 集群级资源与平台功能：view 没有对应读能力，就不该出现入口
	for _, no := range []string{
		MenuNodes, MenuCRD, "/resources/secrets", "/resources/persistentvolumes",
		"/resources/storageclasses", "/resources/roles", "/resources/clusterroles",
		MenuClusters, MenuUsers, MenuAuthz, MenuRoles, MenuAudit, MenuRegistry, MenuBackups,
		"group:platform-group",
	} {
		if has[no] {
			t.Errorf("view 用户不该看见 %s", no)
		}
	}
	// metrics.k8s.io/nodes 可读不等于 core/nodes 可读
	if c, need := APICapFor("/api/nodes/n1"); !need || a.Meets(c) {
		t.Error("view 用户没有 core/nodes 读能力，节点接口要挡住")
	}
}

// 只给了 configmaps/secrets 读：配置分组可见，其余分组不该跟着冒出来
func TestVisibleMenusPerResourceGroup(t *testing.T) {
	a := accWith([]string{"core/configmaps", "core/secrets"}, nil)
	has := map[string]bool{}
	for _, m := range VisibleMenus(a) {
		has[m] = true
	}
	if !has["/resources/secrets"] || !has["group:config-group"] {
		t.Errorf("能读 configmaps/secrets 就该给配置分组: %v", has)
	}
	if has["group:storage-group"] || has[MenuNodes] || has[MenuMonitor] {
		t.Errorf("没有存储/节点/监控读能力却给了分组: %v", has)
	}
}

// 零角色用户（登录了但什么都没授权）：只剩"登录即可"的那几项，集群数据模块一个不给
func TestVisibleMenusForUserWithoutRoles(t *testing.T) {
	a := accWith(nil, nil)
	has := map[string]bool{}
	for _, m := range VisibleMenus(a) {
		has[m] = true
	}
	// Nacos 服务发现/配置、Chart 仓库源是平台侧功能，口径就是登录可浏览（既有行为）
	for _, want := range []string{MenuNacosSvc, MenuNacosCfg, MenuHelmRepos} {
		if !has[want] {
			t.Errorf("零角色用户应仍看见 %s（登录可浏览的既有口径）", want)
		}
	}
	for _, no := range []string{MenuOverview, MenuMonitor, MenuCI, MenuHelm, MenuKBRepos, MenuNodes,
		MenuCRD, "/resources/configmaps", "/workloads/pods", MenuClusters, MenuUsers, "group:workload-group"} {
		if has[no] {
			t.Errorf("零角色用户不该看见 %s", no)
		}
	}
	// 集群数据类接口一律挡住
	for _, p := range []string{"/api/overview", "/api/monitor/overview", "/api/kb/clusters", "/api/resources/configmaps", "/api/nodes"} {
		if c, need := APICapFor(p); !need || a.Meets(c) {
			t.Errorf("零角色用户应被 %s 挡住", p)
		}
	}
}

// 平台观察者：平台分组只读可见，但 admin 专属项不给
func TestVisibleMenusPlatformViewer(t *testing.T) {
	a := &Access{PlatformViewer: true, NS: map[string]bool{}, NSRead: map[string]bool{}}
	has := map[string]bool{}
	for _, m := range VisibleMenus(a) {
		has[m] = true
	}
	for _, want := range []string{MenuAuthz, MenuRoles, MenuAudit, MenuUsage, MenuClusters, MenuMonitor, "group:platform-group"} {
		if !has[want] {
			t.Errorf("viewer 应能看见 %s", want)
		}
	}
	for _, no := range []string{MenuUsers, MenuNotify, MenuRegistry, MenuBackups, MenuNacosAdmin} {
		if has[no] {
			t.Errorf("viewer 不该看见 %s", no)
		}
	}
	// 平台角色的用户组/审计/授权接口收口必须与菜单同一结论
	for _, p := range []string{"/api/groups", "/api/audit", "/api/authz/bindings", "/api/rbac/roles", "/api/clusters"} {
		if c, need := APICapFor(p); !need || !a.Meets(c) {
			t.Errorf("viewer 应能通过 %s 的收口", p)
		}
	}
	for _, p := range []string{"/api/users", "/api/backups", "/api/notify/channels", "/api/registry/config", "/api/nacos/config"} {
		if c, need := APICapFor(p); !need || a.Meets(c) {
			t.Errorf("viewer 不该通过 %s 的收口", p)
		}
	}
}

// 平台管理员：全给（含六个引擎子菜单），且永不收口的路径保持放过
func TestVisibleMenusPlatformAdmin(t *testing.T) {
	a := &Access{PlatformAdmin: true, NS: map[string]bool{}, NSRead: map[string]bool{}}
	menus := VisibleMenus(a)
	for _, want := range []string{MenuNodes, MenuCRD, MenuUsers, "/resources/secrets", "group:kb-group"} {
		if !slices.Contains(menus, want) {
			t.Errorf("管理员应看见 %s", want)
		}
	}
	if got := len(engineMenus); got != 6 {
		t.Errorf("引擎子菜单应有 6 个，实际 %d", got)
	}
	for _, e := range engineMenus {
		if !slices.Contains(menus, e) {
			t.Errorf("管理员应看见引擎菜单 %s", e)
		}
	}
}

// sidebarKeys 是 MainLayout 里挂在 el-menu-item 上的全部 index。后端只把登记过的键
// 发给前端，所以这张清单必须能被 menuCaps / workloadItemCaps / engineMenus 全覆盖——
// 加页面忘了登记就会"谁都看不见"，靠这条测试兜住。
var sidebarKeys = []string{
	"/argocd", "/argocd/repos", "/audit", "/authz", "/backups", "/ci", "/clusters", "/events",
	"/helm/releases", "/helm/repos", "/kb/repos", "/logsearch", "/micro/configs", "/micro/services",
	"/monitor", "/monitor/alerts", "/monitor/grafana", "/nacos", "/namespaces", "/nodes", "/notify",
	"/overview", "/quotas", "/registry", "/resources", "/roles", "/usage", "/users",
	"/workloads/matrix", "/workloads/pods",
	"/resources/clusterrolebindings", "/resources/clusterroles", "/resources/configmaps",
	"/resources/endpointslices", "/resources/gatewayclasses", "/resources/gateways",
	"/resources/grpcroutes", "/resources/horizontalpodautoscalers", "/resources/httproutes",
	"/resources/ingresses", "/resources/limitranges", "/resources/networkpolicies",
	"/resources/persistentvolumeclaims", "/resources/persistentvolumes", "/resources/referencegrants",
	"/resources/rolebindings", "/resources/roles", "/resources/routes", "/resources/secrets",
	"/resources/serviceaccounts", "/resources/services", "/resources/storageclasses",
	"/resources/tcproutes", "/resources/tlsroutes", "/resources/udproutes",
	"/workloads/cronjobs", "/workloads/daemonsets", "/workloads/deployments", "/workloads/jobs",
	"/workloads/statefulsets",
}

func TestVisibleMenusCoversSidebar(t *testing.T) {
	// 管理员视角：侧边栏每一个键都要出现在 menus 里
	a := &Access{PlatformAdmin: true, NS: map[string]bool{}, NSRead: map[string]bool{}}
	has := map[string]bool{}
	for _, m := range VisibleMenus(a) {
		has[m] = true
	}
	for _, k := range sidebarKeys {
		if !has[k] {
			t.Errorf("侧边栏项 %s 没有登记进能力表：管理员都拿不到，普通用户永远看不见", k)
		}
	}
	for _, k := range engineMenus {
		if !has[k] {
			t.Errorf("引擎子菜单 %s 没发给前端", k)
		}
	}
	// 反向：能力表里的键也得真有个页面/分组，拼错的路径不会报错只会静默失效
	for g, items := range menuGroups {
		for _, it := range items {
			if _, ok := menuCaps[it]; ok {
				continue
			}
			if _, ok := workloadItemCaps[it]; ok {
				continue
			}
			if slices.Contains(engineMenus, it) {
				continue
			}
			t.Errorf("分组 %s 里的 %s 不是登记过的菜单键（拼错了？）", g, it)
		}
	}
}

// 接口收口的路径映射：真实路由没有版本段（/api/<模块>/...），前缀写错就等于没收口
func TestAPICapForPaths(t *testing.T) {
	cases := []struct {
		path string
		need bool
		res  string // 期望要求的资源（空表示只校验是否收口）
	}{
		// 永不收口：登录、权限快照、集群切换、个人 token
		{"/api/auth/login", false, ""},
		{"/api/auth/me", false, ""},
		{"/api/rbac/my-permissions", false, ""},
		{"/api/authz/my-permissions", false, ""},
		{"/api/my-clusters", false, ""},
		{"/api/my-clusters/connectivity", false, ""},
		{"/api/tokens", false, ""},
		{"/api/health", false, ""},
		// 有意不收口：跨模块共用的辅助接口、以及登录即可用的镜像标签查询
		{"/api/resource-defs", false, ""},
		{"/api/gateway-availability", false, ""},
		{"/api/yaml", false, ""},
		{"/api/yaml/apply", false, ""},
		{"/api/registry/image-tags", false, ""},
		{"/api/registry/image-vulns", false, ""},
		// 模块前缀
		{"/api/kb/engines", true, "apps.kubeblocks.io/clusters"},
		{"/api/ci/projects", true, ""},
		{"/api/argocd/apps", true, "argoproj.io/applications"},
		{"/api/helm/releases", true, "core/secrets"},
		{"/api/monitor/overview", true, "core/services"},
		{"/api/overview", true, "core/nodes"},
		{"/api/events", true, "core/events"},
		{"/api/search", true, ""},
		{"/api/logs/search", true, ""},
		{"/api/logsources", false, ""}, // 带斜杠才匹配，别把连接配置一起放进来
		{"/api/quotas/overview", true, "core/resourcequotas"},
		{"/api/namespaces", true, "core/namespaces"},
		{"/api/alertevents/stats", true, ""},
		{"/api/alertmanager", true, ""},
		{"/api/rbac/roles", true, ""},
		{"/api/nodes/n1", true, "core/nodes"},
		// 资源类：按 URL 里的资源名判
		{"/api/resources/configmaps", true, "core/configmaps"},
		{"/api/resources/secrets/x/y/yaml", true, "core/secrets"},
		{"/api/generic/apps/v1/deployments", true, "apps/deployments"},
		{"/api/generic/core/v1/pods", true, "core/pods"},
		{"/api/generic/core/v1/pods/ns/name/yaml", true, "core/pods"},
		{"/api/workloads/statefulsets", true, "apps/statefulsets"},
		{"/api/workloads/matrix", true, "apps/deployments"},
		{"/api/pods/mypod/logs", true, "core/pods"},
	}
	for _, c := range cases {
		got, need := APICapFor(c.path)
		if need != c.need {
			t.Errorf("%s: 是否收口 = %v，期望 %v", c.path, need, c.need)
			continue
		}
		if !need || c.res == "" {
			continue
		}
		found := false
		for _, r := range got.res {
			if permResKey(r[0], r[1]) == c.res {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: 应要求 %s 可读，实际 %+v", c.path, c.res, got.res)
		}
	}
}

// 资源类接口收口就是"这个资源可读才给这页"：不拿 ns 角色兜底，
// 否则等于把所有命名空间级资源页放回"登录可浏览"。
func TestResourceCapFloor(t *testing.T) {
	a := viewAccess()
	if c, need := APICapFor("/api/resources/configmaps"); !need || !a.Meets(c) {
		t.Error("view 能读 configmaps，资源接口该放过")
	}
	if c, need := APICapFor("/api/resources/secrets"); !need || a.Meets(c) {
		t.Error("view 读不到 secrets，资源接口该挡住")
	}
	// 完全没有资源读能力、只有 ns 角色：模块页放过，资源页挡住
	nsOnly := accWith(nil, map[string]bool{"app": true})
	if c, need := APICapFor("/api/monitor/overview"); !need || !nsOnly.Meets(c) {
		t.Error("有任一 ns 角色就该能看监控（总览页也依赖它）")
	}
	if c, need := APICapFor("/api/resources/configmaps"); !need || nsOnly.Meets(c) {
		t.Error("只有 ns 角色、没有 configmaps 读能力时，资源浏览器该挡住")
	}
}

// menus 字段进快照：前端侧边栏完全按它渲染
func TestAccessSnapshotCarriesMenus(t *testing.T) {
	a := accWith([]string{"core/pods"}, map[string]bool{"app": true})
	a.Menus = VisibleMenus(a)
	if len(a.Menus) == 0 {
		t.Fatal("menus 为空，前端会把整个侧边栏藏掉")
	}
	if !slices.Contains(a.Menus, MenuPods) {
		t.Errorf("能读 pods 却没给 Pod 菜单: %v", a.Menus)
	}
	if !slices.Contains(a.Menus, "group:workload-group") {
		t.Errorf("有可见子项却没给分组键: %v", a.Menus)
	}
	if slices.Contains(a.Menus, MenuNodes) {
		t.Errorf("读不到 nodes 却给了节点菜单: %v", a.Menus)
	}
}

// 单个资源的自定义角色不能把整个侧边栏点亮：apiGroups=* + resources=pods 只给 pods
func TestWildcardGroupRoleDoesNotGrantEverything(t *testing.T) {
	rp := rolePermOf([]rbacv1.PolicyRule{
		{Verbs: []string{"get", "list"}, APIGroups: []string{"*"}, Resources: []string{"pods"}},
	})
	if rp.allRead {
		t.Fatal("apiGroups=* 而 resources 是具体资源，不该判成全读")
	}
	a := &Access{NS: map[string]bool{}, NSRead: map[string]bool{}, readRes: rp.readRes}
	if !a.CanReadAPI("", "pods") {
		t.Error("*/pods 规则应覆盖 core/pods")
	}
	if a.CanReadAPI("apps", "deployments") {
		t.Error("不该因此就能读 deployments")
	}
	if !a.Meets(capPods) || a.Meets(capWorkload) {
		t.Error("Pod 菜单该给，工作负载菜单不该给")
	}
	// 核心组 resources=* 只放开核心组资源
	b := &Access{NS: map[string]bool{}, NSRead: map[string]bool{},
		readRes: rolePermOf([]rbacv1.PolicyRule{
			{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{""}, Resources: []string{"*"}},
		}).readRes}
	if !b.CanReadAPI("", "secrets") || b.CanReadAPI("apps", "deployments") {
		t.Errorf("核心组通配不该扩到 apps 组: secrets=%v deployments=%v", b.CanReadAPI("", "secrets"), b.CanReadAPI("apps", "deployments"))
	}
	// */* 才是全读
	c := &Access{NS: map[string]bool{}, NSRead: map[string]bool{}, allRead: true}
	if !c.Meets(capNodes) || !c.Meets(capCRD) {
		t.Error("全读应点亮全部资源菜单")
	}
}

// 权限来自"运维手工建的 K8s RBAC"（DB 里一条绑定都没有）时，菜单必须照样能算出来：
// load() 把两侧并集后要重算 Menus，漏了就等于把这批用户挡在门外。
func TestMergeAccessCarriesReadCapsFromK8s(t *testing.T) {
	kp := rolePermOf(upstreamViewRules())
	kacc := &Access{NS: map[string]bool{}, NSRead: map[string]bool{"app": true}, readRes: kp.readRes}
	dbAcc := &Access{NS: map[string]bool{}, NSRead: map[string]bool{}, readRes: map[string]bool{}}
	if len(VisibleMenus(dbAcc)) >= len(VisibleMenus(kacc)) {
		t.Fatal("前置条件不成立：空权限的菜单不该不比 view 多")
	}
	mergeAccess(dbAcc, kacc)
	if !slices.Contains(dbAcc.Menus, MenuMatrix) {
		t.Errorf("并完 K8s 侧 view 之后应能看见工作负载菜单: %v", dbAcc.Menus)
	}
	if !dbAcc.CanReadAPI("apps", "deployments") {
		t.Error("readRes 没并过来")
	}
	if !dbAcc.NSRead["app"] {
		t.Error("NSRead 没并过来")
	}
	// 并集后接口收口也放开（同一份能力，两边结论必须一致）
	if c, need := APICapFor("/api/resources/configmaps"); !need || !dbAcc.Meets(c) {
		t.Error("view 用户应通过 configmaps 资源接口收口")
	}
	if c, need := APICapFor("/api/resources/secrets"); !need || dbAcc.Meets(c) {
		t.Error("并完仍不该能读 secrets")
	}
}
