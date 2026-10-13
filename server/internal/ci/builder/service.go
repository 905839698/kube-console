package builder

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/ci/pipeline"
	"kube-console/server/internal/ci/pipeline/compiler"
	"kube-console/server/internal/ci/runtime"
	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

// tokenRetention 每个构建器保留的有效下载令牌数（新上传轮转；进行中的
// run 持有旧令牌仍可下载，超限的最旧令牌淘汰）。
const tokenRetention = 5

// Service 镜像构建器编排：配置 CRUD、制品上传落盘、托管流水线版本生成与运行。
type Service struct {
	db        *gorm.DB
	cfg       *config.CIConfig
	pipe      *pipeline.Service
	store     *pipeline.Store
	rt        *runtime.Service
	uploadDir string
	maxBytes  int64
}

func New(db *gorm.DB, cfg *config.CIConfig, pipe *pipeline.Service, store *pipeline.Store, rt *runtime.Service) *Service {
	dir := strings.TrimSpace(cfg.BuilderUploadDir)
	if dir == "" {
		dir = "./data/ci-artifacts"
	}
	maxMB := cfg.BuilderUploadMaxMB
	if maxMB <= 0 {
		maxMB = 512
	}
	// 目录建不出来 = 上传必挂，启动即报错好过运行期 500
	if err := os.MkdirAll(dir, 0o750); err != nil {
		panic(fmt.Sprintf("ci: 镜像构建器上传目录不可用 %s: %v", dir, err))
	}
	return &Service{
		db: db, cfg: cfg, pipe: pipe, store: store, rt: rt,
		uploadDir: dir,
		maxBytes:  int64(maxMB) * 1024 * 1024,
	}
}

// ============ 请求与校验 ============

// SaveReq 创建/更新构建器的表单请求（全量字段；更新即整体覆盖配置）。
type SaveReq struct {
	ClusterName  string `json:"-"` // handler 从 X-Cluster 注入
	ProjectID    uint   `json:"projectId" binding:"required"`
	Name         string `json:"name" binding:"required"`
	AppType      string `json:"appType" binding:"required"`
	Registry     string `json:"registry" binding:"required"`
	Repository   string `json:"repository" binding:"required"`
	CredentialID uint   `json:"credentialId" binding:"required"`
	BaseImage    string `json:"baseImage"`
	Port         int    `json:"port"`
	JavaOpts     string `json:"javaOpts"`
	EntryArgs    string `json:"entryArgs"`
	DefaultTag   string `json:"defaultTag"`
}

func (s *Service) validateSave(req *SaveReq) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Registry = strings.TrimSpace(req.Registry)
	req.Repository = strings.TrimSpace(req.Repository)
	req.BaseImage = strings.TrimSpace(req.BaseImage)
	req.DefaultTag = strings.TrimSpace(req.DefaultTag)
	req.JavaOpts = strings.TrimSpace(req.JavaOpts)
	req.EntryArgs = strings.TrimSpace(req.EntryArgs)
	if req.Name == "" || len(req.Name) > 64 {
		return errcode.New(errcode.InvalidParam, "构建器名称需为 1-64 个字符")
	}
	if !slices.Contains(model.CIBuilderAppTypes, req.AppType) {
		return errcode.Newf(errcode.InvalidParam, "应用类型 %q 不支持（java/go/web）", req.AppType)
	}
	if err := ValidateImageFields(req.Registry, req.Repository, req.DefaultTag); err != nil {
		return errcode.New(errcode.InvalidParam, err.Error())
	}
	if req.Port < 0 || req.Port > 65535 {
		return errcode.New(errcode.InvalidParam, "端口需在 1-65535（0 = 按类型默认：Java/Go 8080，前端 80）")
	}
	if err := checkNoInjection(req.BaseImage, "基础镜像"); err != nil {
		return errcode.New(errcode.InvalidParam, err.Error())
	}
	if len(req.JavaOpts) > 512 {
		return errcode.New(errcode.InvalidParam, "JVM 参数过长（≤512）")
	}
	if req.AppType != model.CIBuilderJava {
		req.JavaOpts = ""
	}
	// 启动参数仅 java/go（前端跑 nginx，无 ENTRYPOINT 参数概念）
	if req.AppType == model.CIBuilderWeb {
		req.EntryArgs = ""
	}
	if len(req.EntryArgs) > 512 {
		return errcode.New(errcode.InvalidParam, "启动参数过长（≤512）")
	}
	for _, a := range strings.Fields(req.EntryArgs) {
		if strings.ContainsAny(a, "\"\\`$") {
			return errcode.Newf(errcode.InvalidParam, "启动参数 %q 含非法字符（引号/反斜杠/反引号/$ 不允许；参数不经 shell）", a)
		}
	}
	return nil
}

