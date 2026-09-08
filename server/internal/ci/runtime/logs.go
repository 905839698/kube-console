package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"kube-console/server/internal/model"
	"kube-console/server/internal/ci/errcode"
)

// podWaitTimeout follow 模式下等待 Pod 创建（仍在排队/调度）的最长时间。
const podWaitTimeout = 90 * time.Second

// StreamTaskLogs 流式输出某 TaskRun 的日志到 w。
// 按 step 容器顺序逐个输出，容器前有分隔头。
//
//	follow=true 持续 tail（任务运行中）：Pod 未建好时轮询等待；
//	follow=false 输出全部 step 后返回（已结束）。
//	flush 在每段日志后调用（SSE 客户端需要显式 flush）。
// runID 用于归属校验：taskRunID 是全局自增主键，不校验 PipelineRunID
// 归属的话，任何能看到自己 run 的人可枚举 taskID 读到其他项目的日志。
func (s *Service) StreamTaskLogs(ctx context.Context, runID, taskRunID uint, follow bool, w io.Writer, flush func()) error {
	var tr model.CITaskRun
	if err := s.db.WithContext(ctx).First(&tr, taskRunID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errcode.New(errcode.NotFound, "任务不存在")
		}
		return err
	}
	if tr.RunID != runID {
		return errcode.New(errcode.NotFound, "任务不存在")
	}
	// 落点：run → pipeline(集群) → project(ns)
	var run model.CIRun
	if err := s.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return errcode.New(errcode.NotFound, "任务不存在")
	}
	k8s, _, err := s.k8sForRun(ctx, &run)
	if err != nil {
		return errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	// Pod 未建好：follow（运行中）轮询等待；非 follow（已结束）说明 Pod 已回收
	if tr.TektonPodName == "" {
		if !follow {
			return errcode.New(errcode.NotFound, "任务 Pod 已回收，日志不可回看")
		}
		if err := s.waitPodCreated(ctx, taskRunID, &tr); err != nil {
			return err
		}
	}

	containers, err := k8s.GetTaskRunPodContainers(ctx, tr.TektonNamespace, tr.TektonPodName)
	if err != nil {
		return errcode.Newf(errcode.DepUnavailable, "读取 Pod 失败: %v", err)
	}
	// 只保留业务 step 容器（step-*），排除 step-wait / step-post-* 辅助容器
	steps := make([]string, 0, len(containers))
	for _, c := range containers {
		if strings.HasPrefix(c, "step-") && c != "step-wait" && !strings.HasPrefix(c, "step-post-") {
			steps = append(steps, c)
		}
	}
	if len(steps) == 0 {
		steps = containers
	}
	// 保持 step 执行顺序（按容器名 step-N 的序号）
	sort.SliceStable(steps, func(i, j int) bool {
		return podContainerIndex(steps[i]) < podContainerIndex(steps[j])
	})

	for _, c := range steps {
		fmt.Fprintf(w, "===== %s =====\n", c)
		if flush != nil {
			flush()
		}
		// 单个容器输出失败（如已删除）不中断整体
		_ = k8s.StreamPodLogs(ctx, tr.TektonNamespace, tr.TektonPodName, c, follow, w)
		if flush != nil {
			flush()
		}
		if ctx.Err() != nil {
			return nil // 客户端断开
		}
		// follow 模式：第一个容器 tail 会阻塞直到其退出，再输出下一个 step；
		// 非 follow 模式：逐个输出完全部 step 后返回
	}
	return nil
}

// waitPodCreated 轮询 DB 直到 TaskRun 的 Pod 名出现（syncer 回写）。
func (s *Service) waitPodCreated(ctx context.Context, taskRunID uint, tr *model.CITaskRun) error {
	deadline := time.Now().Add(podWaitTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		var cur model.CITaskRun
		if err := s.db.WithContext(ctx).First(&cur, taskRunID).Error; err == nil && cur.TektonPodName != "" {
			*tr = cur
			return nil
		}
	}
	return errcode.New(errcode.NotFound, "任务 Pod 尚未创建（仍在排队?）")
}

// podContainerIndex 从容器名提取 step 序号近似排序（step-1-xxx → 1）。
func podContainerIndex(name string) int {
	rest := strings.TrimPrefix(name, "step-")
	if i := strings.IndexByte(rest, '-'); i > 0 {
		n := 0
		for _, r := range rest[:i] {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}
