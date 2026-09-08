// Package deploy 发布管理：部署记录 + 一键回滚。
//
// 记录：k8s-deploy（set image 模式）/ helm-deploy 任务成功时由 runtime 调用
// RecordDeployment，按「部署后的镜像快照」落一条记录（发布历史）。
// 回滚：按快照把目标 workload 的容器镜像恢复为记录值（镜像维度回滚，
// 对 helm 发布同样适用；局限：只恢复镜像，不回滚 chart 结构/values 变更）。
package deploy

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/model"
)

type Service struct {
	db    *gorm.DB
	k8sFor func(cluster string) (*tekton.Client, error)
}

func NewService(db *gorm.DB, k8sFor func(cluster string) (*tekton.Client, error)) *Service {
	return &Service{db: db, k8sFor: k8sFor}
}

// RecordDeployment 部署任务成功后生成发布记录（best-effort：失败只记日志，绝不影响执行链路）。
func (s *Service) RecordDeployment(ctx context.Context, run *model.CIRun, cluster string, nodeID, nodeType string, params map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("deploy: run %d 记录发布 panic: %v", run.ID, r)
		}
	}()
	str := func(k string) string {
		v, _ := params[k].(string)
		return strings.TrimSpace(v)
	}
	var projectID uint
	if err := s.db.WithContext(ctx).Model(&model.CIPipeline{}).
		Select("project_id").Where("id = ?", run.PipelineID).Scan(&projectID).Error; err != nil || projectID == 0 {
		return
	}
	k8s, err := s.k8sFor(cluster)
	if err != nil {
		return
	}
	ns := str("namespace")
	if ns == "" {
		ns = "default"
	}
	var (
		targets model.DeployTargetList
		release string
	)
	switch nodeType {
	case "k8s-deploy":
		name := str("deployTarget")
		if name == "" {
			log.Printf("deploy: run %d 节点 %s 为 apply 模式，跳过发布记录", run.ID, nodeID)
			return
		}
		images, err := k8s.GetDeploymentImages(ctx, ns, name)
		if err != nil {
			log.Printf("deploy: run %d 快照 deployment %s/%s 失败: %v", run.ID, ns, name, err)
			return
		}
		targets = model.DeployTargetList{{Kind: "deployment", Name: name, Images: images}}
	case "helm-deploy":
		release = str("release")
		if release == "" {
			return
		}
		names, err := k8s.ListDeploymentsByLabel(ctx, ns, "app.kubernetes.io/instance="+release)
		if err != nil || len(names) == 0 {
			log.Printf("deploy: run %d 快照 release %s/%s 失败或无 workload: %v", run.ID, ns, release, err)
			return
		}
		for _, name := range names {
			images, err := k8s.GetDeploymentImages(ctx, ns, name)
			if err != nil {
				log.Printf("deploy: run %d 快照 deployment %s/%s 失败: %v", run.ID, ns, name, err)
				continue
			}
			targets = append(targets, model.DeployTarget{Kind: "deployment", Name: name, Images: images})
		}
		if len(targets) == 0 {
			return
		}
	default:
		return
	}

	rec := &model.CIDeployment{
		ClusterName:  cluster,
		ProjectID:    projectID,
		PipelineID:   run.PipelineID,
		RunID:        run.ID,
		NodeName:     nodeID,
		Kind:         nodeType,
		Namespace:    ns,
		Release:      release,
		Targets:      targets,
		Status:       model.CIRunStatusSuccess,
		GitCommit:    run.GitCommit,
		GitBranch:    run.GitBranch,
		CreatedBy:    run.StartedBy,
	}
	if err := s.db.WithContext(ctx).Create(rec).Error; err != nil {
		log.Printf("deploy: run %d 写发布记录失败: %v", run.ID, err)
		return
	}
	log.Printf("deploy: run %d 发布已记录 #%d（%s/%s, %d workload）", run.ID, rec.ID, ns, nodeType, len(targets))
}

// List 发布记录列表（集群内；projectID 过滤可选）。
func (s *Service) List(ctx context.Context, cluster string, projectID *uint, page, size int) ([]model.CIDeployment, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := s.db.WithContext(ctx).Model(&model.CIDeployment{}).Where("cluster_name = ?", cluster)
	if projectID != nil {
		q = q.Where("project_id = ?", *projectID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.CIDeployment
	err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&out).Error
	return out, total, err
}

// Rollback 一键回滚：按记录的镜像快照恢复各 workload 的容器镜像，
// 并产生一条 Kind=rollback 的记录（追溯「谁在何时回滚过」）。
func (s *Service) Rollback(ctx context.Context, cluster string, id uint, username string) (*model.CIDeployment, error) {
	var rec model.CIDeployment
	if err := s.db.WithContext(ctx).First(&rec, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "发布记录不存在")
		}
		return nil, err
	}
	if rec.ClusterName != cluster {
		return nil, errcode.New(errcode.NotFound, "发布记录不存在（不属于当前集群）")
	}
	if rec.Kind == "rollback" {
		return nil, errcode.New(errcode.InvalidParam, "回滚记录不能再回滚，请选择原始发布记录")
	}
	if len(rec.Targets) == 0 {
		return nil, errcode.New(errcode.InvalidParam, "该记录没有镜像快照，无法回滚")
	}
	k8s, err := s.k8sFor(cluster)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}

	var errs []string
	for _, t := range rec.Targets {
		if t.Kind != "deployment" {
			errs = append(errs, fmt.Sprintf("%s/%s: 暂不支持该 workload 类型的回滚", t.Kind, t.Name))
			continue
		}
		if _, err := k8s.SetDeploymentImages(ctx, rec.Namespace, t.Name, t.Images); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", t.Kind, t.Name, err))
		}
	}
	status := model.CIRunStatusSuccess
	if len(errs) > 0 {
		status = model.CIRunStatusFailed
	}
	out := &model.CIDeployment{
		ClusterName:  cluster,
		ProjectID:    rec.ProjectID,
		PipelineID:   rec.PipelineID,
		NodeName:     rec.NodeName,
		Kind:         "rollback",
		Namespace:    rec.Namespace,
		Release:      rec.Release,
		Targets:      rec.Targets,
		Status:       status,
		RolledBackFrom: &rec.ID,
		CreatedBy:    username,
	}
	if err := s.db.WithContext(ctx).Create(out).Error; err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return out, errcode.Newf(errcode.DepUnavailable, "回滚部分失败: %s", strings.Join(errs, "; "))
	}
	log.Printf("deploy: 发布 #%d 已回滚（namespace=%s, %d workload）", rec.ID, rec.Namespace, len(rec.Targets))
	return out, nil
}
