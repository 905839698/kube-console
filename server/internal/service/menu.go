// menu.go 菜单与接口的"能力推导"：菜单项不是独立的一套权限，而是"这个用户在当前集群
// 能读哪些资源"的结论；同一份判定也用于接口收口（开关 rbac.enforceMenus）。
//
// 为什么要一份真相：只藏菜单不拦接口 = 遮眼（前端绕过就行）；只拦接口不藏菜单 = 用户
// 看见一堆点进去 403 的入口。两边都从这里取条件，行为才对得上。
//
// 能力来源是用户生效角色的 PolicyRule（见 permission.go 的 rolePermOf 收集 readRes）：
// 内置角色、自定义角色、以及运维手工建的 K8s RBAC 都算，与写权限判定同一口径。
package service

import (
	"strings"

	"kube-console/server/internal/kube"
)

// menuCap 一个菜单项/接口组的可见条件：命中任一项即可见。
// 全空（always）表示登录即可——用于没有 K8s 资源对应的模块（Nacos 微服务、Helm 仓库源）。
type menuCap struct {
	// res 需要可读的资源：(apiGroup, resource)，任一可读即满足
	res [][2]string
	// anyNSRead 在该集群有任意命名空间的任意角色（含只读）即满足
	anyNSRead bool
	// platform 平台层角色（platform-admin 或 platform-viewer）即满足
	platform bool
	// adminOnly 只有 platform-admin 满足（viewer 不含）
	adminOnly bool
	// always 登录即可见
	always bool
}

// r 从 kube.KindMap 取 (apiGroup, resource)。KindMap 里没有的资源（少数 CRD 别名）
// 按核心组处理会判错，所以这里显式返回 ok=false，由调用方改用别的条件。
func r(plural string) [2]string {
	if gvr, ok := kube.KindMap[plural]; ok {
		return [2]string{gvr.Group, gvr.Resource}
	}
	return [2]string{"", plural}
}

// 菜单键 = 侧边栏里那个 index（路径），前端不用再维护一套 ID。
const (
	MenuOverview   = "/overview"
	MenuClusters   = "/clusters"
	MenuNamespaces = "/namespaces"
	MenuNodes      = "/nodes"
	MenuCRD        = "/resources"
	MenuMatrix     = "/workloads/matrix"
	MenuPods       = "/workloads/pods"
	MenuMonitor    = "/monitor"
	MenuAlerts     = "/monitor/alerts"
	MenuGrafana    = "/monitor/grafana"
	MenuCP         = "/monitor/controlplane"
	MenuEvents     = "/events"
	MenuLogSearch  = "/logsearch"
	MenuCI         = "/ci"
	MenuArgoCD     = "/argocd"
	MenuArgoRepos  = "/argocd/repos"
	MenuHelm       = "/helm/releases"
	MenuHelmRepos  = "/helm/repos"
	MenuNacosSvc   = "/micro/services"
	MenuNacosCfg   = "/micro/configs"
	MenuKBRepos    = "/kb/repos"
	MenuQuotas     = "/quotas"
	MenuUsers      = "/users"
	MenuAuthz      = "/authz"
	MenuRoles      = "/roles"
	MenuAudit      = "/audit"
	MenuNotify     = "/notify"
	MenuNacosAdmin = "/nacos"
	MenuRegistry   = "/registry"
	MenuUsage      = "/usage"
	MenuBackups    = "/backups"
)

// resCRD 是 KindMap 里没有的一类资源（CRD 本身），显式写死一对：capCRD 与
// 「自定义资源」接口收口共用它，避免两处各写一遍组名拼写。
var resCRD = [2]string{"apiextensions.k8s.io", "customresourcedefinitions"}

