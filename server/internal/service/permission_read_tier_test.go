// 权限快照的"可读档"单测：项目层 view 只给可见/可触发，不给写。
package service

import (
	"testing"

	"kube-console/server/internal/model"
)

func readTierBind(level, role, ns string) *model.RbacBinding {
	return &model.RbacBinding{
		Level: level, Cluster: "c1", RoleName: role,
		GranteeType: model.GranteeUser, GranteeName: "u1", Namespaces: ns,
	}
}

func TestFromDBReadTier(t *testing.T) {
	db := newRbacTestDB(t)
	if err := db.Create(readTierBind(model.LevelProject, "view", "ci-app")).Error; err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	acc := newTestPermSvc(db).fromDB("c1", "u1")
	if !acc.CanReadNS("ci-app") {
		t.Error("view 绑定应让 ci-app 可读（CI 可见 + 可触发运行）")
	}
	if acc.CanWriteNS("ci-app") {
		t.Error("view 绑定不应可写")
	}
	if acc.CanReadNS("other") {
		t.Error("未绑定命名空间不应可读")
	}

	// edit：可读且可写
	if err := db.Create(readTierBind(model.LevelProject, "edit", "ci-web")).Error; err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	acc = newTestPermSvc(db).fromDB("c1", "u1")
	if !acc.CanReadNS("ci-web") || !acc.CanWriteNS("ci-web") {
		t.Errorf("edit 绑定应既可读又可写: %+v", acc)
	}

	// 集群层只读角色：全 ns 可读，仍不可写
	if err := db.Create(readTierBind(model.LevelCluster, "cluster-viewer", "")).Error; err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	acc = newTestPermSvc(db).fromDB("c1", "u1")
	if !acc.CanReadNS("anything") {
		t.Error("cluster-viewer 应让任意 ns 可读")
	}
	if acc.CanWriteNS("anything") {
		t.Error("cluster-viewer 不应可写")
	}
}