// validateCredential 保存时就校验凭证引用有效（悬空凭证到运行期只剩一句
// build-image 的 WARN，产物镜像都没推出去——参数要在提交前报错）。
func (s *Service) validateCredential(ctx context.Context, clusterName string, projectID, credID uint) error {
	var c model.CICredential
	if err := s.db.WithContext(ctx).First(&c, credID).Error; err != nil {
		return errcode.Newf(errcode.InvalidParam, "推送凭证不存在（id=%d），请在凭据页确认", credID)
	}
	if c.ClusterName != clusterName {
		return errcode.New(errcode.InvalidParam, "推送凭证不属于当前集群")
	}
	if c.ProjectID != nil && *c.ProjectID != projectID {
		return errcode.New(errcode.InvalidParam, "推送凭证属于其它项目（跨项目不可用）")
	}
	if c.Form != model.CIFormDockerconfig && c.Form != model.CIFormBasic {
		return errcode.Newf(errcode.InvalidParam, "推送凭证形态需为「账密」或「DockerConfig」（当前: %s）", c.Form)
	}
	return nil
}

// ============ CRUD ============

// pipelineNameFor 托管流水线名：bld-<净化后的构建器名>（DNS-1123）。
func pipelineNameFor(name string) string {
	safe := strings.ToLower(name)
	var sb strings.Builder
	prevDash := false
	for _, r := range safe {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			sb.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && sb.Len() > 0 {
			sb.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(sb.String(), "-")
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	if out == "" {
		out = "builder"
	}
	return "bld-" + out
}

const pipelineDesc = "镜像构建器托管流水线：上传制品 → 约定 Dockerfile → 构建推送。请通过「镜像构建器」表单修改，不要直接编辑画布。"

func (s *Service) Create(ctx context.Context, uid uint, req *SaveReq) (*model.CIImageBuilder, error) {
	if err := s.validateSave(req); err != nil {
		return nil, err
	}
	if err := s.validateCredential(ctx, req.ClusterName, req.ProjectID, req.CredentialID); err != nil {
		return nil, err
	}
	var dup int64
	if err := s.db.WithContext(ctx).Model(&model.CIImageBuilder{}).
		Where("project_id = ? AND name = ?", req.ProjectID, req.Name).Count(&dup).Error; err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, errcode.Newf(errcode.Conflict, "该项目下已有同名构建器: %s", req.Name)
	}
	p, err := s.store.Create(ctx, uid, pipeline.CreateReq{
		ClusterName: req.ClusterName, ProjectID: req.ProjectID,
		Name: pipelineNameFor(req.Name), Description: pipelineDesc,
	})
	if err != nil {
		return nil, err
	}
	b := &model.CIImageBuilder{
		ClusterName: req.ClusterName, ProjectID: req.ProjectID,
		Name: req.Name, AppType: req.AppType,
		Registry: req.Registry, Repository: req.Repository, CredentialID: req.CredentialID,
		BaseImage: req.BaseImage, Port: req.Port, JavaOpts: req.JavaOpts, EntryArgs: req.EntryArgs, DefaultTag: req.DefaultTag,
		PipelineID: p.ID, CreatedBy: uid,
	}
	if err := s.db.WithContext(ctx).Create(b).Error; err != nil {
		// 回滚托管流水线，不留孤儿
		_ = s.store.Delete(ctx, p.ID)
		return nil, err
	}
	return b, nil
}

func (s *Service) Update(ctx context.Context, id uint, req *SaveReq) (*model.CIImageBuilder, error) {
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 项目与类型不可改（类型决定产物格式与 Dockerfile 形态，换类型 = 重建）
	req.ProjectID = b.ProjectID
	req.ClusterName = b.ClusterName
	req.AppType = b.AppType
	if err := s.validateSave(req); err != nil {
		return nil, err
	}
	if err := s.validateCredential(ctx, b.ClusterName, b.ProjectID, req.CredentialID); err != nil {
		return nil, err
	}
	var dup int64
	if err := s.db.WithContext(ctx).Model(&model.CIImageBuilder{}).
		Where("project_id = ? AND name = ? AND id <> ?", b.ProjectID, req.Name, b.ID).Count(&dup).Error; err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, errcode.Newf(errcode.Conflict, "该项目下已有同名构建器: %s", req.Name)
	}
	updates := map[string]interface{}{
		"name": req.Name, "registry": req.Registry, "repository": req.Repository,
		"credential_id": req.CredentialID, "base_image": req.BaseImage,
		"port": req.Port, "java_opts": req.JavaOpts, "entry_args": req.EntryArgs, "default_tag": req.DefaultTag,
	}
	if err := s.db.WithContext(ctx).Model(b).Updates(updates).Error; err != nil {
		return nil, err
	}
	out, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 已有制品时同步刷新版本（配置变了旧版本就过期）。
	// 失败不吞掉：明确报错，Run 时也会再兜一次。
	if out.FileName != "" {
		if _, err := s.EnsureVersion(ctx, out, s.autoTag()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id uint) error {
	b, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if b.PipelineID > 0 {
		if err := s.store.Delete(ctx, b.PipelineID); err != nil {
			return err
		}
	}
	if err := s.db.WithContext(ctx).Where("builder_id = ?", id).Delete(&model.CIBuilderToken{}).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&model.CIImageBuilder{}, id).Error; err != nil {
		return err
	}
	_ = os.RemoveAll(s.artifactDir(id))
	return nil
}

func (s *Service) get(ctx context.Context, id uint) (*model.CIImageBuilder, error) {
	var b model.CIImageBuilder
	if err := s.db.WithContext(ctx).First(&b, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "构建器不存在")
		}
		return nil, err
	}
	return &b, nil
}

