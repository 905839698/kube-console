// ArgoCD 应用视图（dynamic client 读 Application/AppProject CRD）。
// 注：ci-platform 透传代理（/ci/api/*path）已随内置 CI 上线废弃并从路由移除，
// 相关 handler 一并删除（其 http.Client{Timeout:30s} 会掐断 SSE 流，属带缺陷死代码）。
package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"

	"kube-console/server/internal/kube"
	"sigs.k8s.io/yaml"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// CIHandler ArgoCD 应用视图
type CIHandler struct {
	clusters *service.ClusterManager
	perm     *service.PermissionService // GitOps 动作按目标命名空间写权限判定
}

// NewCIHandler 创建 CI/CD 视图 handler（ArgoCD 视图只读集群对象 + 动作鉴权需要 perm）
func NewCIHandler(_ *gorm.DB, clusters *service.ClusterManager, perm *service.PermissionService) *CIHandler {
	return &CIHandler{clusters: clusters, perm: perm}
}

// ------------------- ArgoCD 应用视图 -------------------

var argocdGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// appprojects 与 applications 同组同版本：应用 spec.project 必须指向其中存在的项目
var argocdProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// ArgoApp ArgoCD Application 视图
type ArgoApp struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Sync      string `json:"sync"`
	Health    string `json:"health"`
	RepoURL   string `json:"repoURL"`
	Path      string `json:"path"`
	Target    string `json:"target"`
	DestNS    string `json:"destNamespace"`
	Project   string `json:"project"`
	AutoSync  bool   `json:"autoSync"`
	Paused    bool   `json:"paused,omitempty"` // automated.suspend=true（原生 PAUSED 标记）
	Age       string `json:"age"`
	// SpecError 是 status.conditions 里的 InvalidSpecError（如引用了不存在的 project）：
	// 此时 sync/health 都是 Unknown，只有这条 condition 说明了原因
	SpecError string `json:"specError,omitempty"`
}

// ArgoApps GET /argocd/apps —— 列出当前集群全部 ArgoCD 应用
func (h *CIHandler) ArgoApps(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	list, err := client.Dynamic.Resource(argocdGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			response.OK(c, gin.H{"installed": false, "items": []ArgoApp{}})
			return
		}
		response.K8sError(c, err)
		return
	}
	items := []ArgoApp{}
	for i := range list.Items {
		u := &list.Items[i]
		app := argoAppBase(u.Object, u.GetName(), u.GetNamespace())
		app.Age = duration.HumanDuration(time.Since(u.GetCreationTimestamp().Time))
		items = append(items, app)
	}
	response.OK(c, gin.H{"installed": true, "items": items})
}

// argoInvalidSpecError 取 status.conditions 里 type=InvalidSpecError 的 message
// （应用 spec 非法时 ArgoCD 只在这里说明原因，sync/health 都留在 Unknown）
func argoInvalidSpecError(obj map[string]any) string {
	conds, ok, _ := unstructuredNested(obj, "status", "conditions")
	if !ok {
		return ""
	}
	list, ok := conds.([]any)
	if !ok {
		return ""
	}
	for _, c := range list {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "InvalidSpecError" {
			msg, _ := m["message"].(string)
			return msg
		}
	}
	return ""
}

// ArgoProjectDestination AppProject 允许的部署目标（server 或 name 二选一，支持 * 通配）
type ArgoProjectDestination struct {
	Server    string `json:"server,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// ArgoProject AppProject 视图：应用表单据此选项目并校验仓库/目标是否被允许
type ArgoProject struct {
	Name         string                   `json:"name"`
	Namespace    string                   `json:"namespace"`
	Description  string                   `json:"description,omitempty"`
	SourceRepos  []string                 `json:"sourceRepos"`
	Destinations []ArgoProjectDestination `json:"destinations"`
}

// ArgoProjects GET /argocd/projects —— 列出当前集群全部 AppProject。
// 应用的 spec.project 必须指向其中存在的项目，否则 ArgoCD 报
// "app is not allowed in project X, or the project does not exist"（sync/health 恒为 Unknown）。
func (h *CIHandler) ArgoProjects(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	list, err := client.Dynamic.Resource(argocdProjectGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			response.OK(c, gin.H{"installed": false, "items": []ArgoProject{}})
			return
		}
		response.K8sError(c, err)
		return
	}
	items := []ArgoProject{}
	for i := range list.Items {
		u := &list.Items[i]
		items = append(items, argoProjectView(u.Object, u.GetName(), u.GetNamespace()))
	}
	response.OK(c, gin.H{"installed": true, "items": items})
}

// argoProjectView 从 AppProject 对象提取表单需要的字段（纯解析，便于单测）
func argoProjectView(obj map[string]any, name, namespace string) ArgoProject {
	p := ArgoProject{Name: name, Namespace: namespace, SourceRepos: []string{}, Destinations: []ArgoProjectDestination{}}
	p.Description, _, _ = unstructuredString(obj, "spec", "description")
	if repos, ok, _ := unstructuredNested(obj, "spec", "sourceRepos"); ok {
		if arr, ok := repos.([]any); ok {
			for _, r := range arr {
				if s, ok := r.(string); ok {
					p.SourceRepos = append(p.SourceRepos, s)
				}
			}
		}
	}
	if dests, ok, _ := unstructuredNested(obj, "spec", "destinations"); ok {
		if arr, ok := dests.([]any); ok {
			for _, d := range arr {
				m, ok := d.(map[string]any)
				if !ok {
					continue
				}
				server, _ := m["server"].(string)
				cname, _ := m["name"].(string)
				ns, _ := m["namespace"].(string)
				p.Destinations = append(p.Destinations, ArgoProjectDestination{Server: server, Name: cname, Namespace: ns})
			}
		}
	}
	return p
}

// ArgoAppGet GET /argocd/apps/:namespace/:name —— 完整 Application 对象 YAML（可视化编辑器加载用）
func (h *CIHandler) ArgoAppGet(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	u, err := client.Dynamic.Resource(argocdGVR).Namespace(c.Param("namespace")).
		Get(ctx, c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.K8sError(c, err)
		return
	}
	b, err := yaml.Marshal(u.Object)
	if err != nil {
		response.Fail(c, 500, 500, "YAML 序列化失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"yaml": string(b)})
}

// kubeClient 当前集群 kube.Client（X-Cluster 头）
func (h *CIHandler) kubeClient(c *gin.Context) *kube.Client {
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

func unstructuredString(obj map[string]any, fields ...string) (string, bool, error) {
	v, ok, err := unstructuredNested(obj, fields...)
	if err != nil || !ok {
		return "", ok, err
	}
	s, ok := v.(string)
	return s, ok, nil
}

func unstructuredNested(obj map[string]any, fields ...string) (any, bool, error) {
	var cur any = obj
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur, ok = m[f]
		if !ok {
			return nil, false, nil
		}
	}
	return cur, true, nil
}

func strOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
