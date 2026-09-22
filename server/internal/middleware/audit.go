// 审计与权限中间件
package middleware

import (
	"bytes"
	"io"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kube-console/server/internal/model"
)

// 审计保留天数（超期自动清理，防止 SQLite 无限膨胀）
const AuditRetentionDays = 90

// AuditRequired 审计中间件：记录认证后的非 GET 请求（谁、何时、对什么、结果、改了什么）。
// GET 请求量大会刷屏，不落库；审计查询本身也是 GET，天然排除。
func AuditRequired(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 写操作读取请求体并回填（截断仅在审计存储时进行）。
		// 限量 64KB：文件上传（multipart）的 body 是全量文件字节，ReadAll 会把
		// 整个文件读进内存（并发大上传放大内存压力）；multipart 只记 path 即可
		body := ""
		isMultipart := strings.HasPrefix(c.Request.Header.Get("Content-Type"), "multipart/")
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" &&
			c.Request.Body != nil && !isMultipart {
			buf, err := io.ReadAll(io.LimitReader(c.Request.Body, 64<<10))
			if err == nil {
				// 必须回填 Body 供后续 handler 读取
				c.Request.Body = io.NopCloser(bytes.NewBuffer(buf))
				body = sanitizeBody(string(buf))
			}
		}

		c.Next()

		if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
			return
		}
		// WebSocket 升级请求不走 HTTP 语义，跳过
		if strings.HasSuffix(c.Request.URL.Path, "/exec") {
			return
		}
		entry := model.AuditLog{
			Username:  CurrentUser(c),
			Method:    c.Request.Method,
			Path:      trunc(c.Request.URL.Path, 512),
			Query:     trunc(c.Request.URL.RawQuery, 512),
			Cluster:   trunc(ClusterName(c), 128),
			Status:    c.Writer.Status(),
			LatencyMs: time.Since(start).Milliseconds(),
			IP:        trunc(c.ClientIP(), 64),
			Body:      trunc(body, 2048),
		}
		db.Create(&entry)
	}
}

var sensitiveKeys = regexp.MustCompile(`(?i)"(password|oldPassword|newPassword|token|secret|kubeconfig|passwordHash)"\s*:\s*"[^"]*"`)

// sanitizeBody 敏感字段打码 + 压缩空白
func sanitizeBody(s string) string {
	s = sensitiveKeys.ReplaceAllString(s, `"$1":"***"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// CleanAuditLogs 清理超期审计记录
func CleanAuditLogs(db *gorm.DB) {
	cutoff := time.Now().AddDate(0, 0, -AuditRetentionDays)
	res := db.Where("created_at < ?", cutoff).Delete(&model.AuditLog{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("已清理 %d 条过期审计日志（保留 %d 天）", res.RowsAffected, AuditRetentionDays)
	}
}

// AdminRequired 平台管理员校验：按当前用户查库验证角色。
// 兜底：配置文件中的初始管理员始终视为 admin（防种子化失败锁死）。
func AdminRequired(db *gorm.DB, fallbackAdmin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAdmin(db, fallbackAdmin, CurrentUserID(c), CurrentUser(c)) {
			c.AbortWithStatusJSON(403, gin.H{"code": 403, "message": "需要平台管理员权限"})
			return
		}
		c.Next()
	}
}

// PlatformScoped 平台管理接口守卫（KubeSphere 式层级角色）：
// GET 读取对 platform-admin / platform-viewer 放行（平台管理只读）；
// 写操作仍需 platform-admin。
func PlatformScoped(db *gorm.DB, fallbackAdmin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, name := CurrentUserID(c), CurrentUser(c)
		if IsAdmin(db, fallbackAdmin, uid, name) {
			c.Next()
			return
		}
		if c.Request.Method == "GET" && IsPlatformViewer(db, uid, name) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(403, gin.H{"code": 403, "message": "需要平台管理员权限（platform-viewer 仅可查看）"})
	}
}

// platformBound 判断用户（含组继承）是否绑定了某平台层角色
func platformBound(db *gorm.DB, uid uint, username, role string) bool {
	if db == nil || username == "" {
		return false
	}
	var u model.User
	groupCond := ""
	groupArgs := []any{}
	if uid > 0 && db.First(&u, uid).Error == nil && u.GroupID > 0 {
		var g model.UserGroup
		if db.First(&g, u.GroupID).Error == nil && g.Name != "" {
			groupCond = " OR (grantee_type = ? AND grantee_name = ?)"
			groupArgs = []any{model.GranteeGroup, g.Name}
		}
	}
	var n int64
	db.Model(&model.RbacBinding{}).
		Where("level = ? AND role_name = ? AND ((grantee_type = ? AND grantee_name = ?)"+groupCond+")",
			append([]any{model.LevelPlatform, role, model.GranteeUser, username}, groupArgs...)...).
		Count(&n)
	return n > 0
}

// IsPlatformViewer 判断用户是否 platform-viewer（平台管理只读）
func IsPlatformViewer(db *gorm.DB, uid uint, username string) bool {
	return platformBound(db, uid, username, "platform-viewer")
}

// IsAdmin 判断指定用户是否平台管理员：User.Role=admin / 兜底账号 /
// platform-admin 角色绑定（含组继承）。
func IsAdmin(db *gorm.DB, fallbackAdmin string, uid uint, username string) bool {
	if username == fallbackAdmin {
		return true
	}
	if uid == 0 {
		return false
	}
	var u model.User
	if err := db.First(&u, uid).Error; err == nil && u.Role == model.RoleAdmin {
		return true
	}
	return platformBound(db, uid, username, "platform-admin")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
