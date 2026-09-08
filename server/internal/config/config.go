// Package config 负责加载配置文件（支持环境变量覆盖）
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	JWT      JWTConfig      `yaml:"jwt"`
	Admin    AdminConfig    `yaml:"admin"`
	K8s      K8sConfig      `yaml:"k8s"`
	CI       CIConfig       `yaml:"ci"`
}

// CIConfig 内置 CI（Tekton）运行参数。节点插件随二进制 embed（server/nodes），无需路径配置。
type CIConfig struct {
	Enabled         bool   `yaml:"enabled"`           // 总开关（关则不启动后台同步循环）
	RunTimeout      string `yaml:"runTimeout"`        // 单次运行超时（默认 2h）
	PVCSize         string `yaml:"pvcSize"`           // 运行工作区 PVC 大小（默认 10Gi）
	PVCStorageClass string `yaml:"pvcStorageClass"`   // 空 = 集群默认（无默认 SC 的集群必须显式指定）
	CacheEnabled    bool   `yaml:"cacheEnabled"`      // 项目级构建缓存 PVC
	CacheSize       string `yaml:"cacheSize"`         // 缓存 PVC 大小（默认 20Gi）
	CacheAccessMode string `yaml:"cacheAccessMode"`   // ReadWriteOnce
	ServiceAccount  string `yaml:"serviceAccount"`    // Tekton 运行 SA（默认 ci-bot）
	ImagePullSecret string `yaml:"imagePullSecret"`   // PipelineRun podTemplate 的镜像拉取 Secret
	PlatformNS      string `yaml:"platformNamespace"` // 平台级凭据 Secret 所在 ns（默认 ci-platform）
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
}

type K8sConfig struct {
	// DebugImage 终端调试容器镜像（无 shell 的 distroless 容器注入用；内网集群需改为私有仓库镜像）
	DebugImage string `yaml:"debugImage"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
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
	applyEnv(cfg)
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Server:   ServerConfig{Port: 8080},
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/kube-console.db"},
		JWT:      JWTConfig{Secret: "kube-console-dev-secret", ExpireIn: 24},
		Admin:    AdminConfig{Username: "admin", Password: "admin123"},
		K8s:      K8sConfig{DebugImage: "busybox:1.36"},
		CI: CIConfig{
			Enabled:         true,
			RunTimeout:      "2h",
			PVCSize:         "10Gi",
			CacheEnabled:    true,
			CacheSize:       "20Gi",
			CacheAccessMode: "ReadWriteOnce",
			ServiceAccount:  "ci-bot",
			ImagePullSecret: "harbor",
			PlatformNS:      "ci-platform",
		},
	}
}

func applyEnv(cfg *Config) {
	cfg.Server.Port = envInt("KC_SERVER_PORT", cfg.Server.Port)
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
	if v := os.Getenv("KC_CI_PLATFORM_NAMESPACE"); v != "" {
		cfg.CI.PlatformNS = v
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
