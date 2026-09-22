// Package runtime 负责 CI 流水线「提交执行」与「状态同步」（多集群 + 项目 ns 隔离）。
//
// 与 ci-platform 原实现的关键差异：
//   - k8s 客户端按集群惰性获取（k8sFor），不再单集群硬编码
//   - 命名空间 = 项目 ns（runMeta 从 run→pipeline→project 解析），不再全局单 ns
//   - Syncer 按 (集群, ns) 分组批量同步
//   - 无 leader election / 业务指标 / GitLab 回写 / 发布记录（延后阶段）
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"kube-console/server/internal/ci/artifact"
	"kube-console/server/internal/ci/credential"
	"kube-console/server/internal/ci/deploy"
	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/ci/pipeline/compiler"
	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/pipeline/validator"
	"kube-console/server/internal/ci/tekton"
	ws "kube-console/server/internal/ci/websocket"
	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
	pipelinesvc "kube-console/server/internal/ci/pipeline"
)

// submitGrace StartRun 提交后 Tekton controller 创建 PipelineRun 对象的正常窗口。
// 窗口内 Get 不到对象不算失败（StartRun/Syncer 竞态），避免误判。
const submitGrace = 60 * time.Second

// syncBatchLimit 单轮 syncOnce 处理的 run 数上限（防积压时单轮失控）。
const syncBatchLimit = 200

// Notifier 执行事件推送接口（由 kube-console 通知服务实现；nil = 关闭）。
type Notifier interface {
	Send(ctx context.Context, ev NotifyEvent)
}

// NotifyEvent 一次执行事件。
type NotifyEvent struct {
	Event    string // run.failed / run.approval_pending
	Pipeline string
	RunNo    int
	Status   string
	Message  string
}

// runMeta 一次运行在集群上的落点（集群 + 项目命名空间）。
type runMeta struct {
	Cluster string
	NS      string
}

// Service 负责“提交执行”与“状态同步”：
//
//	StartRun   DSL → Pipeline CR + PipelineRun + PVC（项目 ns 内），落 ci_runs/ci_task_runs
//	StartSyncer 后台轮询各集群 Tekton 状态 → 回写 DB → WS 推送 → 完成后清理 PVC
type Service struct {
	cfg  *config.CIConfig
	db   *gorm.DB
	pipe *pipelinesvc.Service
	store *pipelinesvc.Store
	hub  *ws.Hub
	creds *credential.Service // 凭证解析（envFrom 注入）

	// k8sFor 按集群名惰性取 Tekton 客户端（内部缓存 + 熔断判断由调用方保证）
	k8sFor func(clusterName string) (*tekton.Client, error)

	runTimeout time.Duration

	notifier  Notifier         // 执行事件推送（失败/待审批），nil = no-op
	artReg    *artifact.Registrar
	deploySvc *deploy.Service

	// nodeParams 缓存：version 不可变（保存即新行），按 versionID 缓存其
	// nodeID→params 解析结果，避免每 3s 对每个 run 反复读库 + 解析 JSON。
	nodeParamsMu    sync.Mutex
	nodeParamsCache map[uint]map[string]map[string]interface{}
}

// NewService 构建执行服务。runTimeout 解析失败回退 2h。
func NewService(cfg *config.CIConfig, db *gorm.DB, pipe *pipelinesvc.Service, store *pipelinesvc.Store,
	hub *ws.Hub, creds *credential.Service, k8sFor func(clusterName string) (*tekton.Client, error)) *Service {
	timeout := 2 * time.Hour
	if d, err := time.ParseDuration(cfg.RunTimeout); err == nil && d > 0 {
		timeout = d
	}
	return &Service{
		cfg: cfg, db: db, pipe: pipe, store: store, hub: hub, creds: creds, k8sFor: k8sFor,
		runTimeout:      timeout,
		nodeParamsCache: map[uint]map[string]map[string]interface{}{},
	}
}

// SetNotifier 注入通知服务（装配阶段调用；nil = 关闭通知）。
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetArtifactRegistrar 注入制品注册器（任务成功时登记；nil = 关闭）。
func (s *Service) SetArtifactRegistrar(r *artifact.Registrar) { s.artReg = r }

// SetDeployService 注入发布记录服务（部署任务成功时快照；nil = 关闭）。
func (s *Service) SetDeployService(d *deploy.Service) { s.deploySvc = d }

// registerOutputs 任务成功后登记制品 + 发布记录（best-effort：失败只记日志）。
func (s *Service) registerOutputs(ctx context.Context, run *model.CIRun, cluster, nodeID, nodeType string, results map[string]string) {
	if s.artReg == nil && s.deploySvc == nil {
		return
	}
	// 节点参数（注册器按参数判断存储/类型；发布记录按 deployTarget/release 定位 workload）
	var ver model.CIPipelineVersion
	if err := s.db.WithContext(ctx).Where("id = ?", run.VersionID).First(&ver).Error; err != nil {
		return
	}
	g, err := pipelinesvc.ParseGraph(ver.GraphJSON)
	if err != nil {
		return
	}
	var params map[string]interface{}
	for _, n := range g.Nodes {
		if n.ID == nodeID {
			params = n.Params
			break
		}
	}
	if s.artReg != nil {
		var projID uint
		_ = s.db.WithContext(ctx).Model(&model.CIPipeline{}).
			Select("project_id").Where("id = ?", run.PipelineID).Scan(&projID).Error
		items := s.artReg.FromTaskRun(nodeType, params, results, artifact.RunCtx{
			ClusterName: cluster, ProjectID: projID, PipelineID: run.PipelineID, PipelineRunID: run.ID,
		})
		if len(items) > 0 {
			if err := s.artReg.Register(ctx, items); err != nil {
				log.Printf("runtime: run %d 制品登记失败: %v", run.ID, err)
			}
		}
	}
	if s.deploySvc != nil && (nodeType == "k8s-deploy" || nodeType == "helm-deploy") {
		s.deploySvc.RecordDeployment(ctx, run, cluster, nodeID, nodeType, params)
	}
}

