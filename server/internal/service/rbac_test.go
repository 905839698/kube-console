// RBAC 判定单测：控制台绑定（DB）为主推导权限（K8s 反推部分见 permission_test.go）
package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/model"
)

func newRbacTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserGroup{}, &model.Role{}, &model.RbacBinding{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := SeedBuiltinRoles(db); err != nil {
		t.Fatalf("seed 内置角色失败: %v", err)
	}
	return db
}

func newTestPermSvc(db *gorm.DB) *PermissionService {
	return NewPermissionService(db, nil, "root")
}

func bind(db *gorm.DB, bd *model.RbacBinding) {
	if err := db.Create(bd).Error; err != nil {
		panic(err)
	}
}

// 项目层绑定：edit 可写指定 ns，view 不可写；ns="*" 全集群可写但非集群级。
func TestFromDBProjectLevel(t *testing.T) {
	db := newRbacTestDB(t)
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c1", RoleName: "edit",
		GranteeType: "user", GranteeName: "dev1", Namespaces: "dev,qa"})
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c2", RoleName: "view",
		GranteeType: "user", GranteeName: "dev1", Namespaces: "prod"})
	s := newTestPermSvc(db)

	acc := s.fromDB("c1", "dev1")
	if !acc.CanWriteNS("dev") || !acc.CanWriteNS("qa") || acc.CanWriteNS("prod") {
		t.Errorf("c1 edit(dev,qa) 判定错误: %+v", acc)
	}
	if acc.AllNSWrite || acc.CanWriteCluster() {
		t.Errorf("项目层 edit 不应有集群级/全 ns 权限: %+v", acc)
	}
	// 跨集群隔离：c2 上只有 view
	if acc2 := s.fromDB("c2", "dev1"); acc2.CanWriteNS("prod") {
		t.Error("c2 只有 view，不应可写")
	}
}

// 集群层：cluster-admin → 集群级写；cluster-viewer → 无写。
func TestFromDBClusterLevel(t *testing.T) {
	db := newRbacTestDB(t)
	bind(db, &model.RbacBinding{Level: model.LevelCluster, Cluster: "c1", RoleName: "cluster-admin",
		GranteeType: "user", GranteeName: "ops1"})
	bind(db, &model.RbacBinding{Level: model.LevelCluster, Cluster: "c1", RoleName: "cluster-viewer",
		GranteeType: "user", GranteeName: "watch1"})
	s := newTestPermSvc(db)

	acc := s.fromDB("c1", "ops1")
	if !acc.CanWriteCluster() || !acc.CanWriteNS("any") {
		t.Errorf("cluster-admin 应集群级可写: %+v", acc)
	}
	if acc2 := s.fromDB("c1", "watch1"); acc2.CanWriteCluster() || acc2.CanWriteNS("any") {
		t.Errorf("cluster-viewer 不应有任何写: %+v", acc2)
	}
}

// 平台层：platform-admin 绑定 = 全放行；platform-viewer 只读标记。
// User.Role=admin 的旧口径继续生效。
func TestFromDBPlatformLevel(t *testing.T) {
	db := newRbacTestDB(t)
	db.Create(&model.User{Username: "legacy", PasswordHash: "x", Role: model.RoleAdmin})
	bind(db, &model.RbacBinding{Level: model.LevelPlatform, RoleName: "platform-admin",
		GranteeType: "user", GranteeName: "padmin"})
	bind(db, &model.RbacBinding{Level: model.LevelPlatform, RoleName: "platform-viewer",
		GranteeType: "user", GranteeName: "pview"})
	s := newTestPermSvc(db)

	if acc := s.fromDB("c1", "padmin"); !acc.PlatformAdmin || !acc.CanWriteCluster() {
		t.Errorf("platform-admin 绑定应全放行: %+v", acc)
	}
	if acc := s.fromDB("c1", "pview"); acc.PlatformAdmin || !acc.PlatformViewer || acc.CanWriteNS("dev") {
		t.Errorf("platform-viewer 应只读: %+v", acc)
	}
	if acc := s.fromDB("c1", "legacy"); !acc.PlatformAdmin {
		t.Error("User.Role=admin 旧口径应保持生效")
	}
	if !s.PlatformAdmin("padmin") || s.PlatformAdmin("pview") {
		t.Error("PlatformAdmin 绑定判定错误")
	}
}

