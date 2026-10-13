// velero.go Velero 备份概览：把 Backup / Schedule / Restore 三类 CR 的
// 进度、失败信息、下次执行时间整理成前端可直接渲染的视图，并支持发起恢复。
//
// Velero CR 落在安装时的 ns（常见 velero，但不保证），所以一律跨 ns 列举；
// 是否"装了 Velero"按 CRD 注册判定，与列表权限错误分开——旧实现把两者混成
// 「未安装」，RBAC 缺失时页面会误导排查方向。
package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
)

var (
	veleroBackupGVR   = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backups"}
	veleroScheduleGVR = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "schedules"}
	veleroRestoreGVR  = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "restores"}
	veleroBSLGVR      = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backupstoragelocations"}
	veleroPVBGVR      = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "podvolumebackups"}

	veleroNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// VeleroInstalled backups.velero.io CRD 是否已注册。
func VeleroInstalled(c *kube.Client) bool {
	res, err := c.Discovery.ServerResourcesForGroupVersion("velero.io/v1")
	if err != nil || res == nil {
		return false
	}
	for _, r := range res.APIResources {
		if r.Name == "backups" {
			return true
		}
	}
	return false
}

// ============ 视图结构 ============

// VeleroBackup 一次备份任务。Warnings/ValidationWarnings/Events 是"失败信息"的
// 可见来源——资源级错误明细在对象存储的 tarball 里，控制台不透出，事件与
// warnings 计数足以定位到"哪坏了"。
type VeleroBackup struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Schedule  string    `json:"schedule,omitempty"` // 由计划触发时回填计划名
	Phase     string    `json:"phase"`
	CreatedAt time.Time `json:"createdAt"`

	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Expiration  *time.Time `json:"expiration,omitempty"`
	DurationSec int64      `json:"durationSec"`

	ProgressTotal     int `json:"progressTotal"` // status.progressItems（资源项）
	ProgressCompleted int `json:"progressCompleted"`

	SnapshotsAttempted int `json:"snapshotsAttempted"`
	SnapshotsCaptured  int `json:"snapshotsCaptured"`

	ValidationWarnings []string `json:"validationWarnings,omitempty"`
	Warnings           int      `json:"warnings"` // status.warnings 条目数（v1.13 前为 map）
	Errors             int      `json:"errors"`   // status.errors 条目数

	StorageLocation    string   `json:"storageLocation,omitempty"`
	IncludedNamespaces []string `json:"includedNamespaces,omitempty"`
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
	Ttl                string   `json:"ttl,omitempty"`
	SnapshotVolumes    *bool    `json:"snapshotVolumes,omitempty"`
	FsBackup           *bool    `json:"fsBackup,omitempty"` // spec.defaultVolumesToFsBackup
}

// VeleroSchedule 备份计划：cron + 暂停态 + 上次结果 + 下次执行（服务端算，
// 前端没有 cron 解析库，也不该在浏览器里再实现一份口径）。
type VeleroSchedule struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Cron       string    `json:"cron"`
	Paused     bool      `json:"paused"`
	CreatedAt  time.Time `json:"createdAt"`
	LastBackup string    `json:"lastBackup,omitempty"`
	LastError  string    `json:"lastError,omitempty"` // status.conditions 里的失败原因

	NextRun *time.Time `json:"nextRun,omitempty"`

	// 模板摘要（等价一份 BackupSpec 的关键项）
	StorageLocation    string   `json:"storageLocation,omitempty"`
	IncludedNamespaces []string `json:"includedNamespaces,omitempty"`
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
	Ttl                string   `json:"ttl,omitempty"`
	SnapshotVolumes    *bool    `json:"snapshotVolumes,omitempty"`
	FsBackup           *bool    `json:"fsBackup,omitempty"`
}

// VeleroRestore 恢复任务。
type VeleroRestore struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	BackupName string    `json:"backupName"`
	Phase      string    `json:"phase"`
	CreatedAt  time.Time `json:"createdAt"`

	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	DurationSec int64      `json:"durationSec"`

	ProgressTotal     int `json:"progressTotal"`
	ProgressCompleted int `json:"progressCompleted"`

	ValidationErrors []string `json:"validationErrors,omitempty"`
	Warnings         int      `json:"warnings"`
	Errors           int      `json:"errors"`

	IncludedNamespaces []string          `json:"includedNamespaces,omitempty"`
	RestorePVs         *bool             `json:"restorePVs,omitempty"`
	PreserveNodePorts  *bool             `json:"preserveNodePorts,omitempty"`
	NamespaceMapping   map[string]string `json:"namespaceMapping,omitempty"`
}

