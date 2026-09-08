// 告警通知：Prometheus firing 告警 → 钉钉/通用 webhook 推送 + 落库去重
package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// NotifyService 通知服务
type NotifyService struct {
	db *gorm.DB
	ms *MonitorService

	mu    sync.Mutex
	// active: fingerprint -> 最近一次已通知的告警（恢复前不重复推）
	active map[string]time.Time
	// lastClean 定期清理过期 active 状态（2h 无更新移除）
	lastClean time.Time
}

// NewNotifyService 创建通知服务并启动轮询
func NewNotifyService(db *gorm.DB, ms *MonitorService, clusters *ClusterManager) *NotifyService {
	s := &NotifyService{db: db, ms: ms, active: map[string]time.Time{}, lastClean: time.Now()}
	go s.pollLoop(clusters)
	return s
}

func (s *NotifyService) pollLoop(clusters *ClusterManager) {
	poll := func() {
		var channels []model.NotifyChannel
		if err := s.db.Where("enabled = ?", true).Find(&channels).Error; err != nil || len(channels) == 0 {
			return
		}
		var clustersList []model.Cluster
		s.db.Find(&clustersList)
		cfg := DefaultPromConfig()
		for _, cl := range clustersList {
			client, err := clusters.Client(cl.Name)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			alerts, err := s.ms.FiringAlerts(ctx, client, cfg)
			cancel()
			if err != nil {
				continue
			}
			if len(alerts) > 0 {
				s.dispatch(cl.Name, alerts, channels)
			}
		}
		// 清理 active 中 2 小时未见的指纹
		s.mu.Lock()
		if time.Since(s.lastClean) > 10*time.Minute {
			for fp, t := range s.active {
				if time.Since(t) > 2*time.Hour {
					delete(s.active, fp)
				}
			}
			s.lastClean = time.Now()
		}
		s.mu.Unlock()
	}
	poll()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		poll()
	}
}

// PromAlertRef 轮询到的告警（由 monitor 层解析）
type PromAlertRef struct {
	Fingerprint string
	Name        string
	Severity    string
	Summary     string
	Description string
	ActiveAt    string
}

// dispatch 推送告警到所有启用渠道（按指纹去重）
func (s *NotifyService) dispatch(cluster string, alerts []PromAlertRef, channels []model.NotifyChannel) {
	for _, a := range alerts {
		s.mu.Lock()
		if t, ok := s.active[a.Fingerprint]; ok && time.Since(t) < 2*time.Hour {
			s.mu.Unlock()
			continue
		}
		s.active[a.Fingerprint] = time.Now()
		s.mu.Unlock()

		for _, ch := range channels {
			if !matchSeverity(a.Severity, ch.MinSeverity) {
				continue
			}
			msg := renderAlert(cluster, a)
			err := SendNotification(ch, fmt.Sprintf("【%s】K8s 告警", cluster), msg)
			s.db.Create(&model.NotifyLog{
				ChannelID: ch.ID, ChannelName: ch.Name, Cluster: cluster,
				AlertName: a.Name, Severity: a.Severity, Message: msg,
				Fingerprint: a.Fingerprint, Ok: err == nil,
				Error: errStr(err), SentAt: time.Now(),
			})
		}
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return truncateStr(err.Error(), 500)
}

// severityRank 数字越大越严重
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical", "crit":
		return 3
	case "warning", "warn":
		return 2
	case "info":
		return 1
	}
	return 2
}

func matchSeverity(actual, minSev string) bool {
	if minSev == "" {
		return true
	}
	return severityRank(actual) >= severityRank(minSev)
}

// renderAlert 钉钉 markdown 正文
func renderAlert(cluster string, a PromAlertRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", a.Name)
	fmt.Fprintf(&b, "- **集群**: %s\n", cluster)
	fmt.Fprintf(&b, "- **级别**: %s\n", strings.ToUpper(a.Severity))
	if a.Summary != "" {
		fmt.Fprintf(&b, "- **概要**: %s\n", a.Summary)
	}
	if a.Description != "" {
		fmt.Fprintf(&b, "- **详情**: %s\n", a.Description)
	}
	if a.ActiveAt != "" {
		fmt.Fprintf(&b, "- **开始时间**: %s\n", a.ActiveAt)
	}
	return b.String()
}

// SendNotification 发送一条通知到渠道（也用于手动测试）
func SendNotification(ch model.NotifyChannel, title, markdown string) error {
	switch ch.Type {
	case "dingtalk":
		return sendDingtalk(ch, title, markdown)
	case "webhook":
		return sendGenericWebhook(ch, title, markdown)
	default:
		return fmt.Errorf("未知渠道类型 %q", ch.Type)
	}
}

