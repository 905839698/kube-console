package runtime

import (
	"context"
	"fmt"
	"log"
	"time"

	"kube-console/server/internal/model"
)

// reconcileInterval 孤儿资源对账间隔。
const reconcileInterval = 10 * time.Minute

// StartReconciler 启动时对账一次，之后周期性运行。
// 清理「集群有、DB 无」的资源，兜底以下场景：
//   - StartRun 在「写库成功 → 删 PVC 前」崩溃留下的 PVC
//   - 人工 kubectl 误删 DB 记录后遗留的 PipelineRun/PVC
//   - 保存新版本后遗留的旧版本 Pipeline CR（pl-{id}-v{n}）
//   - preserveLogs 保留的终态 TaskRun Pod
//
// 多集群：对每个（集群 × 该项目集群下的项目 ns）分别对账；集群不可达时跳过。
func (s *Service) StartReconciler(ctx context.Context) {
	s.reconcileOrphans(ctx)
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOrphans(ctx)
		}
	}
}

func (s *Service) reconcileOrphans(ctx context.Context) {
	var runs []model.CIRun
	if err := s.db.WithContext(ctx).
		Select("id", "cluster_name", "pipeline_id", "tekton_run_name", "pvc_name", "status", "started_at", "finished_at", "created_at").
		Find(&runs).Error; err != nil {
		log.Printf("reconcile: 读 runs 失败: %v", err)
		return
	}
	dbRuns := map[string]model.CIRun{}
	dbPVCs := map[string]bool{}
	byClusterNS := map[string]map[string]bool{} // "cluster/ns" -> 该 ns 有运行的标记
	for i := range runs {
		dbRuns[runs[i].TektonRunName] = runs[i]
		if runs[i].PVCName != "" {
			dbPVCs[runs[i].PVCName] = true
		}
	}

	// 项目落点集合：cluster -> {ns: true}（只对自己创建过的项目 ns 做对账，
	// 避免误删同集群上其他系统的 Tekton 资源）
	var projs []model.CIProject
	_ = s.db.WithContext(ctx).Find(&projs).Error
	nsByCluster := map[string]map[string]bool{}
	for _, p := range projs {
		if nsByCluster[p.ClusterName] == nil {
			nsByCluster[p.ClusterName] = map[string]bool{}
		}
		if p.Namespace != "" {
			nsByCluster[p.ClusterName][p.Namespace] = true
		}
	}

	// 终态 Pod 保留时长：preserveLogs 让结束后仍可回看日志，超期才回收
	const podRetainAfterTerminal = 24 * time.Hour
	now := time.Now()

	for cluster, nsSet := range nsByCluster {
		k8s, err := s.k8sFor(cluster)
		if err != nil {
			log.Printf("reconcile: 集群 %s 不可用，跳过: %v", cluster, err)
			continue
		}
		for ns := range nsSet {
			s.reconcileNS(ctx, k8s, ns, runs, dbRuns, dbPVCs, podRetainAfterTerminal, now)
		}
	}
	_ = byClusterNS
}

