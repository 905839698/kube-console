package middleware

import (
	"bytes"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// LogLevel 日志级别
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// ParseLogLevel 解析日志级别字符串
func ParseLogLevel(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LogLevelDebug
	case "info":
		return LogLevelInfo
	case "warn":
		return LogLevelWarn
	case "error":
		return LogLevelError
	default:
		return LogLevelInfo
	}
}

// responseWriter 捕获响应体
type responseWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *responseWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *responseWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// RequestLogger 自定义请求日志中间件
//   - debug: 记录完整请求体 + 响应体
//   - info:  记录方法、路径、状态码、耗时（4xx/5xx 标 WARN）
//   - warn:  仅记录 4xx/5xx
//   - error: 仅记录 5xx
func RequestLogger(level LogLevel) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// debug 级别：读取并保存请求体
		var reqBody []byte
		if level == LogLevelDebug && c.Request.Body != nil && c.Request.ContentLength > 0 {
			body, err := io.ReadAll(c.Request.Body)
			if err == nil {
				reqBody = body
				c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
			}
		}

		// debug 级别：捕获响应体
		var rw *responseWriter
		if level == LogLevelDebug {
			rw = &responseWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
			c.Writer = rw
		}

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		switch level {
		case LogLevelDebug:
			reqLog := ""
			if len(reqBody) > 0 {
				reqLog = " | req: " + truncate(string(reqBody), 4096)
			}
			respLog := ""
			if rw != nil && rw.body.Len() > 0 {
				respLog = " | resp: " + truncate(rw.body.String(), 4096)
			}
			log.Printf("[DEBUG] %s %s %d %v %s%s%s",
				c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP(), reqLog, respLog)

		case LogLevelInfo:
			if status >= 400 {
				log.Printf("[WARN] %s %s %d %v %s",
					c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP())
			} else {
				log.Printf("[INFO] %s %s %d %v",
					c.Request.Method, c.Request.URL.Path, status, latency)
			}

		case LogLevelWarn:
			if status >= 400 {
				log.Printf("[WARN] %s %s %d %v %s",
					c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP())
			}

		case LogLevelError:
			if status >= 500 {
				log.Printf("[ERROR] %s %s %d %v %s",
					c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP())
			}
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
