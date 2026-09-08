// Package kube 封装 client-go 基础设施：连接构建、YAML 读写、资源状态计算
package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client 一个集群的连接集合（typed + dynamic + discovery），由 ClusterManager 缓存
type Client struct {
	Kubeconfig string
	Config     *rest.Config
	Clientset  kubernetes.Interface
	Dynamic    dynamic.Interface
	Discovery  discovery.DiscoveryInterface
}

// NewClient 从 kubeconfig 内容构建 Client，并校验集群连通性（获取服务端版本）
func NewClient(kubeconfig string) (*Client, error) {
	if kubeconfig == "" {
		return nil, errors.New("kubeconfig 为空")
	}
	cc, err := clientcmd.NewClientConfigFromBytes([]byte(kubeconfig))
	if err != nil {
		return nil, err
	}
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	c := &Client{Kubeconfig: kubeconfig, Config: restCfg, Clientset: clientset, Dynamic: dyn, Discovery: disc}
	// 连通性校验：拉取服务端版本（超时由 rest.Config 控制）
	_, err = c.Discovery.ServerVersion()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// RawConfig 解析 kubeconfig 元信息（server / current context），不建立连接
func RawConfig(kubeconfig string) (server, contextName string, err error) {
	cc, err := clientcmd.NewClientConfigFromBytes([]byte(kubeconfig))
	if err != nil {
		return "", "", err
	}
	raw, err := cc.RawConfig()
	if err != nil {
		return "", "", err
	}
	ctx := raw.Contexts[raw.CurrentContext]
	if ctx != nil {
		if cl := raw.Clusters[ctx.Cluster]; cl != nil {
			server = cl.Server
		}
	}
	return server, raw.CurrentContext, nil
}

// KindMap 支持的资源 kind -> GVR 映射（官方资源全集 + Gateway API）
var KindMap = map[string]schema.GroupVersionResource{
	// 工作负载 apps
	"deployments":              {Group: "apps", Version: "v1", Resource: "deployments"},
	"statefulsets":             {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"daemonsets":               {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"replicasets":              {Group: "apps", Version: "v1", Resource: "replicasets"},
	"replicationcontrollers":   {Group: "", Version: "v1", Resource: "replicationcontrollers"},
	"pods":                     {Group: "", Version: "v1", Resource: "pods"},
	"podtemplates":             {Group: "", Version: "v1", Resource: "podtemplates"},
	// 批处理 batch
	"cronjobs":                 {Group: "batch", Version: "v1", Resource: "cronjobs"},
	"jobs":                     {Group: "batch", Version: "v1", Resource: "jobs"},
	// 服务发现
	"services":                 {Group: "", Version: "v1", Resource: "services"},
	"ingresses":                {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	"endpoints":                {Group: "", Version: "v1", Resource: "endpoints"},
	"endpointslices":           {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"},
	// 配置中心
	"configmaps":               {Group: "", Version: "v1", Resource: "configmaps"},
	"secrets":                  {Group: "", Version: "v1", Resource: "secrets"},
	"serviceaccounts":          {Group: "", Version: "v1", Resource: "serviceaccounts"},
	// 存储
	"persistentvolumeclaims":   {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	"persistentvolumes":        {Group: "", Version: "v1", Resource: "persistentvolumes"},
	"storageclasses":           {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
	// 网络
	"networkpolicies":          {Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	// 安全 RBAC
	"roles":                    {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	"rolebindings":             {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	"clusterroles":             {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	"clusterrolebindings":      {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	// 弹性
	"horizontalpodautoscalers": {Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
	// 配额
	"resourcequotas":           {Group: "", Version: "v1", Resource: "resourcequotas"},
	"limitranges":              {Group: "", Version: "v1", Resource: "limitranges"},
	// Gateway API
	"gatewayclasses":           {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"},
	"gateways":                 {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	"httproutes":               {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	"grpcroutes":               {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes"},
	"tlsroutes":                {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "tlsroutes"},
	"tcproutes":                {Group: "gateway.networking.k8s.io", Version: "v1alpha2", Resource: "tcproutes"},
	"udproutes":                {Group: "gateway.networking.k8s.io", Version: "v1alpha2", Resource: "udproutes"},
	"referencegrants":          {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "referencegrants"},
	// 集群级
	"namespaces":               {Group: "", Version: "v1", Resource: "namespaces"},
	"nodes":                    {Group: "", Version: "v1", Resource: "nodes"},
	"events":                   {Group: "", Version: "v1", Resource: "events"},
}

// KindTitle 资源 kind -> 显示名称
func KindTitle(kind string) string {
	switch kind {
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "daemonsets":
		return "DaemonSet"
	case "replicasets":
		return "ReplicaSet"
	case "replicationcontrollers":
		return "ReplicationController"
	case "cronjobs":
		return "CronJob"
	case "jobs":
		return "Job"
	case "pods":
		return "Pod"
	case "services":
		return "Service"
	case "ingresses":
		return "Ingress"
	case "endpoints":
		return "Endpoints"
	case "endpointslices":
		return "EndpointSlice"
	case "configmaps":
		return "ConfigMap"
	case "secrets":
		return "Secret"
	case "serviceaccounts":
		return "ServiceAccount"
	case "persistentvolumeclaims":
		return "PVC"
	case "persistentvolumes":
		return "PV"
	case "storageclasses":
		return "StorageClass"
	case "networkpolicies":
		return "NetworkPolicy"
	case "roles":
		return "Role"
	case "rolebindings":
		return "RoleBinding"
	case "clusterroles":
		return "ClusterRole"
	case "clusterrolebindings":
		return "ClusterRoleBinding"
	case "horizontalpodautoscalers":
		return "HPA"
	case "resourcequotas":
		return "ResourceQuota"
	case "limitranges":
		return "LimitRange"
	case "gatewayclasses":
		return "GatewayClass"
	case "gateways":
		return "Gateway"
	case "httproutes":
		return "HTTPRoute"
	case "grpcroutes":
		return "GRPCRoute"
	case "tlsroutes":
		return "TLSRoute"
	case "tcproutes":
		return "TCPRoutes"
	case "udproutes":
		return "UDPRoutes"
	case "referencegrants":
		return "ReferenceGrant"
	case "namespaces":
		return "Namespace"
	case "nodes":
		return "Node"
	default:
		return kind
	}
}

// GVRFor 将 kind 转为 GVR
func GVRFor(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := KindMap[kind]
	return gvr, ok
}

// gatewayKinds Gateway API 资源：版本随渠道变化（v1alpha2/v1 提供 TCP/UDP/GRPC/TLS 等，v1beta1/v1 提供 Gateway 等），
// 硬编码版本会因渠道差异导致 404，故通过 discovery 解析实际提供的版本。
var gatewayKinds = map[string]bool{
	"gatewayclasses": true, "gateways": true, "httproutes": true,
	"grpcroutes": true, "tlsroutes": true, "tcproutes": true,
	"udproutes": true, "referencegrants": true,
}

// IsGatewayKind 判断 kind 是否属于 Gateway API 资源（需 discovery 解析版本）
func IsGatewayKind(kind string) bool {
	return gatewayKinds[kind]
}

// gatewayGroup 资源所属的 API 组（用于 discovery 解析版本）
func gatewayGroup(kind string) string {
	return "gateway.networking.k8s.io"
}

// ResolveGVR 将 kind 解析为 GVR：Gateway API 资源通过 discovery 在
// gateway.networking.k8s.io 各版本中按稳定度选择实际提供的版本，避免渠道差异导致的 404。
// 其他资源直接查 KindMap。ok=false 表示该资源在当前集群不存在（Gateway API 未安装或未启用对应资源），
// 此时返回带说明的 error，便于前端提示"该资源当前集群不可用"。
func ResolveGVR(ctx context.Context, c *Client, kind string) (schema.GroupVersionResource, bool, error) {
	if !IsGatewayKind(kind) {
		gvr, ok := GVRFor(kind)
		return gvr, ok, nil
	}
	// 获取该组启用的全部版本
	groups, err := c.Discovery.ServerGroups()
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("集群未提供 Gateway API（gateway.networking.k8s.io）: %w", err)
	}
	var versions []string
	for _, g := range groups.Groups {
		if g.Name == gatewayGroup(kind) {
			for _, v := range g.Versions {
				versions = append(versions, v.Version)
			}
		}
	}
	// 稳定版本（v1 / v1beta1）优先于 alpha，alpha 按序号降序（v1alpha2 > v1alpha1）
	rank := func(v string) int {
		switch {
		case v == "v1":
			return 100
		case v == "v1beta1":
			return 50
		case strings.HasPrefix(v, "v1alpha"):
			n, _ := strconv.Atoi(strings.TrimPrefix(v, "v1alpha"))
			return n
		default:
			return 0
		}
	}
	var best schema.GroupVersionResource
	bestRank := -1
	for _, v := range versions {
		res, err := c.Discovery.ServerResourcesForGroupVersion(gatewayGroup(kind) + "/" + v)
		if err != nil || res == nil {
			continue
		}
		for _, r := range res.APIResources {
			if r.Name == KindMap[kind].Resource && rank(v) > bestRank {
				best = schema.GroupVersionResource{Group: gatewayGroup(kind), Version: v, Resource: KindMap[kind].Resource}
				bestRank = rank(v)
				break
			}
		}
	}
	if bestRank <= 0 {
		return schema.GroupVersionResource{}, false, fmt.Errorf("当前集群未提供 %s（Gateway API 未安装或未启用该资源）", KindMap[kind].Resource)
	}
	return best, true, nil
}

// GatewayResourceAvailability 通过 discovery 解析 Gateway API 资源实际提供的版本
type GatewayResourceAvailability struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Found   bool   `json:"found"`
}

// ListGatewayAvailability 返回全部 Gateway API 资源在该集群实际可用的版本（通过 discovery）
func ListGatewayAvailability(ctx context.Context, c *Client) []GatewayResourceAvailability {
	out := make([]GatewayResourceAvailability, 0, len(gatewayKinds))
	for kind := range gatewayKinds {
		avail := GatewayResourceAvailability{Kind: kind, Found: false}
		if gvr, ok, _ := ResolveGVR(ctx, c, kind); ok {
			avail.Found = true
			avail.Version = gvr.Version
		}
		out = append(out, avail)
	}
	// 按 kind 排序保证稳定输出
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// clusterScopedKinds 集群级资源（访问时不能带 namespace）
var clusterScopedKinds = map[string]bool{
	"persistentvolumes":       true,
	"storageclasses":          true,
	"clusterroles":            true,
	"clusterrolebindings":     true,
	"gatewayclasses":          true,
	"namespaces":              true,
	"nodes":                   true,
	"customresourcedefinitions": true,
}

// IsClusterScoped 判断资源是否为集群级
func IsClusterScoped(kind string) bool {
	return clusterScopedKinds[kind]
}
