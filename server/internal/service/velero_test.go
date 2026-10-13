package service

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func veleroU(obj map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: obj}
}

func TestParseVeleroBackup(t *testing.T) {
	u := veleroU(map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "nightly-20261009", "namespace": "velero",
			"labels": map[string]interface{}{"velero.io/schedule-name": "nightly"},
		},
		"spec": map[string]interface{}{
			"includedNamespaces": []interface{}{"app-a", "app-b"},
			"storageLocation":    "minio-main",
			"snapshotVolumes":    true,
			"ttl":                "168h0m0s",
		},
		"status": map[string]interface{}{
			"phase":                               "PartiallyFailed",
			"startTimestamp":                      "2026-10-09T01:00:00Z",
			"completionTimestamp":                 "2026-10-09T01:02:03Z",
			"backupStorageLocation":               "minio-main",
			"progressItems":                       map[string]interface{}{"total": int64(100), "completed": int64(98)},
			"volumeSnapshotsAttempted":            int64(6),
			"volumeSnapshotsSuccessfullyCaptured": int64(5),
			"validationWarnings":                  []interface{}{"unsupported resource type foo"},
			"errors":                              map[string]interface{}{"backup": "PartialFailure"},
		},
	})
	b := parseVeleroBackup(u)
	if b.Schedule != "nightly" || b.Phase != "PartiallyFailed" {
		t.Fatalf("basic fields: %+v", b)
	}
	if b.DurationSec != 123 {
		t.Fatalf("duration: %d", b.DurationSec)
	}
	if b.ProgressTotal != 100 || b.ProgressCompleted != 98 {
		t.Fatalf("progress: %+v", b)
	}
	if b.SnapshotsAttempted != 6 || b.SnapshotsCaptured != 5 {
		t.Fatalf("snapshots: %+v", b)
	}
	if len(b.ValidationWarnings) != 1 || b.Errors != 1 {
		t.Fatalf("warnings/errors: %+v", b)
	}
	if b.StorageLocation != "minio-main" || len(b.IncludedNamespaces) != 2 {
		t.Fatalf("spec fields: %+v", b)
	}
	if b.SnapshotVolumes == nil || !*b.SnapshotVolumes {
		t.Fatalf("snapshotVolumes ptr: %v", b.SnapshotVolumes)
	}
}

func TestParseVeleroScheduleNextRun(t *testing.T) {
	u := veleroU(map[string]interface{}{
		"metadata": map[string]interface{}{"name": "nightly", "namespace": "velero"},
		"spec": map[string]interface{}{
			"schedule": "0 1 * * *",
			"template": map[string]interface{}{"ttl": "168h", "storageLocation": "s3"},
		},
		"status": map[string]interface{}{"lastBackup": "nightly-20261009"},
	})
	sc := parseVeleroSchedule(u)
	if sc.Cron != "0 1 * * *" || sc.LastBackup != "nightly-20261009" || sc.Paused {
		t.Fatalf("schedule fields: %+v", sc)
	}
	if sc.NextRun == nil || sc.NextRun.Format("15:04") != "01:00" {
		t.Fatalf("nextRun must be computed: %v", sc.NextRun)
	}
	if sc.StorageLocation != "s3" {
		t.Fatalf("template fields: %+v", sc)
	}
	// 暂停后无下次执行
	u2 := veleroU(map[string]interface{}{
		"metadata": map[string]interface{}{"name": "x", "namespace": "velero"},
		"spec":     map[string]interface{}{"schedule": "0 1 * * *", "paused": true},
	})
	if sc2 := parseVeleroSchedule(u2); sc2.NextRun != nil || !sc2.Paused {
		t.Fatalf("paused schedule must not have nextRun: %+v", sc2)
	}
	// 非法 cron 不报错、留空
	u3 := veleroU(map[string]interface{}{
		"metadata": map[string]interface{}{"name": "x", "namespace": "velero"},
		"spec":     map[string]interface{}{"schedule": "not a cron"},
	})
	if parseVeleroSchedule(u3).NextRun != nil {
		t.Fatal("invalid cron must leave nextRun empty")
	}
}

func TestVeleroBackupSpecInput(t *testing.T) {
	// 留空 = 全量（spec 空 map，交给 Velero 默认）
	spec, err := (&VeleroBackupSpecInput{}).toSpec()
	if err != nil || len(spec) != 0 {
		t.Fatalf("empty template must stay cluster-wide: %v %v", spec, err)
	}
	sv := true
	spec, err = (&VeleroBackupSpecInput{
		IncludedNamespaces: []string{"app"}, ExcludedNamespaces: []string{"kube-system"},
		SnapshotVolumes: &sv, StorageLocation: "minio-main", TtlHours: 168,
	}).toSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec["ttl"] != "168h0m0s" || spec["storageLocation"] != "minio-main" || spec["snapshotVolumes"] != true {
		t.Fatalf("spec fields: %+v", spec)
	}
	if _, ok := spec["includedNamespaces"].([]interface{}); !ok {
		t.Fatalf("ns slices must be promoted: %+v", spec)
	}
	if _, err := (&VeleroBackupSpecInput{TtlHours: -1}).toSpec(); err == nil {
		t.Fatal("negative ttl must fail")
	}
	if _, err := (&VeleroBackupSpecInput{StorageLocation: "Bad Loc"}).toSpec(); err == nil {
		t.Fatal("invalid location name must fail")
	}
}

func TestRestoreNameDefault(t *testing.T) {
	in := &VeleroRestoreInput{BackupName: "Nightly_Backup.2026!!"}
	name := in.restoreName()
	if !veleroNameRe.MatchString(name) {
		t.Fatalf("generated name must be dns-1123: %q", name)
	}
	if !strings.HasPrefix(name, "nightly-backup-2026-rst-") {
		t.Fatalf("name shape: %q", name)
	}
	// 显式名原样采用（合法性由 Create 校验）
	in2 := &VeleroRestoreInput{BackupName: "b", Name: "my-restore"}
	if in2.restoreName() != "my-restore" {
		t.Fatalf("explicit name: %q", in2.restoreName())
	}
}
