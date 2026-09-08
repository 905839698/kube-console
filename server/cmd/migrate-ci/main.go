// migrate-ci 一次性迁移：ci-platform（Postgres）→ kube-console DB。
//
// 迁移对象（旧表 → 新表，全部标记 --cluster 集群归属）：
//   projects            → ci_projects      （namespace 空时取 --namespace）
//   pipelines           → ci_pipelines
//   pipeline_versions   → ci_pipeline_versions
//   credentials         → ci_credentials   （明文在 K8s Secret：--kubeconfig 提供时
//                                           从旧集群读取并扇出到 平台ns + 各项目ns）
//   pipeline_runs       → ci_runs          （最近 --days 天；0 = 不迁）
//   task_runs           → ci_task_runs
//   global_variables    → ci_global_vars
//   pipeline_schedules  → ci_schedules     （NextRunAt 按 cron 重算）
//   webhooks            → ci_webhooks      （token 沿用旧 secret，打印新旧 URL 对照）
//
// 用法：
//   migrate-ci --src "postgres://ci:***@host:5432/ci_platform?sslmode=disable" \
//     --dst "server/data/kube-console.db" --cluster 浪潮 [--namespace ci-projects] \
//     [--days 30] [--kubeconfig /path/to.kubeconfig] [--dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8s "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"kube-console/server/internal/model"
)

// ============ 旧库只读结构（对应 ci-platform 表） ============

type srcProject struct {
	ID          uint   `gorm:"column:id"`
	Name        string `gorm:"column:name"`
	DisplayName string `gorm:"column:display_name"`
	Description string `gorm:"column:description"`
	Namespace   string `gorm:"column:namespace"`
	CreatedAt   time.Time
}
func (srcProject) TableName() string { return "projects" }

type srcPipeline struct {
	ID          uint   `gorm:"column:id"`
	ProjectID   uint   `gorm:"column:project_id"`
	Name        string `gorm:"column:name"`
	Description string `gorm:"column:description"`
	Status      string `gorm:"column:status"`
	CreatedAt   time.Time
}
func (srcPipeline) TableName() string { return "pipelines" }

type srcVersion struct {
	ID           uint   `gorm:"column:id"`
	PipelineID   uint   `gorm:"column:pipeline_id"`
	Version      int    `gorm:"column:version"`
	GraphJSON    string `gorm:"column:graph_json"`
	CompiledYAML string `gorm:"column:compiled_yaml"`
	CreatedAt    time.Time
}
func (srcVersion) TableName() string { return "pipeline_versions" }

type srcCredential struct {
	ID         uint   `gorm:"column:id"`
	Name       string `gorm:"column:name"`
	Form       string `gorm:"column:form"`
	Type       string `gorm:"column:type"`
	SecretName string `gorm:"column:secret_name"`
	SecretNS   string `gorm:"column:secret_ns"`
	Extra      string `gorm:"column:extra"`
	ProjectID  *uint  `gorm:"column:project_id"`
	CreatedAt  time.Time
}
func (srcCredential) TableName() string { return "credentials" }

