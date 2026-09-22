package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// ============ CI（由 ci-platform 融合而来，多集群 + 命名空间隔离） ============
//
// 隔离模型：
//   - 集群隔离：Project/Pipeline/Credential/Run 均带 ClusterName（哪个 K8s 集群）
//   - 命名空间隔离：Project.Namespace 是该项目的 K8s 命名空间，Tekton
//     Pipeline/PipelineRun/工作区 PVC/凭证 Secret 全部落在项目 ns 内
//
// 表名 GORM 默认复数：ci_projects / ci_pipelines / ci_pipeline_versions /
// ci_pipeline_run_counters / ci_pipeline_runs / ci_task_runs / ci_credentials。
// 硬删除（无软删），唯一索引保持普通索引（跨驱动兼容）。

// 项目角色
const (
	CIProjRoleAdmin  = "admin"
	CIProjRoleDev    = "developer"
	CIProjRoleReader = "reader"
)

// 运行状态
const (
	CIRunStatusPending   = "pending"
	CIRunStatusRunning   = "running"
	CIRunStatusSuccess   = "success"
	CIRunStatusFailed    = "failed"
	CIRunStatusCancelled = "cancelled"
	CIRunStatusSkipped   = "skipped" // 条件分支未命中的任务（Tekton when 表达式为假）
)

// 凭证 form（密钥形态），驱动 K8s Secret 的数据结构与运行时注入策略。
const (
	CIFormBasic        = "basic"        // 账密：username/password
	CIFormToken        = "token"        // 单 token
	CIFormDockerconfig = "dockerconfig" // 整包 .dockerconfigjson
	CIFormKubeconfig   = "kubeconfig"   // kubeconfig 文件内容（部署到目标集群）
	CIFormAKSK         = "aksk"         // accessKey/secretKey
	CIFormRaw          = "raw"          // 任意 key-value（cosign 公钥、ssh key 等）
)

// CIProject 流水线项目 = 一个 K8s 命名空间（同一集群内名称唯一）
type CIProject struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ClusterName string    `gorm:"size:128;index" json:"clusterName"`
	Name        string    `gorm:"size:64" json:"name"`
	DisplayName string    `gorm:"size:128" json:"displayName"`
	Description string    `gorm:"size:512" json:"description"`
	Namespace   string    `gorm:"size:64" json:"namespace"` // 项目对应的 K8s 命名空间（隔离单元）
	CreatedBy   uint      `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (CIProject) TableName() string { return "ci_projects" }

// CIPipeline 流水线（项目内名称唯一）
type CIPipeline struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ClusterName string    `gorm:"size:128;index" json:"clusterName"`
	ProjectID   uint      `gorm:"index:idx_ci_pipe_proj" json:"projectId"`
	Name        string    `gorm:"size:64;index:idx_ci_pipe_proj" json:"name"`
	Description string    `gorm:"size:512" json:"description"`
	Status      string    `gorm:"size:32;default:active" json:"status"` // active/disabled
	CreatedBy   uint      `json:"createdBy"`
	// LatestVersion 最新版本号（列表时填充，非持久化）
	LatestVersion int `gorm:"-" json:"latestVersion"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (CIPipeline) TableName() string { return "ci_pipelines" }

