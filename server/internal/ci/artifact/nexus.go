// nexus.go Nexus raw/hosted 仓库下载代理。上传发生在流水线 Task 内（curl PUT）。
package artifact

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"kube-console/server/internal/config"
)

// NexusClient 访问 Nexus。
type NexusClient struct {
	base  string
	user  string
	pass  string
	httpc *http.Client
}

func NewNexusClient(cfg *config.CIConfig) *NexusClient {
	return &NexusClient{
		base: strings.TrimRight(cfg.NexusURL, "/"),
		user: cfg.NexusUsername,
		pass: cfg.NexusPassword,
		// 不能用整请求 Timeout（覆盖响应体读取）：大制品在慢链路上传满 120s
		// 会被 io.Copy 中途截断，客户端拿到 HTTP 200 + 损坏文件。
		// ResponseHeaderTimeout 只卡「建连+响应头」，body 流式传输不受限。
		httpc: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
}

// Download 流式下载制品到 w。path 形如 "repository/name/file"。
func (n *NexusClient) Download(ctx context.Context, path string, w io.Writer) error {
	if n.base == "" {
		return fmt.Errorf("Nexus 未配置（ci.nexusUrl）")
	}
	url := n.base + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if n.user != "" {
		req.SetBasicAuth(n.user, n.pass)
	}
	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nexus 下载失败: HTTP %d %s", resp.StatusCode, url)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
