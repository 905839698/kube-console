// Package service 业务服务层：集群注册表 + Kubernetes 资源操作
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// ClusterManager 集群注册表：负责 kubeconfig 持久化、连通性校验、客户端连接缓存
type ClusterManager struct {
	db      *gorm.DB
	mu      sync.RWMutex
	clients map[string]*kube.Client // key: 集群名

	// 连接熔断：近期探测失败的集群，请求直接快速失败，避免等 21s TCP 超时
	failMu   sync.RWMutex
	failures map[string]time.Time

	defsMu sync.RWMutex
	defs   map[string]cachedDefs // 集群名 -> 资源定义缓存
}

// breakerWait 熔断保持时长：探测失败后这段时间内的请求直接快速失败
const breakerWait = 60 * time.Second

type cachedDefs struct {
	groups   []kube.GroupDef
	fetched  time.Time
}

const resourceDefsTTL = 5 * time.Minute

// NewClusterManager 创建集群管理器
func NewClusterManager(db *gorm.DB) *ClusterManager {
	m := &ClusterManager{db: db, clients: make(map[string]*kube.Client), defs: make(map[string]cachedDefs), failures: make(map[string]time.Time)}
	go m.probeLoop()
	return m
}

// probeLoop 后台探活：周期性探测所有集群连通性（5s 超时），
// 失败写熔断表 + 更新库状态；恢复自动清除。数据请求因此可以快速失败，而不是等 21s TCP 超时。
func (m *ClusterManager) probeLoop() {
	probe := func() {
		clusters, err := m.List()
		if err != nil {
			return
		}
		for _, cl := range clusters {
			go func(name string) {
				c, err := m.Client(name)
				if err == nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_, err = c.Clientset.CoreV1().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
					cancel()
				}
				if err != nil {
					m.MarkFailure(name)
					m.db.Model(&model.Cluster{}).Where("name = ?", name).Updates(map[string]any{
						"status": model.ClusterStatusError, "error_message": truncMsg("集群探活失败: "+err.Error(), 1024),
					})
					return
				}
				m.ClearFailure(name)
				now := time.Now()
				m.db.Model(&model.Cluster{}).Where("name = ?", name).Updates(map[string]any{
					"status": model.ClusterStatusConnected, "error_message": "", "last_connected_at": &now,
				})
			}(cl.Name)
		}
	}
	probe()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		probe()
	}
}

func truncMsg(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// MarkFailure 记录一次连接失败（进入熔断窗口）
func (m *ClusterManager) MarkFailure(name string) {
	m.failMu.Lock()
	m.failures[name] = time.Now()
	m.failMu.Unlock()
}

// ClearFailure 清除失败记录（探测恢复）
func (m *ClusterManager) ClearFailure(name string) {
	m.failMu.Lock()
	delete(m.failures, name)
	m.failMu.Unlock()
}

// RecentFailure 是否处于熔断窗口内
func (m *ClusterManager) RecentFailure(name string) bool {
	m.failMu.RLock()
	defer m.failMu.RUnlock()
	t, ok := m.failures[name]
	return ok && time.Since(t) < breakerWait
}

// ClientChecked 带熔断检查的客户端获取：近期探测失败的集群直接快速失败
func (m *ClusterManager) ClientChecked(name string) (*kube.Client, error) {
	if m.RecentFailure(name) {
		return nil, fmt.Errorf("集群 %q 连接异常（60 秒内探测失败，熔断中；将在恢复后自动放行，也可到集群管理重新检测）", name)
	}
	return m.Client(name)
}

// ResourceDefs 获取集群的资源定义（5 分钟缓存，可强制刷新）
func (m *ClusterManager) ResourceDefs(name string, force bool) ([]kube.GroupDef, error) {
	if !force {
		m.defsMu.RLock()
		if d, ok := m.defs[name]; ok && time.Since(d.fetched) < resourceDefsTTL {
			m.defsMu.RUnlock()
			return d.groups, nil
		}
		m.defsMu.RUnlock()
	}
	client, err := m.Client(name)
	if err != nil {
		return nil, err
	}
	groups, err := kube.ListResourceDefs(context.Background(), client)
	if err != nil {
		return nil, err
	}
	m.defsMu.Lock()
	m.defs[name] = cachedDefs{groups: groups, fetched: time.Now()}
	m.defsMu.Unlock()
	return groups, nil
}

// List 返回全部集群（不含 kubeconfig 内容）
func (m *ClusterManager) List() ([]model.Cluster, error) {
	var clusters []model.Cluster
	err := m.db.Order("id ASC").Find(&clusters).Error
	return clusters, err
}

// Get 按名称查询集群（不含 kubeconfig 内容）
func (m *ClusterManager) Get(name string) (*model.Cluster, error) {
	var cluster model.Cluster
	err := m.db.Where("name = ?", name).First(&cluster).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("集群 %q 不存在", name)
	}
	return &cluster, err
}

// GetRaw 按名称查询集群（含 kubeconfig 内容）
func (m *ClusterManager) GetRaw(name string) (*model.Cluster, error) {
	var cluster model.Cluster
	err := m.db.Where("name = ?", name).First(&cluster).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("集群 %q 不存在", name)
		}
		return nil, err
	}
	return &cluster, nil
}

