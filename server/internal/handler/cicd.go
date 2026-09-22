// cicd.go 内置 CI（Tekton）handler——替代原 ci-platform HTTP 透传。
// 所有接口经 X-Cluster 选择集群（集群隔离），项目 ns 承载运行资源（命名空间隔离）。
// 权限：GET 登录即可；写操作 admin（与原有 CI 透传策略一致）。
// SSE/WS 端点走 ?token= 查询参数鉴权（浏览器 EventSource/WebSocket 无法设请求头）。
package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"kube-console/server/internal/ci"
	cierr "kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/ci/runtime"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/pkg/response"
)

// CICDHandler 内置 CI 接口。
type CICDHandler struct {
	deps      *ci.Deps
	db        *gorm.DB
	jwtSecret string
	adminUser string

	refsMu    sync.Mutex
	refsCache map[string]refsCacheEntry // 仓库分支/Tag 5 分钟缓存（git ls-remote 较慢）
}

type refsCacheEntry struct {
	at   time.Time
	data *ciRefs
}

// ciRefs 仓库引用（与 ci.Refs 字段一致的对外形态，避免前端依赖内部类型）。
type ciRefs struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
}

func NewCICDHandler(deps *ci.Deps, db *gorm.DB, jwtSecret, adminUser string) *CICDHandler {
	return &CICDHandler{
		deps: deps, db: db, jwtSecret: jwtSecret, adminUser: adminUser,
		refsCache: map[string]refsCacheEntry{},
	}
}

// rfc1123NameRe DNS-1123 子域（项目命名空间名校验用）。
var rfc1123NameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// reservedNS 系统保留命名空间：项目 ns 是 run 资源（PVC/TaskRun/凭证 Secret）的
// 落点，绑到系统 ns 会污染系统资源。
var reservedNS = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

// ============ 通用助手 ============

func (h *CICDHandler) cluster(c *gin.Context) (string, bool) {
	cl := middleware.ClusterName(c)
	if cl == "" {
		response.Fail(c, http.StatusBadRequest, 400, "缺少 X-Cluster 请求头")
		return "", false
	}
	return cl, true
}

func (h *CICDHandler) isAdmin(c *gin.Context) bool {
	return middleware.IsAdmin(h.db, h.adminUser, middleware.CurrentUserID(c), middleware.CurrentUser(c))
}

// requireWrite 写操作门禁（非 admin 403）。
func (h *CICDHandler) requireWrite(c *gin.Context) bool {
	if !h.isAdmin(c) {
		response.Fail(c, http.StatusForbidden, 403, "该操作需要管理员权限")
		return false
	}
	return true
}

// k8s 取当前集群的 Tekton 客户端。
func (h *CICDHandler) k8s(c *gin.Context, cluster string) (*tekton.Client, bool) {
	k8s, err := h.deps.K8sFor(cluster)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return nil, false
	}
	return k8s, true
}

// errResp 把 ci 业务错误映射为响应。
func (h *CICDHandler) errResp(c *gin.Context, err error) {
	var ce *cierr.Error
	if errors.As(err, &ce) {
		response.Fail(c, ce.Code.HTTPStatus(), int(ce.Code), ce.Msg)
		return
	}
	response.Fail(c, http.StatusInternalServerError, 500, err.Error())
}

// pipelineOfCluster 取流水线并校验其集群与 X-Cluster 一致（防跨集群按 id 越权）。
func (h *CICDHandler) pipelineOfCluster(c *gin.Context, cl string, id uint) (*model.CIPipeline, bool) {
	var p model.CIPipeline
	if err := h.db.First(&p, id).Error; err != nil {
		response.Fail(c, http.StatusNotFound, 404, "流水线不存在")
		return nil, false
	}
	if p.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "流水线不存在（不属于当前集群）")
		return nil, false
	}
	return &p, true
}

// runOfCluster 同上（run）。
func (h *CICDHandler) runOfCluster(c *gin.Context, cl string, id uint) (*model.CIRun, bool) {
	var r model.CIRun
	if err := h.db.First(&r, id).Error; err != nil {
		response.Fail(c, http.StatusNotFound, 404, "执行记录不存在")
		return nil, false
	}
	if r.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "执行记录不存在（不属于当前集群）")
		return nil, false
	}
	return &r, true
}


// pathID 解析 :id 路由参数为 uint（非法返回 0 → 上层 404）。
func pathID(c *gin.Context) uint {
	v, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		return 0
	}
	return uint(v)
}

// ============ 就绪 / 节点 / stream-token ============

// Ready GET /ci/ready —— 当前集群 Tekton 就绪探测。
func (h *CICDHandler) Ready(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	installed, err := h.deps.TektonInstalled(cl)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	response.OK(c, gin.H{
		"tektonInstalled": installed,
		"platformNS":      h.deps.Cfg.PlatformNS,
		"serviceAccount":  h.deps.Cfg.ServiceAccount,
		"cacheEnabled":    h.deps.Cfg.CacheEnabled,
	})
}

// NodeTypes GET /ci/node-types —— 节点插件元数据（设计器面板/属性表单驱动）。
func (h *CICDHandler) NodeTypes(c *gin.Context) {
	_, ok := h.cluster(c)
	if !ok {
		return
	}
	response.OK(c, h.deps.Nodes.List())
}