// 组继承：绑定给组的角色，组内用户生效。自定义项目角色按授权项判定写权限。
func TestFromDBGroupAndCustomRole(t *testing.T) {
	db := newRbacTestDB(t)
	g := model.UserGroup{Name: "devops"}
	db.Create(&g)
	db.Create(&model.User{Username: "u1", PasswordHash: "x", Role: model.RoleUser, GroupID: g.ID})
	db.Create(&model.Role{Name: "logs-only", Level: model.LevelProject,
		Rules: `[{"apiGroup":"","resources":["pods","pods/log"],"verbs":["get","list"]}]`})
	db.Create(&model.Role{Name: "deploy-push", Level: model.LevelProject,
		Rules: `[{"apiGroup":"apps","resources":["deployments"],"verbs":["create","update","patch"]}]`})
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c1", RoleName: "logs-only",
		GranteeType: "group", GranteeName: "devops", Namespaces: "dev"})
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c1", RoleName: "deploy-push",
		GranteeType: "group", GranteeName: "devops", Namespaces: "dev"})
	s := newTestPermSvc(db)

	acc := s.fromDB("c1", "u1")
	if !acc.CanWriteNS("dev") {
		t.Errorf("组继承的写角色应生效: %+v", acc)
	}
	if acc.CanWriteNS("prod") || acc.CanWriteCluster() {
		t.Errorf("不应越出 dev: %+v", acc)
	}
	// 不在组内的用户不受影响
	if acc2 := s.fromDB("c1", "u2"); acc2.CanWriteNS("dev") {
		t.Error("非组内用户不应有权限")
	}
	// 纯读自定义角色单独不产生写权限
	acc3 := s.fromDB("c1", "u3")
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c1", RoleName: "logs-only",
		GranteeType: "user", GranteeName: "u3", Namespaces: "dev"})
	if acc3 = s.fromDB("c1", "u3"); acc3.CanWriteNS("dev") {
		t.Error("只读自定义角色不应可写")
	}
}

// 自定义角色的授权项含集群级资源（nodes）：项目层判定应忽略（收紧），
// 集群层自定义角色含写则给集群级写。
func TestCustomRoleScopeRules(t *testing.T) {
	db := newRbacTestDB(t)
	db.Create(&model.Role{Name: "node-touch", Level: model.LevelProject,
		Rules: `[{"apiGroup":"","resources":["nodes"],"verbs":["patch"]}]`})
	bind(db, &model.RbacBinding{Level: model.LevelProject, Cluster: "c1", RoleName: "node-touch",
		GranteeType: "user", GranteeName: "x", Namespaces: "dev"})
	if acc := newTestPermSvc(db).fromDB("c1", "x"); acc.CanWriteNS("dev") {
		t.Error("项目层角色只有集群级资源授权，不应产生 ns 写权限")
	}
}

// 校验：层级不匹配 / 重复授权 / 集群项目层缺范围
func TestCreateBindingValidation(t *testing.T) {
	db := newRbacTestDB(t)
	svc := &RbacService{db: db}
	if _, err := svc.CreateBinding(BindingReq{Level: model.LevelProject, Cluster: "c1",
		RoleName: "platform-admin", GranteeType: "user", GranteeName: "a"}); err == nil {
		t.Error("层级不匹配应拒绝")
	}
	if _, err := svc.CreateBinding(BindingReq{Level: model.LevelCluster, Cluster: "c1",
		RoleName: "不存在", GranteeType: "user", GranteeName: "a"}); err == nil {
		t.Error("角色不存在应拒绝")
	}
}

// 物化 subject 的 Kind 必须是大写 User/Group（DB 里存小写），否则 apiserver 拒绝
func TestRbacSubjectKind(t *testing.T) {
	u := rbacSubject("user", "zhang.san")
	if u.Kind != "User" || u.Name != "zhang.san" || u.APIGroup != "rbac.authorization.k8s.io" {
		t.Errorf("user subject 错误: %+v", u)
	}
	g := rbacSubject("group", "devops")
	if g.Kind != "Group" || g.Name != "devops" {
		t.Errorf("group subject 错误: %+v", g)
	}
}
