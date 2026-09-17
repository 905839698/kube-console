// ci-platform 集成：/ci/api/*path 通用透传代理（自动登录、token 缓存、SSE/下载流式转发）
// 与 ArgoCD 应用视图（dynamic client 读 Application CRD）
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"

	"kube-console/server/internal/kube"
	"sigs.k8s.io/yaml"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// CIHandler ci-platform 集成
type CIHandler struct {
	db       *gorm.DB
	clusters *service.ClusterManager
	httpc    *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

// NewCIHandler 创建
func NewCIHandler(db *gorm.DB, clusters *service.ClusterManager) *CIHandler {
	return &CIHandler{db: db, clusters: clusters, httpc: &http.Client{Timeout: 30 * time.Second}}
}

func (h *CIHandler) config() (*model.CIIntegration, error) {
	var cfg model.CIIntegration
	if err := h.db.First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("未配置 ci-platform 集成（CI 页面右上角「集成设置」）")
	}
	if !cfg.Enabled || cfg.BaseURL == "" {
		return nil, fmt.Errorf("ci-platform 集成已停用")
	}
	return &cfg, nil
}

// tokenOf 自动登录并缓存 token（50 分钟）
func (h *CIHandler) tokenOf(cfg *model.CIIntegration) (string, error) {
	h.mu.Lock()
	if h.token != "" && time.Since(h.tokenAt) < 50*time.Minute {
		t := h.token
		h.mu.Unlock()
		return t, nil
	}
	h.mu.Unlock()
	return h.loginForTest(cfg)
}

// loginForTest 直接登录（绕过缓存；配置保存验证与 token 刷新共用）
func (h *CIHandler) loginForTest(cfg *model.CIIntegration) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": cfg.Username, "password": cfg.Password})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ci-platform 不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var lr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil || lr.Code != 0 || lr.Data.Token == "" {
		return "", fmt.Errorf("登录失败（HTTP %d）: %s", resp.StatusCode, truncateCI(string(raw), 200))
	}
	h.mu.Lock()
	h.token = lr.Data.Token
	h.tokenAt = time.Now()
	h.mu.Unlock()
	return lr.Data.Token, nil
}

// Status GET /ci/status
func (h *CIHandler) Status(c *gin.Context) {
	var cfg model.CIIntegration
	err := h.db.First(&cfg).Error
	if err != nil {
		response.OK(c, gin.H{"configured": false})
		return
	}
	response.OK(c, gin.H{"configured": cfg.BaseURL != "", "enabled": cfg.Enabled, "baseURL": cfg.BaseURL, "webURL": cfg.WebURL, "username": cfg.Username})
}