// StreamToken GET /ci/stream-token —— 60s 短 token（SSE/WS/下载 URL 用）。
func (h *CICDHandler) StreamToken(c *gin.Context) {
	tok, err := middleware.GenerateTokenWithTTL(middleware.CurrentUserID(c), middleware.CurrentUser(c), h.jwtSecret, 60*time.Second)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 500, "生成短 token 失败")
		return
	}
	response.OK(c, gin.H{"token": tok})
}

// ============ 项目（= 命名空间隔离单元） ============

// ProjectList GET /ci/projects
func (h *CICDHandler) ProjectList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var items []model.CIProject
	if err := h.db.Where("cluster_name = ?", cl).Order("id asc").Find(&items).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, items)
}

// ProjectCreate POST /ci/projects
func (h *CICDHandler) ProjectCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req struct {
		Name        string `json:"name" binding:"required"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Namespace   string `json:"namespace" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: name/namespace 必填")
		return
	}
	// ns 格式 + 保留 ns 黑名单：项目 ns 会成为 run 的 PVC/TaskRun/凭证 Secret 落点，
	// 绑到 kube-system 等系统 ns 会污染系统资源（EnsureNamespace 对任意已存在 ns 放行）
	if !rfc1123NameRe.MatchString(req.Namespace) {
		response.Fail(c, http.StatusBadRequest, 400, "项目命名空间名非法（须为 RFC 1123 小写字母/数字/中划线）: "+req.Namespace)
		return
	}
	if reservedNS[req.Namespace] {
		response.Fail(c, http.StatusBadRequest, 400, "项目命名空间不能使用系统保留命名空间: "+req.Namespace)
		return
	}
	var count int64
	if err := h.db.Model(&model.CIProject{}).Where("cluster_name = ? AND name = ?", cl, req.Name).Count(&count).Error; err != nil {
		h.errResp(c, err)
		return
	}
	if count > 0 {
		response.Fail(c, http.StatusConflict, 409, "项目名已存在")
		return
	}
	k8s, ok := h.k8s(c, cl)
	if !ok {
		return
	}
	// ns 隔离基础：确保命名空间存在（放在重名检查之后，避免 409 时留下孤儿 ns）
	if err := k8s.EnsureNamespace(c.Request.Context(), req.Namespace); err != nil {
		response.Fail(c, http.StatusServiceUnavailable, 503, fmt.Sprintf("创建/校验命名空间 %s 失败: %v", req.Namespace, err))
		return
	}
	// CI 基础设施随项目一起写入：运行 SA + 运行/部署 RBAC。缺 SA 时 TaskRun 会以
	// PodCreationFailed 收场（没有 Pod 也就没有日志），故在建项目时就补齐并失败即报。
	if _, err := k8s.EnsureCIInfra(c.Request.Context(), req.Namespace, h.deps.Cfg.ServiceAccount); err != nil {
		response.Fail(c, http.StatusServiceUnavailable, 503, fmt.Sprintf("写入 CI 基础设施失败: %v", err))
		return
	}
	p := model.CIProject{
		ClusterName: cl, Name: req.Name, DisplayName: req.DisplayName,
		Description: req.Description, Namespace: req.Namespace,
		CreatedBy: middleware.CurrentUserID(c),
	}
	if err := h.db.Create(&p).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, p)
}

