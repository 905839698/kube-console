// Package model 定义 GORM 数据模型
package model

import "time"

// User 平台用户（role: admin | user）
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	Role         string    `gorm:"size:16;default:user" json:"role"` // admin=平台管理员
	GroupID      uint      `gorm:"index;default:0" json:"groupId"`   // 所属用户组（0=未分组）
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// 角色常量
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// AuditLog 操作审计：记录认证后的写操作（非 GET 请求）与关键行为
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"index;size:64" json:"username"`
	Method    string    `gorm:"size:8" json:"method"`
	Path      string    `gorm:"size:512" json:"path"`
	Query     string    `gorm:"size:512" json:"query"`
	Cluster   string    `gorm:"size:128" json:"cluster"`
	Status    int       `json:"status"`
	LatencyMs int64     `json:"latencyMs"`
	IP        string    `gorm:"size:64" json:"ip"`
	Body      string    `gorm:"type:text" json:"body"` // 请求体摘要（截断，敏感字段打码）
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Cluster 已注册的 Kubernetes 集群
type Cluster struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	Name            string     `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Kubeconfig      string     `gorm:"type:text;not null" json:"-"`
	Server          string     `gorm:"size:512" json:"server"`
	Context         string     `gorm:"size:128" json:"context"`
	Status          string     `gorm:"size:32;default:unknown" json:"status"` // connected | error | unknown
	ErrorMessage    string     `gorm:"size:1024" json:"errorMessage"`
	LastConnectedAt *time.Time `json:"lastConnectedAt"`
	// Prometheus 监控配置（通过 kube-apiserver proxy 访问）
	PrometheusNamespace string `gorm:"size:128" json:"prometheusNamespace"`
	PrometheusService   string `gorm:"size:128" json:"prometheusService"`
	PrometheusPort      int    `gorm:"default:9090" json:"prometheusPort"`
	// Grafana 地址（iframe 直连内嵌，如 http://grafana.kuboard:3000）
	GrafanaURL string `gorm:"size:512" json:"grafanaURL"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// 集群状态常量
const (
	ClusterStatusConnected = "connected"
	ClusterStatusError     = "error"
	ClusterStatusUnknown   = "unknown"
)

// HelmRepo Helm chart 仓库源（全局配置，跨集群共享）
type HelmRepo struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"uniqueIndex;size:128;not null" json:"name"`
	URL       string    `gorm:"size:512;not null" json:"url"`
	Username  string    `gorm:"size:128" json:"username"`
	Password  string    `gorm:"size:255" json:"-"` // 私有仓库认证，不输出
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// ------------------- 可观测性与平台扩展配置 -------------------

// LogSource 集群日志源（ES 经 apiserver service proxy 访问）
type LogSource struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ClusterName string `gorm:"uniqueIndex;size:128;not null" json:"clusterName"`
	Namespace   string `gorm:"size:128;not null" json:"namespace"`  // ES 所在命名空间
	Service     string `gorm:"size:128;not null" json:"service"`    // ES service 名
	Port        int    `gorm:"not null" json:"port"`                // ES 端口
	DirectURL   string `gorm:"size:255" json:"directURL"`           // 直连地址（如 http://节点IP:NodePort）。apiserver 代理会剥离 Authorization 头，开启安全的 ES 必须走直连
	IndexPrefix string `gorm:"size:128" json:"indexPrefix"`         // 索引前缀，如 logstash-
	Username    string `gorm:"size:128" json:"username"`            // ES basic auth（可选）
	Password    string `gorm:"size:255" json:"-"`                   // 不回显
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	// 事件归档：把 K8s Events 持久化到同一 ES（复用本条的连接与认证）
	EventEnabled     bool   `gorm:"default:false" json:"eventEnabled"`
	EventIndexPrefix string `gorm:"size:64;default:kc-events-" json:"eventIndexPrefix"`
	UpdatedAt   time.Time `json:"updatedAt"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AlertmanagerConfig 集群 Alertmanager 访问配置（一集群一行；为空表示该集群未接入 AM）
type AlertmanagerConfig struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ClusterName string `gorm:"uniqueIndex;size:128;not null" json:"clusterName"`
	Namespace   string `gorm:"size:128;not null" json:"namespace"`  // AM 所在命名空间
	Service     string `gorm:"size:128;not null" json:"service"`    // AM service 名
	Port        int    `gorm:"not null;default:9093" json:"port"`   // AM 端口（默认 9093）
	DirectURL   string `gorm:"size:512" json:"directURL"`           // 直连地址（apiserver 代理不可达时）
	Insecure    bool   `gorm:"default:false" json:"insecure"`       // 直连自签证书时跳过 TLS 校验（默认关闭）
	// ConfigSecret AM 主配置所在 Secret（"ns/name"，如 monitoring/alertmanager-main-generated），
	// 配置后可在界面在线编辑 alertmanager.yaml（含 .gz 格式自动处理）
	ConfigSecret string `gorm:"size:255" json:"configSecret"`
	UpdatedAt    time.Time `json:"updatedAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

