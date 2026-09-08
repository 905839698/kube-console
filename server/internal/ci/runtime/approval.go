package runtime

import (
	"context"

	"kube-console/server/internal/model"
	"kube-console/server/internal/ci/errcode"
)

// ApprovalAnnotationKey 人工审批注解键（approval task 轮询它）。
const ApprovalAnnotationKey = "ci-platform.io/approval"

// 审批决定。
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

// ApprovalInfo 一个待审批/已审批节点。
type ApprovalInfo struct {
	NodeID    string `json:"nodeId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Decision  string `json:"decision"` // "" / approved / rejected
	TektonRun string `json:"tektonRun"`
}

// ListApprovals 返回某执行中所有 approval 节点及其审批状态。
func (s *Service) ListApprovals(ctx context.Context, runID uint) ([]ApprovalInfo, error) {
	var run model.CIRun
	if err := s.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return nil, errcode.New(errcode.NotFound, "执行记录不存在")
	}
	k8s, meta, err := s.k8sForRun(ctx, &run)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	var trs []model.CITaskRun
	if err := s.db.WithContext(ctx).
		Where("run_id = ? AND node_type = ?", runID, "approval").
		Order("id asc").Find(&trs).Error; err != nil {
		return nil, err
	}
	out := make([]ApprovalInfo, 0, len(trs))
	for _, tr := range trs {
		info := ApprovalInfo{NodeID: tr.NodeID, Name: tr.Name, Status: tr.Status, TektonRun: tr.TektonTaskRun}
		if tr.TektonTaskRun != "" {
			if d, err := k8s.GetTaskRunAnnotation(ctx, meta.NS, tr.TektonTaskRun, ApprovalAnnotationKey); err == nil {
				info.Decision = d
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Decide 批准/驳回某审批节点：给对应 TaskRun 打注解（approval task 轮询到后继续/失败）。
func (s *Service) Decide(ctx context.Context, runID uint, nodeID, decision string) error {
	if decision != DecisionApproved && decision != DecisionRejected {
		return errcode.New(errcode.InvalidParam, "decision 必须是 approved 或 rejected")
	}
	var tr model.CITaskRun
	var run model.CIRun
	if err := s.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return errcode.New(errcode.NotFound, "执行记录不存在")
	}
	k8s, meta, err := s.k8sForRun(ctx, &run)
	if err != nil {
		return errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	err = s.db.WithContext(ctx).
		Where("run_id = ? AND node_id = ? AND node_type = ?", runID, nodeID, "approval").
		First(&tr).Error
	if err != nil {
		return errcode.New(errcode.NotFound, "未找到该审批节点")
	}
	if tr.TektonTaskRun == "" {
		return errcode.New(errcode.InvalidParam, "审批任务尚未在集群创建（可能未调度到）")
	}
	if err := k8s.AnnotateTaskRun(ctx, meta.NS, tr.TektonTaskRun, ApprovalAnnotationKey, decision); err != nil {
		return errcode.Newf(errcode.DepUnavailable, "写入审批注解失败: %v", err)
	}
	return nil
}