// runMetaFor 解析一次运行的落点：run → pipeline（集群）→ project（ns）。
func (s *Service) runMetaFor(ctx context.Context, run *model.CIRun) (runMeta, error) {
	var p model.CIPipeline
	if err := s.db.WithContext(ctx).First(&p, run.PipelineID).Error; err != nil {
		return runMeta{}, fmt.Errorf("流水线不存在: %w", err)
	}
	var proj model.CIProject
	if err := s.db.WithContext(ctx).First(&proj, p.ProjectID).Error; err != nil {
		return runMeta{}, fmt.Errorf("项目不存在: %w", err)
	}
	return runMeta{Cluster: p.ClusterName, NS: proj.Namespace}, nil
}

// k8sForRun 取一次运行所属集群的 Tekton 客户端 + 项目 ns。
func (s *Service) k8sForRun(ctx context.Context, run *model.CIRun) (*tekton.Client, runMeta, error) {
	meta, err := s.runMetaFor(ctx, run)
	if err != nil {
		return nil, runMeta{}, err
	}
	k8s, err := s.k8sFor(meta.Cluster)
	if err != nil {
		return nil, meta, err
	}
	return k8s, meta, nil
}

// ============ 启动执行 ============

// StartRun 提交一次执行：校验 → 编译 → apply Pipeline → 落库 → 建 PVC → 建 PipelineRun。
// branchOverride 非空时覆盖 git-clone 节点的 branch（Webhook 按实际推送分支触发用）。
//
// 一致性约定：DB 是权威（先落 pending 记录再建 K8s 资源），任何 K8s 步骤失败都补偿
// 删除 DB 记录 + 已建 PVC，不留孤儿；runName/PVC 名带随机后缀，杜绝并发撞名。
var globalVarRe = regexp.MustCompile(`\$\{global\.([A-Z0-9_]+)\}`)

// injectGlobalVars 把节点参数中的 ${global.KEY} 替换为该集群的全局变量值（编译前）。
func (s *Service) injectGlobalVars(ctx context.Context, cluster string, g *dsl.Graph) {
	var gvs []model.CIGlobalVar
	if err := s.db.WithContext(ctx).Where("cluster_name = ?", cluster).Find(&gvs).Error; err != nil || len(gvs) == 0 {
		return
	}
	m := make(map[string]string, len(gvs))
	for _, v := range gvs {
		m[v.Key] = v.Value
	}
	for i := range g.Nodes {
		for k, pv := range g.Nodes[i].Params {
			if sv, ok := pv.(string); ok {
				g.Nodes[i].Params[k] = replaceGlobalVars(sv, m)
			}
		}
	}
}

// replaceGlobalVars 替换字符串中的 ${global.KEY} 占位符；未定义的 key 原样保留。
func replaceGlobalVars(sv string, m map[string]string) string {
	if !strings.Contains(sv, "${global.") {
		return sv
	}
	return globalVarRe.ReplaceAllStringFunc(sv, func(match string) string {
		key := match[len("${global.") : len(match)-1]
		if v, ok := m[key]; ok {
			return v
		}
		return match
	})
}