// ProjectUpdate PUT /ci/projects/:id —— 名称/描述可改；ns 不可改（Secret/run 已落原 ns）。
func (h *CICDHandler) ProjectUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "id 非法")
		return
	}
	var p model.CIProject
	if err := h.db.First(&p, id).Error; err != nil || p.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "项目不存在")
		return
	}
	var req struct {
		DisplayName *string `json:"displayName"`
		Description *string `json:"description"`
		Namespace   *string `json:"namespace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	if req.Namespace != nil && *req.Namespace != p.Namespace {
		response.Fail(c, http.StatusBadRequest, 400, "不支持变更项目命名空间（凭证与历史运行已落在原 ns）")
		return
	}
	updates := map[string]interface{}{}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if len(updates) > 0 {
		if err := h.db.Model(&p).Updates(updates).Error; err != nil {
			h.errResp(c, err)
			return
		}
		// 回显更新后的值（p 是更新前加载的对象）
		if err := h.db.First(&p, id).Error; err != nil {
			h.errResp(c, err)
			return
		}
	}
	response.OK(c, p)
}

// ProjectDelete DELETE /ci/projects/:id —— 有流水线或进行中 run 时拒绝。
func (h *CICDHandler) ProjectDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var p model.CIProject
	if err := h.db.First(&p, id).Error; err != nil || p.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "项目不存在")
		return
	}
	var pipes int64
	h.db.Model(&model.CIPipeline{}).Where("project_id = ?", p.ID).Count(&pipes)
	if pipes > 0 {
		response.Fail(c, http.StatusConflict, 409, fmt.Sprintf("项目下有 %d 条流水线，请先删除", pipes))
		return
	}
	var active int64
	h.db.Model(&model.CIRun{}).
		Where("status IN ?", []string{model.CIRunStatusPending, model.CIRunStatusRunning}).
		Joins("JOIN ci_pipelines pp ON pp.id = ci_pipeline_runs.pipeline_id").
		Where("pp.project_id = ?", p.ID).Count(&active)
	if active > 0 {
		response.Fail(c, http.StatusConflict, 409, "项目下有进行中的执行，请等待结束")
		return
	}
	if err := h.db.Delete(&p).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ============ 流水线 ============

// PipelineList GET /ci/pipelines?projectId=
func (h *CICDHandler) PipelineList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	q := h.db.Where("cluster_name = ?", cl)
	if pid := c.Query("projectId"); pid != "" {
		if v, err := strconv.ParseUint(pid, 10, 64); err == nil {
			q = q.Where("project_id = ?", v)
		}
	}
	var items []model.CIPipeline
	if err := q.Order("id asc").Find(&items).Error; err != nil {
		h.errResp(c, err)
		return
	}
	// 附最新版本号（列表规模有限，逐条查可接受）
	for i := range items {
		items[i].LatestVersion = h.latestVersionNo(items[i].ID)
	}
	response.OK(c, items)
}

func (h *CICDHandler) latestVersionNo(pipelineID uint) int {
	var v int
	_ = h.db.Model(&model.CIPipelineVersion{}).Where("pipeline_id = ?", pipelineID).
		Select("COALESCE(MAX(version), 0)").Scan(&v).Error
	return v
}

// PipelineCreate POST /ci/pipelines
func (h *CICDHandler) PipelineCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req struct {
		ProjectID   uint   `json:"projectId" binding:"required"`
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: projectId/name 必填")
		return
	}
	// 项目必须属于当前集群
	var proj model.CIProject
	if err := h.db.First(&proj, req.ProjectID).Error; err != nil || proj.ClusterName != cl {
		response.Fail(c, http.StatusBadRequest, 400, "项目不存在或不属于当前集群")
		return
	}
	p, err := h.deps.Store.Create(c.Request.Context(), middleware.CurrentUserID(c),
		ciPipelineCreateReq(cl, req.ProjectID, req.Name, req.Description))
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, p)
}

// PipelineGet GET /ci/pipelines/:id —— 流水线 + 最新版本（含 DSL，设计器加载用）。
func (h *CICDHandler) PipelineGet(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	p, ok := h.pipelineOfCluster(c, cl, id)
	if !ok {
		return
	}
	out := gin.H{"pipeline": p}
	if latest, err := h.deps.Store.GetLatest(c.Request.Context(), id); err == nil {
		out["latestVersion"] = latest
		if g, err := ciParseGraph(latest.GraphJSON); err == nil {
			out["graph"] = g
		}
	}
	response.OK(c, out)
}

// PipelineUpdate PUT /ci/pipelines/:id
func (h *CICDHandler) PipelineUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	p, err := h.deps.Store.Update(c.Request.Context(), id, ciPipelineUpdateReq(req.Name, req.Description))
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, p)
}

// PipelineDelete DELETE /ci/pipelines/:id
func (h *CICDHandler) PipelineDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var active int64
	h.db.Model(&model.CIRun{}).Where("pipeline_id = ? AND status IN ?", id,
		[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).Count(&active)
	if active > 0 {
		response.Fail(c, http.StatusConflict, 409, "流水线有进行中的执行，请等待结束")
		return
	}
	if err := h.deps.Store.Delete(c.Request.Context(), id); err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// PipelineVersions GET /ci/pipelines/:id/versions
func (h *CICDHandler) PipelineVersions(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	vers, err := h.deps.Store.ListVersions(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, vers)
}

// PipelineSaveVersion POST /ci/pipelines/:id/versions —— 保存 DSL（校验+编译+落版本）。
func (h *CICDHandler) PipelineSaveVersion(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var body ciGraphBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Graph == nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: graphJson 必填")
		return
	}
	v, err := h.deps.Store.SaveVersion(c.Request.Context(), id, middleware.CurrentUserID(c), body.Graph)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, v)
}

// PipelineValidate POST /ci/pipelines/:id/validate
func (h *CICDHandler) PipelineValidate(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var body ciGraphBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Graph == nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: graphJson 必填")
		return
	}
	response.OK(c, h.deps.Pipe.Validate(body.Graph))
}

// PipelineCompile POST /ci/pipelines/:id/compile —— 编译预览（返回 Tekton YAML）。
func (h *CICDHandler) PipelineCompile(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var p *model.CIPipeline
	if p, ok = h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var body ciGraphBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Graph == nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: graphJson 必填")
		return
	}
	// ns 用项目 ns（编译产物里 workspace 等与 ns 无关，仅占位）
	ns := h.deps.Cfg.PlatformNS
	if proj, err := h.projectOf(p.ProjectID); err == nil {
		ns = proj.Namespace
	}
	spec, err := h.deps.Pipe.Compile(body.Graph)
	if err != nil {
		h.errResp(c, err)
		return
	}
	yamlText, err := ciCompileToYAML(spec, ns)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlText})
}

// PipelineRun POST /ci/pipelines/:id/run —— 触发执行（branch/tag 可选覆盖）。
func (h *CICDHandler) PipelineRun(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.pipelineOfCluster(c, cl, id); !ok {
		return
	}
	var req struct {
		VersionID *uint  `json:"versionId"`
		Branch    string `json:"branch"`
		Tag       string `json:"tag"` // 优先于 Branch
	}
	_ = c.ShouldBindJSON(&req) // body 可空
	revision := req.Tag
	if revision == "" {
		revision = req.Branch
	}
	var versionID uint
	if req.VersionID != nil {
		versionID = *req.VersionID
	}
	run, err := h.deps.RT.StartRun(c.Request.Context(), id, versionID,
		middleware.CurrentUserID(c), middleware.CurrentUser(c), "manual", revision, "")
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, run)
}

// PipelineDuplicate POST /ci/pipelines/:id/duplicate —— 复制到目标项目（可同项目，也可跨项目，须同一集群）。
func (h *CICDHandler) PipelineDuplicate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	src, ok := h.pipelineOfCluster(c, cl, id)
	if !ok {
		return
	}
	var req struct {
		TargetProjectID uint   `json:"targetProjectId" binding:"required"`
		Name            string `json:"name"`
		Description     string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: targetProjectId 必填")
		return
	}
	var tp model.CIProject
	if err := h.db.First(&tp, req.TargetProjectID).Error; err != nil || tp.ClusterName != cl {
		response.Fail(c, http.StatusBadRequest, 400, "目标项目不存在或不属于当前集群")
		return
	}
	name := req.Name
	if name == "" {
		name = src.Name + "-copy"
	}
	desc := req.Description
	if desc == "" {
		desc = src.Description
	}
	ctx := c.Request.Context()
	// 复制最新版本 DSL
	latest, err := h.deps.Store.GetLatest(ctx, src.ID)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "源流水线尚无已保存版本")
		return
	}
	g, err := ciParseGraph(latest.GraphJSON)
	if err != nil {
		h.errResp(c, err)
		return
	}
	p, err := h.deps.Store.Create(ctx, middleware.CurrentUserID(c),
		ciPipelineCreateReq(cl, req.TargetProjectID, name, desc))
	if err != nil {
		h.errResp(c, err)
		return
	}
	if _, err := h.deps.Store.SaveVersion(ctx, p.ID, middleware.CurrentUserID(c), g); err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, p)
}

// ============ 执行中心 ============

// RunList GET /ci/runs?pipelineId=&page=&size=&status=
func (h *CICDHandler) RunList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	q := h.db.Model(&model.CIRun{}).Where("cluster_name = ?", cl)
	if pid := c.Query("pipelineId"); pid != "" {
		if v, err := strconv.ParseUint(pid, 10, 64); err == nil {
			q = q.Where("pipeline_id = ?", v)
		}
	}
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	var total int64
	q.Count(&total)
	var items []model.CIRun
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"total": total, "items": items})
}

// RunDetail GET /ci/runs/:id —— run + tasks + 流水线信息。
func (h *CICDHandler) RunDetail(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	run, ok := h.runOfCluster(c, cl, id)
	if !ok {
		return
	}
	var tasks []model.CITaskRun
	if err := h.db.Where("run_id = ?", run.ID).Order("id asc").Find(&tasks).Error; err != nil {
		h.errResp(c, err)
		return
	}
	var pipe model.CIPipeline
	if err := h.db.First(&pipe, run.PipelineID).Error; err != nil {
		// 流水线已删时仍返回 run+tasks（血缘字段留空）
		response.OK(c, gin.H{"run": run, "tasks": tasks})
		return
	}
	response.OK(c, gin.H{
		"run":     run,
		"tasks":   tasks,
		"pipeline": gin.H{"id": pipe.ID, "name": pipe.Name, "projectId": pipe.ProjectID},
	})
}

// RunTasks GET /ci/runs/:id/tasks
func (h *CICDHandler) RunTasks(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.runOfCluster(c, cl, id); !ok {
		return
	}
	var tasks []model.CITaskRun
	if err := h.db.Where("run_id = ?", id).Order("id asc").Find(&tasks).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, tasks)
}

// RunRerun POST /ci/runs/:id/rerun —— 用原版本重新执行。
func (h *CICDHandler) RunRerun(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	run, ok := h.runOfCluster(c, cl, id)
	if !ok {
		return
	}
	// 重跑按原 run 的 revision 复现（tag/commit 触发时 GitBranch 存 tag 名或为空）
	newRun, err := h.deps.RT.StartRun(c.Request.Context(), run.PipelineID, run.VersionID,
		middleware.CurrentUserID(c), middleware.CurrentUser(c), "manual", run.GitBranch, run.GitCommit)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, newRun)
}

// RunCancel POST /ci/runs/:id/cancel
func (h *CICDHandler) RunCancel(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.runOfCluster(c, cl, id); !ok {
		return
	}
	run, err := h.deps.RT.Cancel(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, run)
}

// RunApprovals GET /ci/runs/:id/approvals
func (h *CICDHandler) RunApprovals(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.runOfCluster(c, cl, id); !ok {
		return
	}
	infos, err := h.deps.RT.ListApprovals(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, infos)
}

// RunApprovalDecide POST /ci/runs/:id/approvals —— 批准/驳回审批节点。
func (h *CICDHandler) RunApprovalDecide(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	if _, ok := h.runOfCluster(c, cl, id); !ok {
		return
	}
	var req struct {
		NodeID   string `json:"nodeId" binding:"required"`
		Decision string `json:"decision" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: nodeId/decision 必填")
		return
	}
	if err := h.deps.RT.Decide(c.Request.Context(), id, req.NodeID, req.Decision); err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// RunLogsSSE GET /ci/runs/:id/tasks/:taskID/logs?token=&follow= —— SSE 任务日志（公开路由，query 鉴权）。
