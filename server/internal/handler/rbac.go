// 权限管理 API（KubeSphere 式三层角色）：角色注册表、自定义角色 CRUD、
// 「用户/组 → 角色 → 层级范围」绑定管理、授权项目录与当前用户权限快照。
// 旧 /authz/grant|bindings 保留：历史 kc-grant-* 绑定仍可列出与回收。
package handler

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

type RbacHandler struct {
	svc      *service.RbacService
	clusters *service.ClusterManager
	perm     *service.PermissionService
}

func NewRbacHandler(svc *service.RbacService, clusters *service.ClusterManager, perm *service.PermissionService) *RbacHandler {
	return &RbacHandler{svc: svc, clusters: clusters, perm: perm}
}

// roleView 角色记录（rules 解析为结构化授权项，便于前端渲染）
func roleView(r model.Role) gin.H {
	rules, _ := service.ParseRules(r.Rules)
	if rules == nil {
		rules = []model.RuleItem{}
	}
	return gin.H{
		"id": r.ID, "name": r.Name, "level": r.Level,
		"displayName": r.DisplayName, "description": r.Description,
		"builtin": r.Builtin, "rules": rules,
	}
}

// ListRoles GET /rbac/roles —— 全部角色（内置 + 自定义），登录可见（授权向导要用）
func (h *RbacHandler) ListRoles(c *gin.Context) {
	roles, err := h.svc.ListRoles()
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	out := make([]gin.H, 0, len(roles))
	level := c.Query("level")
	for _, r := range roles {
		if level != "" && r.Level != level {
			continue
		}
		out = append(out, roleView(r))
	}
	response.OK(c, out)
}

// CreateRole POST /rbac/roles —— 新建自定义角色（平台管理员）
func (h *RbacHandler) CreateRole(c *gin.Context) {
	var req service.RoleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误："+err.Error())
		return
	}
	role, err := h.svc.CreateRole(req)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, roleView(*role))
}

// UpdateRole PUT /rbac/roles/:id —— 修改自定义角色（重新物化受影响的 ClusterRole）
func (h *RbacHandler) UpdateRole(c *gin.Context) {
	var req service.RoleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误："+err.Error())
		return
	}
	role, err := h.svc.UpdateRole(idOf(c), req)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, roleView(*role))
}

// DeleteRole DELETE /rbac/roles/:id
func (h *RbacHandler) DeleteRole(c *gin.Context) {
	if err := h.svc.DeleteRole(idOf(c)); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

func idOf(c *gin.Context) uint {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id)
}

// PermissionItems GET /rbac/permission-items —— 自定义角色可勾选的授权项目录
func (h *RbacHandler) PermissionItems(c *gin.Context) {
	response.OK(c, gin.H{
		"items":       service.PermItems,
		"verbs":       service.PermVerbs,
		"verbGroups":  service.PermVerbGroups,
		"builtinRole": service.BuiltinRoles,
	})
}

// bindingView 绑定记录 + 角色展示信息
func (h *RbacHandler) bindingView(bd model.RbacBinding, roleMap map[string]model.Role) gin.H {
	ns := []string{}
	if bd.Namespaces != "" {
		ns = strings.FieldsFunc(bd.Namespaces, func(r rune) bool { return r == ',' || r == ' ' })
	}
	role := roleMap[bd.RoleName]
	return gin.H{
		"id": bd.ID, "level": bd.Level, "cluster": bd.Cluster,
		"roleName": bd.RoleName, "roleDisplay": displayName(role, bd.RoleName),
		"granteeType": bd.GranteeType, "granteeName": bd.GranteeName,
		"namespaces": ns, "builtin": role.Builtin,
		"createdAt": bd.CreatedAt,
	}
}

func displayName(r model.Role, fallback string) string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	return fallback
}

// ListBindings GET /rbac/bindings?cluster= —— 控制台授权记录 +（带集群时）旧版遗留绑定
func (h *RbacHandler) ListBindings(c *gin.Context) {
	cluster := c.Query("cluster")
	bindings, err := h.svc.ListBindings(cluster)
	if err != nil {
		response.Fail(c, 500, 500, err.Error())
		return
	}
	roles, _ := h.svc.ListRoles()
	roleMap := map[string]model.Role{}
	for _, r := range roles {
		roleMap[r.Name] = r
	}
	out := make([]gin.H, 0, len(bindings))
	for _, bd := range bindings {
		v := h.bindingView(bd, roleMap)
		v["legacy"] = false
		out = append(out, v)
	}
	// 旧「授权」页生成的 kc-grant-* / managed 绑定（无 rbac-binding 标签）：兼容展示
	if cluster != "" {
		for _, legacy := range h.legacyK8sGrants(c, cluster) {
			out = append(out, legacy)
		}
	}
	response.OK(c, out)
}

