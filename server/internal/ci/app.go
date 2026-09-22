// Package ci 内置 CI（Tekton）融合层：从 ci-platform 平移并按
// kube-console 的多集群 + 项目命名空间隔离模型重组。
//
//	节点插件（embed）→ 编译器/校验器 → 执行服务（StartRun/Syncer/Reconciler）
//	凭证服务（K8s Secret 扇出）→ handler（internal/handler/cicd.go）
package ci

import (
	"context"
	"io/fs"
	"log"
	"sync"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/credential"
	"kube-console/server/internal/ci/gitrepo"
	"kube-console/server/internal/ci/node"
	"kube-console/server/internal/ci/artifact"
	"kube-console/server/internal/ci/deploy"
	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline"
	"kube-console/server/internal/ci/runtime"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/ci/websocket"
	"kube-console/server/internal/config"
	"kube-console/server/internal/service"
)

// Deps CI 融合层的全部依赖（由 main 装配，handler 引用）。
type Deps struct {
	Cfg  *config.CIConfig
	DB   *gorm.DB
	Nodes nodetype.Registry
	Pipe  *pipeline.Service
	Store *pipeline.Store
	Creds *credential.Service
	RT      *runtime.Service
	Sched   *runtime.Scheduler
	Webhook *runtime.Webhook
	Art     *artifact.Service
	Deploy  *deploy.Service
	Hub     *websocket.Hub

	clusters *service.ClusterManager

	// k8sClients 按集群缓存的 Tekton 客户端（集群变更时 Invalidate 清理）。
	// syncer/reconciler/scheduler/webhook/handler 多 goroutine 并发访问，必须加锁
	// （裸 map 并发读写会触发 runtime fatal 直接崩进程）。
	k8sMu    sync.RWMutex
	k8sCache map[string]*tekton.Client
}

// NewDeps 装配 CI 依赖：加载内嵌节点插件、构建各服务。
// 任一硬性依赖失败返回错误（节点插件缺失 = 二进制不完整）。
func NewDeps(db *gorm.DB, cfg *config.CIConfig, clusters *service.ClusterManager) (*Deps, error) {
	reg := node.NewRegistry()
	nodesFS, err := fs.Sub(node.NodesFS, "nodes")
	if err != nil {
		return nil, err
	}
	// imageRegistry：CI 任务镜像仓库前缀（ci.imageRegistry / KC_CI_IMAGE_REGISTRY），
	// 节点模板里以 {{imageRegistry}} 引用；清单见 docs/images.md
	if cfg.ImageRegistry == "" {
		cfg.ImageRegistry = "harbor.cqyxpt.site:8443/library"
	}
	if err := node.NewLoader(reg).WithGlobals(map[string]interface{}{
		"imageRegistry": cfg.ImageRegistry,
	}).LoadEmbedded(nodesFS); err != nil {
		return nil, err
	}
	log.Printf("ci: 加载节点插件 %d 个（镜像仓库前缀 %s）", len(reg.List()), cfg.ImageRegistry)

	d := &Deps{
		Cfg:      cfg,
		DB:       db,
		Nodes:    reg,
		clusters: clusters,
		Hub:      websocket.NewHub(),
		k8sCache: map[string]*tekton.Client{},
	}
	// Namespace 字段仅兼容保留；运行时按项目 ns 显式传参
	d.Pipe = pipeline.NewService(reg, cfg.PlatformNS)
	d.Store = pipeline.NewStore(db, d.Pipe)
	d.Creds = credential.NewService(db, cfg, d.K8sFor, reg)
	d.RT = runtime.NewService(cfg, db, d.Pipe, d.Store, d.Hub, d.Creds, d.K8sFor)
	d.Art = artifact.NewService(db, cfg)
	d.Deploy = deploy.NewService(db, d.K8sFor)
	d.RT.SetNotifier(NewNotifier(db)) // 执行事件 → kube-console 通知渠道（无渠道时空转）
	d.RT.SetArtifactRegistrar(d.Art.Registrar())
	d.RT.SetDeployService(d.Deploy)
	d.Sched = runtime.NewScheduler(db, d.RT)
	d.Webhook = runtime.NewWebhook(db, d.RT)
	return d, nil
}

// K8sFor 按集群名取 Tekton 客户端（惰性构建 + 缓存；集群配置变更时由 main 调 Invalidate 清理）。
func (d *Deps) K8sFor(clusterName string) (*tekton.Client, error) {
	d.k8sMu.RLock()
	if c, ok := d.k8sCache[clusterName]; ok {
		d.k8sMu.RUnlock()
		return c, nil
	}
	d.k8sMu.RUnlock()

	kc, err := d.clusters.ClientChecked(clusterName)
	if err != nil {
		return nil, err
	}
	c := tekton.NewClient(clusterName, kc, d.Cfg)
	d.k8sMu.Lock()
	// 双重检查：并发首建时只保留一份
	if existing, ok := d.k8sCache[clusterName]; ok {
		d.k8sMu.Unlock()
		return existing, nil
	}
	d.k8sCache[clusterName] = c
	d.k8sMu.Unlock()
	return c, nil
}

// TektonInstalled 探测集群是否安装了 Tekton Pipelines（tekton.dev/v1 提供 pipelines 资源）。
// 注意 API group 是 tekton.dev（CRD 名才是 pipelines.tekton.dev）。
func (d *Deps) TektonInstalled(clusterName string) (bool, error) {
	kc, err := d.clusters.ClientChecked(clusterName)
	if err != nil {
		return false, err
	}
	res, err := kc.Discovery.ServerResourcesForGroupVersion("tekton.dev/v1")
	if err != nil || res == nil {
		return false, nil
	}
	for _, r := range res.APIResources {
		if r.Name == "pipelines" {
			return true, nil
		}
	}
	return false, nil
}

// Invalidate 集群 kubeconfig 变更后清理该集群的 Tekton 客户端缓存。
func (d *Deps) Invalidate(clusterName string) {
	d.k8sMu.Lock()
	delete(d.k8sCache, clusterName)
	d.k8sMu.Unlock()
}

// Start 启动后台循环（syncer + reconciler），ctx 取消时退出。
func (d *Deps) Start(ctx context.Context) {
	if !d.Cfg.Enabled {
		log.Printf("ci: 已禁用（ci.enabled=false），不启动后台循环")
		return
	}
	go d.RT.StartSyncer(ctx)
	go d.RT.StartReconciler(ctx)
	go d.Sched.Start(ctx)
}

// GitRepoRefs 供 handler 查仓库分支/Tag（git ls-remote，git 二进制需在镜像内）。
func GitRepoRefs(ctx context.Context, url, username, password, token string) (*gitrepo.Refs, error) {
	au, err := gitrepo.AuthURL(url, username, password, token)
	if err != nil {
		return nil, err
	}
	return gitrepo.ListRemoteRefs(ctx, au)
}