// VeleroLocation 备份存储位置（BackupStorageLocation CR）——新建备份/计划的
// "位置"下拉数据源；一个都没配置时前端禁用新建并引导先配 BSL。
type VeleroLocation struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Default   bool   `json:"default"`
}

// VeleroOverview 一次拉全三张列表。RBAC 缺哪类就对应列表报错（Partial 说明），
// 不整体失败——只读用户能装 Velero CRD 的 get/list 未必全覆盖 restores。
type VeleroOverview struct {
	Installed bool             `json:"installed"`
	Namespace string           `json:"namespace,omitempty"` // Velero 安装 ns（新建 CR 的落点，从 BSL/Backup 推导）
	Partial   []string         `json:"partial,omitempty"`   // 拉取失败的部分及原因
	Locations []VeleroLocation `json:"locations"`
	Backups   []VeleroBackup   `json:"backups"`
	Schedules []VeleroSchedule `json:"schedules"`
	Restores  []VeleroRestore  `json:"restores"`
}

// ============ 读取 ============

func (s *K8sService) VeleroOverview(ctx context.Context, c *kube.Client) (*VeleroOverview, error) {
	out := &VeleroOverview{Installed: VeleroInstalled(c),
		Locations: []VeleroLocation{}, Backups: []VeleroBackup{}, Schedules: []VeleroSchedule{}, Restores: []VeleroRestore{}}
	if !out.Installed {
		return out, nil
	}
	if items, err := veleroList(ctx, c, veleroBSLGVR); err != nil {
		out.Partial = append(out.Partial, "存储位置: "+err.Error())
	} else {
		for i := range items {
			u := &items[i]
			out.Locations = append(out.Locations, VeleroLocation{
				Name: u.GetName(), Namespace: u.GetNamespace(), Default: nestedBool(u.Object, "spec", "default"),
			})
		}
	}
	if items, err := veleroList(ctx, c, veleroBackupGVR); err != nil {
		out.Partial = append(out.Partial, "备份任务: "+err.Error())
	} else {
		for i := range items {
			out.Backups = append(out.Backups, parseVeleroBackup(&items[i]))
		}
	}
	if items, err := veleroList(ctx, c, veleroScheduleGVR); err != nil {
		out.Partial = append(out.Partial, "备份计划: "+err.Error())
	} else {
		for i := range items {
			out.Schedules = append(out.Schedules, parseVeleroSchedule(&items[i]))
		}
	}
	if items, err := veleroList(ctx, c, veleroRestoreGVR); err != nil {
		out.Partial = append(out.Partial, "恢复任务: "+err.Error())
	} else {
		for i := range items {
			out.Restores = append(out.Restores, parseVeleroRestore(&items[i]))
		}
	}
	// Velero 安装 ns（新建 CR 的落点）：BSL 优先（它是配置备份的前置），
	// 其次任一备份/计划所在 ns——全为空时留空，前端新建会明确报"无法确定命名空间"
	if len(out.Locations) > 0 {
		out.Namespace = out.Locations[0].Namespace
	} else if len(out.Backups) > 0 {
		out.Namespace = out.Backups[0].Namespace
	} else if len(out.Schedules) > 0 {
		out.Namespace = out.Schedules[0].Namespace
	}
	return out, nil
}

func veleroList(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	l, err := c.Dynamic.Resource(gvr).Namespace(corev1.NamespaceAll).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	items := l.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].GetCreationTimestamp().After(items[j].GetCreationTimestamp().Time)
	})
	return items, nil
}