func (s *Service) StartRun(ctx context.Context, pipelineID, versionID, uid uint, username, triggerType, branchOverride, commitOverride string) (*model.CIRun, error) {
	p, err := s.store.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	// 落点：集群 + 项目 ns
	var proj model.CIProject
	if err := s.db.WithContext(ctx).First(&proj, p.ProjectID).Error; err != nil {
		return nil, errcode.New(errcode.NotFound, "项目不存在")
	}
	ns := proj.Namespace
	k8s, err := s.k8sFor(p.ClusterName)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}

	// 1) 取版本（指定或最新）
	var version *model.CIPipelineVersion
	if versionID > 0 {
		var v model.CIPipelineVersion
		if err := s.db.WithContext(ctx).Where("id = ? AND pipeline_id = ?", versionID, pipelineID).First(&v).Error; err != nil {
			return nil, errcode.New(errcode.NotFound, "版本不存在")
		}
		version = &v
	} else {
		version, err = s.store.GetLatest(ctx, pipelineID)
		if err != nil {
			return nil, err
		}
	}

	// 2) 解析 + 校验 + 运行时增强 + 编译
	graph, err := pipelinesvc.ParseGraph(version.GraphJSON)
	if err != nil {
		return nil, err
	}
	if res := s.pipe.Validator.Validate(graph); !res.Valid {
		return nil, &pipelinesvc.CompileError{Errors: res.Errors}
	}
	// 节点按 DAG 拓扑序重排（稳定：同层保持画布原序）
	if order := validator.Topological(graph); order != nil {
		byID := make(map[string]dsl.Node, len(graph.Nodes))
		for _, n := range graph.Nodes {
			byID[n.ID] = n
		}
		sorted := make([]dsl.Node, 0, len(order))
		for _, id := range order {
			if n, ok := byID[id]; ok {
				sorted = append(sorted, n)
			}
		}
		if len(sorted) == len(graph.Nodes) {
			graph.Nodes = sorted
		}
	}
	log.Printf("runtime: StartRun pipeline=%d 集群=%s ns=%s versionID=%d 节点=%v 边=%v",
		p.ID, p.ClusterName, ns, version.ID, nodeIDs(graph), edgePairs(graph))
	// 分支/tag 覆盖时强制完整克隆：浅克隆只含默认分支，切非默认分支需 FETCH_HEAD
	if branchOverride != "" {
		for i := range graph.Nodes {
			if graph.Nodes[i].Type == "git-clone" {
				if graph.Nodes[i].Params == nil {
					graph.Nodes[i].Params = map[string]interface{}{}
				}
				graph.Nodes[i].Params["shallow"] = false
			}
		}
	}
	// 全局变量注入（${global.KEY} → 集群级变量值；未定义 key 保留）
	s.injectGlobalVars(ctx, p.ClusterName, graph)
	s.enrichGraph(graph, branchOverride, commitOverride) // 注入存储/构建端点伪参数 + 分支/commit 覆盖
	spec, err := s.pipe.Compiler.Compile(graph, ns)
	if err != nil {
		return nil, err
	}
	log.Printf("runtime: StartRun pipeline=%d 编译结果 tasks=%v", p.ID, taskRunAfterSummary(spec))

	// 3) 原子分配 runNo；runName/PVC 名加随机后缀（并发触发不撞名）
	runNo, err := s.nextRunNo(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	suffix := randomSuffix()
	runName := fmt.Sprintf("run-%d-%d-%s", p.ID, runNo, suffix)
	pvcName := fmt.Sprintf("ci-ws-%d-%d-%s", p.ID, runNo, suffix)

	// Pipeline CR 按 run 唯一：branchOverride/shallow 这类 run 级差异会被烤进 taskSpec，
	// 若多个 run 共享 pl-<id>-v<version>（apply 整体覆盖），并发触发时先建的 run 生成
	// TaskRun 会读到后一个 run 的 CR（跨 run 串分支，审批自发现修复后此路径漏网）。
	// 每 run 独立 CR 后覆盖不存在；终态时 finishRun/Cancel/cleanupRun 删 CR，
	// 孤儿 CR 由 reconcile 按「活跃 run 的 CR 集合」兜底清理。
	pipelineCRName := fmt.Sprintf("pl-%d-v%d-r%d-%s", p.ID, version.Version, runNo, suffix)

	// 4) ensure 项目 ns + 项目 CI 基础设施 + apply Pipeline CR（幂等覆盖，失败无需回滚）。
	//    ns 必须先于凭证注入创建：凭据 Secret 按需拉齐到 run ns 需要 ns 已存在。
	//    基础设施（运行 SA + RBAC）随项目创建时已写入，这里兜底是为了平台早期版本
	//    建的老项目 / 人工准备的 ns：缺 SA 时 TaskRun 会 PodCreationFailed，那时既没
	//    Pod 也没日志，所以能在提交前把原因说清楚就不要留给 Tekton。
	if err := k8s.EnsureNamespace(ctx, ns); err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "确保项目 ns 失败: %v", err)
	}
	if added, err := k8s.EnsureCIInfra(ctx, ns, s.cfg.ServiceAccount); err != nil {
		if errors.Is(err, tekton.ErrInfraSAUnavailable) {
			return nil, errcode.Newf(errcode.DepUnavailable, "CI 基础设施不可用: %v", err)
		}
		// 其余写入失败不阻断：基础设施可能已由人工备好（例如平台账号只有读权限），
		// 真有问题时 Tekton 会在 TaskRun 上明确报出来，此处留日志便于对照
		log.Printf("runtime: 补齐 CI 基础设施 %s 失败（继续执行）: %v", ns, err)
	} else if len(added) > 0 {
		log.Printf("runtime: 为项目 ns %s 补写 CI 基础设施: %v", ns, added)
	}
	pullSecrets, err := s.injectCredentials(ctx, spec, graph, p.ProjectID, p.ClusterName, ns) // 凭证注入 + Secret 拉齐到 run ns
	if err != nil {
		return nil, err
	}
	if s.cfg.ImagePullSecret != "" {
		pullSecrets = appendUniqueString(pullSecrets, s.cfg.ImagePullSecret)
	}
	specMap, err := specToMap(spec)
	if err != nil {
		return nil, err
	}
	if err := k8s.ApplyPipeline(ctx, ns, pipelineCRName, specMap); err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "apply Pipeline 失败: %v", err)
	}

	// 5) 先落库（pending）。DB 记录是 syncer 的权威驱动，必须先于 K8s 资源存在；
	//    后续 K8s 步骤失败则补偿删除（cleanupRun），不留孤儿。
	branch := gitParam(graph, "branch")
	if branchOverride != "" {
		branch = branchOverride
	}
	commit := gitParam(graph, "commit")
	if commitOverride != "" {
		commit = commitOverride
	}
	now := time.Now()
	run := &model.CIRun{
		ClusterName:      p.ClusterName,
		PipelineID:       p.ID,
		VersionID:        version.ID,
		RunNo:            runNo,
		Status:           model.CIRunStatusPending,
		TriggerType:      triggerType,
		GitCommit:        commit,
		GitBranch:        branch,
		GitRepo:          gitParam(graph, "url"),
		StartedBy:        username,
		TektonRunName:    runName,
		TektonPipelineCR: pipelineCRName,
		PVCName:          pvcName,
		StartedAt:        &now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, err
	}
	for _, n := range graph.Nodes {
		tr := &model.CITaskRun{
			RunID:           run.ID,
			NodeID:          n.ID,
			NodeType:        n.Type,
			Name:            n.ID,
			Status:          model.CIRunStatusPending,
			TektonNamespace: ns,
		}
		if err := s.db.WithContext(ctx).Create(tr).Error; err != nil {
			s.cleanupRun(ctx, run, k8s, ns)
			return nil, err
		}
	}

	// 6) PVC + PipelineRun（失败 → 补偿：删 PVC + DB 记录）
	if err := k8s.CreatePVC(ctx, ns, pvcName, s.cfg.PVCSize); err != nil {
		s.cleanupRun(ctx, run, k8s, ns)
		return nil, errcode.Newf(errcode.DepUnavailable, "创建 workspace PVC 失败: %v", err)
	}
	// 项目级依赖缓存：任一节点勾选 cache 且平台开关开启时，确保缓存 PVC 并绑定
	cacheClaim := s.ensureCachePVC(ctx, k8s, ns, p, graph)
	params := gitParams(graph, branchOverride, commitOverride)
	if err := k8s.CreatePipelineRun(ctx, ns, runName, pipelineCRName, s.cfg.ServiceAccount, pvcName, cacheClaim, params, pullSecrets); err != nil {
		s.cleanupRun(ctx, run, k8s, ns)
		return nil, errcode.Newf(errcode.DepUnavailable, "创建 PipelineRun 失败: %v", err)
	}

	s.publishRun(ctx, run.ID)
	return run, nil
}

