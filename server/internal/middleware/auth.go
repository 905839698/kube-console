// Package middleware 提供 JWT 认证等 Gin 中间件
package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"kube-console/server/internal/model"
)

const (
	// UserIDKey / UsernameKey 存放于 gin.Context 中的键
	UserIDKey   = "userID"
	UsernameKey = "username"
	// ClusterKey 存放当前请求选中的集群名
	ClusterKey = "clusterName"
)

// Claims JWT 载荷
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// GenerateToken 生成 JWT
func GenerateToken(userID uint, username, secret string, expireHours int) (string, error) {
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expireHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "kube-console",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// GenerateTokenWithTTL 生成任意 TTL 的 JWT（SSE/WS 短 token 用）。
func GenerateTokenWithTTL(userID uint, username, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "kube-console",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken 校验 JWT 并返回 Claims（供中间件与 WebSocket handler 复用）
func ParseToken(tokenStr, secret string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, err
	}
	return claims, nil
}

// AuthRequired 认证中间件：JWT 或长期 API Token（kc_pat_ 前缀，查库校验）
func AuthRequired(secret string, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "未登录或令牌格式错误"})
			return
		}
		token := strings.TrimPrefix(header, "Bearer ")
		if strings.HasPrefix(token, "kc_pat_") && db != nil {
			var t model.ApiToken
			sum := sha256.Sum256([]byte(token))
			if err := db.Where("token_hash = ? AND revoked = ?", hex.EncodeToString(sum[:]), false).First(&t).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "API Token 无效或已撤销"})
				return
			}
			if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "API Token 已过期"})
				return
			}
			now := time.Now()
			db.Model(&t).Update("last_used_at", &now)
			c.Set(UserIDKey, t.UserID)
			c.Set(UsernameKey, t.Username)
			c.Next()
			return
		}
		claims, err := ParseToken(token, secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "令牌无效或已过期"})
			return
		}
		c.Set(UserIDKey, claims.UserID)
		c.Set(UsernameKey, claims.Username)
		c.Next()
	}
}

// CurrentUser 从上下文取当前用户名
func CurrentUser(c *gin.Context) string {
	v, _ := c.Get(UsernameKey)
	s, _ := v.(string)
	return s
}

// CurrentUserID 从上下文取当前用户 ID
func CurrentUserID(c *gin.Context) uint {
	v, _ := c.Get(UserIDKey)
	id, _ := v.(uint)
	return id
}

// ClusterName 从 X-Cluster 头取集群名。
// 集群名可能含非 ASCII 字符（如中文“浪潮”）。浏览器遵循 Fetch 规范，会把 HTTP 头值中
// 非 ISO-8859-1 字符剥离为空，导致后端收到空头（“缺少 X-Cluster 请求头”）。
// 因此前端统一用 encodeURIComponent 编码后传输，这里再解码还原；ASCII 名称编码后不变，向后兼容。
func ClusterName(c *gin.Context) string {
	name := c.GetHeader("X-Cluster")
	if name == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(name); err == nil {
		return decoded
	}
	return name
}
