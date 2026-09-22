package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	chartloader "helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	discoverycached "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// HelmService 提供 Helm release 与 chart 仓库操作（无状态，依赖 helm SDK）
type HelmService struct {
	db *gorm.DB

	reposMu   sync.Mutex
	repoCache map[string]cachedRepoIndex // key: repoID
}

type cachedRepoIndex struct {
	index   *repo.IndexFile
	fetched time.Time
}

const repoIndexTTL = 30 * time.Minute

// NewHelmService 创建 Helm 服务
func NewHelmService(db *gorm.DB) *HelmService {
	return &HelmService{db: db, repoCache: make(map[string]cachedRepoIndex)}
}

// ------------------- 内部：RESTClientGetter 适配 -------------------

// restGetter 用 *kube.Client 的 rest.Config 实现 helm 所需的 genericclioptions.RESTClientGetter
type restGetter struct {
	cfg    *rest.Config
	loader clientcmd.ClientConfig
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return g.cfg, nil }

func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return discoverycached.NewMemCacheClient(dc), nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	gvks, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	return restmapper.NewDiscoveryRESTMapper(gvks), nil
}

func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return g.loader
}

// newClientset 构建集群的 kubernetes.Clientset（helm 写入 secret 时需要）
func (s *HelmService) newClientset(c *kube.Client) (*kubernetes.Clientset, error) {
	if c.Config == nil {
		return nil, fmt.Errorf("集群无可用 rest 配置")
	}
	cs, err := kubernetes.NewForConfig(c.Config)
	if err != nil {
		return nil, fmt.Errorf("创建集群 client 失败: %w", err)
	}
	return cs, nil
}

// helmActionCfg 为"指定命名空间"的 release 操作构建 helm action.Configuration。
// 命名空间会被固化进 secret 存储驱动，因此 release 类操作（详情/历史/升级/回滚/卸载/安装）
// 必须传入该 release 所在命名空间，否则只能看到/操作 default 命名空间。
func (s *HelmService) helmActionCfg(c *kube.Client, ns string) (*action.Configuration, error) {
	if ns == "" {
		ns = "default"
	}
	g := &restGetter{cfg: c.Config}
	if c.Kubeconfig != "" {
		g.loader, _ = clientcmd.NewClientConfigFromBytes([]byte(c.Kubeconfig))
	}
	cfg := &action.Configuration{Log: logDiscard}
	if err := cfg.Init(g, ns, "secret", logDiscard); err != nil {
		return nil, fmt.Errorf("初始化 helm 配置失败: %w", err)
	}
	return cfg, nil
}

func logDiscard(_ string, _ ...interface{}) {}

// helmGzipMagic gzip 文件魔数，与 helm 存储驱动保持一致
var helmGzipMagic = []byte{0x1f, 0x8b, 0x08}

// decodeHelmRelease 解码 helm release secret 的 data 字段（base64 → gzip → JSON）。
// 与 helm SDK 内部 storage/driver.decodeRelease 逻辑一致（driver 的对应函数未导出）。
func decodeHelmRelease(data string) (*release.Release, error) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	// 向后兼容：未启用压缩时跳过解压
	if len(b) > 3 && bytes.Equal(b[0:3], helmGzipMagic) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		b2, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		b = b2
	}
	var rls release.Release
	if err := json.Unmarshal(b, &rls); err != nil {
		return nil, err
	}
	return &rls, nil
}

// ------------------- Release 查询 -------------------

type HelmReleaseItem struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Chart       string `json:"chart"`
	ChartVer    string `json:"chartVersion"`
	AppVersion  string `json:"appVersion"`
	Version     int    `json:"version"` // revision
	Status      string `json:"status"`
	Updated     string `json:"updated"` // RFC3339
	Age         string `json:"age"`
	Description string `json:"description"`
}