// sendDingtalk 钉钉自定义机器人（支持加签）
func sendDingtalk(ch model.NotifyChannel, title, markdown string) error {
	webhook := ch.Webhook
	if ch.Secret != "" {
		ts := time.Now().UnixMilli()
		stringToSign := fmt.Sprintf("%d\n%s", ts, ch.Secret)
		mac := hmac.New(sha256.New, []byte(ch.Secret))
		mac.Write([]byte(stringToSign))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		sep := "?"
		if strings.Contains(webhook, "?") {
			sep = "&"
		}
		webhook = fmt.Sprintf("%s%stimestamp=%d&sign=%s", webhook, sep, ts, url.QueryEscape(sign))
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  markdown,
		},
	}
	return postJSON(webhook, payload, func(respBody []byte, status int) error {
		var dr struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if json.Unmarshal(respBody, &dr) == nil && dr.ErrCode != 0 {
			return fmt.Errorf("钉钉返回 %d: %s", dr.ErrCode, dr.ErrMsg)
		}
		return nil
	})
}

// sendGenericWebhook 通用 webhook：POST {title, markdown, text}
func sendGenericWebhook(ch model.NotifyChannel, title, markdown string) error {
	payload := map[string]any{"title": title, "markdown": markdown, "text": markdown}
	return postJSON(ch.Webhook, payload, func(respBody []byte, status int) error {
		if status >= 300 {
			return fmt.Errorf("webhook 返回 %d: %s", status, truncateStr(string(respBody), 200))
		}
		return nil
	})
}

func postJSON(url string, payload any, check func([]byte, int) error) error {
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return check(rb, resp.StatusCode)
}

// ------------------- 权限逆查 -------------------

// UserPermission 用户在某集群的权限视图
type UserPermission struct {
	Kind      string   `json:"kind"` // ClusterRoleBinding | RoleBinding
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"` // 空=集群级
	Role      string   `json:"role"`
	RoleKind  string   `json:"roleKind"`
	Verbs     []string `json:"verbs"`
	Resources []string `json:"resources"`
}

// CollectUserPermissions 汇总某用户在集群内的全部 RBAC 授权（含组）
func CollectUserPermissions(ctx context.Context, c *kube.Client, username string, groups []string) ([]UserPermission, error) {
	rulesOf := func(roleKind, roleName, ns string) ([]string, []string) {
		var verbs, resources []string
		if roleKind == "ClusterRole" {
			cr, err := c.Clientset.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
			if err != nil {
				return verbs, resources
			}
			for _, r := range cr.Rules {
				verbs = append(verbs, r.Verbs...)
				resources = append(resources, r.Resources...)
			}
			return dedup(verbs), dedup(resources)
		}
		role, err := c.Clientset.RbacV1().Roles(ns).Get(ctx, roleName, metav1.GetOptions{})
		if err != nil {
			return verbs, resources
		}
		for _, r := range role.Rules {
			verbs = append(verbs, r.Verbs...)
			resources = append(resources, r.Resources...)
		}
		return dedup(verbs), dedup(resources)
	}
	subjectMatch := func(subjects []rbacv1.Subject) bool {
		for _, s := range subjects {
			if s.Kind == "User" && s.Name == username {
				return true
			}
			if s.Kind == "Group" {
				for _, g := range groups {
					if s.Name == g {
						return true
					}
				}
			}
		}
		return false
	}

	out := []UserPermission{}
	// 集群级绑定
	crbs, err := c.Clientset.RbacV1().ClusterRoleBindings().List(ctx, v1.ListOptions{})
	if err == nil {
		for _, b := range crbs.Items {
			if !subjectMatch(b.Subjects) {
				continue
			}
			verbs, resources := rulesOf("ClusterRole", b.RoleRef.Name, "")
			out = append(out, UserPermission{Kind: "ClusterRoleBinding", Name: b.Name, Namespace: "", Role: b.RoleRef.Name, RoleKind: "ClusterRole", Verbs: verbs, Resources: resources})
		}
	}
	// 命名空间级绑定
	rbs, err := c.Clientset.RbacV1().RoleBindings("").List(ctx, v1.ListOptions{})
	if err == nil {
		for _, b := range rbs.Items {
			if !subjectMatch(b.Subjects) {
				continue
			}
			verbs, resources := rulesOf(b.RoleRef.Kind, b.RoleRef.Name, b.Namespace)
			out = append(out, UserPermission{Kind: "RoleBinding", Name: b.Name, Namespace: b.Namespace, Role: b.RoleRef.Name, RoleKind: b.RoleRef.Kind, Verbs: verbs, Resources: resources})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out, nil
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
