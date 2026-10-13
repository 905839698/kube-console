package model

import "time"

// 镜像构建器应用类型（决定约定 Dockerfile 与产物形态校验）。
const (
	CIBuilderJava = "java" // Java 应用：上传 fat jar
	CIBuilderGo   = "go"   // Go 应用：上传编译好的二进制
	CIBuilderWeb  = "web"  // 前端应用：上传静态资源 tar.gz（内容 = dist 目录）
)

// CIBuilderAppTypes 前端下拉与校验共用。
var CIBuilderAppTypes = []string{CIBuilderJava, CIBuilderGo, CIBuilderWeb}

// CIImageBuilder 镜像构建器：上传本地构建产物 → 平台按应用类型的约定 Dockerfile
// 组装容器镜像并推送到镜像仓库（不经过 git 仓库、不在流水线里编译）。
//
// 每个构建器背后是一条平台托管的隐藏流水线（两个节点：artifact-fetch 从平台
// 拉回制品与 sha256 校验 → build-image 写约定 Dockerfile 并构建推送），
// 执行历史 / 日志 / 制品登记全部复用流水线既有设施：
//   - 运行记录：ci_pipeline_runs（按 pipelineId 过滤）
//   - 版本：ci_pipeline_versions（每次上传/改配置/改 tag 生成新 graph）
//
// 归属与权限口径与流水线一致（按项目 ns 三层 RBAC）。
type CIImageBuilder struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ClusterName string `gorm:"size:128;index" json:"clusterName"`
	ProjectID   uint   `gorm:"index" json:"projectId"`
	Name        string `gorm:"size:64;index:idx_ci_builder_proj" json:"name"` // 项目内唯一（服务层显式查重）

	AppType string `gorm:"size:16" json:"appType"` // java/go/web

	// 镜像产出
	Registry     string `gorm:"size:256" json:"registry"`   // 如 harbor.example.com
	Repository   string `gorm:"size:256" json:"repository"` // 如 library/demo
	CredentialID uint   `json:"credentialId"`               // 推送凭证（dockerconfig/basic）
	BaseImage    string `gorm:"size:512" json:"baseImage"`  // 空 = 平台按类型给默认基础镜像
	Port         int    `json:"port"`                       // EXPOSE；0 = 按类型默认（java/go 8080，web 80）
	JavaOpts     string `gorm:"size:512" json:"javaOpts"`   // 仅 java：JVM 参数（进 ENTRYPOINT 的 sh -c）
	EntryArgs    string `gorm:"size:512" json:"entryArgs"`  // 仅 java/go：容器启动参数（空格分隔，生成 Dockerfile CMD，追加在 ENTRYPOINT 后）
	DefaultTag   string `gorm:"size:128" json:"defaultTag"` // 构建弹窗预填 tag；空 = upload-<北京时间戳>

	// 当前制品（新上传替换旧的；历史追溯靠 run + 镜像 tag）
	FileName   string     `gorm:"size:256" json:"fileName"` // 用户视角的文件名（展示用）
	FileSize   int64      `json:"fileSize"`
	FileSHA256 string     `gorm:"size:64" json:"fileSha256"`
	UploadedAt *time.Time `json:"uploadedAt"`

	// PipelineID 平台托管流水线（构建器创建时自动生成，删除构建器时一并删除）
	PipelineID uint `gorm:"index" json:"pipelineId"`

	CreatedBy uint      `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (CIImageBuilder) TableName() string { return "ci_image_builders" }

// CIBuilderToken 制品下载令牌：任务 Pod 里的 artifact-fetch 凭它回平台下载
// 上传的制品。上传时轮转，旧令牌保留最近几个——进行中的 run 仍持有旧 URL，
// 立即失效会把排队后放行的 run 打挂（失败可见但白跑一趟）。
type CIBuilderToken struct {
	Token     string    `gorm:"primaryKey;size:64" json:"-"`
	BuilderID uint      `gorm:"index" json:"builderId"`
	CreatedAt time.Time `json:"createdAt"`
}

func (CIBuilderToken) TableName() string { return "ci_builder_tokens" }