func (h *CICDHandler) RunLogsSSE(c *gin.Context) {
	claims, ok := h.authByQuery(c)
	if !ok {
		return
	}
	_ = claims
	runID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "run id 非法")
		return
	}
	taskID, err := strconv.ParseUint(c.Param("taskID"), 10, 64)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "taskID 非法")
		return
	}
	var run model.CIRun
	if err := h.db.First(&run, runID).Error; err != nil {
		response.Fail(c, http.StatusNotFound, 404, "执行记录不存在")
		return
	}
	// 集群维度校验（与同文件其它 run 端点一致）：SSE URL 带 ?cluster=，缺失时不拦
	if cl := c.Query("cluster"); cl != "" && run.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "执行记录不存在（不属于当前集群）")
		return
	}
	follow := c.Query("follow") == "1" ||
		run.Status == model.CIRunStatusPending || run.Status == model.CIRunStatusRunning

	flusher, okFlush := c.Writer.(http.Flusher)
	if !okFlush {
		response.Fail(c, http.StatusInternalServerError, 500, "不支持流式响应")
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flush := func() { flusher.Flush() }
	c.SSEvent("start", gin.H{"runId": runID, "taskId": taskID, "follow": follow})
	flush()

	w := &sseLineWriter{w: &c.Writer, flush: flush}
	if err := h.deps.RT.StreamTaskLogs(c.Request.Context(), uint(runID), uint(taskID), follow, w, flush); err != nil {
		fmt.Fprintf(c.Writer, "data: [error] %v\n\n", err)
		flush()
	}
	w.Flush()
	fmt.Fprint(c.Writer, "event: end\n\n")
	flush()
}

