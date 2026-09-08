// kube-console 后端服务入口
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"kube-console/server/internal/ci"
	"kube-console/server/internal/config"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/internal/router"
	"kube-console/server/internal/service"
)

var (
	configPath = flag.String("config", "", "配置文件路径（默认自动探测 configs/config.yaml）")
	addr       = flag.String("addr", "", "监听地址（默认 :<config.port>）")
)

func main() {
	flag.Parse()
	gin.SetMode(gin.ReleaseMode)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	db, err := initDB(&cfg.Database)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	if err := seedAdmin(db, &cfg.Admin); err != nil {
		log.Fatalf("初始化管理员失败: %v", err)
	}

	clusters := service.NewClusterManager(db)

	// 内置 CI（Tekton）：加载内嵌节点插件 + 构建执行/凭证服务；后台循环（syncer/reconciler）
	ciDeps, err := ci.NewDeps(db, &cfg.CI, clusters)
	if err != nil {
		log.Fatalf("初始化内置 CI 失败: %v", err)
	}
	go ciDeps.Start(context.Background())
	// 集群 kubeconfig 变更后失效该集群的 Tekton 客户端缓存
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			clusters.InvalidateChangedForCI(ciDeps.Invalidate)
		}
	}()

	// 审计日志定期清理：启动时一次 + 每 24 小时
	go func() {
		middleware.CleanAuditLogs(db)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			middleware.CleanAuditLogs(db)
		}
	}()

	r := router.Setup(db, cfg, clusters, ciDeps)

	port := cfg.Server.Port
	if port == 0 {
		port = 8080
	}
	if *addr == "" {
		*addr = fmt.Sprintf(":%d", port)
	}
	log.Printf("kube-console 服务启动，监听 %s（数据库: %s）", *addr, cfg.Database.Driver)
	if err := r.Run(*addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// initDB 按配置的驱动初始化数据库（sqlite | mysql | postgres）
func initDB(c *config.DatabaseConfig) (*gorm.DB, error) {
	level := logger.Warn
	if os.Getenv("KC_DEBUG") == "1" {
		level = logger.Info
	}
	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(level)}

	var dialector gorm.Dialector
	switch c.Driver {
	case "sqlite":
		if c.DSN == "" {
			c.DSN = "./data/kube-console.db"
		}
		if dir := filepath.Dir(c.DSN); dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		dialector = sqlite.Open(c.DSN)
	case "mysql":
		dialector = mysql.Open(c.DSN)
	case "postgres":
		dialector = postgres.Open(c.DSN)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（支持: sqlite|mysql|postgres）", c.Driver)
	}

	db, err := gorm.Open(dialector, gormCfg)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err == nil {
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Cluster{}, &model.HelmRepo{}, &model.AuditLog{},
		&model.LogSource{}, &model.NotifyChannel{}, &model.NotifyLog{}, &model.ApiToken{}, &model.UserGroup{}, &model.RegistryConfig{}, &model.CIIntegration{},
		&model.CIProject{}, &model.CIPipeline{}, &model.CIPipelineVersion{}, &model.CIRunCounter{},
		&model.CIRun{}, &model.CITaskRun{}, &model.CICredential{}, &model.CIGlobalVar{},
		&model.CISchedule{}, &model.CIWebhook{}, &model.CIWebhookDelivery{},
		&model.CIArtifact{}, &model.CIDeployment{}); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	return db, nil
}

// seedAdmin 若不存在管理员则从配置创建；已存在时确保其具备 admin 角色
func seedAdmin(db *gorm.DB, admin *config.AdminConfig) error {
	var count int64
	db.Model(&model.User{}).Where("username = ?", admin.Username).Count(&count)
	if count > 0 {
		// 旧库迁移：补齐 admin 角色
		db.Model(&model.User{}).Where("username = ? AND (role IS NULL OR role = '' OR role != ?)", admin.Username, model.RoleAdmin).
			Update("role", model.RoleAdmin)
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(admin.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user := &model.User{Username: admin.Username, PasswordHash: string(hash), Role: model.RoleAdmin}
	if err := db.Create(user).Error; err != nil {
		return err
	}
	log.Printf("已创建默认管理员: %s（请尽快修改密码）", admin.Username)
	return nil
}