func (s *Service) reconcileNS(ctx context.Context, k8s interface {
	ListPipelineRuns(ctx context.Context, namespace string) ([]string, error)
	DeletePipelineRun(ctx context.Context, namespace, name string) error
	ListPVCs(ctx context.Context, namespace, prefix string) ([]string, error)
	DeletePVC(ctx context.Context, namespace, name string) error
	ListPipelines(ctx context.Context, namespace string) ([]string, error)
	DeletePipeline(ctx context.Context, namespace, name string) error
	ListTaskRunPods(ctx context.Context, namespace string) (map[string]string, error)
	DeletePod(ctx context.Context, namespace, name string) error
}, ns string, runs []model.CIRun, dbRuns map[string]model.CIRun, dbPVCs map[string]bool, podRetainAfterTerminal time.Duration, now time.Time) {
	// 1) 孤儿 PipelineRun
	if prs, err := k8s.ListPipelineRuns(ctx, ns); err == nil {
		for _, name := range prs {
			if _, ok := dbRuns[name]; !ok {
				if err := k8s.DeletePipelineRun(ctx, ns, name); err != nil {
					log.Printf("reconcile: 删孤儿 PipelineRun %s: %v", name, err)
				} else {
					log.Printf("reconcile: 清理孤儿 PipelineRun %s", name)
				}
			}
		}
	}

	// 2) 孤儿 workspace PVC
	if pvcs, err := k8s.ListPVCs(ctx, ns, "ci-ws-"); err == nil {
		for _, name := range pvcs {
			if !dbPVCs[name] {
				if err := k8s.DeletePVC(ctx, ns, name); err != nil {
					log.Printf("reconcile: 删孤儿 PVC %s: %v", name, err)
				} else {
					log.Printf("reconcile: 清理孤儿 PVC %s", name)
				}
			}
		}
	}

	// 3) 旧版本 Pipeline CR（DB 版本表之外的 pl-* 全部清理）
	var vers []model.CIPipelineVersion
	if err := s.db.WithContext(ctx).Select("pipeline_id", "version").Find(&vers).Error; err == nil {
		validCRs := map[string]bool{}
		for i := range vers {
			validCRs[fmt.Sprintf("pl-%d-v%d", vers[i].PipelineID, vers[i].Version)] = true
		}
		if pls, err := k8s.ListPipelines(ctx, ns); err == nil {
			for _, name := range pls {
				if !validCRs[name] {
					if err := k8s.DeletePipeline(ctx, ns, name); err != nil {
						log.Printf("reconcile: 删孤儿 Pipeline CR %s: %v", name, err)
					} else {
						log.Printf("reconcile: 清理孤儿 Pipeline CR %s", name)
					}
				}
			}
		}
	}

	// 5) 超时卡住的进行中 run：兜底清理其 PipelineRun + PVC
	if s.runTimeout > 0 {
		expired := now.Add(-s.runTimeout)
		for i := range runs {
			r := runs[i]
			if r.Status != model.CIRunStatusRunning && r.Status != model.CIRunStatusPending {
				continue
			}
			base := r.CreatedAt
			if r.StartedAt != nil {
				base = *r.StartedAt
			}
			if !base.Before(expired) {
				continue
			}
			log.Printf("reconcile: run %d（%s）超过 %v 仍在 %s，兜底清理集群资源", r.ID, r.TektonRunName, s.runTimeout, r.Status)
			if r.TektonRunName != "" {
				if err := k8s.DeletePipelineRun(ctx, ns, r.TektonRunName); err != nil {
					log.Printf("reconcile: 删超时 PipelineRun %s: %v", r.TektonRunName, err)
				}
			}
			if r.PVCName != "" {
				if err := k8s.DeletePVC(ctx, ns, r.PVCName); err != nil {
					log.Printf("reconcile: 删超时 PVC %s: %v", r.PVCName, err)
				}
			}
		}
	}

	// 6) preserveLogs 保留的 Pod 回收
	if pods, err := k8s.ListTaskRunPods(ctx, ns); err == nil {
		expiredRuns := map[string]bool{}
		if s.runTimeout > 0 {
			expired := now.Add(-s.runTimeout)
			for i := range runs {
				r := runs[i]
				if r.Status != model.CIRunStatusRunning && r.Status != model.CIRunStatusPending {
					continue
				}
				base := r.CreatedAt
				if r.StartedAt != nil {
					base = *r.StartedAt
				}
				if base.Before(expired) && r.TektonRunName != "" {
					expiredRuns[r.TektonRunName] = true
				}
			}
		}
		for pod, pr := range pods {
			run, ok := dbRuns[pr]
			if !ok {
				_ = k8s.DeletePod(ctx, ns, pod)
				continue
			}
			if expiredRuns[pr] {
				_ = k8s.DeletePod(ctx, ns, pod)
				continue
			}
			terminal := run.Status == model.CIRunStatusSuccess || run.Status == model.CIRunStatusFailed ||
				run.Status == model.CIRunStatusCancelled
			if !terminal {
				continue
			}
			finished := run.CreatedAt
			if run.StartedAt != nil {
				finished = *run.StartedAt
			}
			if run.FinishedAt != nil {
				finished = *run.FinishedAt
			}
			if now.Sub(finished) > podRetainAfterTerminal {
				_ = k8s.DeletePod(ctx, ns, pod)
			}
		}
	}
}