func (h *RbacHandler) legacyK8sGrants(c *gin.Context, cluster string) []gin.H {
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	out := []gin.H{}
	isLegacy := func(labels map[string]string) bool {
		if labels["console.kube.io/rbac-binding"] != "" {
			return false // 新版物化对象，已有 DB 记录展示
		}
		return labels["console.kube.io/managed"] == "true"
	}
	if crbs, err := client.Clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{}); err == nil {
		for _, b := range crbs.Items {
			if !isLegacy(b.Labels) {
				continue
			}
			out = append(out, legacyView(cluster, b.RoleRef.Name, b.Labels, b.Name, ""))
		}
	}
	if rbs, err := client.Clientset.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, b := range rbs.Items {
			if !isLegacy(b.Labels) {
				continue
			}
			out = append(out, legacyView(cluster, b.RoleRef.Name, b.Labels, b.Name, b.Namespace))
		}
	}
	return out
}

// legacyView 旧「授权」页生成的绑定：按项目层全部/指定命名空间展示（只读展示，回收走旧接口）
func legacyView(cluster, role string, labels map[string]string, name, ns string) gin.H {
	nsList := []string{"*"}
	if ns != "" {
		nsList = []string{ns}
	}
	return gin.H{
		"id": 0, "level": model.LevelProject, "cluster": cluster,
		"roleName": role, "roleDisplay": role,
		"granteeType": "user", "granteeName": labels["console.kube.io/grantee"],
		"namespaces": nsList, "builtin": true,
		"legacy": true, "bindingName": name,
	}
}

// CreateBinding POST /rbac/bindings —— 授权（写 DB + 物化 K8s RBAC）
func (h *RbacHandler) CreateBinding(c *gin.Context) {
	var req service.BindingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误："+err.Error())
		return
	}
	if req.Cluster == "" {
		req.Cluster = middleware.ClusterName(c) // 前端未显式传则跟随当前集群
	}
	bd, err := h.svc.CreateBinding(req)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	roles, _ := h.svc.ListRoles()
	roleMap := map[string]model.Role{}
	for _, r := range roles {
		roleMap[r.Name] = r
	}
	response.OK(c, h.bindingView(*bd, roleMap))
}

// DeleteBinding DELETE /rbac/bindings/:id —— 回收（删 DB + 清理物化对象）
func (h *RbacHandler) DeleteBinding(c *gin.Context) {
	if err := h.svc.DeleteBinding(idOf(c)); err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, nil)
}

// MyPermissions GET /rbac/my-permissions —— 当前用户权限快照（菜单/按钮动态渲染依据）
func (h *RbacHandler) MyPermissions(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	if cluster == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return
	}
	if h.perm == nil {
		response.OK(c, &service.Access{NS: map[string]bool{}})
		return
	}
	acc := h.perm.Access(c.Request.Context(), cluster, middleware.CurrentUser(c))
	// 附带平台层角色（与集群无关）
	acc.Roles = h.rolesOf(middleware.CurrentUser(c), cluster)
	response.OK(c, acc)
}

// rolesOf 用户全部生效角色（平台层 + 指定集群）
func (h *RbacHandler) rolesOf(username, cluster string) []service.RoleBrief {
	out := []service.RoleBrief{}
	if h.svc == nil {
		return out
	}
	bindings, err := h.svc.ListBindings(cluster)
	if err != nil {
		return out
	}
	roles, _ := h.svc.ListRoles()
	roleMap := map[string]model.Role{}
	for _, r := range roles {
		roleMap[r.Name] = r
	}
	groups := map[string]bool{}
	if u, err := h.svc.UserOf(username); err == nil && u.GroupID > 0 {
		if g, err := h.svc.GroupOf(u.GroupID); err == nil {
			groups[g.Name] = true
		}
	}
	for _, bd := range bindings {
		hit := bd.GranteeType == model.GranteeUser && bd.GranteeName == username ||
			bd.GranteeType == model.GranteeGroup && groups[bd.GranteeName]
		if !hit {
			continue
		}
		r := roleMap[bd.RoleName]
		out = append(out, service.RoleBrief{Name: bd.RoleName, DisplayName: displayName(r, bd.RoleName),
			Level: bd.Level, Cluster: bd.Cluster, Namespaces: bd.Namespaces})
	}
	return out
}

// UserRoles GET /rbac/user-roles?username=&cluster= —— 权限查询：用户的控制台角色
func (h *RbacHandler) UserRoles(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		response.Fail(c, 400, 400, "缺少 username 参数")
		return
	}
	response.OK(c, h.rolesOf(username, middleware.ClusterName(c)))
}