// Create 新建集群：校验 kubeconfig 并测试连通性
func (m *ClusterManager) Create(name, kubeconfig string) (*model.Cluster, error) {
	cluster := &model.Cluster{Name: name, Kubeconfig: kubeconfig, Status: model.ClusterStatusUnknown}
	if err := m.verifyAndFill(cluster); err != nil {
		return nil, err
	}
	if err := m.db.Create(cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("集群 %q 已存在", name)
		}
		return nil, err
	}
	m.Invalidate(name)
	return cluster, nil
}

// Update 更新集群 kubeconfig，重建连接缓存
func (m *ClusterManager) Update(name, kubeconfig string) (*model.Cluster, error) {
	cluster, err := m.GetRaw(name)
	if err != nil {
		return nil, err
	}
	if kubeconfig != "" {
		cluster.Kubeconfig = kubeconfig
	}
	if err := m.verifyAndFill(cluster); err != nil {
		return nil, err
	}
	if err := m.db.Save(cluster).Error; err != nil {
		return nil, err
	}
	m.Invalidate(name)
	return cluster, nil
}

// Delete 删除集群并清理连接缓存
func (m *ClusterManager) Delete(name string) error {
	cluster, err := m.GetRaw(name)
	if err != nil {
		return err
	}
	if err := m.db.Delete(cluster).Error; err != nil {
		return err
	}
	m.Invalidate(name)
	return nil
}

// TestConnectivity 测试集群连通性并更新状态，返回最新状态
func (m *ClusterManager) TestConnectivity(name string) (*model.Cluster, error) {
	cluster, err := m.GetRaw(name)
	if err != nil {
		return nil, err
	}
	if err := m.verifyAndFill(cluster); err != nil {
		cluster.Status = model.ClusterStatusError
		cluster.ErrorMessage = err.Error()
		_ = m.db.Save(cluster).Error
		m.Invalidate(name)
		return cluster, fmt.Errorf("连接失败: %w", err)
	}
	now := time.Now()
	cluster.Status = model.ClusterStatusConnected
	cluster.ErrorMessage = ""
	cluster.LastConnectedAt = &now
	_ = m.db.Save(cluster).Error
	m.Invalidate(name)
	return cluster, nil
}

// Client 获取集群客户端（缓存命中直接返回，未命中则构建）
func (m *ClusterManager) Client(name string) (*kube.Client, error) {
	m.mu.RLock()
	c, ok := m.clients[name]
	m.mu.RUnlock()
	if ok {
		return c, nil
	}
	cluster, err := m.GetRaw(name)
	if err != nil {
		return nil, err
	}
	c, err = kube.NewClient(cluster.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("集群 %q 连接失败: %w", name, err)
	}
	m.mu.Lock()
	m.clients[name] = c
	m.mu.Unlock()
	return c, nil
}

// InvalidateChangedForCI 对比 DB 与已加载客户端的 kubeconfig，变更的集群
// 清理其 CI Tekton 客户端缓存（回调传入，避免 service → ci 包依赖）。
func (m *ClusterManager) InvalidateChangedForCI(ciInval func(string)) {
	m.mu.RLock()
	cur := make(map[string]string, len(m.clients))
	for name, c := range m.clients {
		cur[name] = c.Kubeconfig
	}
	m.mu.RUnlock()
	var rows []model.Cluster
	m.db.Find(&rows)
	for _, r := range rows {
		if prev, ok := cur[r.Name]; ok && prev != r.Kubeconfig {
			ciInval(r.Name)
		}
	}
}

// Invalidate 使集群连接缓存失效
func (m *ClusterManager) Invalidate(name string) {
	m.mu.Lock()
	delete(m.clients, name)
	m.mu.Unlock()
}

// UpdatePrometheus 更新集群的 Prometheus 配置
func (m *ClusterManager) UpdatePrometheus(name string, promNs, promSvc string, promPort int) (*model.Cluster, error) {
	cluster, err := m.GetRaw(name)
	if err != nil {
		return nil, err
	}
	cluster.PrometheusNamespace = promNs
	cluster.PrometheusService = promSvc
	if promPort > 0 {
		cluster.PrometheusPort = promPort
	}
	if err := m.db.Save(cluster).Error; err != nil {
		return nil, err
	}
	return cluster, nil
}

// UpdateGrafana 更新集群的 Grafana 地址（iframe 直连内嵌用）
func (m *ClusterManager) UpdateGrafana(name, grafanaURL string) (*model.Cluster, error) {
	cluster, err := m.GetRaw(name)
	if err != nil {
		return nil, err
	}
	cluster.GrafanaURL = strings.TrimSpace(grafanaURL)
	if err := m.db.Save(cluster).Error; err != nil {
		return nil, err
	}
	return cluster, nil
}

// verifyAndFill 校验 kubeconfig 并填充 server/context/状态字段
func (m *ClusterManager) verifyAndFill(cluster *model.Cluster) error {
	server, ctxName, err := kube.RawConfig(cluster.Kubeconfig)
	if err != nil {
		cluster.Status = model.ClusterStatusError
		cluster.ErrorMessage = err.Error()
		return fmt.Errorf("kubeconfig 解析失败: %w", err)
	}
	cluster.Server = server
	cluster.Context = ctxName

	// 完整校验（建立连接获取服务端版本），失败视为连接错误
	if _, err := kube.NewClient(cluster.Kubeconfig); err != nil {
		cluster.Status = model.ClusterStatusError
		cluster.ErrorMessage = err.Error()
		return fmt.Errorf("集群连通性校验失败: %w", err)
	}
	now := time.Now()
	cluster.Status = model.ClusterStatusConnected
	cluster.ErrorMessage = ""
	cluster.LastConnectedAt = &now
	return nil
}