// sseLineWriter 把任意字节流按行切成 SSE data 帧。
type sseLineWriter struct {
	w     *gin.ResponseWriter
	buf   []byte
	flush func()
}

func (s *sseLineWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := s.buf[:i]
		s.buf = s.buf[i+1:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		fmt.Fprintf(*s.w, "data: %s\n\n", line)
		s.flush()
	}
	return len(p), nil
}

func (s *sseLineWriter) Flush() {
	if len(s.buf) == 0 {
		return
	}
	line := s.buf
	s.buf = nil
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	fmt.Fprintf(*s.w, "data: %s\n\n", line)
}

// ============ 凭据 ============

// CredentialList GET /ci/credentials?projectId=
func (h *CICDHandler) CredentialList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var projectID *uint
	if pid := c.Query("projectId"); pid != "" {
		if v, err := strconv.ParseUint(pid, 10, 64); err == nil {
			u := uint(v)
			projectID = &u
		}
	}
	items, err := h.deps.Creds.List(c.Request.Context(), cl, projectID)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, items)
}

// CredentialCreate POST /ci/credentials
func (h *CICDHandler) CredentialCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req ciCredCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: name/form 必填")
		return
	}
	req.Cluster = cl
	cred, err := h.deps.Creds.Create(c.Request.Context(), req)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"id": cred.ID, "name": cred.Name, "form": cred.Form}) // 不回显材料
}

// CredentialUpdate PUT /ci/credentials/:id —— 空字段保持原值（轮换语义）。
func (h *CICDHandler) CredentialUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	cred, err := h.deps.Creds.GetByID(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	if cred.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "凭据不存在（不属于当前集群）")
		return
	}
	var req ciCredUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	updated, err := h.deps.Creds.Update(c.Request.Context(), id, req)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"id": updated.ID, "name": updated.Name})
}

// CredentialDelete DELETE /ci/credentials/:id
func (h *CICDHandler) CredentialDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	cred, err := h.deps.Creds.GetByID(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	if cred.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "凭据不存在（不属于当前集群）")
		return
	}
	if err := h.deps.Creds.Delete(c.Request.Context(), id); err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ============ 全局变量（集群级；${global.KEY} 引用） ============