func parseVeleroBackup(u *unstructured.Unstructured) VeleroBackup {
	b := VeleroBackup{
		Name: u.GetName(), Namespace: u.GetNamespace(), CreatedAt: u.GetCreationTimestamp().Time,
		Schedule: u.GetLabels()["velero.io/schedule-name"],
		Phase:    nestedStr(u.Object, "status", "phase"),
	}
	b.StartedAt = nestedTime(u.Object, "status", "startTimestamp")
	b.FinishedAt = nestedTime(u.Object, "status", "completionTimestamp")
	b.Expiration = nestedTime(u.Object, "status", "expiration")
	if b.StartedAt != nil {
		end := time.Now()
		if b.FinishedAt != nil {
			end = *b.FinishedAt
		}
		b.DurationSec = int64(end.Sub(*b.StartedAt).Seconds())
	}
	b.ProgressTotal = nestedInt(u.Object, "status", "progressItems", "total")
	b.ProgressCompleted = nestedInt(u.Object, "status", "progressItems", "completed")
	b.SnapshotsAttempted = nestedInt(u.Object, "status", "volumeSnapshotsAttempted")
	b.SnapshotsCaptured = nestedInt(u.Object, "status", "volumeSnapshotsSuccessfullyCaptured")
	b.ValidationWarnings = nestedStrSlice(u.Object, "status", "validationWarnings")
	b.Warnings = len(nestedMap(u.Object, "status", "warnings"))
	b.Errors = len(nestedMap(u.Object, "status", "errors"))
	b.StorageLocation = firstNonEmpty(nestedStr(u.Object, "status", "backupStorageLocation"), nestedStr(u.Object, "spec", "storageLocation"))
	b.IncludedNamespaces = nestedStrSlice(u.Object, "spec", "includedNamespaces")
	b.ExcludedNamespaces = nestedStrSlice(u.Object, "spec", "excludedNamespaces")
	b.Ttl = nestedStr(u.Object, "spec", "ttl")
	if sv, ok, _ := unstructured.NestedBool(u.Object, "spec", "snapshotVolumes"); ok {
		b.SnapshotVolumes = &sv
	}
	if fb, ok, _ := unstructured.NestedBool(u.Object, "spec", "defaultVolumesToFsBackup"); ok {
		b.FsBackup = &fb
	}
	return b
}

func parseVeleroSchedule(u *unstructured.Unstructured) VeleroSchedule {
	sc := VeleroSchedule{
		Name: u.GetName(), Namespace: u.GetNamespace(), CreatedAt: u.GetCreationTimestamp().Time,
		Cron:       nestedStr(u.Object, "spec", "schedule"),
		Paused:     nestedBool(u.Object, "spec", "paused"),
		LastBackup: nestedStr(u.Object, "status", "lastBackup"),
	}
	for _, cond := range nestedSlice(u.Object, "status", "conditions") {
		if m, ok := cond.(map[string]interface{}); ok {
			if st, _ := m["status"].(string); st == "True" {
				if reason, _ := m["message"].(string); reason != "" {
					sc.LastError = reason
				}
			}
		}
	}
	// 下次执行：标准 5 段 cron（robfig ParseStandard，与 CI 定时任务同口径）；
	// 解析失败或已暂停就不给值，前端显示 —
	if !sc.Paused && sc.Cron != "" {
		if sched, err := cron.ParseStandard(sc.Cron); err == nil {
			t := sched.Next(time.Now())
			sc.NextRun = &t
		}
	}
	sc.StorageLocation = nestedStr(u.Object, "spec", "template", "storageLocation")
	sc.IncludedNamespaces = nestedStrSlice(u.Object, "spec", "template", "includedNamespaces")
	sc.ExcludedNamespaces = nestedStrSlice(u.Object, "spec", "template", "excludedNamespaces")
	sc.Ttl = nestedStr(u.Object, "spec", "template", "ttl")
	if sv, ok, _ := unstructured.NestedBool(u.Object, "spec", "template", "snapshotVolumes"); ok {
		sc.SnapshotVolumes = &sv
	}
	if fb, ok, _ := unstructured.NestedBool(u.Object, "spec", "template", "defaultVolumesToFsBackup"); ok {
		sc.FsBackup = &fb
	}
	return sc
}

