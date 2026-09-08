// Package response 提供统一的 API 响应格式
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// Response 统一响应结构 {code, message, data}
// code: 0 表示成功，非 0 表示失败（与 HTTP 状态码解耦，前端按 code 判断）
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// OK 成功响应
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: 0, Message: "ok", Data: data})
}

// Fail 失败响应
func Fail(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, Response{Code: code, Message: msg})
}

// FailMessage 业务失败（HTTP 200，code 非 0）
func FailMessage(c *gin.Context, code int, msg string) {
	Fail(c, http.StatusOK, code, msg)
}

// ServerError 服务器内部错误
func ServerError(c *gin.Context, err error) {
	Fail(c, http.StatusInternalServerError, 500, "服务器内部错误: "+err.Error())
}

// K8sError 将 kube-apiserver 返回的错误映射为友好的 HTTP 响应
func K8sError(c *gin.Context, err error) {
	if err == nil {
		OK(c, nil)
		return
	}
	var status int
	var msg string
	switch {
	case k8serrors.IsNotFound(err):
		status, msg = http.StatusNotFound, "资源不存在: "+err.Error()
	case k8serrors.IsForbidden(err):
		status, msg = http.StatusForbidden, "权限不足（RBAC 拒绝）: "+err.Error()
	case k8serrors.IsUnauthorized(err):
		status, msg = http.StatusUnauthorized, "集群认证失败，请检查 kubeconfig: "+err.Error()
	case k8serrors.IsAlreadyExists(err):
		status, msg = http.StatusConflict, "资源已存在: "+err.Error()
	case k8serrors.IsConflict(err):
		status, msg = http.StatusConflict, "资源冲突（版本不一致）: "+err.Error()
	case k8serrors.IsInvalid(err):
		status, msg = http.StatusBadRequest, "资源校验失败: "+err.Error()
	case k8serrors.IsTimeout(err):
		status, msg = http.StatusGatewayTimeout, "集群访问超时: "+err.Error()
	case k8serrors.IsServerTimeout(err):
		status, msg = http.StatusGatewayTimeout, "集群服务超时: "+err.Error()
	default:
		status, msg = http.StatusBadGateway, "访问集群失败: "+err.Error()
	}
	Fail(c, status, status, msg)
}
