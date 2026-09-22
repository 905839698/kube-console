// schedule.go 定时触发：cron 表达式到点触发流水线（triggerType=schedule）。
// 单实例运行（无 leader election）；30s tick 检查 NextRunAt，
// fire 前原子占位（UPDATE ... WHERE next_run_at <= now）防重复触发。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/model"
)

// Scheduler 定时任务调度循环。
type Scheduler struct {
	db *gorm.DB
	rt *Service
}

func NewScheduler(db *gorm.DB, rt *Service) *Scheduler {
	return &Scheduler{db: db, rt: rt}
}

// Start 后台调度（ctx 取消退出）；启动时先校正 NextRunAt（重启后不漏触发/不错触发）。
func (s *Scheduler) Start(ctx context.Context) {
	s.reconcile(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// startRunTimeout 单次 StartRun 超时上限（K8s apply 串行调用，防 fire 阻塞整个 tick）。
const startRunTimeout = 60 * time.Second
// retryDelay 瞬时失败后的快速重试间隔（不等到下个 cron 周期）。
const retryDelay = 60 * time.Second

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now()
	var schedules []model.CISchedule
	if err := s.db.WithContext(ctx).
		Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Find(&schedules).Error; err != nil {
		log.Printf("scheduler: list: %v", err)
		return
	}
	for i := range schedules {
		if err := s.fire(ctx, &schedules[i], now); err != nil {
			log.Printf("scheduler: fire %d (pipeline %d): %v", schedules[i].ID, schedules[i].PipelineID, err)
		}
	}
}

func (s *Scheduler) fire(ctx context.Context, sch *model.CISchedule, now time.Time) error {
	next, err := nextRun(sch.Cron, now)
	if err != nil {
		return err
	}
	// 原子占位：NextRunAt 推到未来，防重复触发
	res := s.db.WithContext(ctx).
		Model(&model.CISchedule{}).
		Where("id = ? AND (next_run_at IS NULL OR next_run_at <= ?)", sch.ID, now).
		Updates(map[string]interface{}{"last_run_at": now, "next_run_at": next})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // 已被处理
	}

	runCtx, cancel := context.WithTimeout(ctx, startRunTimeout)
	defer cancel()
	run, err := s.rt.StartRun(runCtx, sch.PipelineID, 0, 0, "scheduler", "schedule", "", "")
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&model.CISchedule{}).
			Where("id = ?", sch.ID).Update("last_error", truncateStr(err.Error(), 500)).Error
		log.Printf("scheduler: 触发 pipeline %d 失败: %v", sch.PipelineID, err)
		// 瞬时失败（集群不可用类）→ 60s 后快速重试；确定性错误留在原周期
		var ce *errcode.Error
		if errors.As(err, &ce) && ce.Code == errcode.DepUnavailable {
			retryAt := now.Add(retryDelay)
			_ = s.db.WithContext(ctx).Model(&model.CISchedule{}).
				Where("id = ?", sch.ID).Update("next_run_at", retryAt).Error
		}
		return nil
	}
	_ = s.db.WithContext(ctx).Model(&model.CISchedule{}).
		Where("id = ?", sch.ID).Update("last_error", "").Error
	log.Printf("scheduler: 定时触发 pipeline %d → run #%d", sch.PipelineID, run.RunNo)
	return nil
}

// reconcile 校正所有启用 schedule 的 NextRunAt（启动时）。
func (s *Scheduler) reconcile(ctx context.Context) {
	var schedules []model.CISchedule
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&schedules).Error; err != nil {
		return
	}
	now := time.Now()
	for i := range schedules {
		next, err := nextRun(schedules[i].Cron, now)
		if err != nil {
			continue
		}
		_ = s.db.WithContext(ctx).Model(&model.CISchedule{}).
			Where("id = ?", schedules[i].ID).Update("next_run_at", next).Error
	}
}

// nextRun 计算 cron 在 after 之后的下一次触发时间。
func nextRun(expr string, after time.Time) (time.Time, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, errcode.Newf(errcode.InvalidParam, "非法 cron 表达式: %s (%v)", expr, err)
	}
	return sched.Next(after), nil
}

// ValidateCron 校验 cron 表达式（标准 5 段或 6 段）。
func ValidateCron(expr string) error {
	_, err := cron.ParseStandard(expr)
	if err != nil {
		return errcode.Newf(errcode.InvalidParam, "非法 cron 表达式: %s", expr)
	}
	return nil
}

// truncateStr 按 rune 截断，避免把 UTF-8 中文切在半截。
func truncateStr(str string, n int) string {
	if len(str) <= n {
		return str
	}
	r := []rune(str)
	for len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}