func parseVeleroRestore(u *unstructured.Unstructured) VeleroRestore {
	r := VeleroRestore{
		Name: u.GetName(), Namespace: u.GetNamespace(), CreatedAt: u.GetCreationTimestamp().Time,
		BackupName: nestedStr(u.Object, "spec", "backupName"),
		Phase:      nestedStr(u.Object, "status", "phase"),
	}
	r.StartedAt = nestedTime(u.Object, "status", "startTimestamp")
	r.FinishedAt = nestedTime(u.Object, "status", "completionTimestamp")
	if r.StartedAt != nil {
		end := time.Now()
		if r.FinishedAt != nil {
			end = *r.FinishedAt
		}
		r.DurationSec = int64(end.Sub(*r.StartedAt).Seconds())
	}
	r.ProgressTotal = nestedInt(u.Object, "status", "progressItems", "total")
	r.ProgressCompleted = nestedInt(u.Object, "status", "progressItems", "completed")
	r.ValidationErrors = nestedStrSlice(u.Object, "status", "validationErrors")
	r.Warnings = len(nestedMap(u.Object, "status", "warnings"))
	r.Errors = len(nestedMap(u.Object, "status", "errors"))
	r.IncludedNamespaces = nestedStrSlice(u.Object, "spec", "includedNamespaces")
	if v, ok, _ := unstructured.NestedBool(u.Object, "spec", "restorePVs"); ok {
		r.RestorePVs = &v
	}
	if v, ok, _ := unstructured.NestedBool(u.Object, "spec", "preserveNodePorts"); ok {
		r.PreserveNodePorts = &v
	}
	if m, found, _ := unstructured.NestedStringMap(u.Object, "spec", "namespaceMapping"); found {
		r.NamespaceMapping = m
	}
	return r
}

// VeleroEvents 关联事件（备份/恢复的失败信息主要落点）。
func (s *K8sService) VeleroEvents(ctx context.Context, c *kube.Client, namespace, kind, name string) ([]EventItem, error) {
	if kind != "Backup" && kind != "Restore" && kind != "Schedule" {
		return nil, fmt.Errorf("kind 仅支持 Backup/Restore/Schedule")
	}
	return s.listEvents(ctx, c, namespace, kind, name)
}

// VeleroVolumeBackup 一次备份里单个 Pod 卷的文件级备份（PodVolumeBackup CR）。
// PartiallyFailed 的资源级错误明细在对象存储 tarball 里（控制台不解析），
// 但 Canceled/Failed 的 PVB 能直接定位到"哪个 Pod 哪个卷出的问题"。
type VeleroVolumeBackup struct {
	Name      string     `json:"name"`
	Namespace string     `json:"namespace"`
	PodNS     string     `json:"podNamespace"`
	PodName   string     `json:"podName"`
	Volume    string     `json:"volume"`
	Node      string     `json:"node,omitempty"`
	Phase     string     `json:"phase"`
	Message   string     `json:"message,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Completed *time.Time `json:"completedAt,omitempty"`
	Bytes     int64      `json:"bytesBackedUp"`
}

// VeleroBackupVolumes 按 backup 名（velero.io/backup-name 标签）列其全部卷备份，
// 未完成的排前面。
func (s *K8sService) VeleroBackupVolumes(ctx context.Context, c *kube.Client, backupName string) ([]VeleroVolumeBackup, error) {
	if !veleroNameRe.MatchString(backupName) {
		return nil, fmt.Errorf("非法备份名")
	}
	l, err := c.Dynamic.Resource(veleroPVBGVR).Namespace(corev1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: "velero.io/backup-name=" + backupName, Limit: 2000,
	})
	if err != nil {
		return nil, err
	}
	out := make([]VeleroVolumeBackup, 0, len(l.Items))
	for i := range l.Items {
		u := &l.Items[i]
		v := VeleroVolumeBackup{
			Name:      u.GetName(),
			Namespace: u.GetNamespace(),
			PodNS:     nestedStr(u.Object, "spec", "pod", "namespace"),
			PodName:   nestedStr(u.Object, "spec", "pod", "name"),
			Volume:    nestedStr(u.Object, "spec", "volume"),
			Node:      nestedStr(u.Object, "spec", "node"),
			Phase:     nestedStr(u.Object, "status", "phase"),
			Message:   nestedStr(u.Object, "status", "message"),
			StartedAt: nestedTime(u.Object, "status", "startTimestamp"),
			Completed: nestedTime(u.Object, "status", "completionTimestamp"),
		}
		if b, ok, _ := unstructured.NestedInt64(u.Object, "status", "bytesBackedUp"); ok {
			v.Bytes = b
		}
		out = append(out, v)
	}
	notDone := func(p string) bool { return p != "" && p != "Completed" }
	sort.SliceStable(out, func(i, j int) bool {
		return notDone(out[i].Phase) && !notDone(out[j].Phase)
	})
	return out, nil
}

// ============ 写操作 ============

// VeleroRestoreInput 新建恢复。命名空间不填 = 按备份原样全量恢复；
// Restore 必须与源 Backup 同 ns（Velero 约束），前端把备份的 ns 一起带上。
type VeleroRestoreInput struct {
	Namespace          string   `json:"namespace" binding:"required"`
	BackupName         string   `json:"backupName" binding:"required"`
	Name               string   `json:"name"`
	IncludedNamespaces []string `json:"includedNamespaces"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
	RestorePVs         *bool    `json:"restorePVs"`
	PreserveNodePorts  *bool    `json:"preserveNodePorts"`
}

