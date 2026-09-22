// 命名空间级授权：把「用户 → 角色 → 命名空间范围」的授权向导落成 RBAC 绑定。
// 所有由本页创建的绑定都带 console.kube.io/managed 注解，列表/回收只处理这些绑定，
// 不碰用户手工创建的 RBAC 对象。
package handler

import (
	"context"
	"crypto/sha1"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// 预置角色 → 内置 ClusterRole（k8s 自带，无需创建）
var presetRoles = map[string]string{
	"view":  "view",  // 只读
	"edit":  "edit",  // 命名空间内可改（不含角色/配额修改）
	"admin": "admin", // 命名空间管理员（含角色/配额）
}

const (
	managedAnn     = "console.kube.io/managed"
	granteeAnn     = "console.kube.io/grantee"
	grantRoleAnn   = "console.kube.io/role"
	bindingNameFmt = "kc-grant-%s-%s" // kc-grant-<user>-<role>
	nameSanitize   = "[^a-z0-9.-]"
)

// AuthzHandler 授权管理
type AuthzHandler struct {
	clusters *service.ClusterManager
	// perm 控制台自身的写权限判定（与本页写入的 K8s RBAC 同源）：
	// 授权变更后要失效其缓存，否则最长 TTL 内新人拿不到/旧人有残留权限
	perm *service.PermissionService
}

func NewAuthzHandler(clusters *service.ClusterManager, perm *service.PermissionService) *AuthzHandler {
	return &AuthzHandler{clusters: clusters, perm: perm}
}

// MyPermissions GET /authz/my-permissions —— 当前用户在当前集群（X-Cluster）的写权限快照，
// 前端据此隐藏/禁用写按钮（后端仍逐个接口强制判定，UI 只是提示）
func (h *AuthzHandler) MyPermissions(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	if cluster == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return
	}
	if h.perm == nil {
		response.OK(c, &service.Access{NS: map[string]bool{}})
		return
	}
	response.OK(c, h.perm.Access(c.Request.Context(), cluster, middleware.CurrentUser(c)))
}

func (h *AuthzHandler) client(c *gin.Context) *kube.Client {
	name := middleware.ClusterName(c)
	if name == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return nil
	}
	client, err := h.clusters.ClientChecked(name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return nil
	}
	return client
}

type grantReq struct {
	Username   string   `json:"username" binding:"required"`
	Role       string   `json:"role" binding:"required"`       // view | edit | admin | 自定义 ClusterRole 名
	Namespaces []string `json:"namespaces" binding:"required"` // 含 "*" 表示全集群
}

func bindingName(user, role string) string {
	re := regexp.MustCompile(nameSanitize)
	safe := func(s string) string {
		s = strings.ToLower(s)
		return re.ReplaceAllString(s, "-")
	}
	// 净化会吞掉非 [a-z0-9.-] 字符：zhang.san 与 zhang-san 得到同名绑定，
	// 第二人授权时 Create 报 already exists 但 Subjects 仍是第一人（静默丢授权）。
	// 追加 user+role 的短哈希消歧（kc-grant- 前缀保留，isManagedGrant 仍识别）
	sum := sha1.Sum([]byte(user + "\x00" + role))
	return fmt.Sprintf("kc-grant-%s-%s-%x", safe(user), safe(role), sum[:4])
}

// ageOfMeta 资源创建至今的可读时长（与前端 parseDuration 兼容的简写格式）
func ageOfMeta(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t.Time)
	switch {
	case d.Hours() >= 24*365:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	case d.Hours() >= 24:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d.Hours() >= 1:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// Grant POST /authz/grant
func (h *AuthzHandler) Grant(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误：需要 username / role / namespaces")
		return
	}
	clusterRole, ok := presetRoles[req.Role]
	if !ok {
		clusterRole = req.Role // 自定义 ClusterRole
	}
	ann := map[string]string{managedAnn: "true", granteeAnn: req.Username, grantRoleAnn: req.Role}
	subject := rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: req.Username}
	created := 0

	// 全集群：ClusterRoleBinding
	if len(req.Namespaces) == 1 && req.Namespaces[0] == "*" {
		name := bindingName(req.Username, req.Role)
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: ann},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
			Subjects:   []rbacv1.Subject{subject},
		}
		if err := h.upsertCRB(c.Request.Context(), client, crb); err != nil {
			response.Fail(c, 400, 400, "创建 ClusterRoleBinding 失败: "+err.Error())
			return
		}
		created = 1
	} else {
		for _, ns := range req.Namespaces {
			if ns == "" || ns == "*" {
				continue
			}
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName(req.Username, req.Role), Namespace: ns, Labels: ann},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
				Subjects:   []rbacv1.Subject{subject},
			}
			if _, err := client.Clientset.RbacV1().RoleBindings(ns).Create(c.Request.Context(), rb, metav1.CreateOptions{}); err != nil {
				if !strings.Contains(err.Error(), "already exists") {
					response.Fail(c, 400, 400, fmt.Sprintf("创建 RoleBinding(%s) 失败: %v", ns, err))
					return
				}
			}
			created++
		}
	}
	response.OK(c, gin.H{"created": created, "binding": bindingName(req.Username, req.Role)})
	if h.perm != nil {
		// 立即生效：下次请求就按新绑定判定
		h.perm.Invalidate(middleware.ClusterName(c), req.Username)
	}
}

