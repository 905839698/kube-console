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

		// 写操作截取请求体摘要（截断 2KB、敏感字段打码）供审计追溯
		body := ""
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" && c.Request.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(c.Request.Body, 4*1024))
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

// IsAdmin 判断指定用户是否平台管理员
func IsAdmin(db *gorm.DB, fallbackAdmin string, uid uint, username string) bool {
	if username == fallbackAdmin {
		return true
	}
	if uid == 0 {
		return false
	}
	var u model.User
	if err := db.First(&u, uid).Error; err != nil {
		return false
	}
	return u.Role == model.RoleAdmin
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