// ListReleases 跨命名空间列出 release（namespace: ""/*/逗号多选）。
// 直接读取集群中带 owner=helm 标签的 secret（跨全部命名空间），
// 不依赖 helm SDK 把命名空间固化进存储驱动的机制，避免只查到单一命名空间。
func (s *HelmService) ListReleases(ctx context.Context, c *kube.Client, namespace, search string) ([]HelmReleaseItem, error) {
	cs, err := s.newClientset(c)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return nil, err
	}
	nsSet := splitNamespaces(namespace)
	// 与 helm list 一致：同一个 release 保留最新修订版（过滤掉 superseded 的历史版本）
	latest := make(map[string]*HelmReleaseItem) // key: namespace/name
	for i := range list.Items {
		item := list.Items[i]
		data := item.Data["release"]
		if len(data) == 0 {
			continue
		}
		r, err := decodeHelmRelease(string(data))
		if err != nil {
			continue
		}
		if r.Info == nil {
			continue
		}
		if !nsMatch(nsSet, r.Namespace) {
			continue
		}
		rel := HelmReleaseItem{
			Name:      r.Name,
			Namespace: r.Namespace,
			Version:   r.Version,
			Status:    string(r.Info.Status),
			Updated:   r.Info.LastDeployed.Format(time.RFC3339),
			Age:       ageOfTime(r.Info.LastDeployed.Time),
		}
		if r.Chart != nil && r.Chart.Metadata != nil {
			rel.Chart = r.Chart.Name()
			rel.ChartVer = r.Chart.Metadata.Version
			rel.AppVersion = r.Chart.Metadata.AppVersion
		}
		if r.Info.Description != "" {
			rel.Description = r.Info.Description
		}
		if !searchMatch(rel.Name, search) && !searchMatch(rel.Chart, search) {
			continue
		}
		key := r.Namespace + "/" + r.Name
		if prev, ok := latest[key]; ok && prev.Version > r.Version {
			continue
		}
		latest[key] = &rel
	}
	out := make([]HelmReleaseItem, 0, len(latest))
	for _, rel := range latest {
		out = append(out, *rel)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func nsMatch(all []string, ns string) bool {
	for _, a := range all {
		if a == "" || a == ns {
			return true
		}
	}
	return false
}

func ageOfTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d天", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d个月", int(d.Hours()/24/30))
	}
}

// HelmReleaseInfo 单个 release 详情
type HelmReleaseInfo struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart"`
	ChartVer   string `json:"chartVersion"`
	AppVersion string `json:"appVersion"`
	Version    int    `json:"version"`
	Status     string `json:"status"`
	Manifest   string `json:"manifest"`
	Values     string `json:"values"`
	Notes      string `json:"notes"`
	Updated    string `json:"updated"`
}

// ReleaseInfo 获取 release 详情（manifest + values + notes），ns 为该 release 所在命名空间
func (s *HelmService) ReleaseInfo(ctx context.Context, c *kube.Client, ns, name string) (*HelmReleaseInfo, error) {
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return nil, err
	}
	get := action.NewGet(cfg)
	r, err := get.Run(name)
	if err != nil {
		return nil, err
	}
	return s.toReleaseInfo(r), nil
}

// ReleaseUpgradeValues 取 release 当前修订存储的用户 values 与 chart 默认 values，
// 供升级弹窗预填——不依赖 chart 源仓库是否仍注册（仓库删除/改名后仓库路线会失败，
// 而 release 内始终存有完整 chart 包与用户 values）。
func (s *HelmService) ReleaseUpgradeValues(ctx context.Context, c *kube.Client, ns, name string) (config, chartValues string, err error) {
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return "", "", err
	}
	r, err := action.NewGet(cfg).Run(name)
	if err != nil {
		return "", "", err
	}
	if r.Config != nil {
		if b, e := yaml.Marshal(r.Config); e == nil {
			config = string(b)
		}
	}
	if r.Chart != nil && len(r.Chart.Values) > 0 {
		if b, e := yaml.Marshal(r.Chart.Values); e == nil {
			chartValues = string(b)
		}
	}
	return config, chartValues, nil
}

