// ci_cache.go 项目构建缓存的运维入口：看清承载盘（运行工作区，依赖缓存在它里面的
// 子目录）、哪些流水线在用缓存、以及「清空缓存」。
//
// 为什么要专门一页：缓存写满是"构建莫名其妙失败"的头号来源（下载报 ENOSPC、编译
// 半路挂），而它表现成一次普通的流水线失败；没有界面入口就只能 kubectl。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kube-console/server/internal/ci/runtime"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/model"
	"kube-console/server/pkg/response"
)

// CacheUser 某条流水线里勾了「使用缓存」的节点
type CacheUser struct {
	PipelineID uint     `json:"pipelineId"`
	Pipeline   string   `json:"pipeline"`
	Nodes      []string `json:"nodes"`
}

// ProjectCacheView 项目缓存页的数据
type ProjectCacheView struct {
	Project   string `json:"project"`
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster"`

	// CacheEnabled 平台开关（ci.cacheEnabled）：关着时节点勾了也没用，页面上必须说清
	CacheEnabled bool `json:"cacheEnabled"`
	// CacheDir 缓存在承载盘里的子目录名
	CacheDir string `json:"cacheDir"`
	// WorkspaceSize 下一次建承载盘用的容量（ci.pvcSize：工作区与缓存共用这一块盘）
	WorkspaceSize string `json:"workspaceSize"`

	// Workspace 承载盘。缓存就在它里面的 CacheDir 子目录，不是第二块盘。
	Workspace *tekton.PVCView `json:"workspace"`
	// Legacy 旧设计的独立缓存盘（ci-cache-p*）。缓存已并入工作区盘，它们不再被任何
	// 流程引用，列出来让用户主动回收——不在后台悄悄删别人的盘。
	Legacy []tekton.PVCView `json:"legacy"`
	Users  []CacheUser      `json:"users"`

	// Busy 有已提交的 run 在跑：此时清空会把正在用的盘抽掉，接口拒绝
	Busy bool `json:"busy"`
}