func (in *VeleroRestoreInput) restoreName() string {
	if n := strings.TrimSpace(in.Name); n != "" {
		return n
	}
	base := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(in.BackupName), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "backup"
	}
	suffix := time.Now().Format("060102150405")
	name := base + "-rst-" + suffix
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-.")
	}
	return name
}

func (s *K8sService) VeleroCreateRestore(ctx context.Context, c *kube.Client, in *VeleroRestoreInput) (string, error) {
	if !veleroNameRe.MatchString(in.Namespace) {
		return "", fmt.Errorf("命名空间名非法: %s", in.Namespace)
	}
	name := in.restoreName()
	if !veleroNameRe.MatchString(name) || len(name) > 63 {
		return "", fmt.Errorf("恢复名称非法: %s（小写字母/数字/-，字母数字开头结尾，≤63）", name)
	}
	spec := map[string]interface{}{"backupName": in.BackupName}
	if len(in.IncludedNamespaces) > 0 {
		spec["includedNamespaces"] = toAnySlice(in.IncludedNamespaces)
	}
	if len(in.ExcludedNamespaces) > 0 {
		spec["excludedNamespaces"] = toAnySlice(in.ExcludedNamespaces)
	}
	if in.RestorePVs != nil {
		spec["restorePVs"] = *in.RestorePVs
	}
	if in.PreserveNodePorts != nil {
		spec["preserveNodePorts"] = *in.PreserveNodePorts
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Restore",
		"metadata":   map[string]interface{}{"name": name, "namespace": in.Namespace},
		"spec":       spec,
	}}
	if _, err := c.Dynamic.Resource(veleroRestoreGVR).Namespace(in.Namespace).Create(ctx, u, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return name, nil
}

// ============ 新建备份 / 新建计划 ============

