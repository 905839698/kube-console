// webhook.go GitLab Webhook 触发（token 走 URL 路径，公开端点）。
// 校验通过后【异步】触发 StartRun 并立即返回——GitLab 要求快速响应，
// StartRun 同步等待可能超过回调超时，重试会放大成重复 run。
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/model"
)

// Webhook 触发器。
type Webhook struct {
	db *gorm.DB
	rt *Service
}

func NewWebhook(db *gorm.DB, rt *Service) *Webhook {
	return &Webhook{db: db, rt: rt}
}

// GitLabTrigger 从事件体解析出的触发信息。
type GitLabTrigger struct {
	RepoURL      string
	Branch       string // push: 分支名；tag_push: tag 名；MR: 源分支
	Commit       string
	User         string
	IsTag        bool
	IsMR         bool
	TargetBranch string
}

type gitlabEvent struct {
	ObjectKind string `json:"object_kind"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Checkout   string `json:"checkout_sha"`
	UserName   string `json:"user_name"`
	Repository struct {
		GitHTTPURL string `json:"git_http_url"`
		URL        string `json:"url"`
	} `json:"repository"`
	ObjectAttributes struct {
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

// ParseGitLabEvent 解析 GitLab 事件体；不触发的事件返回 (nil, nil)。
func ParseGitLabEvent(body []byte) (*GitLabTrigger, error) {
	var ev gitlabEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, errcode.Newf(errcode.InvalidParam, "事件体解析失败: %v", err)
	}
	repoURL := ev.Repository.GitHTTPURL
	if repoURL == "" {
		repoURL = ev.Repository.URL
	}
	if repoURL == "" {
		return nil, errcode.New(errcode.InvalidParam, "事件缺少 repository 地址")
	}
	switch ev.ObjectKind {
	case "merge_request":
		a := ev.ObjectAttributes
		// 只对「产生新代码状态」的动作触发；merge/close 不触发（合并后的 push 另行触发）
		if a.Action != "open" && a.Action != "reopen" && a.Action != "update" {
			return nil, nil
		}
		if a.SourceBranch == "" || a.LastCommit.ID == "" {
			return nil, nil
		}
		return &GitLabTrigger{
			RepoURL: repoURL, Branch: a.SourceBranch, IsMR: true,
			TargetBranch: a.TargetBranch, Commit: a.LastCommit.ID, User: ev.UserName,
		}, nil
	case "push":
		t := &GitLabTrigger{RepoURL: repoURL, Commit: ev.After, User: ev.UserName}
		if ev.Checkout != "" {
			t.Commit = ev.Checkout
		}
		if !strings.HasPrefix(ev.Ref, "refs/heads/") {
			return nil, nil // 非分支 push（如删除引用），忽略
		}
		t.Branch = strings.TrimPrefix(ev.Ref, "refs/heads/")
		if t.Branch == "" {
			return nil, nil
		}
		return t, nil
	case "tag_push":
		if !strings.HasPrefix(ev.Ref, "refs/tags/") {
			return nil, nil
		}
		t := &GitLabTrigger{RepoURL: repoURL, Commit: ev.After, User: ev.UserName, IsTag: true}
		t.Branch = strings.TrimPrefix(ev.Ref, "refs/tags/")
		if t.Branch == "" {
			return nil, nil
		}
		if ev.Checkout != "" {
			t.Commit = ev.Checkout
		}
		return t, nil
	default:
		return nil, nil
	}
}

// HandleGitLab 校验并（异步）触发：按 token 找绑定 → 分支过滤 → 事件指纹去重 → 异步 StartRun。
// 返回 (pipelineID, 消息, 错误)；err 非 nil 时 handler 按错误码响应 GitLab。
func (w *Webhook) HandleGitLab(ctx context.Context, body []byte, token string) (uint, string, error) {
	trig, err := ParseGitLabEvent(body)
	if err != nil {
		return 0, "", err
	}
	if trig == nil {
		return 0, "", errcode.New(errcode.InvalidParam, "非 push/tag_push/MR 事件或引用无效，忽略")
	}

	var wh model.CIWebhook
	if err := w.db.WithContext(ctx).Where("token = ? AND enabled = ?", token, true).First(&wh).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, "", errcode.New(errcode.Forbidden, "Webhook token 无效或未启用")
		}
		return 0, "", err
	}

	// 分支过滤（MR 按目标分支；wh.Branch 空 = 全部分支）
	filterBranch := trig.Branch
	if trig.IsMR {
		filterBranch = trig.TargetBranch
	}
	if wh.Branch != "" && wh.Branch != filterBranch {
		return 0, "", errcode.Newf(errcode.InvalidParam, "分支 %s 不在过滤范围内（%s）", filterBranch, wh.Branch)
	}

	// 去重：GitLab 对同一事件会重试，相同指纹（webhook|分支|commit）只触发一次。
	// 只对 accepted 去重——failed 投递（如集群瞬时不可用）不占指纹，
	// GitLab 重试同一事件还能再触发一次，避免一次瞬时失败永久丢失该次 push。
	eventHash := eventHashFingerprint(wh.ID, trig.Branch, trig.Commit)
	var cnt int64
	if err := w.db.Model(&model.CIWebhookDelivery{}).
		Where("event_hash = ? AND status = ?", eventHash, "accepted").Count(&cnt).Error; err != nil {
		log.Printf("webhook: 去重查询失败，按新事件处理: %v", err)
	}
	if cnt > 0 {
		log.Printf("webhook: 重复投递已忽略 pipeline=%d branch=%s commit=%s", wh.PipelineID, trig.Branch, trig.Commit)
		return wh.PipelineID, "duplicate delivery ignored", nil
	}

	delivery := &model.CIWebhookDelivery{
		WebhookID: wh.ID, EventHash: eventHash,
		Branch: trig.Branch, Commit: trig.Commit, Status: "pending",
	}
	// 先落 delivery 行再异步触发：event_hash 唯一索引是并发去重的原子闸门
	// （旧实现 Count→StartRun→Create，同事件两个并发投递都能通过 Count 检查）。
	// 唯一键冲突 = 并发重投，按重复忽略；落库失败不阻塞触发（只记日志）。
	if err := w.db.Create(delivery).Error; err != nil {
		if isDuplicateKeyError(err) {
			log.Printf("webhook: 重复投递已忽略 pipeline=%d branch=%s commit=%s", wh.PipelineID, trig.Branch, trig.Commit)
			return wh.PipelineID, "duplicate delivery ignored", nil
		}
		log.Printf("webhook: delivery 落库失败（继续触发）: %v", err)
	}

	// 异步触发：独立 Background ctx + 超时（GitLab 已 200 返回）
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		// tag_push 以 tag 名覆盖 git-clone 的 branch（git --branch 接受 tag）；
		// commit 一并覆盖，GIT_COMMIT 条件参数与 run 记录才能拿到真实 SHA
		run, err := w.rt.StartRun(runCtx, wh.PipelineID, 0, 0, trig.User, "webhook", trig.Branch, trig.Commit)
		if err != nil {
			delivery.Status = "failed"
			delivery.Error = truncateStr(err.Error(), 500)
			log.Printf("webhook: 触发 pipeline %d 失败: %v", wh.PipelineID, err)
		} else {
			delivery.Status = "accepted"
			log.Printf("webhook: 触发 pipeline %d → run #%d (branch=%s commit=%s)", wh.PipelineID, run.RunNo, trig.Branch, trig.Commit)
		}
		_ = w.db.Save(delivery).Error
		// 只保留最近 200 条
		var recent []model.CIWebhookDelivery
		_ = w.db.Where("webhook_id = ?", wh.ID).Order("id desc").Offset(200).Find(&recent).Error
		if len(recent) > 0 {
			ids := make([]uint, 0, len(recent))
			for i := range recent {
				ids = append(ids, recent[i].ID)
			}
			_ = w.db.Where("id IN ?", ids).Delete(&model.CIWebhookDelivery{}).Error
		}
	}()
	return wh.PipelineID, "triggered", nil
}

func eventHashFingerprint(webhookID uint, branch, commit string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s", webhookID, branch, commit)))
	return hex.EncodeToString(sum[:])
}
