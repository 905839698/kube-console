package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/service"
)

// 控制台写操作鉴权：判定依据是「授权」页写入集群的 RBAC 绑定（view/edit/admin），
// 由 service.PermissionService 解析。平台自身的操作都用平台 kubeconfig 执行，K8s
// 不会替我们拦住越权，所以这里必须显式判定——否则任何登录用户都能增删改。
//
//	NamespaceWriteRequired —— 命名空间内资源写（Pod/工作负载/通用资源/YAML/Helm/文件/Nacos 配置）
//	ClusterWriteRequired   —— 集群级写（节点运维、命名空间增删、告警静默）
//
// 平台管理员（User.Role=admin 或配置里的兜底管理员）一律放行；其余按绑定判定。
// 判定失败（集群不可读等）按无权限处理，不放行。

// NamespaceWriteRequired 命名空间粒度写守卫。
func NamespaceWriteRequired(perm *service.PermissionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		ns := resolveNamespace(c)
		acc := perm.Access(c.Request.Context(), ClusterName(c), CurrentUser(c))
		if !acc.CanWriteNS(ns) {
			target := ns
			if target == "" {
				target = "default"
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    http.StatusForbidden,
				"message": fmt.Sprintf("没有命名空间 %s 的写权限：请在「授权」页授予该命名空间的 edit 或 admin 角色", target),
			})
			return
		}
		c.Next()
	}
}

// ClusterWriteRequired 集群级写守卫（节点、命名空间增删、告警静默等）。
func ClusterWriteRequired(perm *service.PermissionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		acc := perm.Access(c.Request.Context(), ClusterName(c), CurrentUser(c))
		if !acc.CanWriteCluster() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    http.StatusForbidden,
				"message": "该操作需要集群级权限：请联系管理员授予集群范围内的 admin/cluster-admin，或使用平台管理员账号",
			})
			return
		}
		c.Next()
	}
}

// ResourceWriteRequired 通用资源写守卫（/resources/:kind 与 /generic/:group/:version/:resource）。
// 这两条路由既能指命名空间级资源、也能指集群级资源（PV/StorageClass/CRD…），
// 若一律按 ns 判定，拥有某个 ns 写权限的用户就能删掉集群级对象（service 层对已知
// 集群级资源会把 namespace 置空，等于全集群生效）。故先判作用域：
// 集群级 / 无法确认（CRD 且 discovery 不可用）→ 需集群级权限；命名空间级 → 按 ns 判。
func ResourceWriteRequired(perm *service.PermissionService, clusters *service.ClusterManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		resource := c.Param("resource")
		if resource == "" {
			resource = c.Param("kind") // /resources/:kind 用的就是复数资源名（KindMap 的键）
		}
		cluster := ClusterName(c)
		acc := perm.Access(c.Request.Context(), cluster, CurrentUser(c))
		if acc.PlatformAdmin {
			c.Next()
			return
		}
		if !resourceNamespaced(c, clusters, cluster, resource) {
			if !acc.CanWriteCluster() {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code":    http.StatusForbidden,
					"message": fmt.Sprintf("资源 %s 是集群级资源（或无法确认作用域），需要集群级权限或平台管理员", resource),
				})
				return
			}
			c.Next()
			return
		}
		if ns := resolveNamespace(c); !acc.CanWriteNS(ns) {
			target := ns
			if target == "" {
				target = "default"
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    http.StatusForbidden,
				"message": fmt.Sprintf("没有命名空间 %s 的写权限：请在「授权」页授予该命名空间的 edit 或 admin 角色", target),
			})
			return
		}
		c.Next()
	}
}

// resourceNamespaced 判断资源是否命名空间级。已知集群级资源直接否定；官方资源（KindMap）
// 默认命名空间级；其余（CRD）查集群 discovery，查不到按集群级处理（宁可收紧）。
func resourceNamespaced(c *gin.Context, clusters *service.ClusterManager, cluster, resource string) bool {
	res := strings.ToLower(resource)
	if kube.IsClusterScoped(res) {
		return false
	}
	if _, ok := kube.KindMap[res]; ok {
		return true
	}
	if clusters != nil {
		if defs, err := clusters.ResourceDefs(cluster, false); err == nil {
			for _, g := range defs {
				for _, r := range g.Resources {
					if strings.EqualFold(r.Resource, res) {
						return r.Namespaced
					}
				}
			}
		}
	}
	return false
}

// resolveNamespace 取本次请求的目标命名空间：先 query（多数接口），再 JSON body
// （工作负载 rollback、Helm 安装、Nacos 配置发布把 ns 放在 body 里）。
// 都取不到时返回空串——由 CanWriteNS 按 default 处理，与 handler 的 queryNamespace 一致。
func resolveNamespace(c *gin.Context) string {
	if ns := strings.TrimSpace(c.Query("namespace")); ns != "" {
		return ns
	}
	if !strings.Contains(c.ContentType(), "json") {
		// multipart（文件上传）等：ns 在 query 里，已在上面取过；不读 body 避免缓冲大文件
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		return ""
	}
	// 读完必须还原：handler 还要 ShouldBindJSON 同一个 body
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var probe struct {
		Namespace string `json:"namespace"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	return strings.TrimSpace(probe.Namespace)
}
