// ArgoCD 应用「贴近原生」增强：结构化详情（操作进度/资源/conditions/历史）、
// 托管资源层级树（status.resources 集合内按 ownerReference 组树）、
// 原生式动作（SYNC / 自动同步开关 / PAUSE / 软硬刷新 / 级联删除）。
// 写动作按应用目标命名空间的写权限判定（KubeSphere 式层级角色）。
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/middleware"
	"kube-console/server/pkg/response"
)

const (
	argoResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"
	argoTreeCacheTTL       = 10 * time.Second
	argoTreeMaxResources   = 200
)

// ---------- 基础视图 ----------

// argoAppBase 从 Application 对象抽取列表/详情共用字段
func argoAppBase(obj map[string]any, name, namespace string) ArgoApp {
	syncStatus, _, _ := unstructuredString(obj, "status", "sync", "status")
	health, _, _ := unstructuredString(obj, "status", "health", "status")
	repo, _, _ := unstructuredString(obj, "spec", "source", "repoURL")
	path, _, _ := unstructuredString(obj, "spec", "source", "path")
	target, _, _ := unstructuredString(obj, "spec", "source", "targetRevision")
	if repo == "" {
		if srcs, ok, _ := unstructuredNested(obj, "spec", "sources"); ok {
			if arr, ok := srcs.([]any); ok && len(arr) > 0 {
				if first, ok := arr[0].(map[string]any); ok {
					repo, _ = first["repoURL"].(string)
					path, _ = first["path"].(string)
					target, _ = first["targetRevision"].(string)
				}
			}
		}
	}
	destNS, _, _ := unstructuredString(obj, "spec", "destination", "namespace")
	project, _, _ := unstructuredString(obj, "spec", "project")

	// argocd 语义：automated 存在（哪怕 {}）即自动同步开启；suspend=true 为原生 PAUSED
	auto, paused := false, false
	if am, ok, _ := unstructuredNested(obj, "spec", "syncPolicy", "automated"); ok {
		if m, ok := am.(map[string]any); ok {
			auto = true
			paused, _ = m["suspend"].(bool)
		}
	}
	return ArgoApp{
		Name: name, Namespace: namespace,
		Sync: strOr(syncStatus, "Unknown"), Health: strOr(health, "Unknown"),
		RepoURL: repo, Path: path, Target: target, DestNS: destNS, Project: project,
		AutoSync: auto, Paused: paused,
		SpecError: argoInvalidSpecError(obj),
	}
}

// ---------- 详情 ----------

type ArgoAppResourceView struct {
	Group           string `json:"group,omitempty"`
	Version         string `json:"version,omitempty"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	Sync            string `json:"sync,omitempty"`
	Health          string `json:"health,omitempty"`
	RequiresPruning bool   `json:"requiresPruning,omitempty"`
	Hook            bool   `json:"hook,omitempty"`
}

type ArgoOperationState struct {
	Phase      string `json:"phase,omitempty"`
	Message    string `json:"message,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Revision   string `json:"revision,omitempty"`
	RetryCount int    `json:"retryCount,omitempty"`
}

type ArgoCondition struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ArgoHistoryItem struct {
	Revision   string `json:"revision,omitempty"`
	Author     string `json:"author,omitempty"`
	Message    string `json:"message,omitempty"`
	DeployedAt string `json:"deployedAt,omitempty"`
	Initiator  string `json:"initiator,omitempty"`
}