// HelmReleaseHistory 一条历史 revision
type HelmReleaseHistory struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Updated string `json:"updated"`
	Age     string `json:"age"`
	Chart   string `json:"chart"`
}

// ReleaseHistory 列出 release 的所有 revision，ns 为该 release 所在命名空间
func (s *HelmService) ReleaseHistory(ctx context.Context, c *kube.Client, ns, name string) ([]HelmReleaseHistory, error) {
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return nil, err
	}
	h := action.NewHistory(cfg)
	h.Max = 256
	rels, err := h.Run(name)
	if err != nil {
		return nil, err
	}
	out := make([]HelmReleaseHistory, 0, len(rels))
	for _, r := range rels {
		hh := HelmReleaseHistory{Version: r.Version}
		// Info 可能为 nil（历史遗留 release），nil 检查必须在解引用之前
		if r.Info != nil {
			hh.Updated = r.Info.LastDeployed.Format(time.RFC3339)
			hh.Age = ageOfTime(r.Info.LastDeployed.Time)
			hh.Status = string(r.Info.Status)
		}
		if r.Chart != nil && r.Chart.Metadata != nil {
			hh.Chart = r.Chart.Name() + "-" + r.Chart.Metadata.Version
		}
		out = append(out, hh)
	}
	return out, nil
}

// ------------------- Release 操作 -------------------

type InstallParams struct {
	RepoName        string `json:"repoName"`
	Chart           string `json:"chart"`
	Version         string `json:"version"` // 空=latest
	ReleaseName     string `json:"releaseName"`
	Namespace       string `json:"namespace"`
	CreateNamespace bool   `json:"createNamespace"`
	Values          string `json:"values"`
	Wait            bool   `json:"wait"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	Atomic          bool   `json:"atomic"`
}

// InstallRelease 从仓库安装 chart 为 release
func (s *HelmService) InstallRelease(ctx context.Context, c *kube.Client, p InstallParams) (*HelmReleaseInfo, error) {
	if p.Namespace == "" {
		p.Namespace = "default"
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 300
	}
	entry, err := s.repoEntry(p.RepoName)
	if err != nil {
		return nil, err
	}
	chrt, err := s.loadChartFromRepo(entry, p.Chart, p.Version)
	if err != nil {
		return nil, err
	}
	vals, err := parseValues(p.Values)
	if err != nil {
		return nil, err
	}
	cfg, err := s.helmActionCfg(c, p.Namespace)
	if err != nil {
		return nil, err
	}
	inst := action.NewInstall(cfg)
	inst.Namespace = p.Namespace
	inst.ReleaseName = p.ReleaseName
	inst.CreateNamespace = p.CreateNamespace
	inst.Wait = p.Wait
	inst.Atomic = p.Atomic
	inst.Timeout = time.Duration(p.TimeoutSeconds) * time.Second
	if p.ReleaseName == "" {
		inst.GenerateName = true
	}
	rel, err := inst.RunWithContext(ctx, chrt, vals)
	if err != nil {
		return nil, err
	}
	return s.toReleaseInfo(rel), nil
}

type UpgradeParams struct {
	RepoName       string `json:"repoName"`
	Chart          string `json:"chart"`
	Version        string `json:"version"`
	Values         string `json:"values"`
	Wait           bool   `json:"wait"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// UpgradeRelease 升级 release 到指定 chart 版本，ns 为该 release 所在命名空间
func (s *HelmService) UpgradeRelease(ctx context.Context, c *kube.Client, ns, name string, p UpgradeParams) (*HelmReleaseInfo, error) {
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 300
	}
	entry, err := s.repoEntry(p.RepoName)
	if err != nil {
		return nil, err
	}
	chrt, err := s.loadChartFromRepo(entry, p.Chart, p.Version)
	if err != nil {
		return nil, err
	}
	vals, err := parseValues(p.Values)
	if err != nil {
		return nil, err
	}
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return nil, err
	}
	up := action.NewUpgrade(cfg)
	up.Namespace = ns
	up.Wait = p.Wait
	up.Timeout = time.Duration(p.TimeoutSeconds) * time.Second
	rel, err := up.RunWithContext(ctx, name, chrt, vals)
	if err != nil {
		return nil, err
	}
	return s.toReleaseInfo(rel), nil
}

