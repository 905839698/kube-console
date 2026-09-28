package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kube-console/server/internal/kube"
)

func kbPolicy(cluster, comp string, methods ...string) *unstructured.Unstructured {
	ms := make([]any, 0, len(methods))
	for _, m := range methods {
		ms = append(ms, map[string]any{"name": m, "actionSetName": m + "-as"})
	}
	return kbUD(cluster+"-"+comp+"-backup-policy", kbPolicyGVR, "BackupPolicy", "app", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{
			"app.kubernetes.io/instance": cluster, "apps.kubeblocks.io/component-name": comp,
		}},
		"spec": map[string]any{
			"backupMethods": ms,
			"target":        map[string]any{"connectionCredential": map[string]any{"secretName": cluster + "-" + comp + "-account-root"}},
		},
	})
}

func kbSchedule(cluster, comp string, items ...map[string]any) *unstructured.Unstructured {
	scheds := make([]any, 0, len(items))
	for _, it := range items {
		scheds = append(scheds, it)
	}
	return kbUD(cluster+"-"+comp+"-backup-schedule", kbSchedGVR, "BackupSchedule", "app", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{
			"app.kubernetes.io/instance": cluster, "apps.kubeblocks.io/component-name": comp,
		}},
		"spec": map[string]any{
			"backupPolicyName": cluster + "-" + comp + "-backup-policy",
			"schedules":        scheds,
		},
	})
}