// CIPipelineVersion 保存 DSL（graph_json）与编译产物（compiled_yaml）。
// (pipeline_id, version) 复合唯一：版本号只在流水线内递增。
type CIPipelineVersion struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	PipelineID   uint      `gorm:"index;uniqueIndex:idx_ci_pipe_ver" json:"pipelineId"`
	Version      int       `gorm:"uniqueIndex:idx_ci_pipe_ver" json:"version"`
	GraphJSON    string    `gorm:"type:text" json:"graphJson"`   // CI Pipeline DSL
	CompiledYAML string    `gorm:"type:text" json:"compiledYaml"` // 编译后的 Tekton YAML
	CreatedBy    uint      `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (CIPipelineVersion) TableName() string { return "ci_pipeline_versions" }

// CIRunCounter 每流水线的 runNo 原子计数器（并发触发防撞号）
type CIRunCounter struct {
	PipelineID uint `gorm:"primaryKey" json:"pipelineId"`
	LastRunNo  int  `gorm:"default:0" json:"lastRunNo"`
}

func (CIRunCounter) TableName() string { return "ci_pipeline_run_counters" }

// CIRun 一次流水线运行（= 一个 Tekton PipelineRun）
type CIRun struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	ClusterName   string     `gorm:"size:128;index" json:"clusterName"`
	PipelineID    uint       `gorm:"index;uniqueIndex:idx_ci_run_no" json:"pipelineId"`
	VersionID     uint       `gorm:"index" json:"versionId"`
	RunNo         int        `gorm:"uniqueIndex:idx_ci_run_no" json:"runNo"` // #1024（与 PipelineID 复合唯一）
	Status        string     `gorm:"size:32;default:pending" json:"status"`
	TriggerType   string     `gorm:"size:32;default:manual" json:"triggerType"` // manual/webhook/schedule
	GitCommit     string     `gorm:"size:64" json:"gitCommit"`
	GitBranch     string     `gorm:"size:128" json:"gitBranch"`
	GitRepo       string     `gorm:"size:256" json:"gitRepo"`
	StartedBy     string     `gorm:"size:64" json:"startedBy"`
	TektonRunName string     `gorm:"size:128" json:"tektonRunName"` // 对应 k8s PipelineRun 名
	TektonPipelineCR string  `gorm:"size:128" json:"-"`             // 本 run 的 Pipeline CR 名（按 run 唯一，终态清理）
	PVCName       string     `gorm:"size:128" json:"pvcName"`       // 本 run 的共享 workspace PVC
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func (CIRun) TableName() string { return "ci_pipeline_runs" }

// CITaskRun 运行中的单个节点（= 一个 Tekton TaskRun）
type CITaskRun struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	RunID           uint       `gorm:"index" json:"runId"`
	NodeID          string     `gorm:"size:64;index" json:"nodeId"` // DSL 节点 id
	NodeType        string     `gorm:"size:64" json:"nodeType"`     // git-clone / trivy-scan ...
	Name            string     `gorm:"size:128" json:"name"`
	Status          string     `gorm:"size:32;default:pending" json:"status"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	TektonTaskRun   string     `gorm:"size:128" json:"tektonTaskRun"` // k8s TaskRun 名
	TektonPodName   string     `gorm:"size:128" json:"tektonPodName"` // TaskRun Pod 名（日志用）
	TektonNamespace string     `gorm:"size:64" json:"-"`
	Results         JSONMap    `gorm:"type:text" json:"results"` // TaskRun results 快照
}

func (CITaskRun) TableName() string { return "ci_task_runs" }

// CICredential 只存引用与元数据，不存明文。明文同步写入 K8s Secret（项目 ns 内）。
type CICredential struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	ClusterName string     `gorm:"size:128;index" json:"clusterName"`
	Name       string     `gorm:"size:64;index:idx_ci_cred_name" json:"name"`
	Form       string     `gorm:"size:32;default:basic" json:"form"` // basic/token/dockerconfig/kubeconfig/aksk/raw
	Type       string     `gorm:"size:32" json:"type"`               // 业务用途标签（git/registry/k8s/cloud/generic）
	SecretName string     `gorm:"size:128" json:"secretName"`        // k8s Secret 名
	SecretNS   string     `gorm:"size:64" json:"secretNs"`
	Extra      JSONObject `gorm:"type:text" json:"extra"` // 结构化非敏感元数据（host/region/endpoint/context）
	ProjectID  *uint      `gorm:"index" json:"projectId"`  // nil=平台级（所有项目可用）
	CreatedBy  uint       `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`

	// References 引用该凭据的流水线名（List 时填充，非持久化）
	References []string `gorm:"-" json:"references,omitempty"`
}

func (CICredential) TableName() string { return "ci_credentials" }

// CIGlobalVar 全局变量（集群级，跨项目复用；节点参数中以 ${global.KEY} 引用，
// StartRun 编译前注入。未定义的 key 原样保留——报错可见，不静默吞掉）。
type CIGlobalVar struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ClusterName string    `gorm:"size:128;index:idx_ci_gv_cluster_key,unique" json:"clusterName"`
	Key         string    `gorm:"size:128;index:idx_ci_gv_cluster_key,unique" json:"key"` // [A-Z0-9_]+
	Value       string    `gorm:"size:1024" json:"value"`
	Description string    `gorm:"size:256" json:"description"`
	CreatedBy   uint      `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (CIGlobalVar) TableName() string { return "ci_global_vars" }

// CISchedule 定时触发流水线（标准 5 段 cron；NextRunAt 30s tick 检查，
// fire 前原子占位防重复触发；瞬时失败 60s 快速重试）。
type CISchedule struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	ClusterName string         `gorm:"size:128;index" json:"clusterName"`
	PipelineID uint           `gorm:"index" json:"pipelineId"`
	ProjectID  uint           `gorm:"index" json:"projectId"`
	Cron       string         `gorm:"size:64" json:"cron"`
	Enabled    bool           `gorm:"default:true" json:"enabled"`
	LastRunAt  *time.Time     `json:"lastRunAt"`
	NextRunAt  *time.Time     `gorm:"index" json:"nextRunAt"`
	LastError  string         `gorm:"size:512" json:"lastError"`
	CreatedBy  uint           `json:"createdBy"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

func (CISchedule) TableName() string { return "ci_schedules" }

