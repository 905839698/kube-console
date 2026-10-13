// Package config 负责加载配置文件（支持环境变量覆盖）
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig        `yaml:"server"`
	Database DatabaseConfig      `yaml:"database"`
	JWT      JWTConfig           `yaml:"jwt"`
	Admin    AdminConfig         `yaml:"admin"`
	K8s      K8sConfig           `yaml:"k8s"`
	CI       CIConfig            `yaml:"ci"`
	Obs      ObservabilityConfig `yaml:"obs"`
	Nacos    NacosConfigYaml     `yaml:"nacos"`
	RBAC     RBACConfig          `yaml:"rbac"`
}

// RBACConfig 权限模型行为开关
type RBACConfig struct {
	// EnforceMenus 打开后，接口按"菜单能力"收口：用户看不见的模块，直接调接口也 403。
	// 默认 false —— 现状是"资源读取登录即可浏览"，一刀切收口会让没绑角色的账号
	// 突然什么都看不见；先在「授权」页把该给的 view/edit 补齐，再打开这个开关。
	EnforceMenus bool `yaml:"enforceMenus"`
}

// NacosConfigYaml Nacos 集成运行参数（连接配置按集群存 DB）
type NacosConfigYaml struct {
	// SyncIntervalSec 命名空间自动同步周期（秒，默认 300）
	SyncIntervalSec int `yaml:"syncIntervalSec"`
	// WebhookPort Pod 注入 Admission Webhook 的 HTTPS 监听端口（默认 9443，0 = 不启动）
	WebhookPort int `yaml:"webhookPort"`
	// SkipNamespaces 不参与 Nacos 同步的系统命名空间（逗号分隔）
	SkipNamespaces string `yaml:"skipNamespaces"`
	// CertDir Webhook 自签证书存放目录（默认 ./data，需持久化以保证 caBundle 跨重启稳定）
	CertDir string `yaml:"certDir"`
}

// ObservabilityConfig 可观测性后台任务参数（告警历史轮询 / 事件归档）
type ObservabilityConfig struct {
	// AlertIntervalSec 告警历史轮询 Alertmanager 的间隔（秒，默认 60）
	AlertIntervalSec int `yaml:"alertIntervalSec"`
	// AlertRetentionDays 告警历史保留天数（默认 90，超期清理）
	AlertRetentionDays int `yaml:"alertRetentionDays"`
	// EventIntervalSec 事件归档轮询 K8s Events 并写 ES 的间隔（秒，默认 60）
	EventIntervalSec int `yaml:"eventIntervalSec"`
	// EventRetentionDays 事件归档索引保留天数（默认 30，0 = 永不清理；按索引名日期删除）
	EventRetentionDays int `yaml:"eventRetentionDays"`
}

// CIConfig 内置 CI（Tekton）运行参数。节点插件随二进制 embed（server/nodes），无需路径配置。
type CIConfig struct {
	Enabled         bool   `yaml:"enabled"`           // 总开关（关则不启动后台同步循环）
	RunTimeout      string `yaml:"runTimeout"`        // 单次运行超时（默认 2h）
	PVCSize         string `yaml:"pvcSize"`           // 运行工作区 PVC 大小（默认 10Gi）
	PVCStorageClass string `yaml:"pvcStorageClass"`   // 空 = 集群默认（无默认 SC 的集群必须显式指定）
	CacheEnabled    bool   `yaml:"cacheEnabled"`      // 项目级构建依赖缓存（与工作区同盘，容量看 pvcSize）
	ServiceAccount  string `yaml:"serviceAccount"`    // Tekton 运行 SA（默认 ci-bot）
	ImagePullSecret string `yaml:"imagePullSecret"`   // PipelineRun podTemplate 的镜像拉取 Secret
	PlatformNS      string `yaml:"platformNamespace"` // 平台级凭据 Secret 所在 ns（默认 ci-platform）
	// ImageRegistry CI 任务镜像仓库前缀（节点模板里 {{imageRegistry}} 的全局取值，
	// 默认 harbor.cqyxpt.site:8443/library）；镜像版本仍是各节点自己的属性
	ImageRegistry string `yaml:"imageRegistry"`
	// RegistryCAFile 自签名镜像仓库的根证书 PEM 文件路径，作为节点模板里
	// {{registryCA}} 的取值来源（env: KC_CI_REGISTRY_CA_FILE）。为空则不注入，
	// build-image 用系统信任库（registry 用公共证书时正好）。
	RegistryCAFile string `yaml:"registryCAFile"`
	// 构建/存储端点（upload-artifact / build-image 节点伪参数默认值，可空）
	BuildkitAddr  string `yaml:"buildkitAddr"`
	NexusURL      string `yaml:"nexusUrl"`
	MinIOEndpoint string `yaml:"minioEndpoint"`
	MinIOBucket   string `yaml:"minioBucket"`
	// 制品存储凭据（下载代理用；可空 = 对应存储未启用）
	NexusUsername  string `yaml:"nexusUsername"`
	NexusPassword  string `yaml:"nexusPassword"`
	MinIOAccessKey string `yaml:"minioAccessKey"`
	MinIOSecretKey string `yaml:"minioSecretKey"`

	// 节点固定：把 CI 运行的所有 Pod（全部 TaskRun 与 affinity-assistant）钉到指定
	// 节点——专用构建节点带污点时，nodeSelector 选节点、tolerations 容忍污点。
	// 两者都会写进 PipelineRun 的 taskRunTemplate.podTemplate（Tekton 会传播给
	// assistant 与任务 Pod）。注意：改为固定节点后，存量项目工作区 PVC 里的 PV
	// 仍钉在旧节点（local-path 按首次消费者绑定），需删除重建 PVC 才会迁移。
	NodeSelector map[string]string `yaml:"nodeSelector"`
	Tolerations  []CIToleration    `yaml:"tolerations"`

	// ServerURL 平台在集群内的服务地址（如 http://kube-console.kube-console.svc:8080）。
	// 镜像构建器的制品下载走它：任务 Pod 里的 artifact-fetch 节点凭访问令牌
	// 回调平台接口取上传产物。未配置时构建器运行报明确错误（不静默）。
	ServerURL string `yaml:"serverUrl"`
	// BuilderUploadDir 镜像构建器上传产物的存放目录（默认 ./data/ci-artifacts）。
	// 与 sqlite 同盘语义：换目录请同步迁移存量文件。
	BuilderUploadDir string `yaml:"builderUploadDir"`
	// BuilderUploadMaxMB 单个上传制品的大小上限（MB，默认 512）。
	BuilderUploadMaxMB int `yaml:"builderUploadMaxMB"`
}