// Get 对外取单条。
func (s *Service) Get(ctx context.Context, id uint) (*model.CIImageBuilder, error) {
	return s.get(ctx, id)
}

// ListByProject 项目内构建器（handler 负责按可读 ns 过滤与富化）。
func (s *Service) ListByProject(ctx context.Context, projectID uint) ([]model.CIImageBuilder, error) {
	var bs []model.CIImageBuilder
	err := s.db.WithContext(ctx).Where("project_id = ?", projectID).Order("id desc").Find(&bs).Error
	return bs, err
}

// ============ 制品上传 ============

var safeUploadBaseRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// artifactStoredName 校验上传文件名并按类型归一到 workspace 固定名。
// 只信任 basename：浏览器/客户端可能带路径，目录穿越一律截掉。
func artifactStoredName(appType, headerName string) (string, error) {
	base := path.Base(strings.ReplaceAll(headerName, "\\", "/"))
	if base == "." || !safeUploadBaseRe.MatchString(base) {
		return "", errcode.Newf(errcode.InvalidParam, "文件名 %q 非法（仅允许字母数字 . _ -，不含路径分隔符）", headerName)
	}
	lower := strings.ToLower(base)
	switch appType {
	case model.CIBuilderJava:
		if !strings.HasSuffix(lower, ".jar") {
			return "", errcode.Newf(errcode.InvalidParam, "Java 应用请上传可执行 fat jar（当前文件: %s；Spring Boot 需 mvn package 的最终产物，非 classes 包）", base)
		}
		return storedJava, nil
	case model.CIBuilderWeb:
		if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") && !strings.HasSuffix(lower, ".zip") {
			return "", errcode.Newf(errcode.InvalidParam, "前端应用请上传静态资源压缩包（tar -czf web.tar.gz -C dist . 或直接选 zip，平台自动归一；当前文件: %s）", base)
		}
		return storedWeb, nil
	default: // go
		for _, ext := range []string{".jar", ".war", ".zip", ".tar.gz", ".tgz"} {
			if strings.HasSuffix(lower, ext) {
				return "", errcode.Newf(errcode.InvalidParam, "Go 应用请上传编译好的二进制（当前文件带 %s 后缀，像是其它形态的产物）", ext)
			}
		}
		return storedGo, nil
	}
}