// RollbackRelease 回滚 release 到指定 revision（0=上一版），ns 为该 release 所在命名空间
func (s *HelmService) RollbackRelease(ctx context.Context, c *kube.Client, ns, name string, version int, wait bool) error {
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return err
	}
	rb := action.NewRollback(cfg)
	rb.Version = version
	rb.Wait = wait
	rb.Timeout = 5 * time.Minute
	// helm v3.17 的 Rollback/Uninstall 无 RunWithContext（仅 Install/Upgrade 有），
	// 由 rb.Timeout 5min 兜底
	return rb.Run(name)
}

// UninstallRelease 卸载 release，ns 为该 release 所在命名空间。
// keepHistory=false（默认）：连 release 记录一并清除——列表行彻底消失，
// 不会残留「uninstalled 状态永不更新」的记录。
func (s *HelmService) UninstallRelease(ctx context.Context, c *kube.Client, ns, name string, keepHistory bool) error {
	cfg, err := s.helmActionCfg(c, ns)
	if err != nil {
		return err
	}
	un := action.NewUninstall(cfg)
	un.KeepHistory = keepHistory
	un.Timeout = 5 * time.Minute
	_, err = un.Run(name)
	if err == nil || keepHistory {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "already uninstalled") {
		return err
	}
	// 历史版本用 --keep-history 留下的 uninstalled 记录：helm 拒绝再次卸载，
	// 直接清掉 release Secret（owner=helm,name=<release>），让界面能彻底删除
	return s.purgeReleaseSecrets(ctx, c, ns, name)
}

func (s *HelmService) purgeReleaseSecrets(ctx context.Context, c *kube.Client, ns, name string) error {
	return c.Clientset.CoreV1().Secrets(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: "owner=helm,name=" + name,
	})
}

func (s *HelmService) toReleaseInfo(r *release.Release) *HelmReleaseInfo {
	info := &HelmReleaseInfo{
		Name:      r.Name,
		Namespace: r.Namespace,
		Version:   r.Version,
		Manifest:  r.Manifest,
	}
	if r.Info != nil {
		info.Status = string(r.Info.Status)
		info.Notes = r.Info.Notes
		info.Updated = r.Info.LastDeployed.Format(time.RFC3339)
	}
	if r.Chart != nil && r.Chart.Metadata != nil {
		info.Chart = r.Chart.Name()
		info.ChartVer = r.Chart.Metadata.Version
		info.AppVersion = r.Chart.Metadata.AppVersion
	}
	if r.Config != nil {
		b, _ := yaml.Marshal(r.Config)
		info.Values = string(b)
	}
	return info
}

// ------------------- Chart 仓库管理 -------------------

type RepoChartItem struct {
	Name          string   `json:"name"`
	LatestVersion string   `json:"latestVersion"`
	Versions      []string `json:"versions"`
	Description   string   `json:"description"`
	Icon          string   `json:"icon"`
}

type HelmRepoItem struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Username   string `json:"username"`
	HasAuth    bool   `json:"hasAuth"`
	UpdatedAt  string `json:"updatedAt"`
	ChartCount int    `json:"chartCount"`
}