// VeleroBackupSpecInput 备份模板字段（手动备份与计划 template 共用）。
type VeleroBackupSpecInput struct {
	IncludedNamespaces []string `json:"includedNamespaces"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
	SnapshotVolumes    *bool    `json:"snapshotVolumes"`
	FsBackup           *bool    `json:"fsBackup"` // defaultVolumesToFsBackup：node-agent 文件级备份卷内容
	StorageLocation    string   `json:"storageLocation"`
	TtlHours           int      `json:"ttlHours"` // 保留期（小时）；0 = Velero 默认 720h
}

func (in *VeleroBackupSpecInput) toSpec() (map[string]interface{}, error) {
	if in.TtlHours < 0 || in.TtlHours > 87600 { // 上限 10 年，拦明显手滑
		return nil, fmt.Errorf("保留期需在 0-87600 小时（0 = 默认 720h）")
	}
	if in.TtlHours > 0 {
		if _, err := time.ParseDuration(fmt.Sprintf("%dh", in.TtlHours)); err != nil {
			return nil, fmt.Errorf("保留期非法: %w", err)
		}
	}
	spec := map[string]interface{}{}
	// 全空 = 集群范围全量备份（Velero 语义），显式留空即可
	if len(in.IncludedNamespaces) > 0 {
		spec["includedNamespaces"] = toAnySlice(in.IncludedNamespaces)
	}
	if len(in.ExcludedNamespaces) > 0 {
		spec["excludedNamespaces"] = toAnySlice(in.ExcludedNamespaces)
	}
	if in.SnapshotVolumes != nil {
		spec["snapshotVolumes"] = *in.SnapshotVolumes
	}
	if in.FsBackup != nil {
		spec["defaultVolumesToFsBackup"] = *in.FsBackup
	}
	if loc := strings.TrimSpace(in.StorageLocation); loc != "" {
		if !veleroNameRe.MatchString(loc) {
			return nil, fmt.Errorf("存储位置名非法: %s", loc)
		}
		spec["storageLocation"] = loc
	}
	if in.TtlHours > 0 {
		spec["ttl"] = fmt.Sprintf("%dh0m0s", in.TtlHours)
	}
	return spec, nil
}

// VeleroBackupCreateInput 手动发起一次备份。Namespace 留空 = 服务端按
// BSL/既有备份推导 Velero 安装 ns。
type VeleroBackupCreateInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	VeleroBackupSpecInput
}

func (s *K8sService) VeleroCreateBackup(ctx context.Context, c *kube.Client, in *VeleroBackupCreateInput) (string, error) {
	ns, err := s.detectVeleroNamespace(ctx, c, in.Namespace)
	if err != nil {
		return "", err
	}
	spec, err := in.toSpec()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "manual-" + time.Now().Format("20060102150405")
	}
	if !veleroNameRe.MatchString(name) || len(name) > 63 {
		return "", fmt.Errorf("备份名称非法: %s（小写字母/数字/-，字母数字开头结尾，≤63）", name)
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       spec,
	}}
	if _, err := c.Dynamic.Resource(veleroBackupGVR).Namespace(ns).Create(ctx, u, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return name, nil
}

// VeleroScheduleCreateInput 新建备份计划。
type VeleroScheduleCreateInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name" binding:"required"`
	Cron      string `json:"cron" binding:"required"`
	Paused    bool   `json:"paused"`
	VeleroBackupSpecInput
}

func (s *K8sService) VeleroCreateSchedule(ctx context.Context, c *kube.Client, in *VeleroScheduleCreateInput) (string, error) {
	ns, err := s.detectVeleroNamespace(ctx, c, in.Namespace)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if !veleroNameRe.MatchString(name) || len(name) > 63 {
		return "", fmt.Errorf("计划名称非法: %s（小写字母/数字/-，字母数字开头结尾，≤63）", name)
	}
	// cron 在这里校验而不是靠 Velero 拒：错误信息给中文示例，提交前就报错
	if _, err := cron.ParseStandard(strings.TrimSpace(in.Cron)); err != nil {
		return "", fmt.Errorf("cron 表达式非法: %s（标准 5 段，如 0 1 * * * = 每天 01:00）", in.Cron)
	}
	spec, err := in.toSpec()
	if err != nil {
		return "", err
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Schedule",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"schedule": strings.TrimSpace(in.Cron),
			"paused":   in.Paused,
			"template": spec,
		},
	}}
	if _, err := c.Dynamic.Resource(veleroScheduleGVR).Namespace(ns).Create(ctx, u, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return name, nil
}

// VeleroScheduleUpdateInput 编辑计划：名称在路径里，body 只带 cron/paused/模板。
type VeleroScheduleUpdateInput struct {
	Cron   string `json:"cron"`
	Paused bool   `json:"paused"`
	VeleroBackupSpecInput
}

