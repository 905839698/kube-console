// 端口转发与容器文件浏览器接口
package handler

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/middleware"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// PortForwardHandler 端口转发
type PortForwardHandler struct {
	clusters *service.ClusterManager
	pf       *service.PortForwardService
}

func NewPortForwardHandler(clusters *service.ClusterManager, pf *service.PortForwardService) *PortForwardHandler {
	return &PortForwardHandler{clusters: clusters, pf: pf}
}

// List GET /portforwards
func (h *PortForwardHandler) List(c *gin.Context) {
	items := make([]gin.H, 0)
	for _, t := range h.pf.List() {
		items = append(items, gin.H{
			"id": t.ID, "cluster": t.Cluster, "namespace": t.Namespace, "pod": t.Pod,
			"localPort": t.LocalPort, "remotePort": t.RemotePort, "createdAt": t.CreatedAt,
		})
	}
	response.OK(c, items)
}

// Start POST /portforwards {namespace,pod,localPort?,port}
func (h *PortForwardHandler) Start(c *gin.Context) {
	cluster := middleware.ClusterName(c)
	if cluster == "" {
		response.Fail(c, 400, 400, "缺少 X-Cluster 请求头")
		return
	}
	client, err := h.clusters.ClientChecked(cluster)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	var req struct {
		Namespace string `json:"namespace" binding:"required"`
		Pod       string `json:"pod" binding:"required"`
		LocalPort int    `json:"localPort"`
		Port      int    `json:"port" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, 400, "参数错误：需要 namespace / pod / port")
		return
	}
	t, err := h.pf.Start(client, cluster, req.Namespace, req.Pod, req.LocalPort, req.Port)
	if err != nil {
		response.Fail(c, 400, 400, "建立转发失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"id": t.ID, "cluster": t.Cluster, "namespace": t.Namespace, "pod": t.Pod,
		"localPort": t.LocalPort, "remotePort": t.RemotePort, "createdAt": t.CreatedAt,
	})
}

// Stop DELETE /portforwards/:id
func (h *PortForwardHandler) Stop(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || !h.pf.Stop(id) {
		response.Fail(c, 404, 404, "转发不存在或已停止")
		return
	}
	response.OK(c, nil)
}

// ------------------- 容器文件浏览器 -------------------

// ListFiles GET /pods/:name/files?namespace=&container=&path=
func (h *K8sHandler) ListFiles(c *gin.Context) {
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
	if err := h.k8s.ExecOnce(c.Request.Context(), client, c.Query("namespace"), c.Param("name"), c.Query("container"),
		file, io.Discard, &stderr, "sh", "-c", fmt.Sprintf("cat > %q", path)); err != nil {
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
	// 仅文件名，做最小转义即可
	r := strings.NewReplacer(" ", "%20", "\"", "%22", "#", "%23", "?", "%3F", "\\", "%5C")
	return r.Replace(s)
}
