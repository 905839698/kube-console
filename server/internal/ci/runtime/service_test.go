package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	pipelinesvc "kube-console/server/internal/ci/pipeline"
	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.CIProject{}, &model.CIPipeline{}, &model.CIPipelineVersion{},
		&model.CIRun{}, &model.CITaskRun{}, &model.CIRunCounter{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedProject(t *testing.T, db *gorm.DB) (*model.CIProject, *model.CIPipeline, *model.CIPipelineVersion) {
	t.Helper()
	proj := &model.CIProject{Name: "demo", Namespace: "ci-demo", ClusterName: "c1"}
	pipe := &model.CIPipeline{Name: "build", ProjectID: 0, ClusterName: "c1"}
	ver := &model.CIPipelineVersion{Version: 1}
	if err := db.Create(proj).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pipe.ProjectID = proj.ID
	if err := db.Create(pipe).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	ver.PipelineID = pipe.ID
	if err := db.Create(ver).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	return proj, pipe, ver
}

func TestProjectWorkspacePVCName(t *testing.T) {
	if got := ProjectWorkspacePVCName(7); got != "ci-ws-p7" {
		t.Errorf("项目工作盘命名应为 ci-ws-p<项目ID>，实际 %s", got)
	}
	// 老命名（每 run 一个）不能被误判成共享盘，否则升级窗口期的残留盘永远回收不掉
	if !isProjectWorkspacePVC("ci-ws-p7") {
		t.Error("ci-ws-p7 应判为项目级共享盘")
	}
	if isProjectWorkspacePVC("ci-ws-12-8-ab12c") {
		t.Error("每 run 一个的老命名不该算共享盘")
	}
}

// 终态/取消/补偿都不该删掉项目的共享工作盘；老命名的残留盘仍要回收
func TestReleaseWorkspaceKeepsProjectClaim(t *testing.T) {
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "ci-ws-p3", Namespace: "ci-demo"},
	}
	legacy := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "ci-ws-12-8-ab12c", Namespace: "ci-demo"},
	}
	cs := fake.NewSimpleClientset(claim, legacy)
	k8s := tekton.NewClient("c1", &kube.Client{Clientset: cs}, &config.CIConfig{})
	s := NewService(&config.CIConfig{}, newTestDB(t), nil, nil, nil, nil,
		func(string) (*tekton.Client, error) { return k8s, nil })

	ctx := context.Background()
	s.releaseWorkspace(ctx, k8s, "ci-demo", "ci-ws-p3")
	s.releaseWorkspace(ctx, k8s, "ci-demo", "ci-ws-12-8-ab12c")
	s.releaseWorkspace(ctx, k8s, "ci-demo", "") // 空名不该去删 ""

	if _, err := cs.CoreV1().PersistentVolumeClaims("ci-demo").Get(ctx, "ci-ws-p3", metav1.GetOptions{}); err != nil {
		t.Errorf("项目共享盘被删了（下次运行又得重新绑盘）: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("ci-demo").Get(ctx, "ci-ws-12-8-ab12c", metav1.GetOptions{}); err == nil {
		t.Error("老命名的每 run 盘应被回收")
	}
}

// 新 run 下发前必须回收同盘旧 Pod：工作区是项目级 RWO 盘，local CSI 不允许同盘多挂，
// preserveLogs 保留的终态 Pod 会一直占着它（24h 租约），下一次触发就 FailedMount。
// 活跃 run 的 Pod 绝不能动，否则会打断正在跑的构建。
func TestReclaimStalePods(t *testing.T) {
	db := newTestDB(t)
	running := &model.CIRun{ClusterName: "c1", Status: model.CIRunStatusRunning,
		TektonRunName: "run-active", RunNo: 1, PipelineID: 1, VersionID: 1}
	failed := &model.CIRun{ClusterName: "c1", Status: model.CIRunStatusFailed,
		TektonRunName: "run-done", RunNo: 2, PipelineID: 1, VersionID: 1}
	if err := db.Create(running).Error; err != nil {
		t.Fatalf("seed running run: %v", err)
	}
	if err := db.Create(failed).Error; err != nil {
		t.Fatalf("seed failed run: %v", err)
	}

	pod := func(name, run string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "ci-demo",
			Labels: map[string]string{"tekton.dev/pipelineRun": run},
		}}
	}
	cs := fake.NewSimpleClientset(
		pod("run-active-fetch-pod", "run-active"),
		pod("run-done-fetch-pod", "run-done"),
		pod("run-orphan-fetch-pod", "run-orphan"), // DB 无记录，一并回收
	)
	k8s := tekton.NewClient("c1", &kube.Client{Clientset: cs}, &config.CIConfig{})
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) { return k8s, nil })

	ctx := context.Background()
	s.reclaimStalePods(ctx, k8s, "c1", "ci-demo")

	if _, err := cs.CoreV1().Pods("ci-demo").Get(ctx, "run-active-fetch-pod", metav1.GetOptions{}); err != nil {
		t.Errorf("活跃 run 的 Pod 不该被删: %v", err)
	}
	for _, name := range []string{"run-done-fetch-pod", "run-orphan-fetch-pod"} {
		if _, err := cs.CoreV1().Pods("ci-demo").Get(ctx, name, metav1.GetOptions{}); err == nil {
			t.Errorf("残留 Pod %s 应被回收", name)
		}
	}
}