func (h *AuthzHandler) upsertCRB(ctx context.Context, client *kube.Client, crb *rbacv1.ClusterRoleBinding) error {
	_, err := client.Clientset.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		existing, gerr := client.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, crb.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		existing.Subjects = crb.Subjects
		existing.RoleRef = crb.RoleRef
		_, err = client.Clientset.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	}
	return err
}

// Grants GET /authz/bindings —— 列出本页创建的授权（managed 注解或 kc-grant- 前缀）
func (h *AuthzHandler) Grants(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	items := []gin.H{}

	crbs, err := client.Clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 400, 400, "读取 ClusterRoleBinding 失败: "+err.Error())
		return
	}
	for _, b := range crbs.Items {
		if !isManagedGrant(&b.ObjectMeta) {
			continue
		}
		items = append(items, gin.H{
			"kind": "ClusterRoleBinding", "name": b.Name, "namespace": "",
			"role": b.RoleRef.Name, "grantee": grantee(b.Labels), "subjects": subjectNames(b.Subjects),
			"age": ageOfMeta(b.CreationTimestamp),
		})
	}
	rbs, err := client.Clientset.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 400, 400, "读取 RoleBinding 失败: "+err.Error())
		return
	}
	for _, b := range rbs.Items {
		if !isManagedGrant(&b.ObjectMeta) {
			continue
		}
		items = append(items, gin.H{
			"kind": "RoleBinding", "name": b.Name, "namespace": b.Namespace,
			"role": b.RoleRef.Name, "grantee": grantee(b.Labels), "subjects": subjectNames(b.Subjects),
			"age": ageOfMeta(b.CreationTimestamp),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i]["grantee"].(string) != items[j]["grantee"].(string) {
			return items[i]["grantee"].(string) < items[j]["grantee"].(string)
		}
		return items[i]["namespace"].(string) < items[j]["namespace"].(string)
	})
	response.OK(c, items)
}

func isManagedGrant(meta *metav1.ObjectMeta) bool {
	if meta.Labels[managedAnn] == "true" {
		return true
	}
	return strings.HasPrefix(meta.Name, "kc-grant-")
}

func grantee(labels map[string]string) string {
	if v := labels[granteeAnn]; v != "" {
		return v
	}
	return ""
}

func subjectNames(subjects []rbacv1.Subject) []string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, s.Kind+":"+s.Name)
	}
	return out
}

// Revoke DELETE /authz/bindings/:kind/:name?namespace=（kind: RoleBinding | ClusterRoleBinding）
func (h *AuthzHandler) Revoke(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	kind, name := c.Param("kind"), c.Param("name")
	ns := c.Query("namespace")
	// 回收后要失效这些主体在控制台的权限缓存
	var affected []string
	collect := func(subjects []rbacv1.Subject) {
		for _, s := range subjects {
			if s.Kind == "User" && s.Name != "" {
				affected = append(affected, s.Name)
			}
		}
	}
	// 只允许回收本工具创建的绑定（managed 注解 / kc-grant- 前缀）：
	// 按名直接 Delete 会删掉运维手工创建的任意同名 RBAC 对象（包注释的声明）
	switch kind {
	case "ClusterRoleBinding":
		b, err := client.Clientset.RbacV1().ClusterRoleBindings().Get(c.Request.Context(), name, metav1.GetOptions{})
		if err != nil {
			response.Fail(c, 404, 404, "绑定不存在: "+err.Error())
			return
		}
		if !isManagedGrant(&b.ObjectMeta) {
			response.Fail(c, 400, 400, "仅可回收本页创建的授权（kc-grant- 前缀或 managed 注解），该绑定请通过资源管理删除")
			return
		}
		collect(b.Subjects)
		if err := client.Clientset.RbacV1().ClusterRoleBindings().Delete(c.Request.Context(), name, metav1.DeleteOptions{}); err != nil {
			response.Fail(c, 400, 400, "删除失败: "+err.Error())
			return
		}
	case "RoleBinding":
		if ns == "" {
			response.Fail(c, 400, 400, "缺少 namespace 参数")
			return
		}
		b, err := client.Clientset.RbacV1().RoleBindings(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
		if err != nil {
			response.Fail(c, 404, 404, "绑定不存在: "+err.Error())
			return
		}
		if !isManagedGrant(&b.ObjectMeta) {
			response.Fail(c, 400, 400, "仅可回收本页创建的授权（kc-grant- 前缀或 managed 注解），该绑定请通过资源管理删除")
			return
		}
		collect(b.Subjects)
		if err := client.Clientset.RbacV1().RoleBindings(ns).Delete(c.Request.Context(), name, metav1.DeleteOptions{}); err != nil {
			response.Fail(c, 400, 400, "删除失败: "+err.Error())
			return
		}
	default:
		response.Fail(c, 400, 400, "kind 只能是 RoleBinding 或 ClusterRoleBinding")
		return
	}
	if h.perm != nil {
		for _, u := range affected {
			h.perm.Invalidate(middleware.ClusterName(c), u)
		}
	}
	response.OK(c, nil)
}