// ProjectCacheGet GET /ci/projects/:id/cache —— 承载盘状态与使用方
func (h *CICDHandler) ProjectCacheGet(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	p, ok := h.projectOfCluster(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.requireNSRun(c, cl, p.Namespace) {
		return
	}
	view := &ProjectCacheView{
		Project: p.Name, Namespace: p.Namespace, Cluster: cl,
		CacheEnabled: h.deps.Cfg.CacheEnabled, CacheDir: runtime.ProjectCacheDir,
		WorkspaceSize: h.deps.Cfg.PVCSize,
		Legacy:        []tekton.PVCView{}, Users: h.cacheUsers(c, p.ID),
	}
	k8s, err := h.deps.K8sFor(cl)
	if err != nil {
		// 集群不可达：DB 侧信息照给，盘状态留空（界面按"读不到"提示，不假装没有盘）
		response.OK(c, view)
		return
	}
	ctx := c.Request.Context()
	if ws, err := k8s.GetPVC(ctx, p.Namespace, runtime.ProjectWorkspacePVCName(p.ID)); err == nil {
		view.Workspace = ws
	}
	if names, err := k8s.ListPVCs(ctx, p.Namespace, "ci-cache-p"); err == nil {
		want := runtime.LegacyProjectCachePVCName(p.ID)
		for _, n := range names {
			if n != want {
				continue // 别的项目的盘不归这里管
			}
			if v, err := k8s.GetPVC(ctx, p.Namespace, n); err == nil && v != nil {
				view.Legacy = append(view.Legacy, *v)
			}
		}
	}
	var busy int64
	if err := h.projectBusyRuns(p.ID).Count(&busy).Error; err == nil {
		view.Busy = busy > 0
	}
	response.OK(c, view)
}

// ProjectCacheClear POST /ci/projects/:id/cache/clear —— 清空依赖缓存
//
// 缓存与工作区同盘，所以清空 = 删承载盘再建一块空的。代价在确认文案里说清：
// 下次运行多一次磁盘绑定；工作目录本来每次运行都由 git-clone 清空重建，没有额外损失。
// 顺带回收旧设计遗留的独立缓存盘（已不被引用，只占容量）。
func (h *CICDHandler) ProjectCacheClear(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	p, ok := h.projectOfCluster(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.requireNSWrite(c, cl, p.Namespace) {
		return
	}
	k8s, err := h.deps.K8sFor(cl)
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, 503, "集群不可用: "+err.Error())
		return
	}
	var busy int64
	if err := h.projectBusyRuns(p.ID).Count(&busy).Error; err != nil {
		h.errResp(c, err)
		return
	}
	if busy > 0 {
		response.Fail(c, http.StatusConflict, 409, "项目下有正在运行的执行，等它结束再清空缓存（正在用的盘被抽掉会让那次失败得莫名其妙）")
		return
	}
	ctx := c.Request.Context()
	ns := p.Namespace
	deleted := []string{}
	if names, e := k8s.ListPVCs(ctx, ns, "ci-cache-p"); e == nil {
		for _, n := range names {
			if n != runtime.LegacyProjectCachePVCName(p.ID) {
				continue
			}
			if e := k8s.DeletePVC(ctx, ns, n); e == nil {
				deleted = append(deleted, n)
			}
		}
	}
	name := runtime.ProjectWorkspacePVCName(p.ID)
	if e := k8s.DeletePVC(ctx, ns, name); e != nil {
		h.errResp(c, e)
		return
	}
	deleted = append(deleted, name)
	// 立刻补一块空盘：否则刷新后显示"没有盘"，用户以为缓存被永久关掉了。
	// 建失败不判死——下次运行还会再建，界面上会写明原因。
	if e := k8s.EnsurePVC(ctx, ns, name, h.deps.Cfg.PVCSize, ""); e != nil {
		response.OK(c, gin.H{"cleared": deleted, "warning": "已清空，但重建空盘失败（下次运行会自动重建）：" + e.Error()})
		return
	}
	response.OK(c, gin.H{"cleared": deleted, "recreated": true})
}

// projectBusyRuns 该项目已提交（pending/running）的 run 计数。
// 排队中的 run 还没占用盘，不该挡住清空。
func (h *CICDHandler) projectBusyRuns(projectID uint) *gorm.DB {
	return h.db.Model(&model.CIRun{}).
		Where("status IN ?", model.CIRunSubmittedStatuses).
		Joins("JOIN ci_pipelines pp ON pp.id = ci_pipeline_runs.pipeline_id").
		Where("pp.project_id = ?", projectID)
}

// cacheUsers 该项目下每条流水线（按其最新版本图）勾了缓存的节点。
// 图坏了不列出（CacheNodesOfGraph 返回 nil）：坏数据不该让整页打不开。
func (h *CICDHandler) cacheUsers(c *gin.Context, projectID uint) []CacheUser {
	ctx := c.Request.Context()
	var pipes []model.CIPipeline
	if err := h.db.WithContext(ctx).Where("project_id = ?", projectID).
		Order("name asc").Find(&pipes).Error; err != nil {
		return []CacheUser{}
	}
	out := []CacheUser{}
	for _, pipe := range pipes {
		var graphs []string
		err := h.db.WithContext(ctx).Model(&model.CIPipelineVersion{}).
			Where("pipeline_id = ?", pipe.ID).Order("version desc").
			Limit(1).Pluck("graph_json", &graphs).Error
		if err != nil || len(graphs) == 0 {
			continue
		}
		nodes := runtime.CacheNodesOfGraph(graphs[0])
		if len(nodes) == 0 {
			continue
		}
		out = append(out, CacheUser{PipelineID: pipe.ID, Pipeline: pipe.Name, Nodes: nodes})
	}
	return out
}