// 占位判定：排队中的 run 也要挡住同流水线的新触发（它排在前面）；
// 放行判定只看已提交的（否则候选会被自己那条 queued 记录挡死）。
// 占用按流水线划分——同项目不同流水线工作区已分目录，互不挡。
func TestQueueOccupancy(t *testing.T) {
	db := newTestDB(t)
	proj, pipe, _ := seedProject(t, db)
	states := []string{model.CIRunStatusQueued, model.CIRunStatusPending, model.CIRunStatusSuccess}
	for i, st := range states {
		if err := db.Create(&model.CIRun{PipelineID: pipe.ID, Status: st, RunNo: i + 1}).Error; err != nil {
			t.Fatalf("seed run %s: %v", st, err)
		}
	}
	// 同项目的另一条流水线：各放一条 queued，不该影响上面的计数
	pipe2 := &model.CIPipeline{Name: "deploy", ProjectID: proj.ID, ClusterName: "c1"}
	if err := db.Create(pipe2).Error; err != nil {
		t.Fatalf("seed pipeline2: %v", err)
	}
	if err := db.Create(&model.CIRun{PipelineID: pipe2.ID, Status: model.CIRunStatusQueued, RunNo: 1}).Error; err != nil {
		t.Fatalf("seed run2: %v", err)
	}
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil, nil)
	n, err := s.pipelineOccupyingRuns(context.Background(), pipe.ID)
	if err != nil {
		t.Fatalf("pipelineOccupyingRuns: %v", err)
	}
	if n != 2 {
		t.Errorf("占位的应是 queued+pending 两条，实际 %d", n)
	}
	m, err := s.pipelineSubmittedRuns(context.Background(), pipe.ID)
	if err != nil {
		t.Fatalf("pipelineSubmittedRuns: %v", err)
	}
	if m != 1 {
		t.Errorf("已提交的只有那条 pending，实际 %d", m)
	}
	// 另一条流水线只有自己那条 queued，不被同项目其它流水线的 run 挡住
	n2, err := s.pipelineOccupyingRuns(context.Background(), pipe2.ID)
	if err != nil {
		t.Fatalf("pipelineOccupyingRuns(pipe2): %v", err)
	}
	if n2 != 1 {
		t.Errorf("pipe2 的占用应只有自己那条 queued，实际 %d", n2)
	}
}

// 排队记录：只落库，不碰集群，启动参数原样存下来供放行时用
func TestEnqueueRunWritesQueuedRow(t *testing.T) {
	db := newTestDB(t)
	_, pipe, ver := seedProject(t, db)
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) {
			t.Error("排队不该访问集群")
			return nil, nil
		})
	run, err := s.enqueueRun(context.Background(), pipe, ver, "alice", "manual", "release-1.2", "")
	if err != nil {
		t.Fatalf("enqueueRun: %v", err)
	}
	if run.Status != model.CIRunStatusQueued || run.RunNo != 1 || run.StartedBy != "alice" {
		t.Fatalf("排队记录不对: %+v", run)
	}
	if run.TektonRunName != "" || run.PVCName != "" || run.StartedAt != nil {
		t.Errorf("排队记录不该带集群侧字段: %+v", run)
	}
	var ov runOverrides
	if err := json.Unmarshal([]byte(run.QueuePayload), &ov); err != nil || ov.Branch != "release-1.2" {
		t.Errorf("分支覆盖没存下来: %q err=%v", run.QueuePayload, err)
	}
}