func argoResourceHealth(m map[string]any) (string, bool) {
	h, ok := m["health"].(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := h["status"].(string)
	return s, ok
}

func argoGitRevision(m map[string]any) string {
	git, ok := m["git"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := git["revision"].(string)
	return s
}

// ArgoAppDetail GET /argocd/apps/:namespace/:name/detail —— 详情抽屉数据
func (h *CIHandler) ArgoAppDetail(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	u, err := h.getArgoApp(c.Request.Context(), client, c.Param("namespace"), c.Param("name"))
	if err != nil {
		response.K8sError(c, err)
		return
	}
	obj := u.Object
	base := argoAppBase(obj, u.GetName(), u.GetNamespace())

	conds := []ArgoCondition{}
	if arr, ok, _ := unstructuredNested(obj, "status", "conditions"); ok {
		if list, ok := arr.([]any); ok {
			for _, it := range list {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				t, _ := m["type"].(string)
				msg, _ := m["message"].(string)
				conds = append(conds, ArgoCondition{Type: t, Message: msg})
			}
		}
	}

	resources := []ArgoAppResourceView{}
	if arr, ok, _ := unstructuredNested(obj, "status", "resources"); ok {
		if list, ok := arr.([]any); ok {
			for _, it := range list {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				rv := ArgoAppResourceView{}
				rv.Group, _ = m["group"].(string)
				rv.Version, _ = m["version"].(string)
				rv.Kind, _ = m["kind"].(string)
				rv.Namespace, _ = m["namespace"].(string)
				rv.Name, _ = m["name"].(string)
				rv.Sync, _ = m["status"].(string)
				rv.Health, _ = argoResourceHealth(m)
				rv.RequiresPruning, _ = m["requiresPruning"].(bool)
				if s, ok := m["hookType"].(string); ok && s != "" {
					rv.Hook = true
				}
				resources = append(resources, rv)
			}
		}
	}

	op := ArgoOperationState{}
	op.Phase, _, _ = unstructuredString(obj, "status", "operationState", "phase")
	if op.Phase != "" {
		op.Message, _, _ = unstructuredString(obj, "status", "operationState", "message")
		op.StartedAt, _, _ = unstructuredString(obj, "status", "operationState", "startedAt")
		op.FinishedAt, _, _ = unstructuredString(obj, "status", "operationState", "finishAt")
		op.Revision, _, _ = unstructuredString(obj, "status", "operationState", "syncResult", "revision")
		if rc, ok, _ := unstructuredNested(obj, "status", "operationState", "retryCount"); ok {
			if f, ok := rc.(float64); ok {
				op.RetryCount = int(f)
			}
		}
	}

	history := []ArgoHistoryItem{}
	if arr, ok, _ := unstructuredNested(obj, "status", "history"); ok {
		if list, ok := arr.([]any); ok {
			for i := len(list) - 1; i >= 0 && len(history) < 10; i-- { // 最新在前
				m, ok := list[i].(map[string]any)
				if !ok {
					continue
				}
				item := ArgoHistoryItem{}
				item.Revision = argoGitRevision(m)
				if item.Revision == "" {
					item.Revision, _ = m["version"].(string)
				}
				item.Author, _ = m["author"].(string)
				item.Message, _ = m["message"].(string)
				item.Initiator, _ = m["initiator"].(string)
				item.DeployedAt, _ = m["deployedAt"].(string)
				history = append(history, item)
			}
		}
	}

	destServer, _, _ := unstructuredString(obj, "spec", "destination", "server")
	syncRevision, _, _ := unstructuredString(obj, "status", "sync", "revision")
	prune, selfHeal := false, false
	if am, ok, _ := unstructuredNested(obj, "spec", "syncPolicy", "automated"); ok {
		if m, ok := am.(map[string]any); ok {
			prune, _ = m["prune"].(bool)
			selfHeal, _ = m["selfHeal"].(bool)
		}
	}
	created := u.GetCreationTimestamp()

	response.OK(c, gin.H{
		"base":         base,
		"destServer":   destServer,
		"syncRevision": syncRevision,
		"prune":        prune,
		"selfHeal":     selfHeal,
		"createdAt":    created,
		"conditions":   conds,
		"resources":    resources,
		"operation":    op,
		"history":      history,
	})
}

// ---------- 资源层级树 ----------

type ArgoTreeNode struct {
	Group           string          `json:"group,omitempty"`
	Kind            string          `json:"kind"`
	Namespace       string          `json:"namespace,omitempty"`
	Name            string          `json:"name"`
	Health          string          `json:"health,omitempty"`
	Sync            string          `json:"sync,omitempty"`
	RequiresPruning bool            `json:"requiresPruning,omitempty"`
	Missing         bool            `json:"missing,omitempty"` // 集群中已找不到该对象
	Children        []*ArgoTreeNode `json:"children,omitempty"`
}

type argoTreeCacheEntry struct {
	nodes []*ArgoTreeNode
	at    time.Time
}

var (
	argoTreeMu    sync.Mutex
	argoTreeCache = map[string]argoTreeCacheEntry{}
)

func argoNodeKey(group, kind, ns, name string) string {
	return group + "|" + kind + "|" + ns + "|" + name
}

type argoResInfo struct {
	group, version, kind, namespace, name string
	health, syncStatus                    string
	requiresPruning                       bool
	ownerRefs                             [][3]string // {apiGroup, kind, name}
	found                                 bool
}

// ArgoAppTree GET /argocd/apps/:namespace/:name/tree —— Application 根节点 +
// 托管资源按 ownerReference 组装的层级树（父子关系只在托管集合内解析）
func (h *CIHandler) ArgoAppTree(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	ns, name := c.Param("namespace"), c.Param("name")
	key := cluster + "|" + ns + "|" + name
	argoTreeMu.Lock()
	if e, ok := argoTreeCache[key]; ok && time.Since(e.at) < argoTreeCacheTTL {
		argoTreeMu.Unlock()
		response.OK(c, gin.H{"tree": e.nodes})
		return
	}
	argoTreeMu.Unlock()

	client := h.kubeClient(c)
	if client == nil {
		return
	}
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}

	infos := []*argoResInfo{}
	index := map[string]*argoResInfo{}
	if arr, ok, _ := unstructuredNested(u.Object, "status", "resources"); ok {
		if list, ok := arr.([]any); ok {
			if len(list) > argoTreeMaxResources {
				list = list[:argoTreeMaxResources]
			}
			for _, it := range list {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				ni := &argoResInfo{}
				ni.group, _ = m["group"].(string)
				ni.version, _ = m["version"].(string)
				ni.kind, _ = m["kind"].(string)
				ni.namespace, _ = m["namespace"].(string)
				ni.name, _ = m["name"].(string)
				ni.syncStatus, _ = m["status"].(string)
				ni.health, _ = argoResourceHealth(m)
				ni.requiresPruning, _ = m["requiresPruning"].(bool)
				k := argoNodeKey(ni.group, ni.kind, ni.namespace, ni.name)
				if _, dup := index[k]; dup {
					continue
				}
				infos = append(infos, ni)
				index[k] = ni
			}
		}
	}

	defs, _ := h.clusters.ResourceDefs(cluster, false)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, ni := range infos {
		wg.Add(1)
		sem <- struct{}{}
		go func(ni *argoResInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			obj, ok := argoGetManaged(c.Request.Context(), client, defs, ni)
			if !ok {
				return
			}
			ni.found = true
			for _, or := range obj.GetOwnerReferences() {
				group := ""
				if i := strings.Index(or.APIVersion, "/"); i > 0 {
					group = or.APIVersion[:i]
				}
				ni.ownerRefs = append(ni.ownerRefs, [3]string{group, or.Kind, or.Name})
			}
		}(ni)
	}
	wg.Wait()

	// 子节点集合：ownerRef 指向托管集合内某节点（ownerRef 与子对象同命名空间）
	isChild := map[string]bool{}
	for _, ni := range infos {
		for _, or := range ni.ownerRefs {
			if _, ok := index[argoNodeKey(or[0], or[1], ni.namespace, or[2])]; ok {
				isChild[argoNodeKey(ni.group, ni.kind, ni.namespace, ni.name)] = true
			}
		}
	}

	var build func(ni *argoResInfo, depth int) *ArgoTreeNode
	build = func(ni *argoResInfo, depth int) *ArgoTreeNode {
		node := &ArgoTreeNode{
			Group: ni.group, Kind: ni.kind, Namespace: ni.namespace, Name: ni.name,
			Health: ni.health, Sync: ni.syncStatus, RequiresPruning: ni.requiresPruning,
			Missing: !ni.found,
		}
		if depth > 8 {
			return node
		}
		selfKey := argoNodeKey(ni.group, ni.kind, ni.namespace, ni.name)
		for _, child := range infos {
			for _, or := range child.ownerRefs {
				if or[0] != ni.group || or[1] != ni.kind || or[2] != ni.name {
					continue
				}
				if argoNodeKey(child.group, child.kind, child.namespace, child.name) == selfKey {
					continue
				}
				node.Children = append(node.Children, build(child, depth+1))
				break
			}
		}
		return node
	}

	roots := []*ArgoTreeNode{}
	for _, ni := range infos {
		if !isChild[argoNodeKey(ni.group, ni.kind, ni.namespace, ni.name)] {
			roots = append(roots, build(ni, 1))
		}
	}
	for _, r := range roots {
		sortArgoTree(r)
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Kind != roots[j].Kind {
			return roots[i].Kind < roots[j].Kind
		}
		return roots[i].Name < roots[j].Name
	})

	base := argoAppBase(u.Object, name, ns)
	appRoot := &ArgoTreeNode{
		Group: "argoproj.io", Kind: "Application", Namespace: ns, Name: name,
		Health: base.Health, Sync: base.Sync, Children: roots,
	}
	tree := []*ArgoTreeNode{appRoot}

	argoTreeMu.Lock()
	argoTreeCache[key] = argoTreeCacheEntry{nodes: tree, at: time.Now()}
	argoTreeMu.Unlock()
	response.OK(c, gin.H{"tree": tree})
}

