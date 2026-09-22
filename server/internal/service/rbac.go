// RBAC 服务（KubeSphere 式层级角色）：角色注册（内置 seed + 自定义 CRUD）与
// 「主体-角色-范围」绑定管理。判定以控制台 DB 为准；集群/项目层绑定在写入的同一
// 事务窗口内物化为 K8s RBAC（RoleBinding/ClusterRoleBinding），让 kubectl 侧同样生效。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// 物化到 K8s 的对象标识：与旧「授权」页同一套 managed 标签体系，
// 额外用 rbacBindingAnn 记录控制台绑定 ID，回收时按标签整批删除。
const (
	rbacManagedAnn  = "console.kube.io/managed"
	rbacBindingAnn  = "console.kube.io/rbac-binding"
	rbacGranteeAnn  = "console.kube.io/grantee"
	rbacRoleAnn     = "console.kube.io/role"
	rbacBindingPfx  = "kc-rbac-"
	rbacRolePfx     = "kc-role-"
	customNameRegex = `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` // DNS-label 风格，可直接做对象名
)

// BuiltinRole 内置角色的静态定义（权限语义在代码里，DB 记录只做注册表与展示）
type BuiltinRole struct {
	Name              string `json:"name"`
	Level             string `json:"level"`
	DisplayName       string `json:"displayName"`
	Description       string `json:"description"`
	// k8sClusterRole 物化时引用的 ClusterRole；空 = 不物化（平台层）
	k8sClusterRole string
	// consoleWrite 控制台写语义：cluster 层直接给集群级写；project 层由 k8s 角色决定
	consoleAllNSWrite bool
	consoleClusterWrite bool
}

// BuiltinRoles 系统内置角色（对齐 KubeSphere 的平台/集群/项目三层角色体系）
var BuiltinRoles = []BuiltinRole{
	{Name: "platform-admin", Level: model.LevelPlatform, DisplayName: "平台管理员",
		Description: "管理平台全部功能：用户、授权、角色、集群注册、平台设置、审计"},
	{Name: "platform-viewer", Level: model.LevelPlatform, DisplayName: "平台观察者",
		Description: "平台管理只读：可查看授权、角色、审计日志与用量报表，不能修改"},
	{Name: "cluster-admin", Level: model.LevelCluster, DisplayName: "集群管理员",
		Description: "集群级写：节点运维、命名空间增删、告警静默，且可写集群内全部命名空间",
		k8sClusterRole: "cluster-admin", consoleAllNSWrite: true, consoleClusterWrite: true},
	{Name: "cluster-viewer", Level: model.LevelCluster, DisplayName: "集群观察者",
		Description: "集群级只读：kubectl 查看集群资源，不能在控制台做任何修改",
		k8sClusterRole: "view"},
	{Name: "view", Level: model.LevelProject, DisplayName: "只读 (view)",
		Description: "可查看命名空间内全部资源，不可修改", k8sClusterRole: "view"},
	{Name: "edit", Level: model.LevelProject, DisplayName: "运维 (edit)",
		Description: "可增删改命名空间内工作负载/配置等（不含角色与配额修改）",
		k8sClusterRole: "edit", consoleAllNSWrite: true},
	{Name: "admin", Level: model.LevelProject, DisplayName: "项目管理员 (admin)",
		Description: "命名空间全部权限（含角色与配额管理）",
		k8sClusterRole: "admin", consoleAllNSWrite: true},
}

func builtinByName(name string) (BuiltinRole, bool) {
	for _, r := range BuiltinRoles {
		if r.Name == name {
			return r, true
		}
	}
	return BuiltinRole{}, false
}