// CIWebhook 流水线 Webhook 触发（GitLab push）：token 走 URL 路径（公开路由），
// 交付记录保留最近 200 条。
type CIWebhook struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	ClusterName string         `gorm:"size:128;index" json:"clusterName"`
	PipelineID uint           `gorm:"index" json:"pipelineId"`
	ProjectID  uint           `gorm:"index" json:"projectId"`
	Token      string         `gorm:"size:64;uniqueIndex" json:"token"` // URL 触发凭据
	Branch     string         `gorm:"size:128" json:"branch"`           // 可选：仅该分支触发
	Enabled    bool           `gorm:"default:true" json:"enabled"`
	CreatedBy  uint           `json:"createdBy"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`

	// URL 由 handler 拼装（非持久化）
	URL string `gorm:"-" json:"url,omitempty"`
}

func (CIWebhook) TableName() string { return "ci_webhooks" }

// CIWebhookDelivery 触发交付记录（accepted/failed + 原因）。
type CIWebhookDelivery struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	WebhookID uint      `gorm:"index" json:"webhookId"`
	EventHash string    `gorm:"size:64;uniqueIndex" json:"-"` // 事件指纹去重（webhook|分支|commit）
	Branch    string    `gorm:"size:128" json:"branch"`
	Commit    string    `gorm:"size:64" json:"commit"`
	Status    string    `gorm:"size:32" json:"status"` // pending / accepted / failed
	Error     string    `gorm:"size:512" json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (CIWebhookDelivery) TableName() string { return "ci_webhook_deliveries" }

// ============ 制品 / 发布记录 ============

// 制品类型与存储类型常量。
const (
	CIArtImage   = "image"
	CIArtGeneric = "generic"
	CIArtMaven   = "maven"
	CIArtNpm     = "npm"
	CIArtZip     = "zip"

	CIStoHarbor = "harbor"
	CIStoNexus  = "nexus"
	CIStoMinIO  = "minio"
)

// CIArtifact 制品登记（任务成功时由 Registrar 落库）。
// 复合唯一 (pipeline_run_id, type, storage_path)：同一 run 内同类型同路径只登记一次。
type CIArtifact struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	ClusterName   string `gorm:"size:128;index" json:"clusterName"`
	ProjectID     uint   `gorm:"index" json:"projectId"`
	PipelineID    uint   `gorm:"index" json:"pipelineId"`
	PipelineRunID uint   `gorm:"index;uniqueIndex:idx_ci_art_dedup" json:"pipelineRunId"`
	Name          string `gorm:"size:256" json:"name"`
	Type          string `gorm:"size:32;uniqueIndex:idx_ci_art_dedup" json:"type"`
	Version       string `gorm:"size:128" json:"version"`
	StorageType   string `gorm:"size:32" json:"storageType"`
	StoragePath   string `gorm:"size:512;uniqueIndex:idx_ci_art_dedup" json:"storagePath"` // harbor ref / nexus path / minio key
	Size          int64  `json:"size"`
	SHA256        string `gorm:"size:64" json:"sha256"`
	Digest        string `gorm:"size:128" json:"digest"` // 镜像 digest
	CreatedAt     time.Time `json:"createdAt"`
}

func (CIArtifact) TableName() string { return "ci_artifacts" }

// DeployTarget 发布记录里的一个 workload 镜像快照（容器名 → 镜像）。
type DeployTarget struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Images map[string]string `json:"images"`
}

// DeployTargetList JSON 序列化（驱动无关文本，sqlite/PG 通用）。
type DeployTargetList []DeployTarget

func (l DeployTargetList) Value() (driver.Value, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
func (l *DeployTargetList) Scan(v any) error {
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		return nil
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, l)
}
func (l DeployTargetList) MarshalJSON() ([]byte, error) { return json.Marshal([]DeployTarget(l)) }

// CIDeployment 发布记录：k8s-deploy/helm-deploy 任务成功时的镜像快照 + 一键回滚。
type CIDeployment struct {
	ID             uint             `gorm:"primaryKey" json:"id"`
	ClusterName    string           `gorm:"size:128;index" json:"clusterName"`
	ProjectID      uint             `gorm:"index" json:"projectId"`
	PipelineID     uint             `gorm:"index" json:"pipelineId"`
	RunID          uint             `gorm:"index" json:"runId"` // 回滚记录为 0
	NodeName       string           `gorm:"size:128" json:"nodeName"`
	Kind           string           `gorm:"size:32" json:"kind"` // k8s-deploy / helm-deploy / rollback
	Namespace      string           `gorm:"size:128" json:"namespace"`
	Release        string           `gorm:"size:128" json:"release"` // helm release（k8s-deploy 为空）
	Targets        DeployTargetList `gorm:"type:text" json:"targets"`
	Status         string           `gorm:"size:32" json:"status"` // success / failed
	GitCommit      string           `gorm:"size:64" json:"gitCommit"`
	GitBranch      string           `gorm:"size:128" json:"gitBranch"`
	RolledBackFrom *uint            `json:"rolledBackFrom"`
	CreatedBy      string           `gorm:"size:64" json:"createdBy"`
	CreatedAt      time.Time        `gorm:"index" json:"createdAt"`
}

func (CIDeployment) TableName() string { return "ci_deployments" }