// 排队中的 run 停止 = 出队：不能要求集群可达（集群正宕机时也该能取消）
func TestCancelQueuedRunSkipsCluster(t *testing.T) {
	db := newTestDB(t)
	_, pipe, _ := seedProject(t, db)
	q := &model.CIRun{PipelineID: pipe.ID, RunNo: 1, Status: model.CIRunStatusQueued, QueuePayload: `{"branch":"b"}`}
	if err := db.Create(q).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) {
			t.Error("取消排队记录不该访问集群")
			return nil, nil
		})
	if _, err := s.Cancel(context.Background(), q.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var after model.CIRun
	if err := db.First(&after, q.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.CIRunStatusCancelled || after.FinishedAt == nil {
		t.Errorf("应置为已取消: %+v", after)
	}
	if after.QueuePayload != "" {
		t.Errorf("出队后启动参数该清空，实际 %q", after.QueuePayload)
	}
}

// 集群暂时不可达时保持排队（下一轮再试），而不是把用户的排队记录判死
func TestPumpKeepsQueuedWhileClusterDown(t *testing.T) {
	db := newTestDB(t)
	_, pipe, ver := seedProject(t, db)
	q := &model.CIRun{PipelineID: pipe.ID, VersionID: ver.ID, RunNo: 1, Status: model.CIRunStatusQueued}
	if err := db.Create(q).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) { return nil, context.DeadlineExceeded })
	s.pumpQueue(context.Background())

	var after model.CIRun
	if err := db.First(&after, q.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.CIRunStatusQueued {
		t.Errorf("集群不可达时应保持 queued，实际 %s", after.Status)
	}
}

// 流水线在排队期间被删：这条记录永远不会被执行，置失败留痕，别在队列里堵着
func TestPumpFailsQueuedWithMissingPipeline(t *testing.T) {
	db := newTestDB(t)
	ver := &model.CIPipelineVersion{PipelineID: 999, Version: 1}
	if err := db.Create(ver).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	q := &model.CIRun{PipelineID: 999, VersionID: ver.ID, RunNo: 1, Status: model.CIRunStatusQueued}
	if err := db.Create(q).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) { return nil, nil })
	s.pumpQueue(context.Background())

	var after model.CIRun
	if err := db.First(&after, q.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.CIRunStatusFailed {
		t.Errorf("流水线已删除时应置失败，实际 %s", after.Status)
	}
}

// 放行失败的原因要写进任务行（执行详情看得到），不能只留在服务日志里
// 版本图本身有问题（确定性失败）时，排队放行必须就地判失败——
// 否则 syncer 每 3 秒重试一条永远不会成功的队列记录，界面永远显示"排队中"
func TestPumpFailsQueuedOnDeterministicLaunchError(t *testing.T) {
	db := newTestDB(t)
	_, pipe, _ := seedProject(t, db)
	ver := &model.CIPipelineVersion{PipelineID: pipe.ID, Version: 2, GraphJSON: "{not-json"}
	if err := db.Create(ver).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	q := &model.CIRun{PipelineID: pipe.ID, VersionID: ver.ID, RunNo: 1, Status: model.CIRunStatusQueued}
	if err := db.Create(q).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	k8s := tekton.NewClient("c1", &kube.Client{Clientset: fake.NewSimpleClientset()}, &config.CIConfig{})
	// pipe/ 只为拿 Validator：本用例在 ParseGraph 就返回，走不到用它的那行
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil,
		func(string) (*tekton.Client, error) { return k8s, nil })
	s.pumpQueue(context.Background())

	var after model.CIRun
	if err := db.First(&after, q.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.CIRunStatusFailed {
		t.Errorf("确定性失败应置 failed，实际 %s", after.Status)
	}
	if after.Message == "" {
		t.Error("失败原因没写进 run.message（界面只剩一个孤零零的「失败」）")
	}
}

func TestFailQueuedLaunchKeepsRowAndWritesMessage(t *testing.T) {
	db := newTestDB(t)
	_, pipe, ver := seedProject(t, db)
	q := &model.CIRun{PipelineID: pipe.ID, VersionID: ver.ID, RunNo: 1, Status: model.CIRunStatusQueued}
	if err := db.Create(q).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	tr := &model.CITaskRun{RunID: q.ID, NodeID: "clone", NodeType: "git-clone", Status: model.CIRunStatusPending}
	if err := db.Create(tr).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	s := NewService(&config.CIConfig{}, db, nil, nil, nil, nil, nil)
	s.failQueuedLaunch(context.Background(), q, errors.New("PVC 绑定超时"))

	var run model.CIRun
	if err := db.First(&run, q.ID).Error; err != nil {
		t.Fatalf("run 行被删了（用户提交消失）: %v", err)
	}
	if run.Status != model.CIRunStatusFailed {
		t.Errorf("应置失败，实际 %s", run.Status)
	}
	var task model.CITaskRun
	if err := db.Where("run_id = ?", q.ID).First(&task).Error; err != nil {
		t.Fatalf("task reload: %v", err)
	}
	if task.Status != model.CIRunStatusFailed {
		t.Errorf("任务行应一起置失败，实际 %+v", task)
	}
	if !strings.Contains(run.Message, "PVC 绑定超时") {
		t.Errorf("失败原因应留在 run 上（界面看得到），实际 %q", run.Message)
	}
}

