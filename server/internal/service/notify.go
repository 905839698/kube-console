// 通知渠道发送：钉钉机器人（加签）/ 通用 webhook。
// 渠道模型与 CRUD 由 CI 执行事件通知使用；K8s 告警通知已迁移至 Alertmanager（见 alertmanager.go）
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
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

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
	// TrimSpace：粘贴时带入的首尾空白会改变 HMAC 密钥/URL，导致钉钉 310000 签名不匹配
	webhook := strings.TrimSpace(ch.Webhook)
	secret := strings.TrimSpace(ch.Secret)
	if secret != "" {
		ts := time.Now().UnixMilli()
		stringToSign := fmt.Sprintf("%d\n%s", ts, secret)
		mac := hmac.New(sha256.New, []byte(secret))
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
			if dr.ErrCode == 310000 {
				return fmt.Errorf("钉钉返回 %d: %s（排查：1. 加签密钥是否与机器人设置中的一致、是否含首尾空白；2. 服务器时间与标准时间偏差是否超过 1 小时）", dr.ErrCode, dr.ErrMsg)
			}
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