// 各能力的定义（改这里就等于同时改菜单与接口，两边不会漂开）
//
// 口径以真机 clusterrole 为准（kubectl get clusterrole view 抄下来的，menu_test.go 里
// 也拿这份真实规则做断言）：view 覆盖 configmaps/services/endpoints(lices)/pods/PVC/SA/
// workloads/ingresses/networkpolicies/resourcequotas/limitranges/namespaces/events/hpa/
// cronjobs/jobs，但**不含** secrets/nodes/persistentvolumes/storageclasses/roles/
// rolebindings/clusterroles/clusterrolebindings/customresourcedefinitions。
// 所以"保密字典"对只读用户消失、"节点"对只读用户消失，是真实能力的反映而不是漏配。
var (
	capOverview = menuCap{res: [][2]string{r("nodes")}, anyNSRead: true}
	capClusters = menuCap{platform: true}
	// 命名空间页是导航入口（命名空间选择器就靠它）。view 角色本身能读 namespaces，
	// 而上游 admin 反而不含 namespaces 读权限——只看资源会让项目 admin 丢掉这个入口，
	// 所以再叠一条"该集群有任意角色"。
	capNS        = menuCap{res: [][2]string{r("namespaces")}, anyNSRead: true}
	capNodes     = menuCap{res: [][2]string{r("nodes")}}
	capCRD       = menuCap{res: [][2]string{resCRD}}
	capWorkload  = menuCap{res: [][2]string{r("deployments"), r("statefulsets"), r("daemonsets"), r("jobs"), r("cronjobs")}}
	capPods      = menuCap{res: [][2]string{r("pods")}}
	capServices  = menuCap{res: [][2]string{r("services")}}
	capEndpoints = menuCap{res: [][2]string{r("endpointslices")}}
	capIngress   = menuCap{res: [][2]string{r("ingresses"), r("ingressclasses")}}
	capGateway   = menuCap{res: [][2]string{r("gateways"), r("gatewayclasses"), r("httproutes")}}
	capNetPol    = menuCap{res: [][2]string{r("networkpolicies")}}
	capConfig    = menuCap{res: [][2]string{r("configmaps")}}
	capSecrets   = menuCap{res: [][2]string{r("secrets")}}
	capSA        = menuCap{res: [][2]string{r("serviceaccounts")}}
	capPVC       = menuCap{res: [][2]string{r("persistentvolumeclaims")}}
	capPV        = menuCap{res: [][2]string{r("persistentvolumes"), r("storageclasses")}}
	// 监控/告警/Grafana/日志检索取的都是 Prometheus 数据（经 apiserver service proxy），
	// 与 PromRule CRD 读取无关；集群总览也调 /monitor/overview，所以给"任一 ns 有角色"的人
	capMonitor = menuCap{res: [][2]string{r("services")}, anyNSRead: true, platform: true}
	capEvents  = menuCap{res: [][2]string{r("events")}, anyNSRead: true}
	// CI 的项目可见性本来就按 ns 收口（见 handler 的 ns 过滤），菜单本身给"能读任一 ns"的人
	capCI    = menuCap{res: [][2]string{{"tekton.dev", "pipelineruns"}, {"tekton.dev", "pipelines"}}, anyNSRead: true}
	capArgo  = menuCap{res: [][2]string{{"argoproj.io", "applications"}, {"argoproj.io", "appprojects"}}}
	capHelm  = menuCap{res: [][2]string{r("secrets")}, anyNSRead: true} // helm v3 的 release 就是 Secret，但 view 读不到 Secret，所以按 ns 角色判
	capNacos = menuCap{always: true}
	capKB    = menuCap{res: [][2]string{{"apps.kubeblocks.io", "clusters"}}, anyNSRead: true}
	// 顶栏全局搜索：跨资源查询，按"任一 ns 有角色"给（它不返回自己没权限的集群数据以外的东西）
	capSearch = menuCap{anyNSRead: true, platform: true}
	// 配额页：resourcequotas/limitranges 都在 view 里，不需要额外兜底
	capQuota = menuCap{res: [][2]string{r("resourcequotas"), r("limitranges")}}
	capPlat  = menuCap{platform: true}
	capAdmin = menuCap{adminOnly: true}
)