// SaveConfig POST /ci/config （admin）
func (h *CIHandler) SaveConfig(c *gin.Context) {
	var in struct {
		BaseURL  string `json:"baseURL"`
		WebURL   string `json:"webURL"`
		Username string `json:"username"`
		Password string `json:"password"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.BaseURL == "" {
		response.Fail(c, 400, 400, "参数错误：baseURL 必填")
		return
	}
	var cfg model.CIIntegration
	err := h.db.First(&cfg).Error
	isNew := err != nil
	if isNew {
		cfg = model.CIIntegration{BaseURL: in.BaseURL, WebURL: in.WebURL, Username: in.Username, Enabled: true}
	} else {
		cfg.BaseURL = in.BaseURL
		cfg.WebURL = in.WebURL
		cfg.Username = in.Username
		if in.Enabled != nil {
			cfg.Enabled = *in.Enabled
		}
	}
	if isNew || in.Password != "" {
		cfg.Password = in.Password
	}
	// 保存前验证登录（新配置或改了密码时）
	if isNew || in.Password != "" {
		if _, err := h.loginForTest(&cfg); err != nil {
			response.Fail(c, 400, 400, "登录验证失败: "+err.Error())
			return
		}
	}
	if isNew {
		h.db.Create(&cfg)
	} else {
		h.db.Save(&cfg)
	}
	h.mu.Lock()
	h.token = ""
	h.mu.Unlock()
	response.OK(c, gin.H{"ok": true})
}

// CIProxy ANY /ci/api/*path —— 通用透传：
//   - GET/HEAD：所有登录用户（只读：项目/流水线/运行/日志/制品/凭证/Globals/管理端查看）
//   - 其它方法（POST/PUT/DELETE）：仅平台管理员（创建/编辑/删除/触发/重跑/停止）
//   - SSE 日志与制品下载按流式转发，不缓冲
func (h *CIHandler) CIProxy(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		uid := middleware.CurrentUserID(c)
		if !middleware.IsAdmin(h.db, "admin", uid, middleware.CurrentUser(c)) {
			response.Fail(c, 403, 403, "CI 写操作需要平台管理员权限")
			return
		}
	}
	cfg, err := h.config()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	path := c.Param("path")
	if path == "" || path == "/" {
		response.Fail(c, 400, 400, "缺少代理路径")
		return
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	token, err := h.tokenOf(cfg)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}

	body, _ := c.GetRawData()
	ctx := c.Request.Context()

	doOnce := func(t string) (*http.Response, error) {
		// ci-platform API 统一前缀 /api/v1（path 参数不含）
		u := strings.TrimRight(cfg.BaseURL, "/") + "/api/v1" + path
		if q := c.Request.URL.RawQuery; q != "" {
			u += "?" + q
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, c.Request.Method, u, rdr)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+t)
		if c.Request.Header.Get("Accept") != "" {
			req.Header.Set("Accept", c.Request.Header.Get("Accept"))
		}
		return h.httpc.Do(req)
	}

	resp, err := doOnce(token)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// 401 → 强制重登一次再试
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if t2, err := h.loginForTest(cfg); err == nil {
			if resp2, err2 := doOnce(t2); err2 == nil {
				resp = resp2
				defer func() { _ = resp.Body.Close() }()
			}
		}
	}

	// ---- 流式转发：SSE 日志 / 文件下载 ----
	ct := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(ct, "text/event-stream")
	isDownload := strings.Contains(path, "/download") || strings.HasPrefix(ct, "application/octet-stream")
	if isSSE || isDownload {
		for k, vs := range resp.Header {
			if strings.HasPrefix(k, "Content-") || k == "Transfer-Encoding" {
				for _, v := range vs {
					c.Writer.Header().Add(k, v)
				}
			}
		}
		c.Writer.WriteHeader(resp.StatusCode)
		flusher, _ := c.Writer.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := c.Writer.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rerr != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}

	// ---- 普通 JSON：响应原样透传（保留 ci-platform 的 code/message/data 包） ----
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.Data(mapUpstreamStatus(resp.StatusCode), "application/json", raw)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

func mapUpstreamStatus(code int) int {
	if code >= 500 {
		return 502
	}
	if code < 200 {
		return 400
	}
	return code
}

func truncateCI(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// DesignerToken GET /ci/designer-token —— 签发 ci-platform 会话 token（供 iframe 设计器自动登录）。
// 权限与只读透传一致（所有登录用户）；token 能力镜像所配置的服务账号。
func (h *CIHandler) DesignerToken(c *gin.Context) {
	cfg, err := h.config()
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	token, err := h.tokenOf(cfg)
	if err != nil {
		response.Fail(c, 502, 502, err.Error())
		return
	}
	response.OK(c, gin.H{"token": token, "webURL": cfg.WebURL})
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
		sync, _, _ := unstructuredString(u.Object, "status", "sync", "status")
		health, _, _ := unstructuredString(u.Object, "status", "health", "status")
		repo, _, _ := unstructuredString(u.Object, "spec", "source", "repoURL")
		path, _, _ := unstructuredString(u.Object, "spec", "source", "path")
		target, _, _ := unstructuredString(u.Object, "spec", "source", "targetRevision")
		destNS, _, _ := unstructuredString(u.Object, "spec", "destination", "namespace")
		project, _, _ := unstructuredString(u.Object, "spec", "project")
		auto := false
		if p, ok, _ := unstructuredNested(u.Object, "spec", "syncPolicy", "automated"); ok && p != nil {
			auto = true
		}
		created := u.GetCreationTimestamp()
		items = append(items, ArgoApp{
			Name: u.GetName(), Namespace: u.GetNamespace(),
			Sync: strOr(sync, "Unknown"), Health: strOr(health, "Unknown"),
			RepoURL: repo, Path: path, Target: target, DestNS: destNS, Project: project, AutoSync: auto,
			Age:       duration.HumanDuration(time.Since(created.Time)),
			SpecError: argoInvalidSpecError(u.Object),
		})
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

// ArgoRefresh POST /argocd/apps/:namespace/:name/refresh —— 打 refresh 注解触发比对
func (h *CIHandler) ArgoRefresh(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"normal"}}}`)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if _, err := client.Dynamic.Resource(argocdGVR).Namespace(c.Param("namespace")).
		Patch(ctx, c.Param("name"), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
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