// CIToleration 一条污点容忍（形状同 corev1.Toleration 的常用子集）。
// Operator 空按 Exists；Effect 空 = 匹配全部效果。
type CIToleration struct {
	Key      string `yaml:"key" json:"key"`
	Operator string `yaml:"operator" json:"operator"`
	Value    string `yaml:"value" json:"value"`
	Effect   string `yaml:"effect" json:"effect"`
}

type K8sConfig struct {
	// DebugImage 终端调试容器镜像（无 shell 的 distroless 容器注入用；内网集群需改为私有仓库镜像）
	DebugImage string `yaml:"debugImage"`
}

type ServerConfig struct {
	Port     int    `yaml:"port"`
	LogLevel string `yaml:"logLevel"` // debug | info | warn | error（默认 info）
}

type DatabaseConfig struct {
	// Driver: sqlite | mysql | postgres
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type JWTConfig struct {
	Secret   string `yaml:"secret"`
	ExpireIn int    `yaml:"expireIn"` // 有效期（小时）
}

type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Load 从指定路径加载配置，不存在时使用默认值；支持环境变量覆盖（KC_ 前缀）
func Load(path string) (*Config, error) {
	cfg := defaultConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Server:   ServerConfig{Port: 8080, LogLevel: "info"},
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/kube-console.db"},
		JWT:      JWTConfig{Secret: "kube-console-dev-secret", ExpireIn: 24},
		Admin:    AdminConfig{Username: "admin", Password: "admin123"},
		K8s:      K8sConfig{DebugImage: "busybox:1.36"},
		CI: CIConfig{
			Enabled:         true,
			RunTimeout:      "2h",
			PVCSize:         "10Gi",
			CacheEnabled:    true,
			ServiceAccount:  "ci-bot",
			ImagePullSecret: "harbor",
			PlatformNS:      "ci-platform",
			ImageRegistry:   "harbor.cqyxpt.site:8443/library",
		},
		Obs: ObservabilityConfig{
			AlertIntervalSec:   60,
			AlertRetentionDays: 90,
			EventIntervalSec:   60,
			EventRetentionDays: 30,
		},
		Nacos: NacosConfigYaml{
			SyncIntervalSec: 300,
			WebhookPort:     9443,
			SkipNamespaces:  "kube-system,kube-public,kube-node-lease,kube-console",
			CertDir:         "./data",
		},
	}
}

