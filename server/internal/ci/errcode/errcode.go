// Package errcode 定义统一业务错误类型，handler 据此返回正确 HTTP 状态码与业务码。
package errcode

import (
	"errors"
	"strings"
	"fmt"
	"net/http"
)

// Code 业务错误码。
type Code int

const (
	OK             Code = 0
	InvalidParam   Code = 40001
	Unauthorized   Code = 40100
	Forbidden      Code = 40300
	NotFound       Code = 40400
	Conflict       Code = 40900
	Internal       Code = 50000
	DepUnavailable Code = 50301
)

// HTTPStatus 返回该错误码对应的 HTTP 状态码。
func (c Code) HTTPStatus() int {
	switch c {
	case InvalidParam:
		return http.StatusBadRequest
	case Unauthorized:
		return http.StatusUnauthorized
	case Forbidden:
		return http.StatusForbidden
	case NotFound:
		return http.StatusNotFound
	case Conflict:
		return http.StatusConflict
	case DepUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Error 带业务码的错误，handler 通过 AsError 识别。
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code.String()
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Msg)
}

func (c Code) String() string {
	switch c {
	case OK:
		return "ok"
	case InvalidParam:
		return "invalid parameter"
	case Unauthorized:
		return "unauthorized"
	case Forbidden:
		return "forbidden"
	case NotFound:
		return "not found"
	case Conflict:
		return "conflict"
	case Internal:
		return "internal error"
	case DepUnavailable:
		return "dependency unavailable"
	default:
		return "unknown"
	}
}

// New 构造业务错误。
func New(c Code, msg string) *Error { return &Error{Code: c, Msg: msg} }

// Newf 构造带格式的业务错误。
func Newf(c Code, format string, args ...interface{}) *Error {
	return &Error{Code: c, Msg: fmt.Sprintf(format, args...)}
}

// AsCode 从任意 error 提取业务码；非业务错误返回 Internal。
func AsCode(err error) (Code, string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, e.Msg
	}
	// 携带业务码的领域错误（如 pipeline CompileError）：实现 Code() 方法即可识别
	var coded interface{ Code() Code }
	if errors.As(err, &coded) {
		return coded.Code(), err.Error()
	}
	return Internal, err.Error()
}

// IsUniqueViolation 跨驱动的唯一约束冲突判断：
// PG 23505 / MySQL 1062 / SQLite "UNIQUE constraint failed"。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "Error 1062") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