// 缓存与运行工作区同盘（claim 的 subPath），所以这里只决定"挂不挂 + 怎么交代"。
// 关键是三种情况都要有明确说法：没勾（什么都不说）、平台开关关着（必须说没启用），
// 启用（说清在哪、几个节点在用）。
func TestCacheBinding(t *testing.T) {
	graph := `{"nodes":[{"id":"clone","type":"git-clone","params":{}},
		{"id":"build","type":"npm-build","params":{"script":"npm ci","cache":true}},
		{"id":"img","type":"build-image","params":{"cache":"true"}}]}`
	g, err := pipelinesvc.ParseGraph(graph)
	if err != nil {
		t.Fatalf("parse graph: %v", err)
	}

	s_off := NewService(&config.CIConfig{}, newTestDB(t), nil, nil, nil, nil, nil)
	s_on := NewService(&config.CIConfig{CacheEnabled: true, PVCSize: "10Gi"}, newTestDB(t), nil, nil, nil, nil, nil)

	if dir, note := s_on.cacheBinding(&model.CIPipeline{ProjectID: 9}, g); dir != ProjectCacheDir {
		t.Errorf("启用时应返回缓存子目录 %s，实际 %q", ProjectCacheDir, dir)
	} else if !strings.Contains(note, "已启用") || !strings.Contains(note, "ci-ws-p9") ||
		!strings.Contains(note, "build(npm-build)") || !strings.Contains(note, "img(build-image)") {
		t.Errorf("启用的说明要带盘名与使用节点，实际 %q", note)
	}

	if dir, note := s_off.cacheBinding(&model.CIPipeline{ProjectID: 9}, g); dir != "" {
		t.Errorf("平台开关关着时不该绑缓存，实际 %q", dir)
	} else if !strings.Contains(note, "未启用") || !strings.Contains(note, "cacheEnabled") {
		t.Errorf("关着必须说清原因（用户勾了缓存却静默不用），实际 %q", note)
	}

	empty := &dsl.Graph{Nodes: []dsl.Node{{ID: "clone", Type: "git-clone"}}}
	if dir, note := s_on.cacheBinding(&model.CIPipeline{ProjectID: 9}, empty); dir != "" || note != "" {
		t.Errorf("没勾缓存时不该有任何说法，实际 %q/%q", dir, note)
	}
}

// 说明文本进 256 的列：超长必须在截断后仍写库成功（MySQL 严格模式会整条拒绝超长值）
func TestCutCacheInfoFitsColumn(t *testing.T) {
	long := strings.Repeat("节点名", 300)
	got := cutCacheInfo(long)
	if len([]rune(got)) > 241 {
		t.Errorf("截断后仍超长：%d 字符", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("截断要留省略号，否则用户以为文本本来就这么短")
	}
}

// 同一条流水线的启动互斥必须是同一把锁，不同流水线互不干扰：同一条流水线被
// webhook 与人工并发触发时，共享的工作区子目录的串行全靠这把锁兜住。
// 锁失效的现象是 clone 步骤 rm ENOENT 刷屏、git clone "destination path '.' already
// exists"、git refs 内部错误——两个 Pod 同时清盘/克隆。
// 粒度是流水线：同项目不同流水线工作区已分目录，必须能并行启动。
func TestPipelineLaunchLockPerPipeline(t *testing.T) {
	s := NewService(&config.CIConfig{}, newTestDB(t), nil, nil, nil, nil, nil)
	if s.pipelineLaunchLock(1) != s.pipelineLaunchLock(1) {
		t.Fatal("同一流水线两次取锁必须是同一把锁，否则并发触发形同虚设")
	}
	if s.pipelineLaunchLock(1) == s.pipelineLaunchLock(2) {
		t.Fatal("不同流水线的启动不能互相阻塞（工作区已按流水线分目录）")
	}
}
