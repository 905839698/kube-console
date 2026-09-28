// MySQL 兼容性测试：DSN 参数补齐（纯函数，任何环境都跑）+ 全量建表与读写（需真实 MySQL）。
// 跑真实库：KC_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3306)/kube_console_test' go test ./cmd/server -run MySQL
package main

import (
	"os"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"kube-console/server/internal/model"
)

func TestNormalizeMySQLDSN(t *testing.T) {
	cases := []struct{ in, want string }{
		{"root:pw@tcp(127.0.0.1:3306)/db", "root:pw@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=True"},
		// 只写了一个参数：另一个补齐，已写的原样保留
		{"root:pw@tcp(127.0.0.1:3306)/db?charset=utf8", "root:pw@tcp(127.0.0.1:3306)/db?charset=utf8&parseTime=True"},
		{"root:pw@tcp(127.0.0.1:3306)/db?parseTime=True&loc=Local", "root:pw@tcp(127.0.0.1:3306)/db?parseTime=True&loc=Local&charset=utf8mb4"},
		// 两个都写了：不改
		{"root:pw@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=True&loc=Local", "root:pw@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=True&loc=Local"},
		// 参数名大小写不敏感（显式 False 也视为已配置，不覆盖）
		{"root:pw@tcp(127.0.0.1:3306)/db?PARSETIME=False", "root:pw@tcp(127.0.0.1:3306)/db?PARSETIME=False&charset=utf8mb4"},
	}
	for _, c := range cases {
		if got := normalizeMySQLDSN(c.in); got != c.want {
			t.Errorf("normalizeMySQLDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMySQLMigration(t *testing.T) {
	dsn := os.Getenv("KC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 KC_TEST_MYSQL_DSN，跳过真实 MySQL 迁移测试")
	}
	db, err := gorm.Open(mysql.Open(normalizeMySQLDSN(dsn)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连接 MySQL 失败: %v", err)
	}
	if err := migrateModels(db); err != nil {
		t.Fatalf("全量建表失败: %v", err)
	}
	// 二次迁移必须幂等（MySQL DDL 与 sqlite/postgres 行为差异最容易在这里暴露）
	if err := migrateModels(db); err != nil {
		t.Fatalf("重复迁移应幂等，失败: %v", err)
	}

	// 保留字列 + 中文写入：key 是 MySQL 保留字，charset 不对则中文乱码
	if err := db.Where("key = ?", "MZ_TEST").Delete(&model.CIGlobalVar{}).Error; err != nil {
		t.Fatalf("清理旧测试数据失败: %v", err)
	}
	gv := model.CIGlobalVar{ClusterName: "t1", Key: "MZ_TEST", Value: "中文值🙂", Description: "兼容测试"}
	if err := db.Create(&gv).Error; err != nil {
		t.Fatalf("写入全局变量失败: %v", err)
	}
	defer db.Where("key = ?", "MZ_TEST").Delete(&model.CIGlobalVar{})

	var back model.CIGlobalVar
	if err := db.Where("cluster_name = ?", "t1").
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).First(&back).Error; err != nil {
		t.Fatalf("按 key 排序读取失败（保留字未加引号？）: %v", err)
	}
	if back.Value != "中文值🙂" {
		t.Errorf("中文往返不一致，得到 %q（检查 DSN charset）", back.Value)
	}

	// 唯一冲突识别（并发首建依赖它）：MySQL 报 1062
	dupe := model.CIGlobalVar{ClusterName: "t1", Key: "MZ_TEST"}
	if err := db.Create(&dupe).Error; err == nil {
		t.Error("重复 (cluster,key) 应触发唯一约束")
	} else if !strings.Contains(err.Error(), "1062") && !strings.Contains(err.Error(), "Duplicate entry") {
		t.Errorf("唯一冲突错误形态异常: %v", err)
	}
}