// Cancel 停止一次进行中的执行（执行中心「停止」入口）。
// Tekton 侧置 PipelineRunCancelled（controller 终止 Pod），DB 侧立即置
// cancelled 并清理 workspace PVC；进行中的 TaskRun 由 syncer 收敛为 cancelled。
// 已终态的 run 重复调用是幂等 no-op。
func (s *Service) Cancel(ctx context.Context, runID uint) (*model.CIRun, error) {
	var run model.CIRun
	if err := s.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.New(errcode.NotFound, "执行记录不存在")
		}
		return nil, err
	}
	switch run.Status {
	case model.CIRunStatusSuccess, model.CIRunStatusFailed, model.CIRunStatusCancelled:
		return &run, nil // 已终态，幂等返回
	}
	k8s, meta, err := s.k8sForRun(ctx, &run)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	if run.TektonRunName != "" {
		if err := k8s.CancelPipelineRun(ctx, meta.NS, run.TektonRunName); err != nil {
			return nil, errcode.Newf(errcode.DepUnavailable, "取消 PipelineRun 失败: %v", err)
		}
	}
	if run.TektonPipelineCR != "" {
		_ = k8s.DeletePipeline(ctx, meta.NS, run.TektonPipelineCR) // best-effort：reconcile 兜底
	}
	now := time.Now()
	// run 置终态 + 未终态的 task 一并置 cancelled
	if err := s.db.WithContext(ctx).Model(&run).Updates(map[string]interface{}{
		"status":      model.CIRunStatusCancelled,
		"finished_at": now,
	}).Error; err != nil {
		return nil, err
	}
	_ = s.db.WithContext(ctx).
		Where("run_id = ? AND status IN ?", run.ID,
			[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
		Updates(map[string]interface{}{"status": model.CIRunStatusCancelled, "finished_at": now}).Error
	if run.PVCName != "" {
		_ = k8s.DeletePVC(ctx, meta.NS, run.PVCName)
	}
	run.Status = model.CIRunStatusCancelled
	run.FinishedAt = &now
	s.publishRun(ctx, run.ID)
	log.Printf("runtime: run %d 已取消（停止）", run.ID)
	return &run, nil
}

// cleanupRun 补偿删除一次失败的启动：PVC + DB 记录（硬删，避免残留占 runNo 唯一键）。
func (s *Service) cleanupRun(ctx context.Context, run *model.CIRun, k8s *tekton.Client, ns string) {
	if run == nil {
		return
	}
	if run.PVCName != "" {
		if err := k8s.DeletePVC(ctx, ns, run.PVCName); err != nil {
			log.Printf("runtime: 补偿删 PVC %s 失败: %v（reconcile 会兜底）", run.PVCName, err)
		}
	}
	if run.TektonPipelineCR != "" {
		_ = k8s.DeletePipeline(ctx, ns, run.TektonPipelineCR)
	}
	_ = s.db.WithContext(ctx).Unscoped().Where("run_id = ?", run.ID).Delete(&model.CITaskRun{}).Error
	if err := s.db.WithContext(ctx).Unscoped().Delete(&model.CIRun{}, run.ID).Error; nil == err {
		// ok
	} else {
		log.Printf("runtime: 补偿删 run 记录 %d 失败: %v", run.ID, err)
	}
}

// randomSuffix 生成 5 位十六进制随机后缀，用于 runName/PVC 名防并发撞名。
func randomSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%05d", time.Now().UnixNano()%100000)
	}
	return fmt.Sprintf("%x", b)[:5]
}

