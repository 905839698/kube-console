// webhook 路径的单测：MutatingWebhookConfiguration 的 service.path 每段都按
// RFC 1123 子域校验，集群名（如中文"测试集群"）直接编码会被 apiserver 整单拒绝，
// 表现是"维护 MutatingWebhookConfiguration 失败"，注入配了也配不上。
package service

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

func newWebhookTestService(t *testing.T) *NacosService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.NacosConfig{}, &model.Cluster{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := db.Create(&model.Cluster{Name: "测试集群"}).Error; err != nil {
		t.Fatalf("建集群失败: %v", err)
	}
	if err := db.Create(&model.Cluster{Name: "langcao-prd"}).Error; err != nil {
		t.Fatalf("建集群失败: %v", err)
	}
	return NewNacosService(db, nil, config.NacosConfigYaml{CertDir: t.TempDir()})
}

// 路径 token 必须恒为 c<数字>；解析回名字要双向一致
func TestNacosWebhookClusterToken(t *testing.T) {
	s := newWebhookTestService(t)

	token, err := webhookClusterToken(s.db, "测试集群")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	if !regexp.MustCompile(`^c[0-9]+$`).MatchString(token) {
		t.Fatalf("token 应为 c<集群ID>，实际 %q", token)
	}
	if got := resolveClusterToken(s.db, token); got != "测试集群" {
		t.Errorf("token %q 应解析回中文集群名，实际 %q", token, got)
	}

	// 合法英文名：新路径也编码为 c<id>（不再依赖名字合法性）
	if token, err = webhookClusterToken(s.db, "langcao-prd"); err != nil || !regexp.MustCompile(`^c[0-9]+$`).MatchString(token) {
		t.Fatalf("英文名集群也应编码为 c<id>，实际 %q err=%v", token, err)
	}
	if got := resolveClusterToken(s.db, token); got != "langcao-prd" {
		t.Errorf("token %q 应解析回 langcao-prd，实际 %q", token, got)
	}

	// 兼容旧路径：合法集群名直接作为 token 也要能解析回自己
	if got := resolveClusterToken(s.db, "langcao-prd"); got != "langcao-prd" {
		t.Errorf("旧路径（裸集群名）应继续可用，实际 %q", got)
	}
	if got := resolveClusterToken(s.db, "c99999"); got != "" {
		t.Errorf("不存在的 token 应解析为空（不注入），实际 %q", got)
	}
}

// 维护出来的 MWC 路径必须整体满足 RFC 1123 段校验，且能解析回中文集群名
func TestNacosEnsureWebhookConfigRFC1123Path(t *testing.T) {
	s := newWebhookTestService(t)
	cs := fake.NewSimpleClientset()
	s.clusters = &ClusterManager{} // ensureWebhookConfig 只用 clientset，不碰管理器
	client := &kube.Client{Clientset: cs}

	cfg := &model.NacosConfig{
		ClusterName: "测试集群", WebhookMode: "service",
		WebhookServiceNS: "kube-public", WebhookServiceName: "kube-console-server",
	}
	if err := s.ensureWebhookConfig(context.Background(), client, cfg); err != nil {
		t.Fatalf("维护 MWC 失败: %v", err)
	}
	mwc, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(
		context.Background(), WebhookConfigName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取 MWC 失败: %v", err)
	}
	if len(mwc.Webhooks) != 1 || mwc.Webhooks[0].ClientConfig.Service == nil {
		t.Fatalf("webhook 配置不完整: %+v", mwc.Webhooks)
	}
	path := *mwc.Webhooks[0].ClientConfig.Service.Path
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`).MatchString(seg) {
			t.Errorf("路径段 %q 不满足 RFC 1123（apiserver 会拒绝整条配置）", seg)
		}
	}
	token, err := webhookClusterToken(s.db, "测试集群")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	if path != "/inject/"+token {
		t.Errorf("路径应为 /inject/%s，实际 %s", token, path)
	}
	if got := resolveClusterToken(s.db, strings.TrimPrefix(path, "/inject/")); got != "测试集群" {
		t.Errorf("MWC 路径应能解析回中文集群名，实际 %q", got)
	}
}