// 告警状态常量
const (
	AlertStateFiring   = "firing"
	AlertStateResolved = "resolved"
)

// AlertEvent 告警历史（Alertmanager 轮询归档）：一次触发→恢复为一条记录，同指纹重复触发新开记录
type AlertEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Cluster     string    `gorm:"index;size:128;not null" json:"cluster"`
	Fingerprint string    `gorm:"index;size:64;not null" json:"fingerprint"`
	AlertName   string    `gorm:"index;size:255" json:"alertName"`
	Severity    string    `gorm:"size:16" json:"severity"`
	Namespace   string    `gorm:"index;size:128" json:"namespace"`
	Labels      string    `gorm:"type:text" json:"labels"` // 原始 labels JSON
	State       string    `gorm:"index;size:16;not null" json:"state"`
	StartedAt   time.Time `json:"startedAt"`
	ResolvedAt  *time.Time `json:"resolvedAt"`
	LastSeenAt  time.Time `gorm:"index" json:"lastSeenAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NacosConfig 集群 Nacos 接入配置（一集群一行）：地址 + 管理凭证 + 注入参数
type NacosConfig struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	ClusterName   string `gorm:"uniqueIndex;size:128;not null" json:"clusterName"`
	Addr          string `gorm:"size:255;not null" json:"addr"`     // 如 http://nacos.mid-platform:8848
	AdminUsername string `gorm:"size:128" json:"adminUsername"`     // Nacos 管理账号（nacos），空 = 匿名（未开启鉴权）
	AdminPassword string `gorm:"size:255" json:"-"`                 // 不回显
	Enabled       bool   `gorm:"default:false" json:"enabled"`      // 启用 ns 自动同步
	// Pod 注入（Admission Webhook）
	InjectEnabled bool   `gorm:"default:false" json:"injectEnabled"` // 维护 MutatingWebhookConfiguration
	AutoLabel     bool   `gorm:"default:true" json:"autoLabel"`      // 同步时给 ns 打 nacos-injection=enabled 标签
	WebhookMode   string `gorm:"size:16;default:url" json:"webhookMode"` // url | service
	WebhookURL    string `gorm:"size:512" json:"webhookURL"`             // url 模式：https://控制台:9443/inject（apiserver 可达）
	WebhookServiceNS   string `gorm:"size:128" json:"webhookServiceNS"`   // service 模式：控制台所在 ns
	WebhookServiceName string `gorm:"size:128" json:"webhookServiceName"` // service 模式：Service 名
	WebhookServicePort int    `gorm:"default:9443" json:"webhookServicePort"`
	UpdatedAt    time.Time `json:"updatedAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