func (s *Service) artifactDir(id uint) string {
	return filepath.Join(s.uploadDir, fmt.Sprintf("b%d", id))
}

// ArtifactPath 制品落盘绝对路径（handler 下载/删除用）。
func (s *Service) ArtifactPath(b *model.CIImageBuilder) string {
	return filepath.Join(s.artifactDir(b.ID), StoredArtifactName(b.AppType))
}

// UploadArtifact 流式落盘 + sha256 + 原子替换 + 令牌轮转 + 刷新托管版本。
// headerName 取 multipart 原始文件名（会被净化）。
func (s *Service) UploadArtifact(ctx context.Context, id uint, headerName string, r io.Reader) (*model.CIImageBuilder, error) {
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	stored, err := artifactStoredName(b.AppType, headerName)
	if err != nil {
		return nil, err
	}
	dir := s.artifactDir(b.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, ".incoming-"+stored+"-"+time.Now().Format("20060102150405.000"))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp) // 成功后 rename 已不在；失败路径清临时文件
	// 多读 1 字节判断超限（不信任 Content-Length）
	lr := io.LimitReader(r, s.maxBytes+1)
	// sha 边写边算（tee），大文件不把内存拖一遍
	hashw, n, err := teeHash(f, lr)
	if err != nil {
		f.Close()
		return nil, err
	}
	if n > s.maxBytes {
		f.Close()
		return nil, errcode.Newf(errcode.InvalidParam, "制品超过大小上限 %d MB（实际 >%d MB），可调整配置 ci.builderUploadMaxMB",
			s.maxBytes/1024/1024, s.maxBytes/1024/1024)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// zip 在平台侧归一成 tar.gz：构建端解包只依赖任务镜像里的 tar（curl:8.5.0
	// 不一定带 unzip），sha256 校验口径也统一为归一后的制品；zip-slip 在归一时拦掉
	if isZipName(headerName) {
		tarTmp := tmp + ".targz"
		defer os.Remove(tarTmp)
		zh, zn, err := s.normalizeZipToTargz(tmp, tarTmp)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(tarTmp, filepath.Join(dir, stored)); err != nil {
			return nil, err
		}
		hashw, n = zh, zn
		tmp = "" // 已改名，收尾只清原始 zip
	}
	final := filepath.Join(dir, stored)
	if tmp != "" {
		if err := os.Rename(tmp, final); err != nil {
			return nil, err
		}
	}
	if _, err := s.rotateToken(ctx, b.ID); err != nil {
		return nil, err
	}
	now := time.Now()
	b.FileName = headerName
	b.FileSize = n
	b.FileSHA256 = hashw
	b.UploadedAt = &now
	if err := s.db.WithContext(ctx).Model(b).Updates(map[string]interface{}{
		"file_name": b.FileName, "file_size": b.FileSize,
		"file_sha256": b.FileSHA256, "uploaded_at": b.UploadedAt,
	}).Error; err != nil {
		return nil, err
	}
	// 立即生成托管版本：校验/编译问题当场报出（不带坏图去跑），用户传完即知能不能构建
	if _, err := s.EnsureVersion(ctx, b, s.tagFor(b, "")); err != nil {
		return nil, err
	}
	return b, nil
}

func teeHash(w io.Writer, r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func isZipName(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.ReplaceAll(name, "\\", "/")), ".zip")
}

