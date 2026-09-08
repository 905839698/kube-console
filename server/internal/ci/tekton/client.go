package tekton

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
)

// Client 封装单个集群的 K8s 访问能力（多集群：每个注册集群一个实例）。
//   - clientset：内置资源（Secret / Pod / PVC / Namespace）
//   - dyn：Tekton CRD（Pipeline / PipelineRun / TaskRun），用 unstructured 操作
//
// 客户端从 kube-console ClusterManager 缓存的 *kube.Client 构建，
// 不再自行解析 kubeconfig（原 ci-platform 的 env/默认/in-cluster 三级已移除）。
type Client struct {
	clusterName string         // 所属集群名（日志/事件标识）
	cfg         *config.CIConfig
	clientset   kubernetes.Interface
	dyn         dynamic.Interface
}

// NewClient 基于已建立的集群客户端构建 Tekton 客户端。
func NewClient(clusterName string, kc *kube.Client, cfg *config.CIConfig) *Client {
	return &Client{
		clusterName: clusterName,
		cfg:         cfg,
		clientset:   kc.Clientset,
		dyn:         kc.Dynamic,
	}
}

// ClusterName 所属集群名。
func (c *Client) ClusterName() string { return c.clusterName }

// EnsureNamespace 确保命名空间存在（幂等）——项目 ns 隔离的基础。
func (c *Client) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = c.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	return err
}
