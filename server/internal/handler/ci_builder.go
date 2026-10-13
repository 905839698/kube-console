// ci_builder.go 镜像构建器接口：上传本地构建产物（Java jar / Go 二进制 /
// 前端静态 tar.gz），平台按应用类型生成约定 Dockerfile，经托管流水线构建并
// 推送镜像。鉴权档位与流水线一致（按项目 ns 三层 RBAC）；制品下载是公开路由，
// 令牌收口（任务 Pod 无法带 JWT）。
package handler

import (
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/ci/builder"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/pkg/response"
)

// ciBuilderSaveBody POST/PUT /ci/builders 的表单 JSON。
type ciBuilderSaveBody struct {
	ProjectID    uint   `json:"projectId"`
	Name         string `json:"name"`
	AppType      string `json:"appType"`
	Registry     string `json:"registry"`
	Repository   string `json:"repository"`
	CredentialID uint   `json:"credentialId"`
	BaseImage    string `json:"baseImage"`
	Port         int    `json:"port"`
	JavaOpts     string `json:"javaOpts"`
	EntryArgs    string `json:"entryArgs"`
	DefaultTag   string `json:"defaultTag"`

	ClusterName string `json:"-"`
}

func (r *ciBuilderSaveBody) toReq() *builder.SaveReq {
	return &builder.SaveReq{
		ClusterName: r.ClusterName, ProjectID: r.ProjectID, Name: r.Name, AppType: r.AppType,
		Registry: r.Registry, Repository: r.Repository, CredentialID: r.CredentialID,
		BaseImage: r.BaseImage, Port: r.Port, JavaOpts: r.JavaOpts, EntryArgs: r.EntryArgs, DefaultTag: r.DefaultTag,
	}
}

func ciFileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ============ 门禁助手 ============

// builderOf 取构建器并校验集群（跨集群按 id 越权防线）。
func (h *CICDHandler) builderOf(c *gin.Context, cl string, id uint) (*model.CIImageBuilder, bool) {
	b, err := h.deps.Builders.Get(c.Request.Context(), id)
	if err != nil {
		h.errResp(c, err)
		return nil, false
	}
	if b.ClusterName != cl {
		response.Fail(c, http.StatusNotFound, 404, "构建器不存在（不属于当前集群）")
		return nil, false
	}
	return b, true
}

// builderNSRead 读档：构建器 → 项目 ns 可读
func (h *CICDHandler) builderNSRead(c *gin.Context, cl string, b *model.CIImageBuilder) bool {
	ns, ok := h.nsOfProjectID(c, cl, b.ProjectID)
	return ok && h.denyNSRead(c, cl, ns, "构建器")
}

// builderNSWrite 改档：需项目 ns 可写（建/改/删/上传都算写）
func (h *CICDHandler) builderNSWrite(c *gin.Context, cl string, b *model.CIImageBuilder) bool {
	ns, ok := h.nsOfProjectID(c, cl, b.ProjectID)
	return ok && h.requireNSWrite(c, cl, ns)
}

// ============ 列表 / CRUD ============

// ciBuilderView 列表项：构建器 + 镜像全名 + 最近一次执行摘要（前端列表直接渲染）。
type ciBuilderView struct {
	model.CIImageBuilder
	Image     string       `json:"image"`
	LastRun   *model.CIRun `json:"lastRun,omitempty"`
	ProjectNS string       `json:"projectNamespace"`
}

func ciBuilderImageRef(b *model.CIImageBuilder) string {
	ref := b.Registry + "/" + b.Repository
	if b.DefaultTag != "" {
		ref += ":" + b.DefaultTag
	}
	return ref
}

// BuilderList GET /ci/builders?projectId= —— 只列可读 ns 的构建器
func (h *CICDHandler) BuilderList(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	projIDs, err := h.readableProjectIDs(c, cl)
	if err != nil {
		h.errResp(c, err)
		return
	}
	if pid := c.Query("projectId"); pid != "" {
		v64, err := strconv.ParseUint(pid, 10, 64)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, 400, "projectId 非法")
			return
		}
		v := uint(v64)
		if !slices.Contains(projIDs, v) {
			response.Fail(c, http.StatusNotFound, 404, "项目不存在或无权访问")
			return
		}
		projIDs = []uint{v}
	}
	nsOf, err := h.projectNSMap(c, cl)
	if err != nil {
		h.errResp(c, err)
		return
	}
	var bs []model.CIImageBuilder
	if len(projIDs) > 0 {
		if err := h.db.Where("cluster_name = ? AND project_id IN ?", cl, projIDs).
			Order("id desc").Find(&bs).Error; err != nil {
			h.errResp(c, err)
			return
		}
	}
	out := make([]ciBuilderView, 0, len(bs))
	for i := range bs {
		b := bs[i]
		v := ciBuilderView{
			CIImageBuilder: b,
			Image:          ciBuilderImageRef(&b),
			ProjectNS:      nsOf[b.ProjectID],
		}
		var lr model.CIRun
		if err := h.db.Where("pipeline_id = ?", b.PipelineID).Order("id desc").First(&lr).Error; err == nil {
			v.LastRun = &lr
		}
		out = append(out, v)
	}
	response.OK(c, gin.H{"items": out})
}

