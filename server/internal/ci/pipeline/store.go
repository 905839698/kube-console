package pipeline

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"kube-console/server/internal/model"
	"kube-console/server/internal/ci/pipeline/compiler"
	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/errcode"
)

// Store 流水线的持久化与版本管理（GORM）。
//
// 版本化策略：每次保存生成新 pipeline_versions 记录，
// 同时存 graph_json（DSL，可再编辑）与 compiled_yaml（Tekton 产物，可审计）。
type Store struct {
	db  *gorm.DB
	Svc *Service // 复用校验/编译
}

func NewStore(db *gorm.DB, svc *Service) *Store {
	return &Store{db: db, Svc: svc}
}

// ============ 请求结构 ============

type CreateReq struct {
	ClusterName string `json:"-"` // 由 handler 从 X-Cluster 注入（集群隔离）
	ProjectID   uint   `json:"projectId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type UpdateReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// GraphBody 前端提交 DSL 的统一 body。
type GraphBody struct {
	Graph *dsl.Graph `json:"graphJson" binding:"required"`
}

// ============ 流水线 CRUD ============

func (s *Store) Create(ctx context.Context, uid uint, req CreateReq) (*model.CIPipeline, error) {
	// 项目内名称唯一是设计约束，但索引非唯一，需显式查（复制同名/撞名时给出可读错误）
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&model.CIPipeline{}).
		Where("project_id = ? AND name = ?", req.ProjectID, req.Name).Count(&cnt).Error; err != nil {
		return nil, err
	}
	if cnt > 0 {
		return nil, errcode.Newf(errcode.Conflict, "流水线名已存在: %s", req.Name)
	}
	p := &model.CIPipeline{
		ClusterName: req.ClusterName,
		ProjectID:   req.ProjectID,
		Name:        req.Name,
		Description: req.Description,
		CreatedBy:   uid,
	}
	if err := s.db.WithContext(ctx).Create(p).Error; err != nil {
		if isUniqueErr(err) {
			return nil, errcode.Newf(errcode.Conflict, "流水线名已存在: %s", req.Name)
		}
		return nil, err
	}
	return p, nil
}

func (s *Store) List(ctx context.Context, projectID *uint) ([]model.CIPipeline, error) {
	q := s.db.WithContext(ctx).Order("id desc")
	if projectID != nil {
		q = q.Where("project_id = ?", *projectID)
	}
	var ps []model.CIPipeline
	err := q.Find(&ps).Error
	return ps, err
}

func (s *Store) Get(ctx context.Context, id uint) (*model.CIPipeline, error) {
	var p model.CIPipeline
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "流水线不存在")
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) Update(ctx context.Context, id uint, req UpdateReq) (*model.CIPipeline, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if req.Name != nil && *req.Name != "" {
		// 索引非唯一，改名撞项目内已有名称需显式拦截（否则静默产生同名流水线）
		if *req.Name != p.Name {
			var cnt int64
			if err := s.db.WithContext(ctx).Model(&model.CIPipeline{}).
				Where("project_id = ? AND name = ?", p.ProjectID, *req.Name).Count(&cnt).Error; err != nil {
				return nil, err
			}
			if cnt > 0 {
				return nil, errcode.Newf(errcode.Conflict, "流水线名已存在: %s", *req.Name)
			}
		}
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(p).Updates(updates).Error; err != nil {
			if isUniqueErr(err) {
				return nil, errcode.New(errcode.Conflict, "流水线名已存在")
			}
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id uint) error {
	if err := s.db.WithContext(ctx).Delete(&model.CIPipeline{}, id).Error; err != nil {
		return err
	}
	// 级联清理版本
	return s.db.WithContext(ctx).Where("pipeline_id = ?", id).Delete(&model.CIPipelineVersion{}).Error
}

// ============ 版本 ============

// SaveVersion 校验 → 编译 → 落库（graph_json + compiled_yaml），版本号自增。
func (s *Store) SaveVersion(ctx context.Context, pipelineID, uid uint, g *dsl.Graph) (*model.CIPipelineVersion, error) {
	if _, err := s.Get(ctx, pipelineID); err != nil {
		return nil, err
	}

	// 校验（失败则不落版本）
	res := s.Svc.Validator.Validate(g)
	if !res.Valid {
		return nil, &CompileError{Errors: res.Errors}
	}

	// 编译（存产物；失败同样不落版本）
	spec, err := s.Svc.Compiler.Compile(g, s.Svc.Namespace)
	if err != nil {
		return nil, err
	}
	yamlText, err := compiler.ToYAML(spec)
	if err != nil {
		return nil, err
	}

	// 版本号 = 当前最大 + 1
	var maxVer int
	_ = s.db.WithContext(ctx).Model(&model.CIPipelineVersion{}).
		Where("pipeline_id = ?", pipelineID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVer).Error

	graphJSON, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	v := &model.CIPipelineVersion{
		PipelineID:   pipelineID,
		Version:      maxVer + 1,
		GraphJSON:    string(graphJSON),
		CompiledYAML: yamlText,
		CreatedBy:    uid,
	}
	if err := s.db.WithContext(ctx).Create(v).Error; err != nil {
		// 并发保存同一流水线：两事务读到同一 MAX(version)，后者撞 (pipeline_id,version)
		// 唯一键。重读 max 重试一次（与 nextRunNo 同一模式的兜底）
		if isUniqueErr(err) {
			var retryMax int
			if serr := s.db.WithContext(ctx).Model(&model.CIPipelineVersion{}).
				Where("pipeline_id = ?", pipelineID).
				Select("COALESCE(MAX(version), 0)").Scan(&retryMax).Error; serr == nil && retryMax >= maxVer {
				v.Version = retryMax + 1
				if serr2 := s.db.WithContext(ctx).Create(v).Error; serr2 != nil {
					return nil, serr2
				}
				return v, nil
			}
		}
		return nil, err
	}
	return v, nil
}

func (s *Store) ListVersions(ctx context.Context, pipelineID uint) ([]model.CIPipelineVersion, error) {
	var vs []model.CIPipelineVersion
	err := s.db.WithContext(ctx).Where("pipeline_id = ?", pipelineID).
		Order("version desc").Find(&vs).Error
	return vs, err
}

// GetLatest 取最新版本（含 graph_json，供 Designer 回显）。
func (s *Store) GetLatest(ctx context.Context, pipelineID uint) (*model.CIPipelineVersion, error) {
	var v model.CIPipelineVersion
	err := s.db.WithContext(ctx).Where("pipeline_id = ?", pipelineID).
		Order("version desc").First(&v).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "流水线尚无已保存版本")
		}
		return nil, err
	}
	return &v, nil
}

// ParseGraph 把 graph_json 解析回 DSL（Designer 回显用）。
func ParseGraph(graphJSON string) (*dsl.Graph, error) {
	var g dsl.Graph
	if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
		return nil, errcode.New(errcode.Internal, "graph_json 解析失败: "+err.Error())
	}
	return &g, nil
}

// isUniqueErr 唯一约束冲突判断（Postgres SQLSTATE 23505，见 errcode.IsUniqueViolation）。
func isUniqueErr(err error) bool {
	return errcode.IsUniqueViolation(err)
}
