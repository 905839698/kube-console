package service

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// 权限判定对齐 KubeSphere 式三层角色（平台 / 集群 / 项目）：
//
//	平台层  platform-admin=控制台全部放行；platform-viewer=平台管理只读
//	集群层  cluster-admin 级角色 → 集群级写 + 全 ns 写；cluster-viewer 只读
//	项目层  view=只读 / edit=该 ns 可写 / admin=该 ns 可写（自定义角色按授权项判定）
//
// 判定以控制台 DB 的绑定记录为主（/rbac 授权页写入），并集保留 K8s RBAC 反推
// （兼容旧 kc-grant-* 授权与运维手工创建的绑定）。平台自身的操作都用平台
// kubeconfig 执行（不做 impersonation），所以集群写入的 RBAC 只管 kubectl；
// 控制台要约束自己，必须在放行前按同样的绑定判定一次——本文件就是那次判定。
type Access struct {
	PlatformAdmin  bool            `json:"platformAdmin"`  // 平台管理员（User.Role=admin / platform-admin 绑定 / 兜底账号）：全部放行
	PlatformViewer bool            `json:"platformViewer"` // 平台观察者：平台管理只读
	AllNSWrite     bool            `json:"allNsWrite"`     // 全集群可写（项目层 namespaces=["*"] 或集群层写角色）
	ClusterWrite   bool            `json:"clusterWrite"`   // 集群级可写（cluster-admin 级：nodes / * 的写）
	NS             map[string]bool `json:"namespaces"`     // 命名空间级可写（项目层绑定在具体 ns 上的 edit/admin）
	Roles          []RoleBrief     `json:"roles,omitempty"`
}

// RoleBrief 用户生效角色的简要快照（前端菜单/用户菜单展示层级角色）
type RoleBrief struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Level       string `json:"level"`
	Cluster     string `json:"cluster,omitempty"`
	Namespaces  string `json:"namespaces,omitempty"`
}

// CanWriteNS 判断用户能否写某命名空间。
// ns 为空按 default（与 handler 的 queryNamespace 口径一致）；"*" 或 "a,b" 需要
// 每一段都可写（AllNSWrite 或逐段已授权）。
func (a *Access) CanWriteNS(ns string) bool {
	if a == nil {
		return false
	}
	if a.PlatformAdmin || a.AllNSWrite {
		return true
	}
	if ns == "" {
		ns = "default"
	}
	for _, part := range strings.Split(ns, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "*" || !a.NS[part] {
			return false
		}
	}
	return true
}

// CanWriteCluster 判断用户能否做集群级写操作（节点运维、命名空间增删、告警静默等）。
func (a *Access) CanWriteCluster() bool {
	return a != nil && (a.PlatformAdmin || a.ClusterWrite)
}

// rolePerm 由一个 RoleRef 解析出的权限摘要。
type rolePerm struct {
	Write        bool // 含任一写动词（create/update/patch/delete/*）
	ClusterWrite bool // 含集群级资源写（nodes 或 *）
}

// rolePermOf 从角色规则提炼写权限。只看动词与资源，不看角色名——自定义 ClusterRole、
// 名字叫 admin 的只读 Role 都能被正确归类。
func rolePermOf(rules []rbacv1.PolicyRule) rolePerm {
	var rp rolePerm
	for _, r := range rules {
		write := false
		for _, v := range r.Verbs {
			switch v {
			case "*", "create", "update", "patch", "delete", "deletecollection":
				write = true
			}
			if write {
				break
			}
		}
		if !write {
			continue
		}
		rp.Write = true
		for _, res := range r.Resources {
			if res == "*" || res == "nodes" {
				rp.ClusterWrite = true
			}
		}
	}
	return rp
}