// VeleroUpdateSchedule 编辑计划：整体替换 cron / paused / template（校验与创建同口径）。
func (s *K8sService) VeleroUpdateSchedule(ctx context.Context, c *kube.Client, ns, name string, in *VeleroScheduleUpdateInput) error {
	if !veleroNameRe.MatchString(name) {
		return fmt.Errorf("计划名称非法: %s", name)
	}
	if _, err := cron.ParseStandard(strings.TrimSpace(in.Cron)); err != nil {
		return fmt.Errorf("cron 表达式非法: %s（标准 5 段，如 0 1 * * * = 每天 01:00）", in.Cron)
	}
	spec, err := in.toSpec()
	if err != nil {
		return err
	}
	cur, err := c.Dynamic.Resource(veleroScheduleGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := unstructured.SetNestedField(cur.Object, strings.TrimSpace(in.Cron), "spec", "schedule"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(cur.Object, in.Paused, "spec", "paused"); err != nil {
		return err
	}
	if err := unstructured.SetNestedMap(cur.Object, spec, "spec", "template"); err != nil {
		return err
	}
	_, err = c.Dynamic.Resource(veleroScheduleGVR).Namespace(ns).Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

// VeleroDeleteSchedule 删除计划（只删计划本身，不连带历史备份）。
func (s *K8sService) VeleroDeleteSchedule(ctx context.Context, c *kube.Client, ns, name string) error {
	if !veleroNameRe.MatchString(ns) || !veleroNameRe.MatchString(name) {
		return fmt.Errorf("非法的命名空间或计划名")
	}
	return c.Dynamic.Resource(veleroScheduleGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// detectVeleroNamespace Velero CR 的落点：显式传入优先，否则 BSL → 备份 → 计划。
func (s *K8sService) detectVeleroNamespace(ctx context.Context, c *kube.Client, explicit string) (string, error) {
	if explicit != "" && veleroNameRe.MatchString(explicit) {
		return explicit, nil
	}
	for _, gvr := range []schema.GroupVersionResource{veleroBSLGVR, veleroBackupGVR, veleroScheduleGVR} {
		items, err := veleroList(ctx, c, gvr)
		if err == nil && len(items) > 0 {
			return items[0].GetNamespace(), nil
		}
	}
	return "", fmt.Errorf("无法确定 Velero 安装命名空间（集群里没有任何 Velero CR），请先在集群部署 Velero 与 BackupStorageLocation")
}

// VeleroSetSchedulePaused 启停备份计划。
func (s *K8sService) VeleroSetSchedulePaused(ctx context.Context, c *kube.Client, namespace, name string, paused bool) error {
	u, err := c.Dynamic.Resource(veleroScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("备份计划 %s/%s 不存在或不可读: %w", namespace, name, err)
	}
	if err := unstructured.SetNestedField(u.Object, paused, "spec", "paused"); err != nil {
		return err
	}
	_, err = c.Dynamic.Resource(veleroScheduleGVR).Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// VeleroDeleteBackup 删除备份记录（连带对象存储中的备份数据——Velero 语义）。
func (s *K8sService) VeleroDeleteBackup(ctx context.Context, c *kube.Client, namespace, name string) error {
	return c.Dynamic.Resource(veleroBackupGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// VeleroDeleteRestore 删除恢复任务记录。
func (s *K8sService) VeleroDeleteRestore(ctx context.Context, c *kube.Client, namespace, name string) error {
	return c.Dynamic.Resource(veleroRestoreGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// ============ unstructured 取值小工具 ============

func nestedStr(obj map[string]interface{}, path ...string) string {
	v, _, _ := unstructured.NestedString(obj, path...)
	return v
}

func nestedInt(obj map[string]interface{}, path ...string) int {
	// JSON 数字在 unstructured 里可能是 int64 或 float64，两条都试
	if v, found, _ := unstructured.NestedInt64(obj, path...); found {
		return int(v)
	}
	if v, found, _ := unstructured.NestedFloat64(obj, path...); found {
		return int(v)
	}
	return 0
}

func nestedBool(obj map[string]interface{}, path ...string) bool {
	v, _, _ := unstructured.NestedBool(obj, path...)
	return v
}

func nestedTime(obj map[string]interface{}, path ...string) *time.Time {
	sv, found, _ := unstructured.NestedString(obj, path...)
	if !found || sv == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, sv)
	if err != nil {
		return nil
	}
	return &t
}

func nestedStrSlice(obj map[string]interface{}, path ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(obj, path...)
	return v
}

func nestedMap(obj map[string]interface{}, path ...string) map[string]interface{} {
	v, _, _ := unstructured.NestedMap(obj, path...)
	return v
}

func nestedSlice(obj map[string]interface{}, path ...string) []interface{} {
	v, _, _ := unstructured.NestedSlice(obj, path...)
	return v
}

func toAnySlice(ss []string) []interface{} {
	out := make([]interface{}, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