func sortArgoTree(n *ArgoTreeNode) {
	sort.Slice(n.Children, func(i, j int) bool {
		if n.Children[i].Kind != n.Children[j].Kind {
			return n.Children[i].Kind < n.Children[j].Kind
		}
		return n.Children[i].Name < n.Children[j].Name
	})
	for _, ch := range n.Children {
		sortArgoTree(ch)
	}
}

// argoGetManaged 按 discovery 解析 GVR 并 Get 托管对象；GVR 未知/对象已删 → false
func argoGetManaged(ctx context.Context, client *kube.Client, defs []kube.GroupDef, ni *argoResInfo) (*unstructured.Unstructured, bool) {
	var res string
	namespaced := true
	found := false
	for _, g := range defs {
		if g.Group != ni.group {
			continue
		}
		for _, r := range g.Resources {
			if r.Kind == ni.kind {
				res, namespaced, found = r.Resource, r.Namespaced, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return nil, false
	}
	version := ni.version
	if version == "" {
		for _, g := range defs {
			if g.Group == ni.group && g.Preferred != "" {
				version = g.Preferred
				break
			}
		}
	}
	if version == "" {
		return nil, false
	}
	gvr := schema.GroupVersionResource{Group: ni.group, Version: version, Resource: res}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if namespaced {
		obj, err := client.Dynamic.Resource(gvr).Namespace(ni.namespace).Get(ctx2, ni.name, metav1.GetOptions{})
		if err != nil {
			return nil, false
		}
		return obj, true
	}
	obj, err := client.Dynamic.Resource(gvr).Get(ctx2, ni.name, metav1.GetOptions{})
	if err != nil {
		return nil, false
	}
	return obj, true
}

// ---------- 动作 ----------

func (h *CIHandler) getArgoApp(ctx context.Context, client *kube.Client, ns, name string) (*unstructured.Unstructured, error) {
	return client.Dynamic.Resource(argocdGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

// requireArgoWrite GitOps 动作权限：应用目标命名空间的写权限（edit/admin/平台管理员）
func (h *CIHandler) requireArgoWrite(c *gin.Context, destNS, op string) bool {
	if h.perm == nil {
		return true
	}
	acc := h.perm.Access(c.Request.Context(), middleware.ClusterName(c), middleware.CurrentUser(c))
	if acc.CanWriteNS(destNS) {
		return true
	}
	target := destNS
	if target == "" {
		target = "default"
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403,
		"message": fmt.Sprintf("%s 需要应用目标命名空间 %s 的写权限（edit/admin 角色或平台管理员）", op, target)})
	return false
}

// ArgoAppSync POST /argocd/apps/:namespace/:name/sync —— 通过 CR 内嵌 operation 触发一次同步。
// 部分版本 CRD 无 operation 字段，merge patch 会被静默裁剪：回填校验并给出明确指引。
func (h *CIHandler) ArgoAppSync(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	destNS, _, _ := unstructuredString(u.Object, "spec", "destination", "namespace")
	if !h.requireArgoWrite(c, destNS, "同步") {
		return
	}
	// ArgoCD ≥2.10：initiatedBy 是对象 {username, automated}；旧版是字符串。
	// 先按新版打，被类型校验拒绝时回退旧版格式
	objPatch := []byte(`{"operation":{"initiatedBy":{"username":"kube-console","automated":false},"sync":{"syncStrategy":{"hook":{}}}}}`)
	strPatch := []byte(`{"operation":{"initiatedBy":"kube-console","sync":{"syncStrategy":{"hook":{}}}}}`)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if _, err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, objPatch, metav1.PatchOptions{}); err != nil {
		if !strings.Contains(err.Error(), "initiatedBy") {
			response.K8sError(c, err)
			return
		}
		if _, err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
			Patch(ctx, name, types.MergePatchType, strPatch, metav1.PatchOptions{}); err != nil {
			response.K8sError(c, err)
			return
		}
	}
	// operation 被保留 = 成功；被裁剪则检查 operationState 是否刚被控制器消费（消费极快时字段已消失）
	check, cerr := client.Dynamic.Resource(argocdGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if cerr == nil {
		if _, kept := check.Object["operation"]; !kept {
			started, _, _ := unstructuredString(check.Object, "status", "operationState", "startedAt")
			t, terr := time.Parse(time.RFC3339, started)
			if terr != nil || time.Since(t) > 60*time.Second {
				response.Fail(c, 400, 400, "该 ArgoCD 版本的 Application CRD 不支持经 CR 触发同步（operation 字段被裁剪）。请开启自动同步，或使用 ArgoCD UI 执行 SYNC。")
				return
			}
		}
	}
	response.OK(c, gin.H{"ok": true, "triggered": true})
}

// ArgoAppAutoSync PUT /argocd/apps/:namespace/:name/autosync?enabled=true|false
func (h *CIHandler) ArgoAppAutoSync(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	destNS, _, _ := unstructuredString(u.Object, "spec", "destination", "namespace")
	if !h.requireArgoWrite(c, destNS, "切换自动同步") {
		return
	}
	enabled := c.Query("enabled") != "false"
	patch := []byte(`{"spec":{"syncPolicy":{"automated":null}}}`)
	if enabled {
		patch = []byte(`{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}`)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if _, err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	if enabled {
		// 触发一次比对，让新开启的自动同步立即执行
		h.touchArgoRefresh(ctx, client, ns, name, "normal")
	}
	response.OK(c, gin.H{"ok": true, "enabled": enabled})
}

// ArgoAppPause PUT /argocd/apps/:namespace/:name/pause?paused=true|false —— 仅 automated 存在时有效
func (h *CIHandler) ArgoAppPause(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	if _, ok, _ := unstructuredNested(u.Object, "spec", "syncPolicy", "automated"); !ok {
		response.Fail(c, 400, 400, "未开启自动同步，无法暂停/恢复（原生 ArgoCD 的 PAUSE 仅对自动同步应用有效）")
		return
	}
	destNS, _, _ := unstructuredString(u.Object, "spec", "destination", "namespace")
	if !h.requireArgoWrite(c, destNS, "暂停/恢复同步") {
		return
	}
	paused := c.Query("paused") != "false"
	patch := []byte(fmt.Sprintf(`{"spec":{"syncPolicy":{"automated":{"suspend":%v}}}}`, paused))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if _, err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true, "paused": paused})
}

func (h *CIHandler) touchArgoRefresh(ctx context.Context, client *kube.Client, ns, name, mode string) {
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":%q}}}`, mode))
	_, _ = client.Dynamic.Resource(argocdGVR).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
}

// ArgoRefresh POST /argocd/apps/:namespace/:name/refresh?mode=normal|hard —— 原生软/硬刷新
func (h *CIHandler) ArgoRefresh(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	destNS, _, _ := unstructuredString(u.Object, "spec", "destination", "namespace")
	if !h.requireArgoWrite(c, destNS, "刷新比对") {
		return
	}
	mode := "normal"
	if c.Query("mode") == "hard" {
		mode = "hard"
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	h.touchArgoRefresh(ctx, client, ns, name, mode)
	response.OK(c, gin.H{"ok": true, "mode": mode})
}

// ArgoAppDelete DELETE /argocd/apps/:namespace/:name?cascade=true —— 级联先补
// resources-finalizer（ArgoCD 原生级联依赖该 finalizer），再删除 CR
func (h *CIHandler) ArgoAppDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	u, err := h.getArgoApp(c.Request.Context(), client, ns, name)
	if err != nil {
		response.K8sError(c, err)
		return
	}
	obj := u.Object
	destNS, _, _ := unstructuredString(obj, "spec", "destination", "namespace")
	cascade := c.Query("cascade") == "true"
	opName := "删除应用（保留资源）"
	if cascade {
		opName = "级联删除应用（含托管资源）"
	}
	if !h.requireArgoWrite(c, destNS, opName) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if cascade {
		has := false
		if fins, ok, _ := unstructuredNested(obj, "metadata", "finalizers"); ok {
			if arr, ok := fins.([]any); ok {
				for _, f := range arr {
					if s, ok := f.(string); ok && s == argoResourcesFinalizer {
						has = true
					}
				}
			}
		}
		if !has {
			var merged []string
			if fins, ok, _ := unstructuredNested(obj, "metadata", "finalizers"); ok {
				if arr, ok := fins.([]any); ok {
					for _, f := range arr {
						if s, ok := f.(string); ok {
							merged = append(merged, s)
						}
					}
				}
			}
			merged = append(merged, argoResourcesFinalizer)
			arrJSON, _ := json.Marshal(merged)
			patch := []byte(`{"metadata":{"finalizers":` + string(arrJSON) + `}}`)
			if _, err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
				Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				response.K8sError(c, fmt.Errorf("添加级联删除 finalizer 失败: %w", err))
				return
			}
		}
	}
	if err := client.Dynamic.Resource(argocdGVR).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true, "cascade": cascade})
}