// menuCaps 菜单项 → 能力。侧边栏与路由守卫都查这张表。
var menuCaps = map[string]menuCap{
	MenuOverview:   capOverview,
	MenuClusters:   capClusters,
	MenuNamespaces: capNS,
	MenuNodes:      capNodes,
	MenuCRD:        capCRD,
	MenuMatrix:     capWorkload,
	MenuPods:       capPods,
	MenuMonitor:    capMonitor,
	MenuAlerts:     capMonitor,
	MenuGrafana:    capMonitor,
	MenuCP:         capMonitor,
	MenuEvents:     capEvents,
	MenuLogSearch:  capMonitor,
	MenuCI:         capCI,
	MenuArgoCD:     capArgo,
	MenuArgoRepos:  capArgo,
	MenuHelm:       capHelm,
	MenuHelmRepos:  capNacos, // Chart 仓库源是平台配置，登录可浏览
	MenuNacosSvc:   capNacos,
	MenuNacosCfg:   capNacos,
	MenuKBRepos:    capKB,
	MenuQuotas:     capQuota,
	MenuUsers:      capAdmin,
	MenuAuthz:      capPlat,
	MenuRoles:      capPlat,
	MenuAudit:      capPlat,
	MenuNotify:     capAdmin,
	MenuNacosAdmin: capAdmin,
	MenuRegistry:   capAdmin,
	MenuUsage:      capPlat,
	MenuBackups:    capAdmin,
}

// workloadItemCaps /resources/<plural> 与 /workloads/<plural> 这类"一个资源一页"的菜单：
// 不逐个列进 menuCaps，按资源名现推（KindMap 是唯一事实来源，加资源不用改这张表）。
var workloadItemCaps = map[string]menuCap{}

func init() {
	for _, plural := range []string{"deployments", "statefulsets", "daemonsets", "cronjobs", "jobs", "horizontalpodautoscalers"} {
		workloadItemCaps["/workloads/"+plural] = menuCap{res: [][2]string{r(plural)}}
	}
	for _, plural := range []string{
		"services", "endpointslices", "ingresses", "routes", "gatewayclasses", "gateways",
		"httproutes", "grpcroutes", "tlsroutes", "tcproutes", "udproutes", "referencegrants",
		"networkpolicies", "configmaps", "secrets", "serviceaccounts",
		"persistentvolumeclaims", "persistentvolumes", "storageclasses",
		"roles", "rolebindings", "clusterroles", "clusterrolebindings",
		"resourcequotas", "limitranges", "horizontalpodautoscalers",
	} {
		c := workloadItemCaps["/resources/"+plural]
		c.res = append(c.res, r(plural))
		workloadItemCaps["/resources/"+plural] = c
	}
	// 「路由」页列的是带路由注解的 ConfigMap（ListGeneric("routes") 走 configmapGVR），
	// KindMap 里没有 routes 这个复数名：按 configmaps 判能力，否则谁都匹配不上、页面永久隐藏
	workloadItemCaps["/resources/routes"] = capConfig
}

// menuGroups 一级分组 → 子项键。组的可见性 = 任一子项可见（分组本身没有能力含义）。
var menuGroups = map[string][]string{
	"cluster-group":   {MenuClusters, MenuNamespaces, MenuNodes, MenuCRD},
	"workload-group":  {MenuMatrix, "/workloads/deployments", "/workloads/statefulsets", "/workloads/daemonsets", "/workloads/cronjobs", "/workloads/jobs", MenuPods, "/resources/horizontalpodautoscalers"},
	"net-route-group": {"/resources/services", "/resources/endpointslices", "/resources/routes", "/resources/ingresses"},
	"net-gateway-group": {"/resources/gatewayclasses", "/resources/gateways", "/resources/httproutes", "/resources/tcproutes",
		"/resources/tlsroutes", "/resources/udproutes", "/resources/grpcroutes", "/resources/referencegrants"},
	"net-policy-group": {"/resources/networkpolicies"},
	"network-group":    {"/resources/services", "/resources/endpointslices", "/resources/routes", "/resources/ingresses", "/resources/gatewayclasses", "/resources/gateways", "/resources/httproutes", "/resources/networkpolicies"},
	"config-group":     {"/resources/configmaps", "/resources/secrets", "/resources/serviceaccounts"},
	"storage-group":    {"/resources/persistentvolumeclaims", "/resources/persistentvolumes", "/resources/storageclasses"},
	"observe-group":    {MenuMonitor, MenuCP, MenuAlerts, MenuGrafana, MenuEvents, MenuLogSearch},
	"cicd-group":       {MenuCI, MenuArgoCD, MenuArgoRepos, MenuHelm, MenuHelmRepos},
	"micro-group":      {MenuNacosSvc, MenuNacosCfg},
	"kb-group":         append([]string{MenuKBRepos}, engineMenus...),
	"security-group":   {"/resources/roles", "/resources/rolebindings", "/resources/clusterroles", "/resources/clusterrolebindings"},
	"quota-group":      {MenuQuotas, "/resources/limitranges"},
	"platform-group":   {MenuUsers, MenuAuthz, MenuRoles, MenuAudit, MenuNotify, MenuNacosAdmin, MenuRegistry, MenuUsage, MenuBackups, MenuClusters},
}

