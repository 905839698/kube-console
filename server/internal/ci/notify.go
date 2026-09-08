// notify.go CI 执行事件 → kube-console 通知渠道（钉钉/通用 webhook），
// 统一走 NotifyChannel（不再保留 ci-platform 的 SMTP/企微双轨）。
package ci

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/runtime"
	"kube-console/server/internal/model"
	"kube-console/server/internal/service"
)

// notifyAdapter 实现 runtime.Notifier：把执行事件推送到全部启用渠道。
type notifyAdapter struct{ db *gorm.DB }

func (a *notifyAdapter) Send(_ context.Context, ev runtime.NotifyEvent) {
	var channels []model.NotifyChannel
	if err := a.db.Where("enabled = ?", true).Find(&channels).Error; err != nil || len(channels) == 0 {
		return
	}
	icon := "❌"
	if ev.Event == "run.succeeded" {
		icon = "✅"
	} else if ev.Event == "run.approval_pending" {
		icon = "⏸️"
	}
	title := fmt.Sprintf("%s CI 流水线「%s」%s", icon, ev.Pipeline, eventLabel(ev.Event))
	markdown := fmt.Sprintf("### %s\n\n- 流水线: **%s**\n- 执行: #%d\n- 状态: %s\n- %s",
		title, ev.Pipeline, ev.RunNo, ev.Status, ev.Message)
	for _, ch := range channels {
		if err := service.SendNotification(ch, title, markdown); err != nil {
			log.Printf("ci notify: 渠道 %s 推送失败: %v", ch.Name, err)
		}
	}
}

func eventLabel(ev string) string {
	switch ev {
	case "run.failed":
		return "执行失败"
	case "run.succeeded":
		return "执行成功"
	case "run.approval_pending":
		return "等待人工审批"
	default:
		return strings.ReplaceAll(ev, "run.", "")
	}
}

// NewNotifier 构建 CI 通知适配器（无启用渠道时 Send 为空转）。
func NewNotifier(db *gorm.DB) runtime.Notifier {
	return &notifyAdapter{db: db}
}
