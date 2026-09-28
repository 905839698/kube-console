// 凭据 Secret 落点单测：只写平台 ns + 归属项目 ns，不再扇出到集群内全部项目 ns。
package credential

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

func newTestSvc(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.CIProject{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := db.Create(&[]model.CIProject{
		{ClusterName: "c1", Name: "a", Namespace: "ci-a"},
		{ClusterName: "c1", Name: "b", Namespace: "ci-b"},
	}).Error; err != nil {
		t.Fatalf("建项目失败: %v", err)
	}
	return NewService(db, &config.CIConfig{PlatformNS: "ci-platform"}, nil, nil)
}

func TestSecretNamespacesScopedToOwner(t *testing.T) {
	s := newTestSvc(t)
	ctx := context.Background()

	// 平台级：只留平台 ns 的权威副本（run 时按需拉齐到各自 ns）
	if got := s.secretNamespaces(ctx, "c1", nil); len(got) != 1 || got[0] != "ci-platform" {
		t.Errorf("平台级凭据应只落平台 ns，得到 %v", got)
	}

	// 项目级：平台 ns + 归属项目 ns，且不含其它项目 ns
	var a uint
	if err := s.db.Model(&model.CIProject{}).Where("name = ?", "a").Select("id").Scan(&a).Error; err != nil {
		t.Fatalf("查项目失败: %v", err)
	}
	got := s.secretNamespaces(ctx, "c1", &a)
	if len(got) != 2 || got[0] != "ci-platform" || got[1] != "ci-a" {
		t.Errorf("项目 a 的凭据应落 [ci-platform ci-a]，得到 %v", got)
	}
	for _, ns := range got {
		if ns == "ci-b" {
			t.Error("凭据不得出现在其它项目的 ns 里（跨项目密钥泄漏）")
		}
	}
}