// buildAccess 由 RBAC 绑定推导访问级别（纯函数，便于单测）。
// resolve 把 roleRef 解析成权限摘要（ClusterRoleBinding 传 ns=""）。
func buildAccess(crbs []rbacv1.ClusterRoleBinding, rbs []rbacv1.RoleBinding,
	username string, groups []string, platformAdmin bool,
	resolve func(ref rbacv1.RoleRef, ns string) rolePerm) *Access {

	acc := &Access{PlatformAdmin: platformAdmin, NS: map[string]bool{}}
	if platformAdmin {
		acc.AllNSWrite = true
		acc.ClusterWrite = true
		return acc
	}
	match := func(subjects []rbacv1.Subject) bool {
		for _, s := range subjects {
			if s.Kind == "User" && s.Name == username {
				return true
			}
			if s.Kind == "Group" {
				for _, g := range groups {
					if s.Name == g {
						return true
					}
				}
			}
		}
		return false
	}
	for i := range crbs {
		b := &crbs[i]
		if !match(b.Subjects) {
			continue
		}
		if rp := resolve(b.RoleRef, ""); rp.Write {
			acc.AllNSWrite = true
			if rp.ClusterWrite {
				acc.ClusterWrite = true
			}
		}
	}
	for i := range rbs {
		b := &rbs[i]
		if !match(b.Subjects) {
			continue
		}
		if resolve(b.RoleRef, b.Namespace).Write {
			acc.NS[b.Namespace] = true
		}
	}
	return acc
}

// PermissionService 按「用户 → 授权页绑定」判定控制台写权限，带短 TTL 缓存。
// 授权变更（Grant/Revoke）后调用 Invalidate 立即生效，其余靠 TTL 兜底。
type PermissionService struct {
	db            *gorm.DB
	clusters      *ClusterManager
	fallbackAdmin string
	ttl           time.Duration

	mu    sync.Mutex
	cache map[string]permCacheEntry
}

type permCacheEntry struct {
	acc *Access
	at  time.Time
}

// NewPermissionService 创建权限服务；fallbackAdmin 为配置里的兜底管理员用户名。
func NewPermissionService(db *gorm.DB, clusters *ClusterManager, fallbackAdmin string) *PermissionService {
	return &PermissionService{
		db: db, clusters: clusters, fallbackAdmin: fallbackAdmin,
		ttl:   30 * time.Second,
		cache: map[string]permCacheEntry{},
	}
}

// Access 返回用户在某集群的写权限快照。集群不可读/查询失败时按「无写权限」降级
// （fail-closed）并记日志——宁可挡住写操作，也不要放行。
func (s *PermissionService) Access(ctx context.Context, cluster, username string) *Access {
	if cluster == "" || username == "" {
		return &Access{NS: map[string]bool{}}
	}
	key := cluster + "\x00" + username
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Since(e.at) < s.ttl {
		s.mu.Unlock()
		return e.acc
	}
	s.mu.Unlock()

	acc := s.load(ctx, cluster, username)

	s.mu.Lock()
	s.cache[key] = permCacheEntry{acc: acc, at: time.Now()}
	s.mu.Unlock()
	return acc
}

// Invalidate 丢弃某用户在某集群的缓存（授权/回收后调用）。
func (s *PermissionService) Invalidate(cluster, username string) {
	s.mu.Lock()
	delete(s.cache, cluster+"\x00"+username)
	s.mu.Unlock()
}

// PlatformAdmin 判断用户名是否平台管理员：User.Role=admin / 兜底账号 /
// platform-admin 角色绑定（含组继承）任一成立。
func (s *PermissionService) PlatformAdmin(username string) bool {
	if username == "" {
		return false
	}
	if username == s.fallbackAdmin {
		return true
	}
	if s.db == nil {
		return false
	}
	var u model.User
	if err := s.db.Where("username = ?", username).First(&u).Error; err == nil && u.Role == model.RoleAdmin {
		return true
	}
	conds := []string{"(grantee_type = ? AND grantee_name = ?)"}
	args := []any{model.GranteeUser, username}
	for _, g := range s.groupsOf(username) {
		conds = append(conds, "(grantee_type = ? AND grantee_name = ?)")
		args = append(args, model.GranteeGroup, g)
	}
	var n int64
	s.db.Model(&model.RbacBinding{}).
		Where("level = ? AND role_name = ? AND ("+strings.Join(conds, " OR ")+")",
			append([]any{model.LevelPlatform, "platform-admin"}, args...)...).Count(&n)
	return n > 0
}

