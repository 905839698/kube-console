// Pod 端口转发：基于 client-go SPDY 隧道，在服务端本机监听端口并转发到 Pod。
// 隧道存活于内存，浏览器访问 http://<服务端地址>:<本地端口> 即达 Pod 端口。
package service

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	spdy "k8s.io/client-go/transport/spdy"

	"kube-console/server/internal/kube"
)

// pfTunnel 一条活跃转发
type pfTunnel struct {
	ID         int
	Cluster    string
	Namespace  string
	Pod        string
	LocalPort  int
	RemotePort int
	stopCh     chan struct{}
	logs       *strings.Builder
	logsMu     *sync.Mutex
	CreatedAt  time.Time
}

// PortForwardService 管理所有转发隧道
type PortForwardService struct {
	mu      sync.Mutex
	nextID  int
	tunnels map[int]*pfTunnel
}

func NewPortForwardService() *PortForwardService {
	return &PortForwardService{nextID: 1, tunnels: map[int]*pfTunnel{}}
}

// Start 建立转发；localPort 传 0 自动挑选空闲端口。阻塞至隧道就绪（或超时失败）。
func (s *PortForwardService) Start(c *kube.Client, cluster, namespace, pod string, localPort, remotePort int) (*pfTunnel, error) {
	if remotePort <= 0 || remotePort > 65535 {
		return nil, fmt.Errorf("目标端口不合法")
	}
	if localPort < 0 || localPort > 65535 {
		return nil, fmt.Errorf("本地端口不合法")
	}
	if localPort == 0 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("挑选本地端口失败: %w", err)
		}
		localPort = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}
	if localPort == 0 {
		return nil, fmt.Errorf("无法确定本地端口")
	}

	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).
		SubResource("portforward").
		VersionedParams(&corev1.PodPortForwardOptions{Ports: []int32{int32(remotePort)}}, scheme.ParameterCodec)

	transport, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return nil, fmt.Errorf("建立传输通道失败: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	logs := &strings.Builder{}
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", localPort, remotePort)}, stopCh, readyCh, logs, logs)
	if err != nil {
		return nil, err
	}
	go func() {
		_ = fw.ForwardPorts() // 错误写入 logs，由 list 接口透出
	}()

	select {
	case <-readyCh:
	case <-time.After(15 * time.Second):
		close(stopCh)
		return nil, fmt.Errorf("隧道建立超时（Pod 端口 %d 不可达？）", remotePort)
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return nil, fmt.Errorf("获取转发端口失败: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	t := &pfTunnel{
		ID: s.nextID, Cluster: cluster, Namespace: namespace, Pod: pod,
		LocalPort: int(ports[0].Local), RemotePort: remotePort,
		stopCh: stopCh, logs: logs, logsMu: &sync.Mutex{}, CreatedAt: time.Now(),
	}
	s.nextID++
	s.tunnels[t.ID] = t
	return t, nil
}

// Stop 关闭并移除隧道
func (s *PortForwardService) Stop(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tunnels[id]; ok {
		close(t.stopCh)
		delete(s.tunnels, id)
		return true
	}
	return false
}

// List 当前全部隧道
func (s *PortForwardService) List() []pfTunnel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pfTunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		out = append(out, *t)
	}
	return out
}

// TunnelLog 隧道日志（含错误信息）
func (s *PortForwardService) TunnelLog(id int) string {
	s.mu.Lock()
	t, ok := s.tunnels[id]
	s.mu.Unlock()
	if !ok {
		return ""
	}
	t.logsMu.Lock()
	defer t.logsMu.Unlock()
	return t.logs.String()
}