// BuilderCreate POST /ci/builders
func (h *CICDHandler) BuilderCreate(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	var req ciBuilderSaveBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: projectId/name/appType/registry/repository/credentialId 必填")
		return
	}
	var proj model.CIProject
	if err := h.db.First(&proj, req.ProjectID).Error; err != nil || proj.ClusterName != cl {
		response.Fail(c, http.StatusBadRequest, 400, "项目不存在或不属于当前集群")
		return
	}
	if !h.requireNSWrite(c, cl, proj.Namespace) {
		return
	}
	req.ClusterName = cl
	b, err := h.deps.Builders.Create(c.Request.Context(), middleware.CurrentUserID(c), req.toReq())
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, b)
}

// BuilderUpdate PUT /ci/builders/:id —— 配置整体覆盖（项目与应用类型不可改）
func (h *CICDHandler) BuilderUpdate(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	b, ok := h.builderOf(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.builderNSWrite(c, cl, b) {
		return
	}
	var req ciBuilderSaveBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "参数错误: name/appType/registry/repository/credentialId 必填")
		return
	}
	req.ClusterName = cl
	out, err := h.deps.Builders.Update(c.Request.Context(), b.ID, req.toReq())
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, out)
}

// BuilderDelete DELETE /ci/builders/:id —— 连带删除托管流水线与制品文件
func (h *CICDHandler) BuilderDelete(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	b, ok := h.builderOf(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.builderNSWrite(c, cl, b) {
		return
	}
	var active int64
	h.db.Model(&model.CIRun{}).Where("pipeline_id = ? AND status IN ?", b.PipelineID,
		model.CIRunActiveStatuses).Count(&active)
	if active > 0 {
		response.Fail(c, http.StatusConflict, 409, "构建器有进行中的构建，请等待结束或先取消执行")
		return
	}
	if err := h.deps.Builders.Delete(c.Request.Context(), b.ID); err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ============ 制品上传 / 构建 / 预览 ============

// BuilderUpload POST /ci/builders/:id/artifact —— multipart 字段 file
func (h *CICDHandler) BuilderUpload(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	b, ok := h.builderOf(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.builderNSWrite(c, cl, b) {
		return
	}
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "multipart 解析失败: "+err.Error())
		return
	}
	file, hdr, err := c.Request.FormFile("file")
	if err != nil {
		response.Fail(c, http.StatusBadRequest, 400, "缺少上传文件（表单字段 file）")
		return
	}
	defer file.Close()
	out, err := h.deps.Builders.UploadArtifact(c.Request.Context(), b.ID, hdr.Filename, file)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, out)
}

// BuilderRun POST /ci/builders/:id/run —— body 可空；tag 覆盖本次构建镜像 tag
func (h *CICDHandler) BuilderRun(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	b, ok := h.builderOf(c, cl, pathID(c))
	if !ok {
		return
	}
	ns, ok := h.nsOfProjectID(c, cl, b.ProjectID)
	if !ok {
		return
	}
	if !h.requireNSRun(c, cl, ns) {
		return
	}
	var req struct {
		Tag string `json:"tag"`
	}
	_ = c.ShouldBindJSON(&req) // body 可空
	run, err := h.deps.Builders.Run(c.Request.Context(), b.ID, middleware.CurrentUserID(c), middleware.CurrentUser(c), req.Tag)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, run)
}

// BuilderPreview GET /ci/builders/:id/preview —— 托管流水线编译产物（Tekton YAML，
// 内含约定 Dockerfile 的 preShell；排障用，不落版本）
func (h *CICDHandler) BuilderPreview(c *gin.Context) {
	cl, ok := h.cluster(c)
	if !ok {
		return
	}
	b, ok := h.builderOf(c, cl, pathID(c))
	if !ok {
		return
	}
	if !h.builderNSRead(c, cl, b) {
		return
	}
	yamlText, err := h.deps.Builders.Preview(c.Request.Context(), b.ID)
	if err != nil {
		h.errResp(c, err)
		return
	}
	response.OK(c, gin.H{"yaml": yamlText})
}

// ============ 制品下载（公开路由，令牌收口）============

// sanitizeFilenameHeader 只剥 Content-Disposition 的破坏字符（引号/分号/换行）。
var filenameBadRe = regexp.MustCompile(`["';\r\n\\]`)

// BuilderArtifactGet GET /ci/artifact/:token —— 任务 Pod 里
// artifact-fetch 节点回平台下载上传的制品。无 JWT（Pod 没有用户身份），
// 按随机令牌精确匹配 ci_builder_tokens；越权面 = 猜中 48hex 令牌拿到
// 一个构建产物文件，且下载行为落在审计可见的公开路由组。
func (h *CICDHandler) BuilderArtifactGet(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	if len(token) < 16 {
		response.Fail(c, http.StatusNotFound, 404, "制品链接无效或已过期")
		return
	}
	b, err := h.deps.Builders.LookupToken(c.Request.Context(), token)
	if err != nil {
		h.errResp(c, err)
		return
	}
	if b.FileName == "" {
		response.Fail(c, http.StatusNotFound, 404, "该构建器尚未上传制品")
		return
	}
	path := h.deps.Builders.ArtifactPath(b)
	if !ciFileExists(path) {
		response.Fail(c, http.StatusNotFound, 404, "制品文件已丢失，请重新上传")
		return
	}
	safe := filenameBadRe.ReplaceAllString(b.FileName, "_")
	c.Header("Content-Disposition", `attachment; filename="`+safe+`"`)
	http.ServeFile(c.Writer, c.Request, path)
}
