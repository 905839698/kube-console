// service.go 制品管理：列表 / 详情（含血缘）/ 下载代理。
package artifact

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

// Service 制品管理。
type Service struct {
	db    *gorm.DB
	nexus *NexusClient
	minio *MinIOClient
	reg   *Registrar
}

func NewService(db *gorm.DB, cfg *config.CIConfig) *Service {
	s := &Service{db: db, nexus: NewNexusClient(cfg), reg: NewRegistrar(db)}
	if m, err := NewMinIOClient(cfg); err == nil {
		s.minio = m
	}
	return s
}

// Registrar 暴露注册器（runtime 任务成功时登记制品）。
func (s *Service) Registrar() *Registrar { return s.reg }

// List 制品列表（query: projectId/pipelineRunId/type/page/size），集群内。
func (s *Service) List(ctx context.Context, cluster string, projectID, runID *uint, artType string, page, size int) ([]model.CIArtifact, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	q := s.db.WithContext(ctx).Model(&model.CIArtifact{}).Where("cluster_name = ?", cluster)
	if projectID != nil {
		q = q.Where("project_id = ?", *projectID)
	}
	if runID != nil {
		q = q.Where("pipeline_run_id = ?", *runID)
	}
	if artType != "" {
		q = q.Where("type = ?", artType)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.CIArtifact
	err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&out).Error
	return out, total, err
}

// Detail 制品详情 + 血缘（commit/branch/run/构建人/流水线）。
type Detail struct {
	model.CIArtifact
	PipelineName string `json:"pipelineName"`
	GitCommit    string `json:"gitCommit"`
	GitBranch    string `json:"gitBranch"`
	RunNo        int    `json:"runNo"`
	StartedBy    string `json:"startedBy"`
	RunStartedAt string `json:"runStartedAt"`
	PullCommand  string `json:"pullCommand,omitempty"` // 镜像类
}

func (s *Service) Get(ctx context.Context, cluster string, id uint) (*Detail, error) {
	var a model.CIArtifact
	if err := s.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "制品不存在")
		}
		return nil, err
	}
	if a.ClusterName != cluster {
		return nil, errcode.New(errcode.NotFound, "制品不存在（不属于当前集群）")
	}
	d := &Detail{CIArtifact: a}
	if a.PipelineRunID > 0 {
		var run model.CIRun
		if err := s.db.WithContext(ctx).First(&run, a.PipelineRunID).Error; err == nil {
			d.GitCommit = run.GitCommit
			d.GitBranch = run.GitBranch
			d.RunNo = run.RunNo
			d.StartedBy = run.StartedBy
			if run.StartedAt != nil {
				d.RunStartedAt = run.StartedAt.Format("2006-01-02 15:04:05")
			}
			var p model.CIPipeline
			if err := s.db.WithContext(ctx).First(&p, run.PipelineID).Error; err == nil {
				d.PipelineName = p.Name
			}
		}
	}
	if a.Type == model.CIArtImage {
		d.PullCommand = "docker pull " + a.StoragePath
	}
	return d, nil
}

// DownloadMeta 下载前的元信息；镜像类返回错误（应走 pull 而非下载）。
func (s *Service) DownloadMeta(ctx context.Context, cluster string, id uint) (contentType, disposition string, err error) {
	var a model.CIArtifact
	if err := s.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", "", errcode.New(errcode.NotFound, "制品不存在")
		}
		return "", "", err
	}
	if a.ClusterName != cluster {
		return "", "", errcode.New(errcode.NotFound, "制品不存在（不属于当前集群）")
	}
	if a.StorageType == model.CIStoHarbor {
		return "", "", errcode.Newf(errcode.InvalidParam, "容器镜像请拉取: docker pull %s", a.StoragePath)
	}
	if a.StorageType == model.CIStoMinIO && s.minio == nil {
		return "", "", errcode.New(errcode.DepUnavailable, "MinIO 未配置")
	}
	name := a.Name
	if name == "" {
		name = fmt.Sprintf("artifact-%d", a.ID)
	}
	// name 来自流水线 results（用户可控），含引号会破坏响应头，先转义
	name = strings.NewReplacer(`"`, `'`, "\r", "", "\n", "").Replace(name)
	return "application/octet-stream", "attachment; filename=\"" + name + "\"", nil
}

// DownloadStream 从存储后端流式代理下载到 w（调用前先取 DownloadMeta 设好响应头）。
func (s *Service) DownloadStream(ctx context.Context, cluster string, id uint, w io.Writer) error {
	var a model.CIArtifact
	if err := s.db.WithContext(ctx).First(&a, id).Error; err != nil {
		return errcode.New(errcode.NotFound, "制品不存在")
	}
	if a.ClusterName != cluster {
		return errcode.New(errcode.NotFound, "制品不存在（不属于当前集群）")
	}
	switch a.StorageType {
	case model.CIStoMinIO:
		if s.minio == nil {
			return errcode.New(errcode.DepUnavailable, "MinIO 未配置")
		}
		return s.minio.Download(ctx, a.StoragePath, w)
	case model.CIStoNexus:
		return s.nexus.Download(ctx, a.StoragePath, w)
	default:
		return errcode.Newf(errcode.InvalidParam, "不支持下载的存储类型: %s", a.StorageType)
	}
}