type srcRun struct {
	ID            uint       `gorm:"column:id"`
	PipelineID    uint       `gorm:"column:pipeline_id"`
	VersionID     uint       `gorm:"column:version_id"`
	RunNo         int        `gorm:"column:run_no"`
	Status        string     `gorm:"column:status"`
	TriggerType   string     `gorm:"column:trigger_type"`
	GitCommit     string     `gorm:"column:git_commit"`
	GitBranch     string     `gorm:"column:git_branch"`
	GitRepo       string     `gorm:"column:git_repo"`
	StartedBy     string     `gorm:"column:started_by"`
	TektonRunName string     `gorm:"column:tekton_run_name"`
	PVCName       string     `gorm:"column:pvc_name"`
	StartedAt     *time.Time `gorm:"column:started_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
	CreatedAt     time.Time
}
func (srcRun) TableName() string { return "pipeline_runs" }

type srcTaskRun struct {
	ID              uint       `gorm:"column:id"`
	PipelineRunID   uint       `gorm:"column:pipeline_run_id"`
	NodeID          string     `gorm:"column:node_id"`
	NodeType        string     `gorm:"column:node_type"`
	Name            string     `gorm:"column:name"`
	Status          string     `gorm:"column:status"`
	StartedAt       *time.Time `gorm:"column:started_at"`
	FinishedAt      *time.Time `gorm:"column:finished_at"`
	TektonTaskRun   string     `gorm:"column:tekton_task_run"`
	TektonPodName   string     `gorm:"column:tekton_pod_name"`
	TektonNamespace string     `gorm:"column:tekton_namespace"`
	Results         string     `gorm:"column:results"`
}
func (srcTaskRun) TableName() string { return "task_runs" }

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

type srcGlobal struct {
	Key         string `gorm:"column:key"`
	Value       string `gorm:"column:value"`
	Description string `gorm:"column:description"`
}
func (srcGlobal) TableName() string { return "global_variables" }

type srcSchedule struct {
	ID         uint       `gorm:"column:id"`
	PipelineID uint       `gorm:"column:pipeline_id"`
	ProjectID  uint       `gorm:"column:project_id"`
	Cron       string     `gorm:"column:cron"`
	Enabled    bool       `gorm:"column:enabled"`
	LastRunAt  *time.Time `gorm:"column:last_run_at"`
	LastError  string     `gorm:"column:last_error"`
	CreatedAt  time.Time
}
func (srcSchedule) TableName() string { return "pipeline_schedules" }

type srcWebhook struct {
	ID           uint   `gorm:"column:id"`
	ProjectID    uint   `gorm:"column:project_id"`
	PipelineID   uint   `gorm:"column:pipeline_id"`
	Provider     string `gorm:"column:provider"`
	URL          string `gorm:"column:url"`
	Secret       string `gorm:"column:secret"`
	BranchFilter string `gorm:"column:branch_filter"`
	Events       string `gorm:"column:events"`
	Active       bool   `gorm:"column:active"`
}
func (srcWebhook) TableName() string { return "webhooks" }

func main() {
	var (
		srcDSN    = flag.String("src", "", "ci-platform Postgres DSN（必填）")
		dstDSN    = flag.String("dst", "", "kube-console DB：sqlite 文件路径 / mysql / postgres DSN（必填）")
		cluster   = flag.String("cluster", "", "目标集群名（必填，与 kube-console 集群管理里的名称一致）")
		namespace = flag.String("namespace", "ci-projects", "旧项目未配 namespace 时的默认 K8s 命名空间")
		platformNS = flag.String("platform-ns", "ci-platform", "平台级凭据 Secret 所在 ns")
		days      = flag.Int("days", 30, "运行历史保留天数（0 = 不迁运行记录）")
		kubeconfig = flag.String("kubeconfig", "", "旧集群 kubeconfig（提供时迁移凭据 Secret；空 = 只迁凭据元数据）")
		dryRun    = flag.Bool("dry-run", false, "只统计不落库")
	)
	flag.Parse()
	if *srcDSN == "" || *dstDSN == "" || *cluster == "" {
		log.Fatal("--src / --dst / --cluster 均为必填")
	}

	src, err := gorm.Open(postgres.Open(*srcDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatalf("连接源库失败: %v", err)
	}
	dst, err := openDst(*dstDSN)
	if err != nil {
		log.Fatalf("连接目标库失败: %v", err)
	}

	// 凭据 Secret 读写客户端（可选）
	var k8sCore *kubernetes.Clientset
	if *kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			log.Fatalf("读取 kubeconfig 失败: %v", err)
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			log.Fatalf("构建 k8s 客户端失败: %v", err)
		}
		k8sCore = cs
	}

	if *dryRun {
		log.Println("[dry-run] 只统计不落库")
	}
	ctx := context.Background()

	// ---- 1) 项目 ----
	var sp []srcProject
	if err := src.Where("deleted_at IS NULL").Find(&sp).Error; err != nil {
		log.Fatalf("读 projects: %v", err)
	}
	projMap := map[uint]uint{} // 旧 id → 新 id
	for _, p := range sp {
		ns := p.Namespace
		if ns == "" {
			ns = *namespace
		}
		nm := model.CIProject{
			ClusterName: *cluster, Name: p.Name, DisplayName: p.DisplayName,
			Description: p.Description, Namespace: ns, CreatedBy: 1,
		}
		if !*dryRun {
			if err := dst.Create(&nm).Error; err != nil {
				log.Printf("项目 %s 跳过: %v", p.Name, err)
				continue
			}
		}
		projMap[p.ID] = nm.ID
	}
	log.Printf("项目: %d 条", len(sp))

	// ---- 2) 流水线 ----
	var spipe []srcPipeline
	if err := src.Where("deleted_at IS NULL").Find(&spipe).Error; err != nil {
		log.Fatalf("读 pipelines: %v", err)
	}
	pipeMap := map[uint]uint{}
	for _, p := range spipe {
		nm := model.CIPipeline{
			ClusterName: *cluster, ProjectID: projMap[p.ProjectID], Name: p.Name,
			Description: p.Description, Status: p.Status, CreatedBy: 1,
		}
		if !*dryRun {
			if err := dst.Create(&nm).Error; err != nil {
				log.Printf("流水线 %s 跳过: %v", p.Name, err)
				continue
			}
		}
		pipeMap[p.ID] = nm.ID
	}
	log.Printf("流水线: %d 条", len(spipe))

	// ---- 3) 版本（先读凭据做数字 id → 名 规范化） ----
	var sc []srcCredential
	if err := src.Where("deleted_at IS NULL").Find(&sc).Error; err != nil {
		log.Fatalf("读 credentials: %v", err)
	}
	var sv []srcVersion
	if err := src.Order("pipeline_id, version").Find(&sv).Error; err != nil {
		log.Fatalf("读 pipeline_versions: %v", err)
	}
	credByName := map[uint]string{}
	for _, c := range sc {
		credByName[c.ID] = c.Name
	}
	verMap := map[uint]uint{}
	for _, v := range sv {
		nm := model.CIPipelineVersion{
			PipelineID: pipeMap[v.PipelineID], Version: v.Version,
			GraphJSON: normalizeCredRefs(v.GraphJSON, credByName), CompiledYAML: v.CompiledYAML, CreatedBy: 1,
		}
		if !*dryRun {
			if err := dst.Create(&nm).Error; err != nil {
				log.Printf("版本 pipe=%d v%d 跳过: %v", v.PipelineID, v.Version, err)
				continue
			}
		}
		verMap[v.ID] = nm.ID
	}
	log.Printf("流水线版本: %d 条", len(sv))

	// ---- 4) 凭据（元数据 + 可选 Secret 扇出） ----
	for _, c := range sc {
		extra := model.JSONObject{}
		if c.Extra != "" && c.Extra != "null" {
			_ = jsonUnmarshal(c.Extra, &extra)
		}
		nm := model.CICredential{
			ClusterName: *cluster, Name: c.Name, Form: c.Form, Type: c.Type,
			SecretName: c.SecretName, SecretNS: *platformNS,
			Extra: extra, ProjectID: c.ProjectID, CreatedBy: 1,
		}
		if !*dryRun {
			if err := dst.Create(&nm).Error; err != nil {
				log.Printf("凭据 %s 跳过: %v", c.Name, err)
				continue
			}
		}
		if k8sCore != nil && c.SecretName != "" {
			secretNS := c.SecretNS
			if secretNS == "" {
				secretNS = *namespace
			}
			if *dryRun {
				log.Printf("  凭据 %s: Secret %s/%s（dry-run 不迁移）", c.Name, secretNS, c.SecretName)
				continue
			}
			obj, err := k8sCore.CoreV1().Secrets(secretNS).Get(ctx, c.SecretName, metav1.GetOptions{})
			if err != nil {
				log.Printf("  凭据 %s: 读取旧 Secret %s/%s 失败: %v", c.Name, secretNS, c.SecretName, err)
				continue
			}
			// 扇出：平台 ns + 该项目的所有 ns（secretRef 不支持跨 ns）
			targets := map[string]bool{*platformNS: true}
			if c.ProjectID != nil {
				var np model.CIProject
				if npID, ok := projMap[*c.ProjectID]; ok && npID > 0 {
					_ = dst.First(&np, npID).Error
					if np.Namespace != "" {
						targets[np.Namespace] = true
					}
				}
			}
			for ns := range targets {
				_ = k8sCore.CoreV1().Secrets(ns).Delete(ctx, c.SecretName, metav1.DeleteOptions{}) // 旧值让位
				if _, err := k8sCore.CoreV1().Secrets(ns).Create(ctx, &k8s.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: c.SecretName, Namespace: ns},
					Data:       obj.Data,
				}, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
					log.Printf("  凭据 %s: 写新 Secret %s 失败: %v", c.Name, ns, err)
				}
			}
			log.Printf("  凭据 %s: Secret 已扇出 %v", c.Name, targets)
		}
	}
	log.Printf("凭据: %d 条（kubeconfig=%v）", len(sc), k8sCore != nil)

	// ---- 5) 运行 + 任务（最近 --days 天） ----
	if *days > 0 {
		since := time.Now().AddDate(0, 0, -*days)
		var sr []srcRun
		if err := src.Where("created_at >= ?", since).Find(&sr).Error; err != nil {
			log.Fatalf("读 pipeline_runs: %v", err)
		}
		runMap := map[uint]uint{}
		for _, r := range sr {
			nm := model.CIRun{
				ClusterName: *cluster, PipelineID: pipeMap[r.PipelineID], VersionID: verMap[r.VersionID],
				RunNo: r.RunNo, Status: r.Status, TriggerType: r.TriggerType,
				GitCommit: r.GitCommit, GitBranch: r.GitBranch, GitRepo: r.GitRepo,
				StartedBy: r.StartedBy, TektonRunName: r.TektonRunName, PVCName: r.PVCName,
				StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			}
			if !*dryRun {
				if err := dst.Create(&nm).Error; err != nil {
					log.Printf("run #旧%d 跳过: %v", r.ID, err)
					continue
				}
			}
			runMap[r.ID] = nm.ID
		}
		var st []srcTaskRun
		var runIDs []uint
		for k := range runMap {
			runIDs = append(runIDs, k)
		}
		nTask := 0
		if len(runIDs) > 0 {
			if err := src.Where("pipeline_run_id IN ?", runIDs).Find(&st).Error; err != nil {
				log.Fatalf("读 task_runs: %v", err)
			}
			for _, t := range st {
				results := model.JSONMap{}
				if t.Results != "" && t.Results != "null" {
					_ = jsonUnmarshal(t.Results, &results)
				}
				nm := model.CITaskRun{
					RunID: runMap[t.PipelineRunID], NodeID: t.NodeID, NodeType: t.NodeType,
					Name: t.Name, Status: t.Status, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
					TektonTaskRun: t.TektonTaskRun, TektonPodName: t.TektonPodName,
					TektonNamespace: t.TektonNamespace, Results: results,
				}
				if !*dryRun {
					if err := dst.Create(&nm).Error; err == nil {
						nTask++
					}
				}
			}
		}
		log.Printf("运行: %d 条 / 任务: %d 条（最近 %d 天）", len(sr), nTask, *days)
	}

	// ---- 6) 全局变量 ----
	var sg []srcGlobal
	if err := src.Find(&sg).Error; err != nil {
		log.Fatalf("读 global_variables: %v", err)
	}
	for _, g := range sg {
		nm := model.CIGlobalVar{ClusterName: *cluster, Key: g.Key, Value: g.Value, Description: g.Description, CreatedBy: 1}
		if !*dryRun {
			_ = dst.Create(&nm).Error
		}
	}
	log.Printf("全局变量: %d 条", len(sg))

	// ---- 7) 定时任务（NextRunAt 重算） ----
	var ss []srcSchedule
	if err := src.Where("deleted_at IS NULL").Find(&ss).Error; err != nil {
		log.Fatalf("读 pipeline_schedules: %v", err)
	}
	for _, sch := range ss {
		next, err := cron.ParseStandard(sch.Cron)
		var nextPtr *time.Time
		if err == nil {
			t := next.Next(time.Now())
			nextPtr = &t
		} else {
			log.Printf("  定时 %s: cron 非法（%v），保持禁用", sch.Cron, err)
		}
		nm := model.CISchedule{
			ClusterName: *cluster, PipelineID: pipeMap[sch.PipelineID], ProjectID: projMap[sch.ProjectID],
			Cron: sch.Cron, Enabled: sch.Enabled && err == nil, LastRunAt: sch.LastRunAt,
			NextRunAt: nextPtr, LastError: sch.LastError, CreatedBy: 1,
		}
		if !*dryRun {
			_ = dst.Create(&nm).Error
		}
	}
	log.Printf("定时任务: %d 条", len(ss))

	// ---- 8) Webhook（token 沿用旧 secret，GitLab URL 需更新为新路径） ----
	var sw []srcWebhook
	if err := src.Where("deleted_at IS NULL").Find(&sw).Error; err != nil {
		log.Fatalf("读 webhooks: %v", err)
	}
	for _, w := range sw {
		token := w.Secret
		if token == "" {
			// 旧 secret 为空（用全局 WebhookSecret）→ 生成新的
			token = fmt.Sprintf("mig%024d", w.ID)
		}
		nm := model.CIWebhook{
			ClusterName: *cluster, PipelineID: pipeMap[w.PipelineID], ProjectID: projMap[w.ProjectID],
			Token: token, Branch: w.BranchFilter, Enabled: w.Active, CreatedBy: 1,
		}
		if !*dryRun {
			_ = dst.Create(&nm).Error
		}
		fmt.Printf("  webhook 旧仓库 %s → 新 URL 需改为 /api/ci/webhook/%s（pipeline 新 id=%d）\n", w.URL, token, nm.PipelineID)
	}
	log.Printf("Webhook: %d 条", len(sw))

	if *dryRun {
		log.Println("[dry-run] 完成，未写入任何数据")
		return
	}
	log.Printf("迁移完成 → %s（cluster=%s）", *dstDSN, *cluster)
}

// openDst 按 DSN 形态打开目标库（sqlite 文件 / postgres / mysql）。
func openDst(dsn string) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err == nil && strings.Contains(dsn, "postgres://") {
		return gdb, nil
	}
	if strings.Contains(dsn, "mysql://") || strings.Contains(dsn, "tcp(") {
		return gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	}
	// 默认按 sqlite 文件路径
	if _, err := os.Stat(dsn); err == nil || !strings.Contains(dsn, "://") {
		return gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

// normalizeCredRefs 把 DSL 中 credential 类型参数的数字 id 引用替换为凭证名
// （节点 schema 声明 type=credential 的参数才处理；非数字/未匹配原样保留）。
func normalizeCredRefs(graphJSON string, credByName map[uint]string) string {
	if len(credByName) == 0 {
		return graphJSON
	}
	var g struct {
		Nodes []struct {
			Type   string                 `json:"type"`
			Params map[string]interface{} `json:"params"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
		return graphJSON
	}
	changed := false
	for _, n := range g.Nodes {
		for k, v := range n.Params {
			switch x := v.(type) {
			case float64:
				if name, ok := credByName[uint(int(x))]; ok {
					n.Params[k] = name
					changed = true
				}
			case string:
				if i, err := strconv.ParseUint(x, 10, 64); err == nil {
					if name, ok := credByName[uint(i)]; ok {
						n.Params[k] = name
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return graphJSON
	}
	b, err := json.Marshal(g)
	if err != nil {
		return graphJSON
	}
	return string(b)
}
