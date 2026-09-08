// 用户管理与审计查询接口（仅平台管理员）
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/middleware"
	"kube-console/server/internal/model"
	"kube-console/server/pkg/response"
)

// UserHandler 用户与审计
type UserHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewUserHandler(db *gorm.DB, cfg *config.Config) *UserHandler {
	return &UserHandler{db: db, cfg: cfg}
}

// List GET /users
func (h *UserHandler) List(c *gin.Context) {
	var users []model.User
	if err := h.db.Select("id, username, role, created_at, updated_at").Order("id").Find(&users).Error; err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, users)
}

type createUserReq struct {
	Username string `json:"username" binding:"required,min=2,max=64"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"`
}

// Create POST /users
func (h *UserHandler) Create(c *gin.Context) {
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误：用户名 2-64 字符")
		return
	}
	if !passwordValid(req.Password) {
		response.Fail(c, 400, 400, "密码至少 8 位且需同时包含字母和数字")
		return
	}
	role := model.RoleUser
	if req.Role == model.RoleAdmin {
		role = model.RoleAdmin
	}
	var count int64
	h.db.Model(&model.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		response.Fail(c, 400, 400, "用户名已存在")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	user := &model.User{Username: req.Username, PasswordHash: string(hash), Role: role}
	if err := h.db.Create(user).Error; err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, user)
}

// ResetPassword PUT /users/:id/password（管理员重置密码）
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if !passwordValid(req.Password) {
		response.Fail(c, 400, 400, "密码至少 8 位且需同时包含字母和数字")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.ServerError(c, err)
		return
	}
	res := h.db.Model(&model.User{}).Where("id = ?", id).Update("password_hash", string(hash))
	if res.Error != nil {
		response.ServerError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		response.Fail(c, 404, 404, "用户不存在")
		return
	}
	response.OK(c, nil)
}

// UpdateRole PUT /users/:id/role
func (h *UserHandler) UpdateRole(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Role string `json:"role" binding:"required,oneof=admin user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "角色只能是 admin 或 user")
		return
	}
	// 防止把唯一管理员降级
	if req.Role != model.RoleAdmin {
		var u model.User
		if err := h.db.First(&u, id).Error; err == nil && u.Role == model.RoleAdmin && u.Username == h.cfg.Admin.Username {
			response.Fail(c, 400, 400, "初始管理员不能降级")
			return
		}
	}
	res := h.db.Model(&model.User{}).Where("id = ?", id).Update("role", req.Role)
	if res.Error != nil {
		response.ServerError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		response.Fail(c, 404, 404, "用户不存在")
		return
	}
	response.OK(c, nil)
}

// Delete DELETE /users/:id
func (h *UserHandler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if uint(id) == middleware.CurrentUserID(c) {
		response.Fail(c, 400, 400, "不能删除自己")
		return
	}
	var u model.User
	if err := h.db.First(&u, id).Error; err != nil {
		response.Fail(c, 404, 404, "用户不存在")
		return
	}
	if u.Username == h.cfg.Admin.Username {
		response.Fail(c, 400, 400, "初始管理员不能删除")
		return
	}
	if err := h.db.Delete(&model.User{}, id).Error; err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, nil)
}

// AuditList GET /audit?username=&q=&page=&size=
func (h *UserHandler) AuditList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := h.db.Model(&model.AuditLog{})
	if u := c.Query("username"); u != "" {
		q = q.Where("username = ?", u)
	}
	if s := c.Query("q"); s != "" {
		like := "%" + s + "%"
		q = q.Where("path LIKE ? OR query LIKE ?", like, like)
	}
	var total int64
	q.Count(&total)
	var logs []model.AuditLog
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&logs).Error; err != nil {
		response.ServerError(c, err)
		return
	}
	response.OK(c, gin.H{"total": total, "items": logs, "page": page, "size": size})
}