// 策略按标签+命名双口径归属；方法与凭据 Secret 名要透出
func TestKBListBackupPolicies(t *testing.T) {
	dyn := kbFakeDyn(kbPolicy("db", "mysql", "xtrabackup", "volume-snapshot"))
	out, err := KBListBackupPolicies(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db")
	if err != nil {
		t.Fatalf("KBListBackupPolicies: %v", err)
	}
	if len(out) != 1 || out[0].Component != "mysql" || len(out[0].Methods) != 2 {
		t.Fatalf("策略视图不对: %+v", out)
	}
	if out[0].Methods[0].Name != "xtrabackup" || out[0].CredSecret != "db-mysql-account-root" {
		t.Errorf("方法/凭据不对: %+v", out[0])
	}
}

// 改计划：只动 enabled/cron/retention，其余字段（parameters 等）必须保留
func TestKBUpdateBackupScheduleKeepsOtherFields(t *testing.T) {
	sched := kbSchedule("db", "mysql",
		map[string]any{"name": "xtrabackup", "backupMethod": "xtrabackup", "cronExpression": "0 18 * * 0", "enabled": false, "retentionPeriod": "8d", "parameters": []any{map[string]any{"name": "k", "value": "v"}}},
		map[string]any{"name": "volume-snapshot", "backupMethod": "volume-snapshot", "cronExpression": "0 18 * * *", "enabled": false},
	)
	dyn := kbFakeDyn(sched)
	err := KBUpdateBackupSchedule(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", "mysql", []KBScheduleItem{
		{Method: "xtrabackup", Enabled: true, Cron: "0 6 * * *", Retention: "14d"},
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	obj, _ := dyn.Resource(kbSchedGVR).Namespace("app").Get(context.Background(), "db-mysql-backup-schedule", metav1.GetOptions{})
	items, _, _ := unstructured.NestedSlice(obj.Object, "spec", "schedules")
	first := items[0].(map[string]any)
	if first["enabled"] != true || first["cronExpression"] != "0 6 * * *" || first["retentionPeriod"] != "14d" {
		t.Errorf("目标项没更新: %v", first)
	}
	if _, ok := first["parameters"]; !ok {
		t.Errorf("operator 填的 parameters 被抹掉了: %v", first)
	}
	if items[1].(map[string]any)["enabled"] != false {
		t.Errorf("没点名的项不应被改动: %v", items[1])
	}
	// 坏 cron 要在本地拦
	err = KBUpdateBackupSchedule(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", "mysql", []KBScheduleItem{
		{Method: "xtrabackup", Enabled: true, Cron: "every day"},
	})
	if err == nil || !strings.Contains(err.Error(), "cron") {
		t.Errorf("坏 cron 应被拦: %v", err)
	}
}

// 没配 BackupRepo 时创建备份要在本地报错；配了则按策略解析方法并用 generateName 下发
func TestKBCreateBackup(t *testing.T) {
	policy := kbPolicy("db", "mysql", "xtrabackup", "volume-snapshot")
	// 未配置仓库
	if _, err := KBCreateBackup(context.Background(), &kube.Client{Dynamic: kbFakeDyn(policy)}, "app", "db",
		&KBBackupCreateInput{Component: "mysql"}); err == nil || !strings.Contains(err.Error(), "BackupRepo") {
		t.Fatalf("无仓库应明确报错: %v", err)
	}
	repo := kbUD("oss-repo", kbRepoGVR, "BackupRepo", "", map[string]any{
		"spec":   map[string]any{"storageProviderRef": "oss", "accessMethod": "Mount"},
		"status": map[string]any{"phase": "Available", "isDefault": true},
	})
	dyn := kbFakeDyn(policy, repo)
	_, err := KBCreateBackup(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBBackupCreateInput{Component: "mysql", Retention: "7d"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// fake 动态客户端不实现 generateName（真 apiserver 会生成名字），按列表取回校验
	created, err := dyn.Resource(kbBackupGVR).Namespace("app").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(created.Items) != 1 {
		t.Fatalf("备份未创建: %v", err)
	}
	obj := &created.Items[0]
	if got := str(obj, "spec", "backupPolicyName"); got != "db-mysql-backup-policy" {
		t.Errorf("backupPolicyName = %s", got)
	}
	if got := str(obj, "spec", "backupMethod"); got != "xtrabackup" {
		t.Errorf("缺省方法应取策略第一个，实际 %s", got)
	}
	if got := str(obj, "spec", "retentionPeriod"); got != "7d" {
		t.Errorf("retentionPeriod = %s", got)
	}
	if obj.GetGenerateName() != "backup-db-" {
		t.Errorf("generateName 前缀不对: %s", obj.GetGenerateName())
	}
}

// 备份列表按策略归属过滤（他集群的策略产生的备份不显示）
func TestKBListBackupsJoinsByPolicy(t *testing.T) {
	mine := kbUD("backup-db-aaa", kbBackupGVR, "Backup", "app", map[string]any{
		"spec":   map[string]any{"backupPolicyName": "db-mysql-backup-policy", "backupMethod": "xtrabackup"},
		"status": map[string]any{"phase": "Completed", "totalSize": "2Gi"},
	})
	others := kbUD("backup-other-bbb", kbBackupGVR, "Backup", "app", map[string]any{
		"spec":   map[string]any{"backupPolicyName": "other-mysql-backup-policy"},
		"status": map[string]any{"phase": "Completed"},
	})
	dyn := kbFakeDyn(kbPolicy("db", "mysql", "xtrabackup"), mine, others)
	out, err := KBListBackups(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db")
	if err != nil {
		t.Fatalf("KBListBackups: %v", err)
	}
	if len(out) != 1 || out[0].Name != "backup-db-aaa" || out[0].Phase != "Completed" {
		t.Fatalf("备份列表不对: %+v", out)
	}
}

// 恢复到新集群：复制 spec、清身份元数据、按官方注解协议声明恢复来源；备份/组件要前置校验
func TestKBRestoreToNewCluster(t *testing.T) {
	src := kbUD("db", kbClusterGVR, "Cluster", "app", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/instance": "db"}},
		"spec": map[string]any{
			"clusterDef":        "mysql",
			"terminationPolicy": "Delete",
			"componentSpecs": []any{map[string]any{
				"name": "mysql", "componentDef": "mysql-8.4-1.0.2", "replicas": int64(2),
				"volumeClaimTemplates": []any{map[string]any{
					"name": "data",
					"spec": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "20Gi"}}},
				}},
			}},
		},
	})
	backup := kbUD("backup-db-aaa", kbBackupGVR, "Backup", "app", map[string]any{
		"spec": map[string]any{"backupPolicyName": "db-mysql-backup-policy", "backupMethod": "xtrabackup"},
	})
	dyn := kbFakeDyn(src, backup, kbPolicy("db", "mysql", "xtrabackup"))
	create := func(in *KBRestoreInput) error {
		_, err := KBRestoreToNewCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", in)
		return err
	}
	if err := create(&KBRestoreInput{Name: "db2", Component: "mysql", BackupName: "no-such-backup"}); err == nil {
		t.Fatal("备份不存在应报错")
	}
	if err := create(&KBRestoreInput{Name: "db2", Component: "proxy", BackupName: "backup-db-aaa"}); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatal("源集群没有的组件应报错")
	}
	if _, err := KBRestoreToNewCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBRestoreInput{Name: "db", Component: "mysql", BackupName: "backup-db-aaa"}); err == nil {
		t.Fatal("新集群名不能与源集群相同")
	}
	if _, err := KBRestoreToNewCluster(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db",
		&KBRestoreInput{Name: "db2", Component: "mysql", BackupName: "backup-db-aaa"}); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	obj, _ := dyn.Resource(kbClusterGVR).Namespace("app").Get(context.Background(), "db2", metav1.GetOptions{})
	if obj == nil {
		t.Fatal("新集群未创建")
	}
	if obj.GetLabels()["app.kubernetes.io/instance"] != "" {
		t.Errorf("身份标签要清掉: %v", obj.GetLabels())
	}
	ann := obj.GetAnnotations()["kubeblocks.io/restore-from-backup"]
	if ann == "" {
		t.Fatal("恢复注解缺失")
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal([]byte(ann), &payload); err != nil {
		t.Fatalf("注解不是合法 JSON: %v", ann)
	}
	m := payload["mysql"]
	if m == nil || m["name"] != "backup-db-aaa" || m["namespace"] != "app" || m["volumeRestorePolicy"] != "Parallel" {
		t.Errorf("注解内容不对: %s", ann)
	}
	if got := str(obj, "spec", "terminationPolicy"); got != "Delete" {
		t.Errorf("spec 应整体复制: %s", got)
	}
}

func TestKBListBackupRepos(t *testing.T) {
	repo := kbUD("oss-repo", kbRepoGVR, "BackupRepo", "", map[string]any{
		"spec":   map[string]any{"storageProviderRef": "oss", "accessMethod": "Mount", "pathPrefix": "kubeblocks"},
		"status": map[string]any{"phase": "Available", "isDefault": true, "generatedStorageClassName": "kubeblocks-oss"},
	})
	dyn := kbFakeDyn(repo)
	repos, err := KBListBackupRepos(context.Background(), &kube.Client{Dynamic: dyn})
	if err != nil {
		t.Fatalf("KBListBackupRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Provider != "oss" || !repos[0].Default || repos[0].Phase != "Available" {
		t.Fatalf("仓库视图不对: %+v", repos)
	}
}