// ListRepos 列出全部仓库
func (s *HelmService) ListRepos() ([]HelmRepoItem, error) {
	var repos []model.HelmRepo
	if err := s.db.Order("id ASC").Find(&repos).Error; err != nil {
		return nil, err
	}
	out := make([]HelmRepoItem, 0, len(repos))
	for _, r := range repos {
		item := HelmRepoItem{
			ID: r.ID, Name: r.Name, URL: r.URL, Username: r.Username,
			HasAuth: r.Password != "", UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
		}
		if idx, ok := s.getCachedIndex(r.ID); ok {
			item.ChartCount = len(idx.Entries)
		}
		out = append(out, item)
	}
	return out, nil
}

// AddRepo 新增仓库
func (s *HelmService) AddRepo(name, url, username, password string) (*model.HelmRepo, error) {
	r := &model.HelmRepo{Name: name, URL: url, Username: username, Password: password}
	if err := s.db.Create(r).Error; err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateRepo 更新仓库（空字段不覆盖）
func (s *HelmService) UpdateRepo(id uint, url, username, password string) (*model.HelmRepo, error) {
	var r model.HelmRepo
	if err := s.db.First(&r, id).Error; err != nil {
		return nil, err
	}
	if url != "" {
		r.URL = url
	}
	if username != "" {
		r.Username = username
	}
	if password != "" {
		r.Password = password
	}
	if err := s.db.Save(&r).Error; err != nil {
		return nil, err
	}
	s.invalidateRepoIndex(id)
	return &r, nil
}

// RemoveRepo 删除仓库
func (s *HelmService) RemoveRepo(id uint) error {
	if err := s.db.Delete(&model.HelmRepo{}, id).Error; err != nil {
		return err
	}
	s.invalidateRepoIndex(id)
	return nil
}

// RefreshRepo 重新拉取仓库 index.yaml，返回 chart 数量
func (s *HelmService) RefreshRepo(id uint) (int, error) {
	var r model.HelmRepo
	if err := s.db.First(&r, id).Error; err != nil {
		return 0, err
	}
	entry := &repo.Entry{Name: r.Name, URL: r.URL, Username: r.Username, Password: r.Password}
	getters := getter.All(cli.New())
	cr, err := repo.NewChartRepository(entry, getters)
	if err != nil {
		return 0, err
	}
	// DownloadIndexFile 把 index.yaml 下载到缓存文件并返回路径，需读回 IndexFile
	path, err := cr.DownloadIndexFile()
	if err != nil {
		return 0, fmt.Errorf("拉取仓库 index 失败: %w", err)
	}
	idx, err := repo.LoadIndexFile(path)
	if err != nil {
		return 0, fmt.Errorf("解析仓库 index 失败: %w", err)
	}
	// repoCache 是并发读写共享 map（RefreshRepo 写 + RepoCharts/ListRepos 的
	// getCachedIndex 读），必须持锁——裸写触发 runtime fatal 崩整个进程
	s.reposMu.Lock()
	s.repoCache[keyOf(id)] = cachedRepoIndex{index: idx, fetched: time.Now()}
	s.reposMu.Unlock()
	return len(idx.Entries), nil
}

// RepoCharts 列出仓库内 chart（按名称折叠版本）
func (s *HelmService) RepoCharts(repoID uint, search string) ([]RepoChartItem, error) {
	idx, ok := s.getCachedIndex(repoID)
	if !ok {
		if _, err := s.RefreshRepo(repoID); err != nil {
			return nil, err
		}
		idx, ok = s.getCachedIndex(repoID)
		if !ok {
			return nil, fmt.Errorf("仓库 index 不可用，请先刷新")
		}
	}
	out := make([]RepoChartItem, 0)
	names := make([]string, 0, len(idx.Entries))
	for n := range idx.Entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if search != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(search)) {
			continue
		}
		versions := idx.Entries[name]
		if len(versions) == 0 {
			continue
		}
		item := RepoChartItem{
			Name:          name,
			LatestVersion: versions[0].Version,
			Description:   versions[0].Description,
			Icon:          versions[0].Icon,
		}
		for _, v := range versions {
			if !v.Removed {
				item.Versions = append(item.Versions, v.Version)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *HelmService) repoEntry(name string) (*repo.Entry, error) {
	var r model.HelmRepo
	if err := s.db.Where("name = ?", name).First(&r).Error; err != nil {
		return nil, fmt.Errorf("仓库 %q 不存在", name)
	}
	return &repo.Entry{Name: r.Name, URL: r.URL, Username: r.Username, Password: r.Password}, nil
}

// loadChartFromRepo 从仓库下载 chart 到临时目录并加载
func (s *HelmService) loadChartFromRepo(entry *repo.Entry, chartName, version string) (*chart.Chart, error) {
	getters := getter.All(cli.New())
	cr, err := repo.NewChartRepository(entry, getters)
	if err != nil {
		return nil, err
	}
	// DownloadIndexFile 下载 index.yaml 到缓存文件并返回路径，需读回 IndexFile
	dlPath, err := cr.DownloadIndexFile()
	if err != nil {
		return nil, fmt.Errorf("拉取仓库 index 失败: %w", err)
	}
	index, err := repo.LoadIndexFile(dlPath)
	if err != nil {
		return nil, fmt.Errorf("解析仓库 index 失败: %w", err)
	}
	if version == "" {
		versions := index.Entries[chartName]
		if len(versions) == 0 {
			return nil, fmt.Errorf("仓库中不存在 chart %q", chartName)
		}
		version = versions[0].Version
	}
	var chartURL string
	for _, v := range index.Entries[chartName] {
		if v.Version == version && len(v.URLs) > 0 {
			chartURL = v.URLs[0]
			break
		}
	}
	if chartURL == "" {
		return nil, fmt.Errorf("chart %s 版本 %s 不存在", chartName, version)
	}
	if !strings.HasPrefix(chartURL, "http") {
		chartURL = strings.TrimRight(entry.URL, "/") + "/" + chartURL
	}
	client, err := getters.ByScheme("https")
	if err != nil {
		client, err = getters.ByScheme("http")
		if err != nil {
			return nil, err
		}
	}
	buf, err := client.Get(chartURL, getter.WithBasicAuth(entry.Username, entry.Password))
	if err != nil {
		return nil, fmt.Errorf("下载 chart 失败: %w", err)
	}
	tmp, err := os.MkdirTemp("", "helm-chart-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	chartFile := filepath.Join(tmp, "chart.tgz")
	if err := os.WriteFile(chartFile, buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return chartloader.Load(chartFile)
}

func (s *HelmService) getCachedIndex(id uint) (*repo.IndexFile, bool) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	c, ok := s.repoCache[keyOf(id)]
	if !ok {
		return nil, false
	}
	if time.Since(c.fetched) > repoIndexTTL {
		return nil, false
	}
	return c.index, true
}

func (s *HelmService) invalidateRepoIndex(id uint) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	delete(s.repoCache, keyOf(id))
}

func keyOf(id uint) string {
	return fmt.Sprintf("repo-%d", id)
}

// ChartValues 获取 chart 的默认 values（chart 包内 values.yaml），供安装/升级弹窗预填。
func (s *HelmService) ChartValues(repoName, chartName, version string) (string, error) {
	entry, err := s.repoEntry(repoName)
	if err != nil {
		return "", err
	}
	chrt, err := s.loadChartFromRepo(entry, chartName, version)
	if err != nil {
		return "", err
	}
	if len(chrt.Values) == 0 {
		return "", nil
	}
	b, err := yaml.Marshal(chrt.Values)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseValues 解析 values YAML 字符串为 map
func parseValues(values string) (map[string]interface{}, error) {
	if strings.TrimSpace(values) == "" {
		return map[string]interface{}{}, nil
	}
	var vals map[string]interface{}
	if err := yaml.Unmarshal([]byte(values), &vals); err != nil {
		return nil, fmt.Errorf("values YAML 解析失败: %w", err)
	}
	if vals == nil {
		vals = map[string]interface{}{}
	}
	return vals, nil
}