// NacosNamespace K8s ns ↔ Nacos ns 映射与凭据（同步产物；K8s ns 删除后标记保留，防误删 Nacos 侧配置）
type NacosNamespace struct {
	ID               uint   `gorm:"primaryKey" json:"id"`
	ClusterName      string `gorm:"index;size:128;not null" json:"clusterName"`
	K8sNamespace     string `gorm:"size:128;not null" json:"k8sNamespace"`
	NacosNamespaceId string `gorm:"size:128" json:"nacosNamespaceId"` // = K8s ns 名
	Username         string `gorm:"size:128" json:"username"`
	Password         string `gorm:"size:255" json:"-"` // 随机生成，注入 Secret 用；界面可查看
	Status           string `gorm:"size:16;default:synced" json:"status"` // synced | deleted-in-k8s | error
	Error            string `gorm:"size:512" json:"error"`
	SyncedAt         time.Time `json:"syncedAt"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (NacosNamespace) TableName() string { return "nacos_namespaces" }

// NotifyChannel 告警通知渠道（钉钉机器人 / 通用 webhook）
type NotifyChannel struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Type      string    `gorm:"size:16;not null;default:dingtalk" json:"type"` // dingtalk | webhook
	Webhook   string    `gorm:"size:512;not null" json:"webhook"`
	Secret    string    `gorm:"size:255" json:"-"` // 钉钉加签密钥，不回显
	MinSeverity string  `gorm:"size:16;default:warning" json:"minSeverity"` // 只推送 >= 该级别
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// NotifyLog 通知发送记录（幂等去重后落库）
type NotifyLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ChannelID   uint      `gorm:"index" json:"channelId"`
	ChannelName string    `gorm:"size:128" json:"channelName"`
	Cluster     string    `gorm:"size:128" json:"cluster"`
	AlertName   string    `gorm:"size:255" json:"alertName"`
	Severity    string    `gorm:"size:16" json:"severity"`
	Message     string    `gorm:"type:text" json:"message"`
	Fingerprint string    `gorm:"index;size:64" json:"fingerprint"`
	Ok          bool      `json:"ok"`
	Error       string    `gorm:"size:512" json:"error"`
	SentAt      time.Time `gorm:"index" json:"sentAt"`
}

// ApiToken 长期 API 凭证（脚本/CI 调用），明文仅创建时返回一次
type ApiToken struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:128;not null" json:"name"`
	TokenHash  string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	UserID     uint       `gorm:"index;not null" json:"userId"`
	Username   string     `gorm:"index;size:64;not null" json:"username"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Revoked    bool       `gorm:"default:false" json:"revoked"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// UserGroup 用户组：命名空间授权可直接授予组（K8s RoleBinding Subject Kind=Group）
type UserGroup struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

// RegistryConfig 镜像仓库连接配置（Harbor v2 API，全局单行）
type RegistryConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	URL       string    `gorm:"size:255;not null" json:"url"` // 如 https://harbor.cqyun.pt.site:8443
	Username  string    `gorm:"size:128" json:"username"`
	Password  string    `gorm:"size:255" json:"-"`
	Insecure  bool      `gorm:"default:true" json:"insecure"` // 自签证书跳过校验
	UpdatedAt time.Time `json:"updatedAt"`
}

// KubeconfigServerHost 从 kubeconfig server 字段提取 apiserver 地址（host:port）
func (cl *Cluster) KubeconfigServerHost() string {
	if cl.Server != "" {
		return cl.Server
	}
	return ""
}

// CIIntegration ci-platform 集成配置（全局单行：地址 + 平台服务账号）
type CIIntegration struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	BaseURL   string    `gorm:"size:255;not null" json:"baseURL"` // API 地址，如 http://localhost:8090
	WebURL    string    `gorm:"size:255" json:"webURL"`           // ci-platform 前端地址（设计器/详情跳转）
	Username  string    `gorm:"size:128" json:"username"`
	Password  string    `gorm:"size:255" json:"-"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}
