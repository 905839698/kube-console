// minio.go MinIO 下载代理（大文件制品）。上传发生在流水线 Task 内，平台侧只做下载。
package artifact

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"kube-console/server/internal/config"
)

// MinIOClient 访问 MinIO。
type MinIOClient struct {
	c      *minio.Client
	bucket string
}

func NewMinIOClient(cfg *config.CIConfig) (*MinIOClient, error) {
	if cfg.MinIOEndpoint == "" {
		return nil, fmt.Errorf("MinIO 未配置（ci.minioEndpoint）")
	}
	// 按 endpoint scheme 推导 TLS（Secure:false 硬编码会让 HTTPS MinIO 全部下载失败）
	secure := strings.HasPrefix(cfg.MinIOEndpoint, "https")
	// 客户端必须带上限：minio-go 默认 http.Client 无 Timeout，MinIO 挂起时
	// goroutine 与连接永久占用。用 ResponseHeaderTimeout 卡「建连+响应头」，
	// 不卡响应体（大文件流式下载不能按整请求超时，否则传满 120s 被截断）
	c, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: secure,
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		},
	})
	if err != nil {
		return nil, err
	}
	bucket := cfg.MinIOBucket
	if bucket == "" {
		bucket = "ci-artifacts"
	}
	return &MinIOClient{c: c, bucket: bucket}, nil
}

// Download 流式下载对象到 w。objectKey 不含 bucket 前缀（storage_path 形如 "bucket/key"）。
func (m *MinIOClient) Download(ctx context.Context, objectKey string, w io.Writer) error {
	key := objectKey
	if i := indexByte(objectKey, '/'); i >= 0 {
		key = objectKey[i+1:]
	}
	obj, err := m.c.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer obj.Close()
	_, err = io.Copy(w, obj)
	return err
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
