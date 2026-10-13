// Pod 注入证书 SAN 的单测：apiserver 是按 <svc>.<ns>.svc 回调的，SAN 少这个域名
// 就会 TLS 校验失败，而 failurePolicy=Ignore 让整件事一声不响。
package service

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

func newCertTestService(t *testing.T) *NacosService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.NacosConfig{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return NewNacosService(db, nil, config.NacosConfigYaml{CertDir: t.TempDir()})
}

func (s *NacosService) addConfig(t *testing.T, c model.NacosConfig) {
	t.Helper()
	if err := s.db.Create(&c).Error; err != nil {
		t.Fatalf("建接入配置失败: %v", err)
	}
}

func certDNS(t *testing.T, certPEM []byte) map[string]bool {
	t.Helper()
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		t.Fatal("证书 PEM 解不出来")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	out := map[string]bool{}
	for _, n := range c.DNSNames {
		out[n] = true
	}
	return out
}

// 控制台不在 kube-console 命名空间时（真机就在 kube-public），SAN 必须跟着配置走
func TestNacosCertSANsFollowConfiguredService(t *testing.T) {
	s := newCertTestService(t)
	s.addConfig(t, model.NacosConfig{
		ClusterName: "c1", Addr: "http://nacos.mid:8848",
		WebhookMode: "service", WebhookServiceNS: "kube-public", WebhookServiceName: "kube-console-server",
	})
	pemBytes, _, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert: %v", err)
	}
	dns := certDNS(t, pemBytes)
	for _, want := range []string{
		"kube-console-server",
		"kube-console-server.kube-public",
		"kube-console-server.kube-public.svc", // apiserver 实际用的就是这个
		"kube-console-server.kube-public.svc.cluster.local",
		"localhost",
	} {
		if !dns[want] {
			t.Errorf("证书 SAN 缺少 %s（apiserver 回调会 TLS 校验失败，注入静默不生效）：%v", want, dns)
		}
	}
	if dns["kube-console-server.kube-console.svc"] {
		t.Error("写死的 kube-console 命名空间域名不该再出现在按配置生成的证书里")
	}
}

// URL 模式：主机名与 IP 分别落到对应的 SAN 类型上
func TestNacosCertSANsFromWebhookURL(t *testing.T) {
	s := newCertTestService(t)
	s.addConfig(t, model.NacosConfig{
		ClusterName: "c2", Addr: "http://nacos.mid:8848",
		WebhookMode: "url", WebhookURL: "https://console.example.internal:9443/inject",
	})
	s.addConfig(t, model.NacosConfig{
		ClusterName: "c3", Addr: "http://nacos2:8848",
		WebhookMode: "url", WebhookURL: "https://10.0.0.9:9443",
	})
	pemBytes, _, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert: %v", err)
	}
	dns := certDNS(t, pemBytes)
	if !dns["console.example.internal"] {
		t.Errorf("URL 主机名没进 SAN: %v", dns)
	}
	blk, _ := pem.Decode(pemBytes)
	c, _ := x509.ParseCertificate(blk.Bytes)
	found := false
	for _, ip := range c.IPAddresses {
		if ip.String() == "10.0.0.9" {
			found = true
		}
	}
	if !found {
		t.Errorf("URL 是 IP 时没落到 IPAddresses: %v", c.IPAddresses)
	}
}

// 换了配置要重签；配置没变时不能每轮同步都重签（caBundle 会一直抖）
func TestNacosCertRegeneratesOnlyWhenNeeded(t *testing.T) {
	s := newCertTestService(t)
	s.addConfig(t, model.NacosConfig{
		ClusterName: "c1", Addr: "http://nacos:8848",
		WebhookMode: "service", WebhookServiceNS: "kube-console", WebhookServiceName: "kube-console-server",
	})
	first, _, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert: %v", err)
	}
	again, _, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert(第二次): %v", err)
	}
	if string(first) != string(again) {
		t.Error("配置没变却重签了：每轮同步都会重写 caBundle，注入会周期性失败")
	}

	// 新集群落在另一个命名空间：必须重签并覆盖两个 ns
	s.addConfig(t, model.NacosConfig{
		ClusterName: "c9", Addr: "http://nacos9:8848",
		WebhookMode: "service", WebhookServiceNS: "mid", WebhookServiceName: "console-svc",
	})
	third, _, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert(第三次): %v", err)
	}
	if string(third) == string(first) {
		t.Fatal("多了 mid/console-svc 这个回调地址却没重签")
	}
	dns := certDNS(t, third)
	for _, want := range []string{"kube-console-server.kube-console.svc", "console-svc.mid.svc"} {
		if !dns[want] {
			t.Errorf("重签后的证书少了 %s: %v", want, dns)
		}
	}
	// 磁盘上那份也要同步换掉，否则重启后又回到旧 SAN
	onDisk, err := os.ReadFile(filepath.Join(s.cfg.CertDir, "nacos-webhook-cert.pem"))
	if err != nil {
		t.Fatalf("读证书文件失败: %v", err)
	}
	if string(onDisk) != string(third) {
		t.Error("重签后没写回磁盘（重启会用旧证书）")
	}
}

// GetCertificate 必须拿到当前内存里那份：重签之后旧文件描述不该再被用到
func TestNacosGetCertificateServesCurrentCert(t *testing.T) {
	s := newCertTestService(t)
	if _, err := s.getCertificate(nil); err == nil {
		t.Fatal("还没签证书时应该报错，而不是返回空证书")
	}
	pemBytes, keyPEM, err := s.ensureCert()
	if err != nil {
		t.Fatalf("ensureCert: %v", err)
	}
	got, err := s.getCertificate(nil)
	if err != nil {
		t.Fatalf("getCertificate: %v", err)
	}
	if len(got.Certificate) == 0 {
		t.Error("GetCertificate 返回了空证书")
	}
	if string(s.certPEM) != string(pemBytes) || string(s.keyPEM) != string(keyPEM) {
		t.Error("ensureCert 没把证书存进内存（caBundle 与握手用的不是同一份）")
	}
}