// engineMenus 引擎子菜单：六个引擎同一能力（能读 KubeBlocks 的 Cluster），侧边栏按
// visibleEngines 过滤时也拿这份键去问后端。
var engineMenus = []string{"/kb/mysql", "/kb/redis", "/kb/kafka", "/kb/mongodb", "/kb/elasticsearch", "/kb/postgresql"}

// Meets 该能力当前用户是否满足。
func (a *Access) Meets(c menuCap) bool {
	if a == nil {
		return false
	}
	if c.always {
		return true
	}
	if a.PlatformAdmin {
		return true
	}
	if c.adminOnly {
		return false
	}
	if c.platform && a.PlatformViewer {
		return true
	}
	if c.anyNSRead && (a.AllNSRead || len(a.NSRead) > 0) {
		return true
	}
	for _, g := range c.res {
		if a.CanReadAPI(g[0], g[1]) {
			return true
		}
	}
	return false
}

// capOf 菜单键 → 能力：显式表 → 资源页自动表 → 引擎子菜单 → 默认（登录可见）。
// 默认只兜住"详情页/子路由"这类不单独设菜单的键（如 /ci/pipelines/7/design）。
// 注意：新增侧边栏项必须登记进 menuCaps 或 workloadItemCaps，否则 VisibleMenus 不会
// 把它发给前端（前端按后端给的键集判定显隐，漏登记 = 谁都看不见）——有测试守着这条。
func capOf(key string) menuCap {
	if c, ok := menuCaps[key]; ok {
		return c
	}
	if c, ok := workloadItemCaps[key]; ok {
		return c
	}
	if strings.HasPrefix(key, "/kb/") {
		return capKB
	}
	return menuCap{always: true}
}

// MenuVisible 单个菜单键是否可见（路由守卫用得上：直接敲 URL 也要挡）
func (a *Access) MenuVisible(key string) bool {
	return a.Meets(capOf(key))
}