// ensureCachePVC 项目级依赖缓存 PVC（ci-cache-p<projectID>）：
//   - 图中任一节点勾选 cache 且平台开关开启 → 幂等确保 PVC 存在并返回其名
//   - 未使用 / 平台关闭 / 创建失败 → 返回空串，PipelineRun 不绑定 cache workspace
func (s *Service) ensureCachePVC(ctx context.Context, k8s *tekton.Client, ns string, p *model.CIPipeline, g *dsl.Graph) string {
	if !s.cfg.CacheEnabled {
		return ""
	}
	used := false
	for i := range g.Nodes {
		if v, ok := g.Nodes[i].Params["cache"].(bool); ok && v {
			used = true
			break
		}
	}
	if !used {
		return ""
	}
	name := fmt.Sprintf("ci-cache-p%d", p.ProjectID)
	if err := k8s.EnsurePVC(ctx, ns, name, s.cfg.CacheSize, s.cfg.CacheAccessMode); err != nil {
		log.Printf("runtime: 缓存 PVC %s 确保失败（本次运行不使用缓存）: %v", name, err)
		return ""
	}
	return name
}

// ============ 状态同步 ============

// StartSyncer 启动后台轮询（3s 间隔），ctx 取消时退出。
func (s *Service) StartSyncer(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncOnce(ctx)
		}
	}
}

