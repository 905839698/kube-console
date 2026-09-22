// RBAC 权限模型（KubeSphere 式层级角色）：平台 / 集群 / 项目三层，
// 内置角色 + 自定义角色，授权=「主体-角色-范围」绑定记录（控制台为准，
// 集群/项目层绑定同时物化为 K8s RBAC 供 kubectl 侧生效）。
package model

import "time"

// 授权层级
const (
	LevelPlatform = "platform" // 平台层：控制台自身管理权限（用户/授权/集群注册等平台管理）
	LevelCluster  = "cluster"  // 集群层：某集群的集群级操作（节点运维、命名空间增删等）
	LevelProject  = "project"  // 项目层：命名空间内资源读写
)

// 绑定主体类型
const (
	GranteeUser  = "user"
	GranteeGroup = "group"
)

// Role 角色注册表：内置角色启动时 seed（Builtin=true，权限语义在代码里定义，
// Rules 仅为展示摘要）；自定义角色由平台管理员创建，Rules 为授权项 JSON。
type Role struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Level       string    `gorm:"size:16;not null;index" json:"level"` // platform | cluster | project
	DisplayName string    `gorm:"size:128" json:"displayName"`
	Description string    `gorm:"size:512" json:"description"`
	Builtin     bool      `gorm:"default:false" json:"builtin"`
	Rules       string    `gorm:"type:text" json:"rules"` // JSON: [{"resources":["pods",...],"verbs":["get",...]}]；内置角色为空
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// RbacBinding 控制台授权记录：把某层级的角色授予用户或用户组。
// project 层 Namespaces 为逗号分隔列表或 "*"（全部命名空间，物化为 ClusterRoleBinding）；
// cluster 层 Cluster 必填；platform 层两者为空。
type RbacBinding struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Level       string    `gorm:"size:16;not null;index" json:"level"`
	Cluster     string    `gorm:"size:128;not null;index" json:"cluster"`
	RoleName    string    `gorm:"size:128;not null" json:"roleName"`
	GranteeType string    `gorm:"size:16;not null;default:user" json:"granteeType"` // user | group
	GranteeName string    `gorm:"size:128;not null;index" json:"granteeName"`
	Namespaces  string    `gorm:"size:1024" json:"namespaces"` // project 层范围；"*"=全部
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// RuleItem 自定义角色的一个授权项（一组资源 × 一组动词）
type RuleItem struct {
	APIGroup  string   `json:"apiGroup,omitempty"` // "" = core group
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}
