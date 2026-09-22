// 容器文件浏览器接口
package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// ------------------- 容器文件浏览器 -------------------

// allowPodExec 文件浏览基于 pods/exec 通道实现，与 Pod 终端同口径：
// 需要该 Pod 所在命名空间的写权限（edit/admin 或平台管理员）。
func (h *K8sHandler) allowPodExec(c *gin.Context, ns string) bool {
	if h.perm == nil {
		return true
	}
	if h.perm.Access(c.Request.Context(), middleware.ClusterName(c), middleware.CurrentUser(c)).CanWriteNS(ns) {
		return true
	}
	response.Fail(c, http.StatusForbidden, 403, "容器文件浏览基于 exec 通道，需要该命名空间的写权限（edit/admin 或平台管理员）")
	return false
}

// ListFiles GET /pods/:name/files?namespace=&container=&path=
func (h *K8sHandler) ListFiles(c *gin.Context) {
	if !h.allowPodExec(c, c.Query("namespace")) {
		return
	}
	client := h.client(c)
	if client == nil {
		return
	}
	out, err := h.k8s.FileList(c.Request.Context(), client, c.Query("namespace"), c.Param("name"), c.Query("container"), c.Query("path"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, out)
}

// DownloadFile GET /pods/:name/files/download?namespace=&container=&path=
func (h *K8sHandler) DownloadFile(c *gin.Context) {
	if !h.allowPodExec(c, c.Query("namespace")) {
		return
	}
	client := h.client(c)
	if client == nil {
		return
	}
	ns, pod, container, path := c.Query("namespace"), c.Param("name"), c.Query("container"), c.Query("path")
	if path == "" || strings.Contains(path, "..") {
		response.Fail(c, 400, 400, "路径不合法")
		return
	}
	// 先确认存在并取文件名
	if out, err := h.k8s.FileList(c.Request.Context(), client, ns, pod, container, dirOf(path)); err == nil {
		if list, ok := out["entries"].([]service.FileEntry); ok {
			base := baseName(path)
			for _, e := range list {
				if e["name"] == base && e["type"] != "dir" {
					c.Header("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlEscape(base)))
					break
				}
			}
		}
	}
	if err := h.k8s.ExecOnce(c.Request.Context(), client, ns, pod, container, nil, c.Writer, io.Discard, "cat", path); err != nil {
		// 响应可能已写入部分内容，只能终止
		c.Status(http.StatusBadGateway)
		return
	}
}

// UploadFile POST /pods/:name/files/upload?namespace=&container=&path=（multipart 字段 file）
func (h *K8sHandler) UploadFile(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	path := c.Query("path")
	if path == "" || strings.Contains(path, "..") {
		response.Fail(c, 400, 400, "路径不合法")
		return
	}
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		response.Fail(c, 400, 400, "缺少文件字段 file")
		return
	}
	defer file.Close()
	var stderr strings.Builder
	// 直接 argv 执行 tee，不走 shell：Go %q 不转义 $ 和反引号，
	// /data/x$(curl ...) 这类路径在 sh -c 双引号内仍会执行（容器内命令注入）
	if err := h.k8s.ExecOnce(c.Request.Context(), client, c.Query("namespace"), c.Param("name"), c.Query("container"),
		file, io.Discard, &stderr, "tee", "--", path); err != nil {
		response.Fail(c, 400, 400, "上传失败: "+err.Error())
		return
	}
	response.OK(c, nil)
}

// FileAction POST /pods/:name/files/action?namespace=&container= {action: rm|mkdir|mv, path, to?}
func (h *K8sHandler) FileAction(c *gin.Context) {
	client := h.client(c)
	if client == nil {
		return
	}
	var req struct {
		Action string `json:"action" binding:"required,oneof=rm mkdir mv"`
		Path   string `json:"path" binding:"required"`
		To     string `json:"to"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误")
		return
	}
	if strings.Contains(req.Path, "..") || strings.Contains(req.To, "..") {
		response.Fail(c, 400, 400, "路径不合法")
		return
	}
	var cmd []string
	switch req.Action {
	case "rm":
		cmd = []string{"rm", "-rf", req.Path}
	case "mkdir":
		cmd = []string{"mkdir", "-p", req.Path}
	case "mv":
		if req.To == "" {
			response.Fail(c, 400, 400, "mv 需要 to 参数")
			return
		}
		cmd = []string{"mv", req.Path, req.To}
	}
	var stderr strings.Builder
	if err := h.k8s.ExecOnce(c.Request.Context(), client, c.Query("namespace"), c.Param("name"), c.Query("container"),
		nil, io.Discard, &stderr, cmd...); err != nil {
		response.Fail(c, 400, 400, "操作失败: "+stderr.String()+" "+err.Error())
		return
	}
	response.OK(c, nil)
}

func dirOf(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}

func baseName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

func urlEscape(s string) string {
	// RFC 5987（filename*=UTF-8''）：attr-char 之外的字节一律百分号编码。
	// 旧实现只替换少量字符，文件名含裸 %（如 100%.log）会产出非法的 filename* 值
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