// syncOnce 同步进行中的 run：按 (集群, 项目 ns) 分组，每组固定 2 次 API 调用
// （List PipelineRun 状态 + List 全部 TaskRun），与并发 run 数解耦。
func (s *Service) syncOnce(ctx context.Context) {
	var runs []model.CIRun
	if err := s.db.WithContext(ctx).
		Where("status IN ?", []string{model.CIRunStatusPending, model.CIRunStatusRunning}).
		Order("id asc").Limit(syncBatchLimit).
		Find(&runs).Error; err != nil {
		log.Printf("syncer: list runs: %v", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	// 批量解析落点：run → pipeline(集群) → project(ns)
	pipeIDs := make([]uint, 0, len(runs))
	seenPipe := map[uint]bool{}
	for i := range runs {
		if !seenPipe[runs[i].PipelineID] {
			seenPipe[runs[i].PipelineID] = true
			pipeIDs = append(pipeIDs, runs[i].PipelineID)
		}
	}
	var pipes []model.CIPipeline
	_ = s.db.WithContext(ctx).Where("id IN ?", pipeIDs).Find(&pipes).Error
	pipesByID := map[uint]model.CIPipeline{}
	for _, p := range pipes {
		pipesByID[p.ID] = p
	}
	projIDs := make([]uint, 0, len(pipes))
	for _, p := range pipes {
		projIDs = append(projIDs, p.ProjectID)
	}
	var projs []model.CIProject
	_ = s.db.WithContext(ctx).Where("id IN ?", projIDs).Find(&projs).Error
	projByID := map[uint]model.CIProject{}
	for _, pr := range projs {
		projByID[pr.ID] = pr
	}

	type groupKey struct{ cluster, ns string }
	groups := map[groupKey][]*model.CIRun{}
	groupMeta := map[groupKey]runMeta{}
	for i := range runs {
		p, ok := pipesByID[runs[i].PipelineID]
		if !ok {
			// 流水线已删除：不能再跟踪状态，收敛为 cancelled（并尽力取消集群资源），
			// 否则该 run 永远停在 pending/running，reconcile 每轮空刷日志
			s.finalizeLostRun(ctx, &runs[i])
			continue
		}
		proj, ok := projByID[p.ProjectID]
		if !ok {
			s.finalizeLostRun(ctx, &runs[i])
			continue
		}
		key := groupKey{p.ClusterName, proj.Namespace}
		groups[key] = append(groups[key], &runs[i])
		groupMeta[key] = runMeta{Cluster: p.ClusterName, NS: proj.Namespace}
	}

	for key, group := range groups {
		meta := groupMeta[key]
		k8s, err := s.k8sFor(key.cluster)
		if err != nil {
			log.Printf("syncer: 集群 %s 不可用，跳过 %d 个 run: %v", key.cluster, len(group), err)
			continue
		}
		var prStatuses map[string]tekton.RunStatus
		var trsByPR map[string][]tekton.TaskRunInfo
		if v, err := k8s.ListPipelineRunStatuses(ctx, meta.NS); err == nil {
			prStatuses = v
		} else {
			log.Printf("syncer: [%s/%s] list pipelineruns: %v", key.cluster, meta.NS, err)
		}
		if v, err := k8s.ListTaskRunsAll(ctx, meta.NS); err == nil {
			trsByPR = v
		} else {
			log.Printf("syncer: [%s/%s] list taskruns: %v", key.cluster, meta.NS, err)
		}
		// PipelineRun 状态拉取失败时跳过本组（防把「查不到」误判为「被删」误杀 run）
		if prStatuses == nil {
			continue
		}
		for _, run := range group {
			st, found := prStatuses[run.TektonRunName]
			trs := []tekton.TaskRunInfo{}
			if trsByPR != nil {
				trs = trsByPR[run.TektonRunName]
			}
			if err := s.syncRun(ctx, k8s, meta, run, st, found, trs); err != nil {
				log.Printf("syncer: sync run %d: %v", run.ID, err)
			}
		}
	}
}

// finalizeLostRun 流水线/项目行已删除的活跃 run：尽力解析落点取消集群侧
// PipelineRun，DB 侧收敛为 cancelled。ns 无法解析时只做 DB 收敛（集群资源交
// reconcile 按孤儿清理；项目 ns 若已随项目删除，PVC/CR 也随之消失）。
func (s *Service) finalizeLostRun(ctx context.Context, run *model.CIRun) {
	var meta runMeta
	var k8s *tekton.Client
	var p model.CIPipeline
	if err := s.db.WithContext(ctx).First(&p, run.PipelineID).Error; err == nil {
		var proj model.CIProject
		if err := s.db.WithContext(ctx).First(&proj, p.ProjectID).Error; err == nil {
			meta = runMeta{Cluster: p.ClusterName, NS: proj.Namespace}
			if k, err := s.k8sFor(p.ClusterName); err == nil {
				k8s = k
			}
		}
	}
	if k8s != nil {
		if run.TektonRunName != "" {
			_ = k8s.CancelPipelineRun(ctx, meta.NS, run.TektonRunName)
		}
	}
	log.Printf("syncer: run %d 的流水线/项目已删除，收敛为 cancelled", run.ID)
	s.finishRun(ctx, k8s, meta, run, model.CIRunStatusCancelled, "流水线或项目已被删除")
}

func (s *Service) syncRun(ctx context.Context, k8s *tekton.Client, meta runMeta, run *model.CIRun, st tekton.RunStatus, found bool, taskRuns []tekton.TaskRunInfo) error {
	ns := meta.NS

	// 批量模式下 PipelineRun 不存在（不在 List 结果里）= 集群里已被删
	if !found {
		// StartRun 刚提交时 controller 可能还没建出对象（正常窗口），
		// 宽限期内不判失败，避免与 StartRun 竞态误杀
		if now := time.Now(); run.CreatedAt.Add(submitGrace).After(now) {
			return nil
		}
		s.finishRun(ctx, k8s, meta, run, model.CIRunStatusFailed, "PipelineRun 在集群中不存在（被删除?）")
		return nil
	}

	// 超时兜底：DB 里 running/pending 但实际卡在集群（controller 停摆、Pod 悬挂）
	if run.Status == model.CIRunStatusRunning || run.Status == model.CIRunStatusPending {
		if s.runTimeout > 0 && run.StartedAt != nil &&
			time.Since(*run.StartedAt) > s.runTimeout {
			log.Printf("syncer: run %d 执行超过 %v（真实状态 %s），兜底标记失败", run.ID, s.runTimeout, st.Status)
			s.finishRun(ctx, k8s, meta, run, model.CIRunStatusFailed, fmt.Sprintf("执行超时（超过 %v）", s.runTimeout))
			return nil
		}
	}

	changed := false // 有状态变化才推送，避免每 3s 无条件打扰前端

	// 更新 task_runs（增量：DB 里所有 task 均已终态时跳过逐个比对）
	{
		var nonTerm int64
		_ = s.db.WithContext(ctx).Model(&model.CITaskRun{}).
			Where("run_id = ? AND status IN ?", run.ID,
				[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
			Count(&nonTerm).Error
		if nonTerm > 0 {
			for _, tr := range taskRuns {
				if tr.TaskName == "" {
					continue
				}
				newStatus := mapTaskStatus(tr.Status.Status)
				var old model.CITaskRun
				oldStatus := ""
				if err := s.db.WithContext(ctx).
					Where("run_id = ? AND node_id = ?", run.ID, tr.TaskName).
					First(&old).Error; err == nil {
					oldStatus = old.Status
				}
				// 状态、TaskRun 名、Pod 名都没变 → 不写库
				if newStatus == oldStatus && old.TektonTaskRun == tr.Name && old.TektonPodName == tr.PodName {
					continue
				}
				changed = true
				updates := map[string]interface{}{
					"status":          newStatus,
					"tekton_task_run": tr.Name,
					"tekton_pod_name": tr.PodName,
				}
				if tr.Status.StartTime != nil {
					updates["started_at"] = *tr.Status.StartTime
				}
				if tr.Status.CompletionTime != nil {
					updates["finished_at"] = *tr.Status.CompletionTime
				}
				// 终态转换时回写 TaskRun results 快照（vuln_summary / decision / imageRef 等）
				terminal := newStatus == model.CIRunStatusSuccess || newStatus == model.CIRunStatusFailed
				var ress map[string]string
				if terminal && oldStatus != newStatus {
					if rs, err := k8s.GetTaskRunResults(ctx, ns, tr.Name); err == nil && len(rs) > 0 {
						ress = rs
						updates["results"] = model.JSONMap(rs)
					}
				}
				_ = s.db.WithContext(ctx).
					Where("run_id = ? AND node_id = ?", run.ID, tr.TaskName).
					Model(&model.CITaskRun{}).Updates(updates).Error

				// 审批节点刚转 running → 推送「待审批」（transition 只发生一次，天然去重）
				if newStatus == model.CIRunStatusRunning && oldStatus != model.CIRunStatusRunning &&
					old.NodeType == "approval" {
					s.notify(ctx, "run.approval_pending", run, fmt.Sprintf("任务 %s 等待人工审批", tr.TaskName))
				}
				// 任务转成功 → 登记制品 / 发布记录（best-effort，不影响同步链路）
				if newStatus == model.CIRunStatusSuccess && oldStatus != model.CIRunStatusSuccess {
					s.registerOutputs(ctx, run, meta.Cluster, tr.TaskName, old.NodeType, ress)
				}
			}
		}
	}

	// 更新 run 状态（乐观守卫：只允许从 pending/running 推进）
	newStatus := st.Status
	terminal := newStatus == model.CIRunStatusSuccess || newStatus == model.CIRunStatusFailed || newStatus == model.CIRunStatusCancelled
	if terminal && run.Status != newStatus {
		finishedAt := st.CompletionTime
		if finishedAt == nil {
			now := time.Now()
			finishedAt = &now
		}
		res := s.db.WithContext(ctx).Model(&model.CIRun{}).
			Where("id = ? AND status IN ?", run.ID,
				[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
			Updates(map[string]interface{}{
				"status":      newStatus,
				"finished_at": *finishedAt,
			})
		if res.Error != nil {
			log.Printf("syncer: 回写 run %d 终态失败: %v（下轮重试）", run.ID, res.Error)
			changed = true
		} else if res.RowsAffected == 0 {
			log.Printf("syncer: run %d 已被并发置终态，跳过回写 %s", run.ID, newStatus)
		} else {
			changed = true
		}
		// PVC 清理仅在本次回写成功时执行；若被并发取消，取消路径已删过
		if changed && res.Error == nil && run.PVCName != "" {
			if err := k8s.DeletePVC(ctx, ns, run.PVCName); err != nil {
				log.Printf("syncer: delete pvc %s: %v（reconcile 会兜底）", run.PVCName, err)
			}
		}
	} else if newStatus != run.Status {
		res := s.db.WithContext(ctx).Model(&model.CIRun{}).
			Where("id = ? AND status IN ?", run.ID,
				[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
			Update("status", newStatus)
		if res.Error != nil {
			log.Printf("syncer: 回写 run %d 状态失败: %v（下轮重试）", run.ID, res.Error)
		}
		changed = res.RowsAffected > 0
	}

	if changed {
		s.publishRun(ctx, run.ID)
	}
	return nil
}

// finishRun 收敛一次 run 到终态：DB 状态 + 未终态 task 置终态 + 清理 PVC/Pipeline CR。
// k8s 允许为 nil（流水线/项目已删除、ns 无法解析时只做 DB 收敛，集群资源交 reconcile）。
func (s *Service) finishRun(ctx context.Context, k8s *tekton.Client, meta runMeta, run *model.CIRun, status, reason string) {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&model.CIRun{}).
		Where("id = ? AND status IN ?", run.ID,
			[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
		Updates(map[string]interface{}{
			"status":      status,
			"finished_at": now,
		})
	if res.Error != nil {
		log.Printf("syncer: finishRun run %d 写库失败: %v", run.ID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		log.Printf("syncer: run %d 已被并发置终态，finishRun 跳过（%s）", run.ID, reason)
		return
	}
	_ = s.db.WithContext(ctx).
		Where("run_id = ? AND status IN ?", run.ID,
			[]string{model.CIRunStatusPending, model.CIRunStatusRunning}).
		Updates(map[string]interface{}{"status": status, "finished_at": now}).Error
	if k8s != nil {
		if run.PVCName != "" {
			_ = k8s.DeletePVC(ctx, meta.NS, run.PVCName)
		}
		if run.TektonPipelineCR != "" {
			_ = k8s.DeletePipeline(ctx, meta.NS, run.TektonPipelineCR)
		}
	}
	run.Status = status
	run.FinishedAt = &now
	s.publishRun(ctx, run.ID)
	log.Printf("syncer: run %d finished as %s (%s)", run.ID, status, reason)
	if status == model.CIRunStatusFailed {
		s.notify(ctx, "run.failed", run, "失败原因: "+reason)
	} else if status == model.CIRunStatusSuccess {
		s.notify(ctx, "run.succeeded", run, "全部任务执行完成")
	}
}

// notify 推送执行事件（未配置通知渠道时 no-op；取流水线名拼消息）。
func (s *Service) notify(ctx context.Context, event string, run *model.CIRun, msg string) {
	if s.notifier == nil {
		return
	}
	name := ""
	_ = s.db.WithContext(ctx).Model(&model.CIPipeline{}).
		Select("name").Where("id = ?", run.PipelineID).Scan(&name).Error
	s.notifier.Send(ctx, NotifyEvent{
		Event: event, Pipeline: name, RunNo: run.RunNo, Status: run.Status, Message: msg,
	})
}

// mapTaskStatus 把 Tekton 状态映射为平台状态。
func mapTaskStatus(s string) string {
	switch s {
	case "success", "failed", "cancelled", "skipped":
		return s
	case "running":
		return model.CIRunStatusRunning
	default:
		return model.CIRunStatusPending
	}
}

// ============ 推送 ============

func (s *Service) publishRun(ctx context.Context, runID uint) {
	if s.hub == nil {
		return
	}
	var run model.CIRun
	if err := s.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return
	}
	var tasks []model.CITaskRun
	_ = s.db.WithContext(ctx).Where("run_id = ?", runID).Order("id asc").Find(&tasks).Error

	payload := ws.RunPayload{RunID: run.ID, Status: run.Status, Cluster: run.ClusterName}
	for _, t := range tasks {
		ts := ws.TaskStatusPayload{NodeID: t.NodeID, Name: t.Name, Status: t.Status}
		if t.StartedAt != nil {
			v := t.StartedAt.Format(time.RFC3339)
			ts.StartedAt = &v
		}
		if t.FinishedAt != nil {
			v := t.FinishedAt.Format(time.RFC3339)
			ts.FinishedAt = &v
		}
		payload.Tasks = append(payload.Tasks, ts)
	}
	s.hub.PublishRun(fmt.Sprintf("%d", runID), payload)
}

// ============ helpers ============

// nodeParamsByRun 从 run 对应的版本 DSL 提取 nodeID -> params。
func (s *Service) nodeParamsByRun(ctx context.Context, run *model.CIRun) map[string]map[string]interface{} {
	if run.VersionID == 0 {
		return nil
	}
	s.nodeParamsMu.Lock()
	if cached, ok := s.nodeParamsCache[run.VersionID]; ok {
		s.nodeParamsMu.Unlock()
		return cached
	}
	s.nodeParamsMu.Unlock()

	out := map[string]map[string]interface{}{}
	var v model.CIPipelineVersion
	if err := s.db.WithContext(ctx).First(&v, run.VersionID).Error; err == nil {
		if g, err := pipelinesvc.ParseGraph(v.GraphJSON); err == nil {
			for _, n := range g.Nodes {
				out[n.ID] = n.Params
			}
		}
	}
	s.nodeParamsMu.Lock()
	if len(s.nodeParamsCache) >= 1024 {
		s.nodeParamsCache = map[uint]map[string]map[string]interface{}{}
	}
	s.nodeParamsCache[run.VersionID] = out
	s.nodeParamsMu.Unlock()
	return out
}

// gitParams 从 DSL 的 git-clone 节点推导 GIT_* 参数（供条件分支 when 引用）。
// branchOverride/commitOverride 非空时覆盖（webhook 按实际推送的分支/commit 触发）。
func gitParams(g *dsl.Graph, branchOverride, commitOverride string) []tekton.RunParam {
	branch, repo, commit := "", "", ""
	for _, n := range g.Nodes {
		if n.Type != "git-clone" || n.Params == nil {
			continue
		}
		if v, ok := n.Params["branch"].(string); ok {
			branch = v
		}
		if v, ok := n.Params["url"].(string); ok {
			repo = v
		}
		if v, ok := n.Params["commit"].(string); ok && v != "" {
			commit = v
		}
		break
	}
	if branchOverride != "" {
		branch = branchOverride
	}
	if commitOverride != "" {
		commit = commitOverride
	}
	ref := compiler.ConditionVarNames(g)
	vals := map[string]string{"GIT_BRANCH": branch, "GIT_COMMIT": commit, "GIT_REPO": repo}
	var out []tekton.RunParam
	for _, name := range []string{"GIT_BRANCH", "GIT_COMMIT", "GIT_REPO"} {
		if ref[name] {
			out = append(out, tekton.RunParam{Name: name, Value: vals[name]})
		}
	}
	return out
}

// nextRunNo 从计数器表原子分配 runNo（事务内读改写，跨驱动兼容；
// 首建时以该流水线已有 MAX(run_no)+1 初始化，兼容存量数据）。
// 计数器行用 SELECT ... FOR UPDATE 行锁串行化并发触发——普通 First 在 MySQL
// REPEATABLE READ 下是快照读，两个事务会读到同一个 LastRunNo，后写的 Save 直接
// 覆盖前者 → 重复 runNo → ci_runs 复合唯一键冲突，其中一个触发报裸错误。
func (s *Service) nextRunNo(ctx context.Context, pipelineID uint) (int, error) {
	var runNo int
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxNo int
		_ = tx.Model(&model.CIRun{}).Where("pipeline_id = ?", pipelineID).
			Select("COALESCE(MAX(run_no), 0)").Scan(&maxNo).Error
		var c model.CIRunCounter
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("pipeline_id = ?", pipelineID).First(&c).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			c = model.CIRunCounter{PipelineID: pipelineID, LastRunNo: 0}
			if cerr := tx.Create(&c).Error; cerr != nil {
				if !isDuplicateKeyError(cerr) {
					return cerr
				}
				// 并发首建：对方已建好这行，重新加锁读取（此刻行已存在，FOR UPDATE 生效）
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("pipeline_id = ?", pipelineID).First(&c).Error; err != nil {
					return err
				}
			}
		}
		// 取 DB 计数器与已有最大值中的较大者（防计数器落后）
		next := c.LastRunNo + 1
		if maxNo >= next {
			next = maxNo + 1
		}
		c.LastRunNo = next
		if err := tx.Save(&c).Error; err != nil {
			return err
		}
		runNo = next
		return nil
	})
	if err != nil {
		return 0, err
	}
	return runNo, nil
}

// isDuplicateKeyError 各驱动的「唯一键冲突」判错（MySQL 1062 / SQLite UNIQUE 约束）
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") ||
		strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

// gitParam 从 DSL 里取第一个 git-clone 节点的参数（commit/branch/url）。
func gitParam(graph *dsl.Graph, key string) string {
	for _, n := range graph.Nodes {
		if n.Type != "git-clone" {
			continue
		}
		if v, ok := n.Params[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// nodeIDs 诊断用：节点 id 列表。
func nodeIDs(g *dsl.Graph) []string {
	ids := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// edgePairs 诊断用：边 "src->dst" 列表。
func edgePairs(g *dsl.Graph) []string {
	pairs := make([]string, 0, len(g.Edges))
	for _, e := range g.Edges {
		pairs = append(pairs, e.Source+"->"+e.Target)
	}
	return pairs
}

// taskRunAfterSummary 诊断用：[{name runAfter}] 列表。
func taskRunAfterSummary(spec *compiler.PipelineSpec) []string {
	out := make([]string, 0, len(spec.Spec.Tasks))
	for _, t := range spec.Spec.Tasks {
		out = append(out, t.Name+"("+strings.Join(t.RunAfter, ",")+")")
	}
	return out
}

// specToMap 把 PipelineSpec 转成 unstructured 可用的 map（经 JSON 中转）。
func specToMap(spec *compiler.PipelineSpec) (map[string]interface{}, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