// userBindings 该用户（含组继承）在某集群生效的控制台绑定
// （platform 层绑定 Cluster 为空，对所有集群生效）。
func (s *PermissionService) userBindings(cluster, username string) []model.RbacBinding {
	if s.db == nil {
		return nil
	}
	conds := []string{"(grantee_type = ? AND grantee_name = ?)"}
	args := []any{model.GranteeUser, username}
	for _, g := range s.groupsOf(username) {
		conds = append(conds, "(grantee_type = ? AND grantee_name = ?)")
		args = append(args, model.GranteeGroup, g)
	}
	var out []model.RbacBinding
	if err := s.db.Where("cluster = ? OR level = ?", cluster, model.LevelPlatform).
		Where("("+strings.Join(conds, " OR ")+")", args...).
		Find(&out).Error; err != nil {
		return nil
	}
	return out
}

// InvalidateCluster 丢弃某集群的全部权限缓存（组授权/角色变更等影响面不确定时使用）。
func (s *PermissionService) InvalidateCluster(cluster string) {
	prefix := cluster + "\x00"
	s.mu.Lock()
	for k := range s.cache {
		if strings.HasPrefix(k, prefix) {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
}

// InvalidateAll 丢弃全部权限缓存（平台层变更用）。
func (s *PermissionService) InvalidateAll() {
	s.mu.Lock()
	s.cache = map[string]permCacheEntry{}
	s.mu.Unlock()
}

// fromDB 由控制台绑定记录（KubeSphere 式角色）推导权限——判定的主数据源。
func (s *PermissionService) fromDB(cluster, username string) *Access {
	acc := &Access{NS: map[string]bool{}}
	if s.db == nil || username == "" {
		return acc
	}
	if s.PlatformAdmin(username) {
		acc.PlatformAdmin = true
		acc.AllNSWrite = true
		acc.ClusterWrite = true
		return acc
	}
	var roles []model.Role
	s.db.Find(&roles)
	roleByName := make(map[string]model.Role, len(roles))
	for _, r := range roles {
		roleByName[r.Name] = r
	}
	for _, bd := range s.userBindings(cluster, username) {
		role, ok := roleByName[bd.RoleName]
		switch bd.Level {
		case model.LevelPlatform:
			switch bd.RoleName {
			case "platform-admin":
				acc.PlatformAdmin = true
			case "platform-viewer":
				acc.PlatformViewer = true
			default:
				// 自定义平台角色：按只读处理（平台层权限项暂不细分）
				if ok {
					acc.PlatformViewer = true
				}
			}
			if acc.PlatformAdmin {
				acc.AllNSWrite = true
				acc.ClusterWrite = true
			}
		case model.LevelCluster:
			if !ok {
				continue
			}
			if b, isBuiltin := builtinByName(role.Name); isBuiltin {
				acc.AllNSWrite = acc.AllNSWrite || b.consoleAllNSWrite
				acc.ClusterWrite = acc.ClusterWrite || b.consoleClusterWrite
				continue
			}
			rp := rolePermOf(policyRules(role))
			// 集群层自定义角色：有写动词即视为集群级
			if rp.Write {
				acc.AllNSWrite = true
				acc.ClusterWrite = true
			}
		case model.LevelProject:
			if !ok || !projectRoleWritable(role) {
				continue
			}
			if bd.Namespaces == "*" {
				acc.AllNSWrite = true
				continue
			}
			for _, ns := range strings.Split(bd.Namespaces, ",") {
				if ns = strings.TrimSpace(ns); ns != "" {
					acc.NS[ns] = true
				}
			}
		}
	}
	return acc
}

// projectRoleWritable 项目层角色的写语义：内置 edit/admin 可写；自定义角色看授权项
// 是否含写动词（忽略集群级资源——那是集群层角色的事）。
func projectRoleWritable(role model.Role) bool {
	if b, ok := builtinByName(role.Name); ok {
		return b.consoleAllNSWrite
	}
	rules, err := ParseRules(role.Rules)
	if err != nil {
		return false
	}
	for _, r := range rules {
		for _, res := range r.Resources {
			if kube.IsClusterScoped(strings.SplitN(res, "/", 2)[0]) {
				continue
			}
			for _, v := range r.Verbs {
				switch v {
				case "*", "create", "update", "patch", "delete", "deletecollection":
					return true
				}
			}
		}
	}
	return false
}

// policyRules 角色授权项 → K8s PolicyRule（复用 rolePermOf 判定）
func policyRules(role model.Role) []rbacv1.PolicyRule {
	rules, err := ParseRules(role.Rules)
	if err != nil {
		return nil
	}
	out := make([]rbacv1.PolicyRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, rbacv1.PolicyRule{Verbs: r.Verbs, APIGroups: []string{r.APIGroup}, Resources: r.Resources})
	}
	return out
}

// load 推导权限：以控制台绑定记录为主，并集 K8s RBAC 反推
// （旧「授权」页 kc-grant-* 与运维手工创建的绑定继续按原语义生效）。
func (s *PermissionService) load(ctx context.Context, cluster, username string) *Access {
	acc := s.fromDB(cluster, username)
	if acc.PlatformAdmin {
		return acc
	}
	client, err := s.clusters.ClientChecked(cluster)
	if err != nil {
		// K8s 反推部分按空处理（fail-closed）；DB 判定不依赖集群可读
		log.Printf("perm: 集群 %s 不可用，K8s 侧绑定不再参与判定: %v", cluster, err)
		return acc
	}
	kacc := s.fromK8sRBAC(ctx, client, cluster, username)
	acc.PlatformAdmin = acc.PlatformAdmin || kacc.PlatformAdmin
	acc.PlatformViewer = acc.PlatformViewer || kacc.PlatformViewer
	acc.AllNSWrite = acc.AllNSWrite || kacc.AllNSWrite
	acc.ClusterWrite = acc.ClusterWrite || kacc.ClusterWrite
	for ns, v := range kacc.NS {
		if v {
			acc.NS[ns] = true
		}
	}
	if acc.PlatformAdmin {
		acc.AllNSWrite = true
		acc.ClusterWrite = true
	}
	return acc
}

// fromK8sRBAC 从集群 RBAC 绑定反推权限（读失败按无权限降级）
func (s *PermissionService) fromK8sRBAC(ctx context.Context, client *kube.Client, cluster, username string) *Access {
	empty := &Access{NS: map[string]bool{}}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	groups := s.groupsOf(username)
	roleCache := map[string][]rbacv1.PolicyRule{}
	// roleRules 读取角色规则（RoleRef 指向 ClusterRole 或 ns 内 Role），按 role 缓存一次
	roleRules := func(ref rbacv1.RoleRef, ns string) []rbacv1.PolicyRule {
		key := ref.Kind + "|" + ns + "|" + ref.Name
		if rs, ok := roleCache[key]; ok {
			return rs
		}
		var rs []rbacv1.PolicyRule
		if ref.Kind == "ClusterRole" {
			if cr, err := client.Clientset.RbacV1().ClusterRoles().Get(ctx, ref.Name, metav1.GetOptions{}); err == nil {
				rs = cr.Rules
			}
		} else if ns != "" {
			if r, err := client.Clientset.RbacV1().Roles(ns).Get(ctx, ref.Name, metav1.GetOptions{}); err == nil {
				rs = r.Rules
			}
		}
		roleCache[key] = rs
		return rs
	}
	resolve := func(ref rbacv1.RoleRef, ns string) rolePerm {
		return rolePermOf(roleRules(ref, ns))
	}

	crbs, err := client.Clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("perm: 读 ClusterRoleBinding 失败（集群 %s），按无写权限处理: %v", cluster, err)
		return empty
	}
	rbs, err := client.Clientset.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("perm: 读 RoleBinding 失败（集群 %s），按无写权限处理: %v", cluster, err)
		return empty
	}
	return buildAccess(crbs.Items, rbs.Items, username, groups, false, resolve)
}

// groupsOf 取用户所属组名（授权页的绑定 subject 可以是 Group）。
func (s *PermissionService) groupsOf(username string) []string {
	if s.db == nil {
		return nil
	}
	var u model.User
	if err := s.db.Where("username = ?", username).First(&u).Error; err != nil || u.GroupID == 0 {
		return nil
	}
	var g model.UserGroup
	if err := s.db.First(&g, u.GroupID).Error; err != nil || g.Name == "" {
		return nil
	}
	return []string{g.Name}
}
