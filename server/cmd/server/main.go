// kube-console 后端服务入口
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
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
	if err := service.SeedBuiltinRoles(db); err != nil {
		log.Fatalf("初始化内置角色失败: %v", err)
	}

	clusters := service.NewClusterManager(db)

	// 告警历史归档：轮询各集群 Alertmanager，firing→resolved 状态机落库
	archive := service.NewAlertArchiveService(db, clusters, cfg.Obs.AlertIntervalSec, cfg.Obs.AlertRetentionDays)
	archive.Start()
	// 事件归档：轮询各集群 K8s Events 增量写入 ES（复用日志源连接，按日志源开关启用）
	eventArchive := service.NewEventArchiveService(db, clusters, cfg.Obs.EventIntervalSec, cfg.Obs.EventRetentionDays)
	eventArchive.Start()

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

	// Nacos 微服务集成：ns 自动同步 + Pod 注入 Admission Webhook（HTTPS :webhookPort）
	nacosSvc := service.NewNacosService(db, clusters, cfg.Nacos)
	// 日志采集注入：复用 nacos 的 webhook 通道（端口/证书/集群 token），策略存库、Pod 准入时注入
	logInjectSvc := service.NewLogInjectService(db, clusters, nacosSvc, service.NewK8sService(cfg.K8s.DebugImage))
	nacosSvc.MountInjectRoute("/log-inject/", logInjectSvc.HandleInject)
	logInjectSvc.Start()
	nacosSvc.Start()

	r := router.Setup(db, cfg, clusters, ciDeps, archive, eventArchive, nacosSvc, logInjectSvc)

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
		dialector = mysql.Open(normalizeMySQLDSN(c.DSN))
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
	if err := migrateModels(db); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	warnCINSConflicts(db)
	return db, nil
}

// migrateModels 建表/升级全部元数据表（顺序无关，供启动与 MySQL 兼容测试共用）。
func migrateModels(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.User{}, &model.Cluster{}, &model.HelmRepo{}, &model.AuditLog{},
		&model.LogSource{}, &model.NotifyChannel{}, &model.NotifyLog{}, &model.ApiToken{}, &model.UserGroup{}, &model.RegistryConfig{}, &model.CIIntegration{},
		&model.AlertmanagerConfig{}, &model.AlertEvent{}, &model.NacosConfig{}, &model.NacosNamespace{}, &model.LogCollectionPolicy{},
		&model.CIProject{}, &model.CIPipeline{}, &model.CIPipelineVersion{}, &model.CIRunCounter{},
		&model.CIRun{}, &model.CITaskRun{}, &model.CICredential{}, &model.CIGlobalVar{},
		&model.CISchedule{}, &model.CIWebhook{}, &model.CIWebhookDelivery{},
		&model.CIArtifact{}, &model.CIDeployment{},
		&model.CIImageBuilder{}, &model.CIBuilderToken{},
		&model.Role{}, &model.RbacBinding{})
}

// normalizeMySQLDSN 补齐 MySQL 连接的两个必需参数：
//   - charset=utf8mb4：缺省时驱动按 latin1 存取，中文与 emoji 会乱码甚至报错
//   - parseTime=True：缺省时 DATETIME 列不会被解析成 time.Time，GORM 扫描直接报错
//
// 已有的显式配置一律尊重，不覆盖。
func normalizeMySQLDSN(dsn string) string {
	base, params := dsn, ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		base, params = dsn[:i], dsn[i+1:]
	}
	has := func(key string) bool {
		for _, p := range strings.Split(params, "&") {
			if len(p) > len(key) && strings.EqualFold(p[:len(key)], key) && p[len(key)] == '=' {
				return true
			}
		}
		return false
	}
	var add []string
	if !has("charset") {
		add = append(add, "charset=utf8mb4")
	}
	if !has("parseTime") {
		add = append(add, "parseTime=True")
	}
	if len(add) == 0 {
		return dsn
	}
	if params == "" {
		return base + "?" + strings.Join(add, "&")
	}
	return dsn + "&" + strings.Join(add, "&") // 保留调用方已写的参数
}

// warnCINSConflicts 历史数据体检：新规则要求「一个命名空间只被一个 CI 项目绑定」，
// 旧库里同集群共用一个 ns 的项目会破坏凭据与运行的隔离。只告警不改数据——
// 保留哪个项目须由管理员决定。
func warnCINSConflicts(db *gorm.DB) {
	var projects []model.CIProject
	if err := db.Select("cluster_name", "name", "namespace").Order("id asc").Find(&projects).Error; err != nil {
		log.Printf("ci: 检查项目命名空间绑定失败: %v", err)
		return
	}
	byKey := map[string][]string{}
	for _, p := range projects {
		if p.Namespace == "" {
			continue
		}
		k := p.ClusterName + "\x00" + p.Namespace
		byKey[k] = append(byKey[k], p.Name)
	}
	for k, names := range byKey {
		if len(names) > 1 {
			parts := strings.SplitN(k, "\x00", 2)
			log.Printf("ci 隔离告警: 集群 %s 的命名空间 %s 被 %d 个项目共用（%s）——请保留其一，权限与凭据隔离要求一个 ns 只绑一个项目",
				parts[0], parts[1], len(names), strings.Join(names, ", "))
		}
	}
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