type globalVarReq struct {
	Key         string `json:"key" binding:"required"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// GlobalList GET /ci/globals
func (h *CICDHandler) GlobalList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var out []model.CIGlobalVar
	if err := h.db.Where("cluster_name = ?", cl).Order("key asc").Find(&out).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, out)
}

// GlobalCreate POST /ci/globals
func (h *CICDHandler) GlobalCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req globalVarReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: key 必填")
		return
	}
	if !validGlobalKey(req.Key) {
		response.Fail(c, http.StatusBadRequest, 400, "key 仅允许大写字母/数字/下划线，长度 1-128")
		return
	}
	gv := model.CIGlobalVar{ClusterName: cl, Key: req.Key, Value: req.Value, Description: req.Description, CreatedBy: middleware.CurrentUserID(c)}
	if err := h.db.Create(&gv).Error; err != nil {
		if cierr.IsUniqueViolation(err) {
			response.Fail(c, http.StatusConflict, 409, "全局变量 "+req.Key+" 已存在")
			return
		}
		h.errResp(c, err)
		return
	}
	response.OK(c, gv)
}

// GlobalUpdate PUT /ci/globals/:id —— key 不可改（引用按 key 解析），仅改值/描述。
func (h *CICDHandler) GlobalUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var gv model.CIGlobalVar
	if err := h.db.First(&gv, id).Error; err != nil || gv.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "全局变量不存在（不属于当前集群）")
		return
	}
	var req globalVarReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	gv.Value = req.Value
	gv.Description = req.Description
	if err := h.db.Save(&gv).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gv)
}

// GlobalDelete DELETE /ci/globals/:id
func (h *CICDHandler) GlobalDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var gv model.CIGlobalVar
	if err := h.db.First(&gv, id).Error; err != nil || gv.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "全局变量不存在（不属于当前集群）")
		return
	}
	if err := h.db.Delete(&gv).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

func validGlobalKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for _, r := range k {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// ============ 定时任务 ============

// ScheduleList GET /ci/schedules?pipelineId=
func (h *CICDHandler) ScheduleList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	q := h.db.Where("cluster_name = ?", cl).Order("id desc")
	if v := c.Query("pipelineId"); v != "" {
		if pid, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("pipeline_id = ?", pid)
		}
	}
	var out []model.CISchedule
	if err := q.Find(&out).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, out)
}

// ScheduleCreate POST /ci/schedules
func (h *CICDHandler) ScheduleCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req struct {
		PipelineID uint   `json:"pipelineId" binding:"required"`
		Cron       string `json:"cron" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: pipelineId/cron 必填")
		return
	}
	if err := runtime.ValidateCron(req.Cron); err != nil {
		h.errResp(c, err)
		return
	}
	p, ok := h.pipelineOfCluster(c, cl, req.PipelineID)
	if !ok {
		return
	}
	next, err := nextRunTime(req.Cron, time.Now())
	if err != nil {
		h.errResp(c, err)
		return
	}
	sch := &model.CISchedule{
		ClusterName: cl, PipelineID: p.ID, ProjectID: p.ProjectID,
		Cron: req.Cron, Enabled: true, NextRunAt: &next, CreatedBy: middleware.CurrentUserID(c),
	}
	if err := h.db.Create(sch).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, sch)
}

// ScheduleUpdate PUT /ci/schedules/:id —— 启停 / 改 cron
func (h *CICDHandler) ScheduleUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var sch model.CISchedule
	if err := h.db.First(&sch, id).Error; err != nil || sch.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "定时任务不存在（不属于当前集群）")
		return
	}
	var req struct {
		Enabled *bool   `json:"enabled"`
		Cron    *string `json:"cron"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	if req.Cron != nil && *req.Cron != sch.Cron {
		if err := runtime.ValidateCron(*req.Cron); err != nil {
			h.errResp(c, err)
			return
		}
		sch.Cron = *req.Cron
	}
	if req.Enabled != nil {
		sch.Enabled = *req.Enabled
	}
	if sch.Enabled {
		next, err := nextRunTime(sch.Cron, time.Now())
		if err != nil {
			h.errResp(c, err)
			return
		}
		sch.NextRunAt = &next
	}
	if err := h.db.Save(&sch).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, sch)
}

// ScheduleDelete DELETE /ci/schedules/:id
func (h *CICDHandler) ScheduleDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var sch model.CISchedule
	if err := h.db.First(&sch, id).Error; err != nil || sch.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "定时任务不存在（不属于当前集群）")
		return
	}
	if err := h.db.Delete(&sch).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

func nextRunTime(expr string, after time.Time) (time.Time, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, cierr.Newf(cierr.InvalidParam, "非法 cron 表达式: %s", expr)
	}
	return sched.Next(after), nil
}

// ============ Webhook（GitLab 触发） ============

func webhookURL(base, token string) string {
	return strings.TrimRight(base, "/") + "/api/ci/webhook/" + token
}

// WebhookList GET /ci/webhooks?pipelineId=
func (h *CICDHandler) WebhookList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	q := h.db.Where("cluster_name = ?", cl).Order("id desc")
	if v := c.Query("pipelineId"); v != "" {
		if pid, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("pipeline_id = ?", pid)
		}
	}
	var out []model.CIWebhook
	if err := q.Find(&out).Error; err != nil {
		h.errResp(c, err)
		return
	}
	base := reqScheme(c) + "://" + c.Request.Host
	// token 是 URL 触发凭据（POST /ci/webhook/:token 为公开端点），列表对非 admin
	// 脱敏——否则任何登录用户拿到全部流水线 token 即可触发任意流水线
	mask := !h.isAdmin(c)
	for i := range out {
		tok := out[i].Token
		if mask {
			tok = maskToken(tok)
		}
		out[i].Token = tok
		out[i].URL = webhookURL(base, tok)
	}
	response.OK(c, out)
}

// maskToken 只回显前 4 位（列表展示用；admin 看原文）。
func maskToken(t string) string {
	if len(t) <= 4 {
		return "****"
	}
	return t[:4] + "****"
}

// WebhookCreate POST /ci/webhooks
func (h *CICDHandler) WebhookCreate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req struct {
		PipelineID uint   `json:"pipelineId" binding:"required"`
		Branch     string `json:"branch"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: pipelineId 必填")
		return
	}
	p, ok := h.pipelineOfCluster(c, cl, req.PipelineID)
	if !ok {
		return
	}
	// token：24 字节随机 hex（URL 触发凭据，泄露即等于触发权）
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		response.Fail(c, http.StatusInternalServerError, 500, "token 生成失败")
		return
	}
	wh := &model.CIWebhook{
		ClusterName: cl, PipelineID: p.ID, ProjectID: p.ProjectID,
		Token: hex.EncodeToString(buf), Branch: req.Branch, Enabled: true,
		CreatedBy: middleware.CurrentUserID(c),
	}
	if err := h.db.Create(wh).Error; err != nil {
		h.errResp(c, err)
		return
	}
	wh.URL = webhookURL(reqScheme(c)+"://"+c.Request.Host, wh.Token)
	response.OK(c, wh)
}