func applyEnv(cfg *Config) error {
	cfg.Server.Port = envInt("KC_SERVER_PORT", cfg.Server.Port)
	if v := os.Getenv("KC_LOG_LEVEL"); v != "" {
		cfg.Server.LogLevel = v
	}
	if v := os.Getenv("KC_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
	}
	if v := os.Getenv("KC_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("KC_JWT_SECRET"); v != "" {
		cfg.JWT.Secret = v
	}
	cfg.JWT.ExpireIn = envInt("KC_JWT_EXPIRE_IN", cfg.JWT.ExpireIn)
	if v := os.Getenv("KC_ADMIN_USERNAME"); v != "" {
		cfg.Admin.Username = v
	}
	if v := os.Getenv("KC_ADMIN_PASSWORD"); v != "" {
		cfg.Admin.Password = v
	}
	if v := os.Getenv("KC_K8S_DEBUG_IMAGE"); v != "" {
		cfg.K8s.DebugImage = v
	}
	if v := os.Getenv("KC_CI_ENABLED"); v == "0" || v == "false" {
		cfg.CI.Enabled = false
	}
	if v := os.Getenv("KC_CI_RUN_TIMEOUT"); v != "" {
		cfg.CI.RunTimeout = v
	}
	if v := os.Getenv("KC_CI_PVC_SIZE"); v != "" {
		cfg.CI.PVCSize = v
	}
	if v := os.Getenv("KC_CI_SERVICE_ACCOUNT"); v != "" {
		cfg.CI.ServiceAccount = v
	}
	if v := os.Getenv("KC_CI_IMAGE_PULL_SECRET"); v != "" {
		cfg.CI.ImagePullSecret = v
	}
	if v := os.Getenv("KC_CI_IMAGE_REGISTRY"); v != "" {
		cfg.CI.ImageRegistry = v
	}
	if v := os.Getenv("KC_CI_REGISTRY_CA_FILE"); v != "" {
		cfg.CI.RegistryCAFile = v
	}
	if v := os.Getenv("KC_CI_SERVER_URL"); v != "" {
		cfg.CI.ServerURL = v
	}
	if v := os.Getenv("KC_CI_BUILDER_UPLOAD_DIR"); v != "" {
		cfg.CI.BuilderUploadDir = v
	}
	if v := os.Getenv("KC_CI_BUILDER_UPLOAD_MAX_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CI.BuilderUploadMaxMB = n
		}
	}
	if v := os.Getenv("KC_CI_PLATFORM_NAMESPACE"); v != "" {
		cfg.CI.PlatformNS = v
	}
	if v := os.Getenv("KC_CI_NODE_SELECTOR"); v != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return fmt.Errorf("KC_CI_NODE_SELECTOR 不是合法的 JSON 对象（如 {\"kubernetes.io/hostname\":\"node-a\"}）: %w", err)
		}
		cfg.CI.NodeSelector = m
	}
	if v := os.Getenv("KC_CI_TOLERATIONS"); v != "" {
		var ts []CIToleration
		if err := json.Unmarshal([]byte(v), &ts); err != nil {
			return fmt.Errorf("KC_CI_TOLERATIONS 不是合法的 JSON 数组（如 [{\"key\":\"type\",\"operator\":\"Exists\"}]）: %w", err)
		}
		cfg.CI.Tolerations = ts
	}
	if v := os.Getenv("KC_CI_NEXUS_USERNAME"); v != "" {
		cfg.CI.NexusUsername = v
	}
	if v := os.Getenv("KC_CI_NEXUS_PASSWORD"); v != "" {
		cfg.CI.NexusPassword = v
	}
	if v := os.Getenv("KC_CI_MINIO_ACCESS_KEY"); v != "" {
		cfg.CI.MinIOAccessKey = v
	}
	if v := os.Getenv("KC_CI_MINIO_SECRET_KEY"); v != "" {
		cfg.CI.MinIOSecretKey = v
	}
	cfg.Obs.AlertIntervalSec = envInt("KC_OBS_ALERT_INTERVAL", cfg.Obs.AlertIntervalSec)
	cfg.Obs.AlertRetentionDays = envInt("KC_OBS_ALERT_RETENTION_DAYS", cfg.Obs.AlertRetentionDays)
	cfg.Obs.EventIntervalSec = envInt("KC_OBS_EVENT_INTERVAL", cfg.Obs.EventIntervalSec)
	cfg.Obs.EventRetentionDays = envInt("KC_OBS_EVENT_RETENTION_DAYS", cfg.Obs.EventRetentionDays)
	cfg.Nacos.SyncIntervalSec = envInt("KC_NACOS_SYNC_INTERVAL", cfg.Nacos.SyncIntervalSec)
	cfg.Nacos.WebhookPort = envInt("KC_NACOS_WEBHOOK_PORT", cfg.Nacos.WebhookPort)
	if v := os.Getenv("KC_NACOS_SKIP_NAMESPACES"); v != "" {
		cfg.Nacos.SkipNamespaces = v
	}
	if v := os.Getenv("KC_NACOS_CERT_DIR"); v != "" {
		cfg.Nacos.CertDir = v
	}
	// 默认关，所以只认显式打开（写成 KC_RBAC_ENFORCE_MENUS=0 也还是关，不会因为
	// 手滑填了个奇怪的值就把接口收口打开、把没授权的用户挡在外面）
	if v := os.Getenv("KC_RBAC_ENFORCE_MENUS"); v == "1" || v == "true" {
		cfg.RBAC.EnforceMenus = true
	}
	return nil
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// DefaultConfigPath 返回默认配置文件路径（优先 configs/config.yaml）
func DefaultConfigPath() string {
	for _, p := range []string{
		"configs/config.yaml",
		"../configs/config.yaml",
		"configs/config.example.yaml",
		"../configs/config.example.yaml",
	} {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	return "configs/config.example.yaml"
}