// VisibleMenus 算出该用户可见的菜单键（项 + 组）。
// 组键以 "group:" 前缀返回，前端按它决定 el-sub-menu 是否渲染。
func VisibleMenus(a *Access) []string {
	if a == nil {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for key := range menuCaps {
		if a.Meets(capOf(key)) {
			out, seen[key] = append(out, key), true
		}
	}
	for key := range workloadItemCaps {
		if a.Meets(capOf(key)) && !seen[key] {
			out, seen[key] = append(out, key), true
		}
	}
	// 引擎子菜单：能读 KubeBlocks Cluster 就给全部六个
	if a.Meets(capKB) {
		for _, k := range engineMenus {
			if !seen[k] {
				out, seen[k] = append(out, k), true
			}
		}
	}
	for g, items := range menuGroups {
		for _, it := range items {
			if seen[it] {
				out = append(out, "group:"+g)
				break
			}
		}
	}
	return out
}

// ---------- 接口收口 ----------

// apiCap 一条接口前缀需要的能力
type apiCap struct {
	prefix string
	cap    menuCap
}

// apiCaps 路径前缀 → 能力。最长前缀优先匹配（见 APICapFor），所以可以同时写
// /api/helm 和更具体的条目。只登记"控制台自有、K8s RBAC 表达不出来"的模块；
// 资源类接口（/resources、/generic、/workloads、/pods）由 resourceCapFor 按
// URL 里的资源名单独判，/nodes 这类既是模块也是资源的走前缀这条更直白的路。
//
// 不登记的（保持既有判定，别在这里重复一套）：/api/yaml*（读写都有各自的
// ns/写权限判定）、/api/resource-defs 与 /api/gateway-availability（只有元数据）、
// /api/registry|nacos|logsources|alertmanager 的管理端点（已在 admin/strict 组里按平台角色判）。
var apiCaps = []apiCap{
	{"/api/kb", capKB},
	{"/api/ci", capCI},
	{"/api/argocd", capArgo},
	{"/api/helm", capHelm},
	{"/api/monitor", capMonitor},
	{"/api/alertevents", capMonitor},
	{"/api/alertmanager", capAdmin},
	{"/api/logs/", capMonitor}, // 日志检索；不含 /api/logsources（那是 admin 组的连接配置）
	{"/api/overview", capOverview},
	{"/api/events", capEvents},
	{"/api/search", capSearch},
	{"/api/nodes", capNodes},
	{"/api/namespaces", capNS},
	{"/api/quotas", capQuota},
	{"/api/rbac", capPlat},
	{"/api/authz", capPlat},
	{"/api/audit", capPlat},
	{"/api/usage", capPlat},
	{"/api/groups", capPlat},
	{"/api/clusters", capClusters},
	{"/api/users", capAdmin},
	{"/api/backups", capAdmin},
	{"/api/notify", capAdmin},
	{"/api/registry/config", capAdmin},
	{"/api/registry/projects", capAdmin},
	{"/api/registry/tags", capAdmin},
	{"/api/registry/scan", capAdmin},
	{"/api/registry/vulns", capAdmin},
	{"/api/nacos/config", capAdmin},
	{"/api/nacos/status", capAdmin},
	{"/api/nacos/sync", capAdmin},
	{"/api/nacos/reset-password", capAdmin},
	{"/api/nacos/namespaces", capAdmin},
	{"/api/nacos/users", capAdmin},
}

// apiNever 永不收口的路径：登录、权限快照、集群切换与个人设置所需的最小集合。
// 把这些挡掉会让用户连"自己有什么权限"都读不到，直接白屏。
var apiNever = []string{
	"/api/auth/",               // 登录 / me / 改密码
	"/api/rbac/my-permissions", // 权限快照：菜单显隐就是它给出的
	"/api/authz/my-permissions",
	"/api/my-clusters", // 顶栏集群切换器（含 connectivity）
	"/api/tokens",      // 个人 API Token
	"/api/health",
}

// APICapFor 该请求路径需要的能力（ok=false 表示不纳入收口）。导出给接口守卫用。
func APICapFor(path string) (menuCap, bool) {
	for _, never := range apiNever {
		if strings.HasPrefix(path, never) {
			return menuCap{}, false
		}
	}
	if c, ok := resourceCapFor(path); ok {
		return c, true
	}
	best := -1
	var found menuCap
	for _, a := range apiCaps {
		if strings.HasPrefix(path, a.prefix) && len(a.prefix) > best {
			best, found = len(a.prefix), a.cap
		}
	}
	if best >= 0 {
		return found, true
	}
	return menuCap{}, false
}

// resourceCapFor 资源类接口的能力：从 URL 里取出资源复数名，按 KindMap 判可读。
// 路径没有版本段（/api/<模块>/...），所以模块名在 segs[1]。覆盖：
// /api/resources/<kind>[/...]、/api/generic/<group|core>/<version>/<resource>[/...]、
// /api/workloads/<kind>[/...]、/api/pods[/...]。取不到资源名就不收口。
//
// 这里刻意不加 anyNSRead 兜底：资源浏览器页就是"这个资源你能读才给你这页"，
// 用 ns 角色兜底等于把所有命名空间级资源页又放回"登录可浏览"。
func resourceCapFor(path string) (menuCap, bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// /api/<模块>/... → segs[0]=api segs[1]=模块
	if len(segs) < 3 {
		return menuCap{}, false
	}
	var plural string
	generic := false
	switch segs[1] {
	case "resources":
		plural = segs[2]
	case "workloads":
		if segs[2] == "matrix" { // /api/workloads/matrix 是矩阵页，不是某种资源
			return capWorkload, true
		}
		plural = segs[2]
	case "generic":
		// /api/generic/<group>/<version>/<resource>[/name[/yaml]]，核心组前端传字面量 core
		if len(segs) >= 5 {
			plural = segs[4]
		}
		generic = true
	case "pods":
		plural = "pods"
	default:
		return menuCap{}, false
	}
	if plural == "" || plural == "yaml" {
		return menuCap{}, false
	}
	res := [][2]string{r(plural)}
	if generic {
		// GVR 形式是「自定义资源」页在用：能读 CRD 就等于能浏览这页（侧边栏那一项
		// 的能力就是 capCRD），否则会出现"入口给得堂堂正正，点进去 403"
		res = append(res, resCRD)
	}
	return menuCap{res: res}, true
}