// normalizeZipToTargz 把上传的 zip 静态包重打包成 tar.gz（gzip 压缩的 ustar），
// 供 artifact-fetch 节点用 busybox tar 解包。三道防护：
//   - zip-slip：条目名先反斜杠归一、再 path.Clean("/"+name) 剥掉 ../ 与前导 /
//   - 解压炸弹：解包总字节受 builderUploadMaxMB 约束（超限明确报错）
//   - 完整性：条目实际字节与 zip 头声明不一致（截断/伪造头）或整包为空 → 报错，
//     不悄悄丢弃，避免打出空站点镜像还"构建成功"
//
// 返回归一后 tar.gz 的 sha256 与大小。
func (s *Service) normalizeZipToTargz(zipPath, outPath string) (string, int64, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", 0, errcode.Newf(errcode.InvalidParam, "zip 解析失败（文件损坏或非 zip 格式）: %v", err)
	}
	defer zr.Close()

	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", 0, err
	}
	closed := false
	defer func() {
		if !closed {
			out.Close()
		}
	}()
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	var total, count int64
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		name := path.Clean("/" + strings.ReplaceAll(zf.Name, "\\", "/"))[1:]
		if name == "" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, errcode.Newf(errcode.InvalidParam, "zip 条目 %s 读取失败: %v", zf.Name, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(zf.UncompressedSize64), Typeflag: tar.TypeReg,
		}); err != nil {
			rc.Close()
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, err
		}
		// 先按 zip 头声明的解压大小截断（tar 条目 Size 必须与实际写入字节一致，
		// 否则多余字节会打烂 tar 流），再叠加总预算限（防爆）
		declared := int64(zf.UncompressedSize64)
		if declared < 0 {
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, errcode.Newf(errcode.InvalidParam, "zip 条目 %s 声明大小非法", name)
		}
		budget := s.maxBytes + 1 - total
		n, err := io.Copy(tw, io.LimitReader(io.LimitReader(rc, declared), budget))
		rc.Close()
		if err != nil {
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, errcode.Newf(errcode.InvalidParam, "zip 条目 %s 解包失败: %v", name, err)
		}
		total += n
		if total > s.maxBytes {
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, errcode.Newf(errcode.InvalidParam, "zip 解包后超过大小上限 %d MB（疑似异常压缩包），可调整配置 ci.builderUploadMaxMB",
				s.maxBytes/1024/1024)
		}
		if n != declared {
			tw.Close()
			gw.Close()
			out.Close()
			closed = true
			return "", 0, errcode.Newf(errcode.InvalidParam, "zip 条目 %s 实际大小与声明不符（包被截断或损坏）", name)
		}
		count++
	}
	if count == 0 {
		tw.Close()
		gw.Close()
		out.Close()
		closed = true
		return "", 0, errcode.New(errcode.InvalidParam, "zip 包里没有任何文件")
	}
	if err := tw.Close(); err != nil {
		out.Close()
		closed = true
		return "", 0, err
	}
	if err := gw.Close(); err != nil {
		out.Close()
		closed = true
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		closed = true
		return "", 0, err
	}
	closed = true
	f, err := os.Open(outPath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return teeHash(io.Discard, f)
}

// ============ 下载令牌 ============

func newTokenString() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// rotateToken 新令牌置顶，只保留最近 tokenRetention 个。
func (s *Service) rotateToken(ctx context.Context, builderID uint) (string, error) {
	tok, err := newTokenString()
	if err != nil {
		return "", err
	}
	if err := s.db.WithContext(ctx).Create(&model.CIBuilderToken{Token: tok, BuilderID: builderID}).Error; err != nil {
		return "", err
	}
	var olds []model.CIBuilderToken
	if err := s.db.WithContext(ctx).Where("builder_id = ?", builderID).
		Order("created_at desc, token").Limit(100).Find(&olds).Error; err == nil {
		if len(olds) > tokenRetention {
			cut := olds[tokenRetention].Token
			s.db.WithContext(ctx).Where("builder_id = ? AND (created_at < ? OR token = ?)",
				builderID, olds[tokenRetention].CreatedAt, cut).Delete(&model.CIBuilderToken{})
		}
	}
	return tok, nil
}

// currentToken 取最近一次轮转的令牌（版本图用它——刚轮转的令牌必然有效）。
func (s *Service) currentToken(ctx context.Context, builderID uint) (string, error) {
	var t model.CIBuilderToken
	if err := s.db.WithContext(ctx).Where("builder_id = ?", builderID).
		Order("created_at desc, token").First(&t).Error; err != nil {
		return "", errcode.New(errcode.Internal, "构建器下载令牌缺失（上传时生成）")
	}
	return t.Token, nil
}

// LookupToken 制品下载鉴权（公开路由按 token 找构建器）。
func (s *Service) LookupToken(ctx context.Context, token string) (*model.CIImageBuilder, error) {
	var t model.CIBuilderToken
	if err := s.db.WithContext(ctx).First(&t, "token = ?", token).Error; err != nil {
		return nil, errcode.New(errcode.NotFound, "制品链接无效或已过期")
	}
	return s.get(ctx, t.BuilderID)
}

// ============ 版本与运行 ============