// SeedBuiltinRoles 把内置角色写入角色注册表（幂等；已存在时刷新展示文案）
func SeedBuiltinRoles(db *gorm.DB) error {
	for _, r := range BuiltinRoles {
		var existing model.Role
		err := db.Where("name = ?", r.Name).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := db.Create(&model.Role{
				Name: r.Name, Level: r.Level, DisplayName: r.DisplayName,
				Description: r.Description, Builtin: true,
			}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		db.Model(&existing).Updates(map[string]any{
			"level": r.Level, "display_name": r.DisplayName,
			"description": r.Description, "builtin": true,
		})
	}
	return nil
}

// RbacService 角色与绑定管理
type RbacService struct {
	db       *gorm.DB
	clusters *ClusterManager
	perm     *PermissionService
}

func NewRbacService(db *gorm.DB, clusters *ClusterManager, perm *PermissionService) *RbacService {
	return &RbacService{db: db, clusters: clusters, perm: perm}
}

var customNameRe = regexp.MustCompile(customNameRegex)

func validRoleName(s string) error {
	if s == "" || len(s) > 63 || !customNameRe.MatchString(s) {
		return fmt.Errorf("角色名需为小写字母/数字/-/.（%d~63 位）", 1)
	}
	return nil
}

// ParseRules 解析角色授权项 JSON
func ParseRules(s string) ([]model.RuleItem, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var rules []model.RuleItem
	if err := json.Unmarshal([]byte(s), &rules); err != nil {
		return nil, fmt.Errorf("授权项 JSON 解析失败: %w", err)
	}
	for _, r := range rules {
		if len(r.Resources) == 0 || len(r.Verbs) == 0 {
			return nil, errors.New("每个授权项都必须包含资源与动词")
		}
	}
	return rules, nil
}

func (s *RbacService) ListRoles() ([]model.Role, error) {
	var roles []model.Role
	err := s.db.Order("level, builtin desc, name").Find(&roles).Error
	return roles, err
}

type RoleReq struct {
	Name        string          `json:"name" binding:"required"`
	Level       string          `json:"level" binding:"required"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	Rules       json.RawMessage `json:"rules"`
}

func validLevel(l string) bool {
	return l == model.LevelPlatform || l == model.LevelCluster || l == model.LevelProject
}

// CreateRole 创建自定义角色（平台/集群/项目层均可；内置角色不可复制名）
func (s *RbacService) CreateRole(req RoleReq) (*model.Role, error) {
	if !validLevel(req.Level) {
		return nil, errors.New("层级只能是 platform / cluster / project")
	}
	if _, ok := builtinByName(req.Name); ok {
		return nil, fmt.Errorf("%s 是内置角色名，请换一个", req.Name)
	}
	if err := validRoleName(req.Name); err != nil {
		return nil, err
	}
	rules, err := ParseRules(string(req.Rules))
	if err != nil {
		return nil, err
	}
	if req.Level != model.LevelPlatform && len(rules) == 0 {
		return nil, errors.New("集群/项目层自定义角色必须至少配置一个授权项")
	}
	rulesJSON := "[]"
	if len(rules) > 0 {
		b, _ := json.Marshal(rules)
		rulesJSON = string(b)
	}
	role := &model.Role{Name: req.Name, Level: req.Level, DisplayName: req.DisplayName,
		Description: req.Description, Rules: rulesJSON}
	if err := s.db.Create(role).Error; err != nil {
		return nil, fmt.Errorf("创建角色失败（可能重名）: %w", err)
	}
	return role, nil
}

// UpdateRole 修改自定义角色（描述/授权项）；授权项变更后把 ClusterRole 重新物化到
// 所有引用它的集群，并失效相关权限缓存。
func (s *RbacService) UpdateRole(id uint, req RoleReq) (*model.Role, error) {
	var role model.Role
	if err := s.db.First(&role, id).Error; err != nil {
		return nil, errors.New("角色不存在")
	}
	if role.Builtin {
		return nil, errors.New("内置角色不可修改")
	}
	rules, err := ParseRules(string(req.Rules))
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(rules)
	updates := map[string]any{
		"rules": string(b), "display_name": req.DisplayName, "description": req.Description,
	}
	if err := s.db.Model(&role).Updates(updates).Error; err != nil {
		return nil, err
	}
	// 重新物化到所有引用该角色的集群
	var bindings []model.RbacBinding
	s.db.Where("role_name = ?", role.Name).Find(&bindings)
	seen := map[string]bool{}
	for _, bd := range bindings {
		if bd.Cluster == "" || seen[bd.Cluster] {
			continue
		}
		seen[bd.Cluster] = true
		if client, err := s.clusters.ClientChecked(bd.Cluster); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := s.ensureCustomClusterRole(ctx, client, role.Name, rules); err != nil {
				cancel()
				continue
			}
			cancel()
		}
		s.invalidate(bd.Cluster)
	}
	return &role, nil
}

// DeleteRole 删除自定义角色；有绑定时拒绝。
func (s *RbacService) DeleteRole(id uint) error {
	var role model.Role
	if err := s.db.First(&role, id).Error; err != nil {
		return errors.New("角色不存在")
	}
	if role.Builtin {
		return errors.New("内置角色不可删除")
	}
	var n int64
	s.db.Model(&model.RbacBinding{}).Where("role_name = ?", role.Name).Count(&n)
	if n > 0 {
		return fmt.Errorf("还有 %d 条授权在使用该角色，请先回收", n)
	}
	// 清理各集群物化的 ClusterRole（尽力而为）
	var clusters []model.Cluster
	s.db.Find(&clusters)
	for _, cl := range clusters {
		client, err := s.clusters.ClientChecked(cl.Name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = client.Clientset.RbacV1().ClusterRoles().Delete(ctx, rbacRolePfx+role.Name, metav1.DeleteOptions{})
		cancel()
	}
	return s.db.Delete(&role).Error
}

type BindingReq struct {
	Level       string   `json:"level" binding:"required"`
	Cluster     string   `json:"cluster"`
	RoleName    string   `json:"roleName" binding:"required"`
	GranteeType string   `json:"granteeType" binding:"required"`
	GranteeName string   `json:"granteeName" binding:"required"`
	Namespaces  []string `json:"namespaces"` // project 层；["*"]=全部
}

// CreateBinding 授权：写 DB 记录并物化 K8s RBAC；物化失败回滚 DB（判定以 DB 为准，
// 但不能留下「控制台有记录、kubectl 无权限」的不一致）。
func (s *RbacService) CreateBinding(req BindingReq) (*model.RbacBinding, error) {
	if !validLevel(req.Level) {
		return nil, errors.New("层级只能是 platform / cluster / project")
	}
	if req.GranteeType != model.GranteeUser && req.GranteeType != model.GranteeGroup {
		return nil, errors.New("主体类型只能是 user / group")
	}
	var role model.Role
	if err := s.db.Where("name = ?", req.RoleName).First(&role).Error; err != nil {
		return nil, fmt.Errorf("角色 %s 不存在", req.RoleName)
	}
	if role.Level != req.Level {
		return nil, fmt.Errorf("角色 %s 属于 %s 层，不能按 %s 层授权", role.Name, role.Level, req.Level)
	}
	cluster := strings.TrimSpace(req.Cluster)
	namespaces := ""
	if req.Level != model.LevelPlatform {
		if cluster == "" {
			return nil, errors.New("缺少集群")
		}
		if _, err := s.clusters.ClientChecked(cluster); err != nil {
			return nil, err
		}
		if req.Level == model.LevelProject {
			ns := cleanNamespaces(req.Namespaces)
			if len(ns) == 0 {
				return nil, errors.New("请选择至少一个命名空间（或选择全部命名空间）")
			}
			namespaces = strings.Join(ns, ",")
		}
	} else {
		cluster = ""
	}
	// 完全重复的绑定拒绝（同主体同角色同范围）
	var dup int64
	s.db.Model(&model.RbacBinding{}).Where(
		"level = ? AND cluster = ? AND role_name = ? AND grantee_type = ? AND grantee_name = ? AND namespaces = ?",
		req.Level, cluster, req.RoleName, req.GranteeType, req.GranteeName, namespaces).Count(&dup)
	if dup > 0 {
		return nil, errors.New("相同授权已存在")
	}

	bd := &model.RbacBinding{Level: req.Level, Cluster: cluster, RoleName: req.RoleName,
		GranteeType: req.GranteeType, GranteeName: req.GranteeName, Namespaces: namespaces}
	if err := s.db.Create(bd).Error; err != nil {
		return nil, err
	}
	if req.Level != model.LevelPlatform {
		if err := s.materialize(bd, &role); err != nil {
			s.db.Delete(bd)
			return nil, fmt.Errorf("物化 K8s RBAC 失败，授权已回滚: %w", err)
		}
	}
	s.invalidate(cluster)
	return bd, nil
}

// DeleteBinding 回收授权：删除 DB 记录并按绑定标签清理物化的 K8s 对象
func (s *RbacService) DeleteBinding(id uint) error {
	var bd model.RbacBinding
	if err := s.db.First(&bd, id).Error; err != nil {
		return errors.New("授权记录不存在")
	}
	if err := s.unmaterialize(&bd); err != nil {
		return err
	}
	if err := s.db.Delete(&bd).Error; err != nil {
		return err
	}
	s.invalidate(bd.Cluster)
	return nil
}

// ListBindings 控制台授权记录（cluster 为空返回全部）
func (s *RbacService) ListBindings(cluster string) ([]model.RbacBinding, error) {
	var out []model.RbacBinding
	q := s.db.Order("level, id desc")
	if cluster != "" {
		q = q.Where("cluster = ? OR level = ?", cluster, model.LevelPlatform)
	}
	err := q.Find(&out).Error
	return out, err
}

// rbacSubject DB 主体类型（user/group 小写）→ K8s Subject（Kind 必须是大写 User/Group）
func rbacSubject(granteeType, name string) rbacv1.Subject {
	kind := rbacv1.UserKind
	if granteeType == model.GranteeGroup {
		kind = rbacv1.GroupKind
	}
	return rbacv1.Subject{Kind: kind, APIGroup: rbacv1.GroupName, Name: name}
}

func cleanNamespaces(ns []string) []string {
	out := make([]string, 0, len(ns))
	seen := map[string]bool{}
	for _, n := range ns {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func (s *RbacService) invalidate(cluster string) {
	if s.perm == nil {
		return
	}
	if cluster == "" {
		s.perm.InvalidateAll()
		return
	}
	s.perm.InvalidateCluster(cluster)
}

// k8sRoleRef 角色 → 物化用的 ClusterRole 名
func k8sRoleRef(role *model.Role) string {
	if b, ok := builtinByName(role.Name); ok && b.k8sClusterRole != "" {
		return b.k8sClusterRole
	}
	return rbacRolePfx + role.Name
}

// materialize 把绑定落成 K8s RBAC。project：每个 ns 一个 RoleBinding（"*" 用
// ClusterRoleBinding）；cluster：一个 ClusterRoleBinding。
func (s *RbacService) materialize(bd *model.RbacBinding, role *model.Role) error {
	client, err := s.clusters.ClientChecked(bd.Cluster)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	crName := k8sRoleRef(role)
	if !role.Builtin {
		rules, err := ParseRules(role.Rules)
		if err != nil {
			return err
		}
		if err := s.ensureCustomClusterRole(ctx, client, role.Name, rules); err != nil {
			return err
		}
	}

	subject := rbacSubject(bd.GranteeType, bd.GranteeName)
	labels := map[string]string{
		rbacManagedAnn: "true", rbacBindingAnn: fmt.Sprint(bd.ID),
		rbacGranteeAnn: bd.GranteeName, rbacRoleAnn: bd.RoleName,
	}
	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: crName}
	name := rbacBindingPfx + fmt.Sprint(bd.ID)

	if bd.Level == model.LevelCluster || bd.Namespaces == "*" {
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			RoleRef:    roleRef, Subjects: []rbacv1.Subject{subject},
		}
		return s.upsertCRB(ctx, client, crb)
	}
	for _, ns := range strings.Split(bd.Namespaces, ",") {
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			RoleRef:    roleRef, Subjects: []rbacv1.Subject{subject},
		}
		if _, err := client.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("创建 RoleBinding(%s) 失败: %w", ns, err)
			}
		}
	}
	return nil
}

func (s *RbacService) upsertCRB(ctx context.Context, client *kube.Client, crb *rbacv1.ClusterRoleBinding) error {
	_, err := client.Clientset.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		existing, gerr := client.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, crb.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		existing.Subjects = crb.Subjects
		existing.RoleRef = crb.RoleRef
		existing.Labels = crb.Labels
		_, err = client.Clientset.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	}
	return err
}

// ensureCustomClusterRole 创建/更新自定义角色的 ClusterRole（kc-role- 前缀，managed 标签）
func (s *RbacService) ensureCustomClusterRole(ctx context.Context, client *kube.Client, roleName string, rules []model.RuleItem) error {
	policies := make([]rbacv1.PolicyRule, 0, len(rules))
	for _, r := range rules {
		policies = append(policies, rbacv1.PolicyRule{
			Verbs: r.Verbs, APIGroups: []string{r.APIGroup}, Resources: r.Resources,
		})
	}
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: rbacRolePfx + roleName,
			Labels: map[string]string{rbacManagedAnn: "true", rbacRoleAnn: roleName}},
		Rules: policies,
	}
	_, err := client.Clientset.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		existing, gerr := client.Clientset.RbacV1().ClusterRoles().Get(ctx, cr.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		existing.Rules = cr.Rules
		existing.Labels = cr.Labels
		_, err = client.Clientset.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
	}
	return err
}

// unmaterialize 按绑定标签清理物化对象（对象已被手工删除也不报错）
func (s *RbacService) unmaterialize(bd *model.RbacBinding) error {
	if bd.Level == model.LevelPlatform {
		return nil
	}
	client, err := s.clusters.ClientChecked(bd.Cluster)
	if err != nil {
		// 集群不可用：不能阻止回收 DB 记录（物化对象成为孤儿，由巡检兜底）
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sel := metav1.ListOptions{LabelSelector: rbacBindingAnn + "=" + fmt.Sprint(bd.ID)}
	if crbs, err := client.Clientset.RbacV1().ClusterRoleBindings().List(ctx, sel); err == nil {
		for _, b := range crbs.Items {
			_ = client.Clientset.RbacV1().ClusterRoleBindings().Delete(ctx, b.Name, metav1.DeleteOptions{})
		}
	}
	if rbs, err := client.Clientset.RbacV1().RoleBindings("").List(ctx, sel); err == nil {
		for _, b := range rbs.Items {
			_ = client.Clientset.RbacV1().RoleBindings(b.Namespace).Delete(ctx, b.Name, metav1.DeleteOptions{})
		}
	}
	return nil
}

// UserOf 按用户名查用户（角色快照展示组继承用）
func (s *RbacService) UserOf(username string) (*model.User, error) {
	var u model.User
	if err := s.db.Where("username = ?", username).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GroupOf 按 ID 查用户组
func (s *RbacService) GroupOf(id uint) (*model.UserGroup, error) {
	var g model.UserGroup
	if err := s.db.First(&g, id).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// ------------------- 自定义角色的授权项目录（UI 勾选清单） -------------------

// PermItem 一个可勾选的 API 授权项（资源组）
type PermItem struct {
	Group      string   `json:"group"`
	APIGroup   string   `json:"apiGroup"`
	Resources  []string `json:"resources"`
	Namespaced bool     `json:"namespaced"`
}

// PermVerbs 可勾选动词（含通配）
var PermVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "*"}

// PermVerbGroups 动词按读/写分组（UI 用）
var PermVerbGroups = []map[string]any{
	{"label": "读取", "verbs": []string{"get", "list", "watch"}},
	{"label": "写入", "verbs": []string{"create", "update", "patch", "delete", "deletecollection"}},
	{"label": "全部", "verbs": []string{"*"}},
}

// PermItems 常用资源授权项目录（基于 kube.KindMap 的官方资源分组）
var PermItems = []PermItem{
	{Group: "Pod 与日志", APIGroup: "", Resources: []string{"pods", "pods/log", "pods/exec", "pods/status"}, Namespaced: true},
	{Group: "工作负载", APIGroup: "apps", Resources: []string{"deployments", "statefulsets", "daemonsets", "replicasets"}, Namespaced: true},
	{Group: "任务", APIGroup: "batch", Resources: []string{"jobs", "cronjobs"}, Namespaced: true},
	{Group: "自动扩缩", APIGroup: "autoscaling", Resources: []string{"horizontalpodautoscalers"}, Namespaced: true},
	{Group: "服务与路由", APIGroup: "", Resources: []string{"services", "endpoints"}, Namespaced: true},
	{Group: "路由与网关", APIGroup: "networking.k8s.io", Resources: []string{"ingresses", "networkpolicies", "ingressclasses"}, Namespaced: false},
	{Group: "配置", APIGroup: "", Resources: []string{"configmaps"}, Namespaced: true},
	{Group: "保密字典", APIGroup: "", Resources: []string{"secrets"}, Namespaced: true},
	{Group: "服务账号", APIGroup: "", Resources: []string{"serviceaccounts"}, Namespaced: true},
	{Group: "存储", APIGroup: "", Resources: []string{"persistentvolumeclaims"}, Namespaced: true},
	{Group: "集群存储", APIGroup: "", Resources: []string{"persistentvolumes", "storageclasses"}, Namespaced: false},
	{Group: "配额与限制", APIGroup: "", Resources: []string{"resourcequotas", "limitranges"}, Namespaced: true},
	{Group: "事件", APIGroup: "", Resources: []string{"events"}, Namespaced: true},
	{Group: "RBAC", APIGroup: "rbac.authorization.k8s.io", Resources: []string{"roles", "rolebindings", "clusterroles", "clusterrolebindings"}, Namespaced: false},
	{Group: "命名空间", APIGroup: "", Resources: []string{"namespaces"}, Namespaced: false},
	{Group: "节点", APIGroup: "", Resources: []string{"nodes", "nodes/proxy", "nodes/stats"}, Namespaced: false},
	{Group: "Helm/Release", APIGroup: "helm.cattle.io", Resources: []string{"helmpods", "helmoperations"}, Namespaced: true},
	{Group: "自定义资源定义", APIGroup: "apiextensions.k8s.io", Resources: []string{"customresourcedefinitions"}, Namespaced: false},
}
