// minio.go MinIO 下载代理（大文件制品）。上传发生在流水线 Task 内，平台侧只做下载。
package artifact

import (
	"context"
	"fmt"
	"io"

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
	c, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: false,
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