// EnsureVersion 按当前配置+制品+tag 生成托管流水线 DSL；与最新版本一致就复用，
// 不一致才 SaveVersion（版本号只随真实变更增长）。
func (s *Service) EnsureVersion(ctx context.Context, b *model.CIImageBuilder, tag string) (*model.CIPipelineVersion, error) {
	if b.FileName == "" || b.FileSHA256 == "" {
		return nil, errcode.New(errcode.InvalidParam, "请先上传构建产物")
	}
	token, err := s.currentToken(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	g, err := BuildGraph(GraphInput{
		PipelineName:  pipelineNameFor(b.Name),
		Builder:       b,
		Tag:           tag,
		Token:         token,
		ServerURL:     s.cfg.ServerURL,
		ImageRegistry: s.cfg.ImageRegistry,
	})
	if err != nil {
		return nil, errcode.New(errcode.InvalidParam, err.Error())
	}
	graphJSON, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	if latest, err := s.store.GetLatest(ctx, b.PipelineID); err == nil && latest.GraphJSON == string(graphJSON) {
		return latest, nil
	}
	v, err := s.store.SaveVersion(ctx, b.PipelineID, b.CreatedBy, g)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Run 一次构建：确保版本与当前制品/配置一致 → StartRun（托管流水线，
// 同构建器串行排队、日志与制品登记走流水线既有 UI）。
func (s *Service) Run(ctx context.Context, id uint, uid uint, username, tag string) (*model.CIRun, error) {
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.FileName == "" {
		return nil, errcode.New(errcode.InvalidParam, "请先上传构建产物")
	}
	if strings.TrimSpace(s.cfg.ServerURL) == "" {
		return nil, errcode.New(errcode.DepUnavailable,
			"未配置 ci.serverUrl（任务 Pod 回连平台后端的集群内 Service 地址，如 http://kube-console-server.kube-console:8080，非浏览器访问地址），镜像构建器无法运行")
	}
	if _, err := os.Stat(s.ArtifactPath(b)); err != nil {
		return nil, errcode.New(errcode.InvalidParam, "制品文件已丢失（平台数据目录被清理？），请重新上传")
	}
	tag = s.tagFor(b, tag)
	if err := ValidateImageFields(b.Registry, b.Repository, tag); err != nil {
		return nil, errcode.New(errcode.InvalidParam, err.Error())
	}
	v, err := s.EnsureVersion(ctx, b, tag)
	if err != nil {
		return nil, err
	}
	run, err := s.rt.StartRun(ctx, b.PipelineID, v.ID, uid, username, "manual", "", "")
	if err != nil {
		return nil, err
	}
	return run, nil
}

// tagFor 运行 tag 优先级：本次输入 > 构建器默认 > upload-<北京时间戳>。
func (s *Service) tagFor(b *model.CIImageBuilder, override string) string {
	if t := strings.TrimSpace(override); t != "" {
		return t
	}
	if t := strings.TrimSpace(b.DefaultTag); t != "" {
		return t
	}
	return s.autoTag()
}

func (s *Service) autoTag() string {
	// 与 git-clone build_tag 同款北京时间口径，不带分支信息（上传构建无 git 上下文）
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return "upload-" + time.Now().In(loc).Format("20060102150405")
}

// Preview 当前配置+制品的编译产物预览（不落版本；排障看约定 Dockerfile 与 Tekton YAML）。
func (s *Service) Preview(ctx context.Context, id uint) (string, error) {
	b, err := s.get(ctx, id)
	if err != nil {
		return "", err
	}
	token, err := s.currentToken(ctx, b.ID)
	if err != nil {
		token = "REPLACE_AFTER_UPLOAD"
	}
	g, err := BuildGraph(GraphInput{
		PipelineName:  pipelineNameFor(b.Name),
		Builder:       b,
		Tag:           s.tagFor(b, ""),
		Token:         token,
		ServerURL:     s.cfg.ServerURL,
		ImageRegistry: s.cfg.ImageRegistry,
	})
	if err != nil {
		// 尚未上传时 Preview 仍想给 Dockerfile 形态：BuildGraph 会因缺制品报错，原样透出
		return "", err
	}
	spec, err := s.pipe.Compile(g)
	if err != nil {
		return "", err
	}
	yamlText, err := compiler.ToYAML(spec)
	if err != nil {
		return "", err
	}
	return yamlText, nil
}