// WebhookUpdate PUT /ci/webhooks/:id —— 启停 / 改分支过滤
func (h *CICDHandler) WebhookUpdate(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var wh model.CIWebhook
	if err := h.db.First(&wh, id).Error; err != nil || wh.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "Webhook 不存在（不属于当前集群）")
		return
	}
	var req struct {
		Enabled *bool   `json:"enabled"`
		Branch  *string `json:"branch"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	if req.Enabled != nil {
		wh.Enabled = *req.Enabled
	}
	if req.Branch != nil {
		wh.Branch = *req.Branch
	}
	if err := h.db.Save(&wh).Error; err != nil {
		h.errResp(c, err)
		return
	}
	wh.URL = webhookURL(reqScheme(c)+"://"+c.Request.Host, wh.Token)
	response.OK(c, wh)
}

// WebhookDelete DELETE /ci/webhooks/:id
func (h *CICDHandler) WebhookDelete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	var wh model.CIWebhook
	if err := h.db.First(&wh, id).Error; err != nil || wh.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "Webhook 不存在（不属于当前集群）")
		return
	}
	if err := h.db.Delete(&wh).Error; err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// GitLabWebhook POST /ci/webhook/:token —— GitLab 回调公开端点（token 走路径）。
func (h *CICDHandler) GitLabWebhook(c *gin.Context) {
	// 上限 8MB：GitLab push 事件含完整 commits 数组，大 push 可超 1MB；
	// 超限直接 413（截断的 JSON 解析必败，GitLab 重试也只会同样失败）
	if c.Request.ContentLength > 8<<20 {
		response.Fail(c, http.StatusRequestEntityTooLarge, 413, "请求体超过 8MB 上限")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "读取请求体失败")
		return
	}
	pid, msg, err := h.deps.Webhook.HandleGitLab(c.Request.Context(), body, c.Param("token"))
	if err != nil {
		// 错误码直回 GitLab（404 无绑定 / 403 token / 400 分支过滤）
		var ce *cierr.Error
		if errors.As(err, &ce) {
			c.JSON(ce.Code.HTTPStatus(), gin.H{"code": int(ce.Code), "message": ce.Msg})
			return
		}
		response.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	response.OK(c, gin.H{"triggered": pid > 0, "pipelineId": pid, "message": msg})
}

// reqScheme 请求协议（TLS 反代下 c.Request.TLS 为空，按 X-Forwarded-Proto 兜底）。
func reqScheme(c *gin.Context) string {
	if p := c.GetHeader("X-Forwarded-Proto"); p == "https" {
		return "https"
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

// ============ 制品 ============

// ArtifactList GET /ci/artifacts?projectId=&runId=&type=&page=&size=
func (h *CICDHandler) ArtifactList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var projectID, runID *uint
	if v := c.Query("projectId"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			nv := uint(n)
			projectID = &nv
		}
	}
	if v := c.Query("runId"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			nv := uint(n)
			runID = &nv
		}
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "50"))
	out, total, err := h.deps.Art.List(c.Request.Context(), cl, projectID, runID, c.Query("type"), page, size)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"total": total, "items": out})
}

// ArtifactGet GET /ci/artifacts/:id —— 详情 + 血缘
func (h *CICDHandler) ArtifactGet(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	d, err := h.deps.Art.Get(c.Request.Context(), cl, id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, d)
}

// ArtifactDownload GET /ci/artifacts/:id/download —— 流式代理下载（MinIO/Nexus）。
func (h *CICDHandler) ArtifactDownload(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	ct, disp, err := h.deps.Art.DownloadMeta(c.Request.Context(), cl, id)
	if err != nil {
		h.errResp(c, err)
		return
	}
	c.Header("Content-Type", ct)
	c.Header("Content-Disposition", disp)
	c.Header("X-Accel-Buffering", "no")
	if err := h.deps.Art.DownloadStream(c.Request.Context(), cl, id, c.Writer); err != nil {
		log.Printf("artifact: 下载 #id=%d 失败: %v", id, err)
	}
}

// ============ 发布记录 ============

// DeploymentList GET /ci/deployments?projectId=&page=&size=
func (h *CICDHandler) DeploymentList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var projectID *uint
	if v := c.Query("projectId"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			nv := uint(n)
			projectID = &nv
		}
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	out, total, err := h.deps.Deploy.List(c.Request.Context(), cl, projectID, page, size)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"total": total, "items": out})
}

// DeploymentRollback POST /ci/deployments/:id/rollback —— 按镜像快照一键回滚。
func (h *CICDHandler) DeploymentRollback(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	id := pathID(c)
	out, err := h.deps.Deploy.Rollback(c.Request.Context(), cl, id, middleware.CurrentUser(c))
	if err != nil {
		h.errResp(c, err)
		return
	}
	if out.Status == model.CIRunStatusFailed {
		response.Fail(c, http.StatusBadGateway, 502, "回滚部分失败（详见记录）")
		return
	}
	response.OK(c, out)
}

// ============ 仓库 refs / k8s targets ============

// RepoRefs GET /ci/repo/refs?url=&username=&password=&token= —— git ls-remote（5 分钟缓存）。
func (h *CICDHandler) RepoRefs(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	url := c.Query("url")
	if url == "" {
		response.Fail(c, http.StatusBadRequest, 400, "url 必填")
		return
	}
	// 鉴权只走 credential 名（查库取凭证 → 读 K8s Secret）：
	// query 传明文 username/password/token 会落在反向代理 access log 的请求行里，已下线
	username, password, token := "", "", ""
	if credName := c.Query("credential"); credName != "" {
		k8s, ok := h.k8s(c, cl)
		if ok {
			var cred model.CICredential
			if err := h.db.Where("cluster_name = ? AND name = ?", cl, credName).First(&cred).Error; err == nil {
				if data, err := k8s.SecretData(c.Request.Context(), cred.SecretNS, cred.SecretName); err == nil {
					username = string(data[tekton.SecretKeyUsername])
					password = string(data[tekton.SecretKeyPassword])
					token = string(data[tekton.SecretKeyToken])
				}
			}
		}
	}
	key := url + "\x00" + username + "\x00" + token
	h.refsMu.Lock()
	if e, ok := h.refsCache[key]; ok && time.Since(e.at) < 5*time.Minute {
		h.refsMu.Unlock()
		response.OK(c, e.data)
		return
	}
	h.refsMu.Unlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	refs, err := ci.GitRepoRefs(ctx, url, username, password, token)
	if err != nil {
		response.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	out := &ciRefs{Branches: refs.Branches, Tags: refs.Tags}
	h.refsMu.Lock()
	h.refsCache[key] = refsCacheEntry{at: time.Now(), data: out}
	if len(h.refsCache) > 256 {
		h.refsCache = map[string]refsCacheEntry{}
	}
	h.refsMu.Unlock()
	response.OK(c, out)
}

// K8sTargets GET /ci/k8s/targets?namespace= —— k8s-deploy 节点面板的下拉数据。
func (h *CICDHandler) K8sTargets(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	k8s, ok := h.k8s(c, cl)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	infos, err := k8s.NamespaceWorkloads(c.Request.Context(), ns)
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, 503, err.Error())
		return
	}
	response.OK(c, gin.H{"namespace": ns, "workloads": infos})
}

// ============ WS 状态推送（公开路由，query 鉴权） ============

// RunWS GET /ci/ws?token=&cluster=&runId= —— 执行状态 WebSocket。
func (h *CICDHandler) RunWS(c *gin.Context) {
	claims, ok := h.authByQuery(c)
	if !ok {
		return
	}
	_ = claims
	runID := c.Query("runId")
	if runID == "" {
		c.String(http.StatusBadRequest, "runId required")
		return
	}
	// 集群维度校验（前端 runWsUrl 带 ?cluster=）：Upgrade 前挡住跨集群偷听
	if cl := c.Query("cluster"); cl != "" {
		if uid, perr := strconv.ParseUint(runID, 10, 64); perr == nil {
			var run model.CIRun
			if gerr := h.db.First(&run, uid).Error; gerr != nil || run.ClusterName != cl {
				c.String(http.StatusNotFound, "run not found in cluster")
				return
			}
		}
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	msgs, cancel := h.deps.Hub.Subscribe(runID)
	defer cancel()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case msg, okMsg := <-msgs:
			if !okMsg {
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case <-c.Request.Context().Done():
			return
		}
	}
}

// authByQuery ?token= 查询参数鉴权（SSE/WS 无法设请求头）。
func (h *CICDHandler) authByQuery(c *gin.Context) (*middleware.Claims, bool) {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		response.Fail(c, http.StatusUnauthorized, 401, "缺少 token 参数")
		return nil, false
	}
	claims, err := middleware.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		response.Fail(c, http.StatusUnauthorized, 401, "令牌无效或已过期")
		return nil, false
	}
	return claims, true
}

// projectOf 按 id 取项目。
func (h *CICDHandler) projectOf(id uint) (*model.CIProject, error) {
	var p model.CIProject
	if err := h.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}
