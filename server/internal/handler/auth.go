// Package handler HTTP 处理层
package handler

import (
	"crypto/subtle"
	"strconv"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/pkg/response"
)

// 登录失败锁定：同一用户名连续失败 5 次，锁定 10 分钟
const (
	maxLoginFails   = 5
	loginLockWindow = 10 * time.Minute
)

var loginFails = struct {
	sync.Mutex
	m map[string]*loginFailState
}{m: map[string]*loginFailState{}}

type loginFailState struct {
	count     int
	lockUntil time.Time
}

// passwordComplexity 至少 8 位且同时包含字母和数字
var passwordComplexity = regexp.MustCompile(`^[A-Za-z0-9!@#$%^&*()_+\-=\[\]{};:,.<>?/\\|~]+$`)

func passwordValid(p string) bool {
	if len(p) < 8 || len(p) > 64 || !passwordComplexity.MatchString(p) {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, ch := range p {
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z':
			hasLetter = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// auditLogin 登录成功/失败写审计（公开路由无中间件，这里手动记录）
func auditLogin(db *gorm.DB, username string, ok bool, c *gin.Context) {
	status := 200
	if !ok {
		status = 401
	}
	db.Create(&model.AuditLog{
		Username: username, Method: "POST", Path: "/api/auth/login",
		Status: status, LatencyMs: 0, IP: c.ClientIP(),
		Body:    `{"event":"login","result":"` + map[bool]string{true: "success", false: "failed"}[ok] + `"}`,
		CreatedAt: time.Now(),
	})
}

// AuthHandler 认证接口
type AuthHandler struct {
	db      *gorm.DB
	cfg     *config.Config
}

func NewAuthHandler(db *gorm.DB, cfg *config.Config) *AuthHandler {
	return &AuthHandler{db: db, cfg: cfg}
}

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 管理员登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "用户名和密码不能为空")
		return
	}
	// 失败锁定检查
	loginFails.Lock()
	st, locked := loginFails.m[req.Username]
	if locked && st.lockUntil.After(time.Now()) {
		loginFails.Unlock()
		remaining := int(time.Until(st.lockUntil).Minutes()) + 1
		auditLogin(h.db, req.Username, false, c)
		response.Fail(c, http.StatusTooManyRequests, 429, "失败次数过多，账号已锁定，请约 "+strconv.Itoa(remaining)+" 分钟后重试")
		return
	}
	loginFails.Unlock()

	ok := false
	defer func() { auditLogin(h.db, req.Username, ok, c) }()

	fail := func() {
		loginFails.Lock()
		s := loginFails.m[req.Username]
		if s == nil {
			s = &loginFailState{}
			loginFails.m[req.Username] = s
		}
		s.count++
		if s.count >= maxLoginFails {
			s.lockUntil = time.Now().Add(loginLockWindow)
			s.count = 0
		}
		loginFails.Unlock()
		response.Fail(c, 401, 401, "用户名或密码错误")
	}

	user := &model.User{}
	isFallbackAdmin := false
	if err := h.db.Where("username = ?", req.Username).First(user).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			response.ServerError(c, err)
			return
		}
		// 兜底：与初始 admin 密码比较，防止种子化失败导致无法登录
		if h.cfg.Admin.Username == req.Username && subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.cfg.Admin.Password)) == 1 {
			user.ID, user.Username, user.Role = 1, h.cfg.Admin.Username, model.RoleAdmin
			isFallbackAdmin = true
		} else {
			fail()
			return
		}
	}
	if !isFallbackAdmin {
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
			fail()
			return
		}
		if user.Role == "" {
			user.Role = model.RoleUser
		}
	}
	// 登录成功，清除失败计数
	loginFails.Lock()
	delete(loginFails.m, req.Username)
	loginFails.Unlock()
	ok = true
	token, err := middleware.GenerateToken(user.ID, user.Username, h.cfg.JWT.Secret, h.cfg.JWT.ExpireIn)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, gin.H{"token": token, "username": user.Username, "role": user.Role})
}

// Me 当前登录用户信息
func (h *AuthHandler) Me(c *gin.Context) {
	role := model.RoleUser
	if middleware.IsAdmin(h.db, h.cfg.Admin.Username, middleware.CurrentUserID(c), middleware.CurrentUser(c)) {
		role = model.RoleAdmin
	}
	response.OK(c, gin.H{"username": middleware.CurrentUser(c), "role": role})
}

// ChangePassword 当前用户修改自己的密码
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword" binding:"required"`
		NewPassword string `json:"newPassword" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if !passwordValid(req.NewPassword) {
		response.Fail(c, 400, 400, "新密码至少 8 位且需同时包含字母和数字")
		return
	}
	user := &model.User{}
	if err := h.db.First(user, middleware.CurrentUserID(c)).Error; err != nil {
		response.Fail(c, 400, 400, "用户不存在")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)) != nil {
		response.Fail(c, 400, 400, "原密码错误")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	if err := h.db.Model(user).Update("password_hash", string(hash)).Error; err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, nil)
}
