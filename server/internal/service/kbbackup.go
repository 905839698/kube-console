// kbbackup.go KubeBlocks 数据库级备份恢复（dataprotection.kubeblocks.io/v1alpha1，字段 1.0.2 实测）：
//   - BackupPolicy/BackupSchedule 由 operator 建集群时按组件自动生成，命名 <cluster>-<component>-backup-policy/-schedule，
//     都带 app.kubernetes.io/instance=<cluster> 标签——集群关联按标签 + 命名双保险
//   - Backup 手动触发（generateName 防重名），phase/totalSize/failureReason 在 status
//   - 备份能否成功取决于 BackupRepo（集群级）：没配仓库时把状态明确透出，而不是让创建去撞错
//   - 恢复两条路：带注解建新集群（kubeblocks.io/restore-from-backup，官方 1.0 流程）、
//     RebuildInstance 工单（rebuildFrom[]）重建存量组件
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
)

var (
	kbBackupGVR  = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "backups"}
	kbPolicyGVR  = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "backuppolicies"}
	kbSchedGVR   = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "backupschedules"}
	kbRepoGVR    = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "backuprepos"}
	kbRestoreGVR = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "restores"}
)

// RestoreAnnotation 恢复到新集群的注解键（1.0 官方流程：新 Cluster 带此注解，
// operator 起 Pod 前先做数据恢复；旧版 dataprotection.kubeblocks.io/isRestore 已废弃）
const RestoreAnnotation = "kubeblocks.io/restore-from-backup"

// ---------- 视图 ----------

type KBBackupMethod struct {
	Name     string `json:"name"`
	Snapshot bool   `json:"snapshot"` // 卷快照备份（需要 CSI snapshot 支持）
}

type KBBackupPolicyView struct {
	Component string           `json:"component"`
	Name      string           `json:"name"`
	RepoName  string           `json:"repoName"`
	Phase     string           `json:"phase,omitempty"`
	Methods   []KBBackupMethod `json:"methods"`
	CredSecret string          `json:"credSecret,omitempty"` // 备份用的连接凭据 Secret（只报名字）
}

type KBScheduleItem struct {
	Name      string `json:"name"` // schedule 项名（如 xtrabackup）
	Method    string `json:"method"`
	Cron      string `json:"cronExpression"`
	Enabled   bool   `json:"enabled"`
	Retention string `json:"retention,omitempty"`
}

type KBScheduleView struct {
	Component  string          `json:"component"`
	Name       string          `json:"name"`
	PolicyName string          `json:"policyName"`
	Phase      string          `json:"phase,omitempty"`
	Items      []KBScheduleItem `json:"items"`
}

type KBBackupView struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Method      string `json:"method"`
	Policy      string `json:"policy"`
	Phase       string `json:"phase"`
	FailureReason string `json:"failureReason,omitempty"`
	Repo        string `json:"repo,omitempty"`
	TotalSize   string `json:"totalSize,omitempty"`
	Retention   string `json:"retention,omitempty"`
	CreatedAt   string `json:"createdAt"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Expiration  string `json:"expiration,omitempty"`
}

type KBBackupAction struct {
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	TargetPod  string `json:"targetPod,omitempty"`
	TotalSize  string `json:"totalSize,omitempty"`
	StartAt    string `json:"startAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

type KBBackupDetail struct {
	KBBackupView
	Actions         []KBBackupAction `json:"actions,omitempty"`
	TimeRange       map[string]any   `json:"timeRange,omitempty"`
	VolumeSnapshots int              `json:"volumeSnapshots,omitempty"`
}

type KBRepoView struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Default   bool   `json:"default"`
	Provider  string `json:"provider,omitempty"`
	AccessMethod string `json:"accessMethod,omitempty"`
	PathPrefix   string `json:"pathPrefix,omitempty"`
	StorageClass  string `json:"storageClass,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type KBRestoreView struct {
	Name       string `json:"name"`
	Backup     string `json:"backup,omitempty"`
	Phase      string `json:"phase"`
	Message    string `json:"message,omitempty"`
	Duration   string `json:"duration,omitempty"`
	CreatedAt  string `json:"createdAt"`
	CompletedAt string `json:"completedAt,omitempty"`
}

// ---------- BackupRepo（集群级，判断备份可行性）----------

// KBListBackupRepos 列出全部备份仓库。空列表 = 未配置备份目的地，创建备份必然失败。
func KBListBackupRepos(ctx context.Context, c *kube.Client) ([]KBRepoView, error) {
	list, err := c.Dynamic.Resource(kbRepoGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrorsIsNotFoundOrNoMatch(err) {
			return []KBRepoView{}, nil
		}
		return nil, err
	}
	out := make([]KBRepoView, 0, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		v := KBRepoView{Name: it.GetName()}
		v.Provider, _, _ = unstructured.NestedString(it.Object, "spec", "storageProviderRef")
		v.AccessMethod, _, _ = unstructured.NestedString(it.Object, "spec", "accessMethod")
		v.PathPrefix, _, _ = unstructured.NestedString(it.Object, "spec", "pathPrefix")
		v.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
		v.Default, _, _ = unstructured.NestedBool(it.Object, "status", "isDefault")
		v.StorageClass, _, _ = unstructured.NestedString(it.Object, "status", "generatedStorageClassName")
		if ts := it.GetCreationTimestamp(); !ts.IsZero() {
			v.CreatedAt = ts.Format("2006-01-02 15:04:05")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---------- BackupPolicy / BackupSchedule（operator 自动生成的集群资产）----------

// kbBackupOwnerPolicy 命名 + 标签双口径判定 BackupPolicy/BackupSchedule 是否属于该集群
func kbBackupOwner(metaName, cluster string) bool {
	return strings.HasPrefix(metaName, cluster+"-")
}

func kbListOwned(ctx context.Context, c *kube.Client, gvr schema.GroupVersionResource, ns, cluster string) (*unstructured.UnstructuredList, error) {
	list, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + cluster,
	})
	if err != nil {
		if apierrorsIsNotFoundOrNoMatch(err) {
			return &unstructured.UnstructuredList{}, nil
		}
		return nil, err
	}
	// 标签缺失（手工建/旧版本）时按命名前缀兜底
	kept := make([]unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		if kbBackupOwner(list.Items[i].GetName(), cluster) {
			kept = append(kept, list.Items[i])
		}
	}
	list.Items = kept
	return list, nil
}

// KBListBackupPolicies 该集群各组件的备份策略（含可用的备份方法）
func KBListBackupPolicies(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBBackupPolicyView, error) {
	list, err := kbListOwned(ctx, c, kbPolicyGVR, ns, cluster)
	if err != nil {
		return nil, err
	}
	out := make([]KBBackupPolicyView, 0, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		v := KBBackupPolicyView{Name: it.GetName(), Component: it.GetLabels()["apps.kubeblocks.io/component-name"]}
		if v.Component == "" {
			v.Component = strings.TrimSuffix(strings.TrimPrefix(it.GetName(), cluster+"-"), "-backup-policy")
		}
		v.RepoName, _, _ = unstructured.NestedString(it.Object, "spec", "backupRepoName")
		v.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
		v.CredSecret, _, _ = unstructured.NestedString(it.Object, "spec", "target", "connectionCredential", "secretName")
		methods, _, _ := unstructured.NestedSlice(it.Object, "spec", "backupMethods")
		for _, m := range methods {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			mi := KBBackupMethod{}
			mi.Name, _ = mm["name"].(string)
			mi.Snapshot, _, _ = unstructured.NestedBool(mm, "snapshotVolumes")
			v.Methods = append(v.Methods, mi)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	return out, nil
}

// KBGetBackupSchedule 备份计划（各组件各方法的 cron/启停/保留期）
func KBGetBackupSchedule(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBScheduleView, error) {
	list, err := kbListOwned(ctx, c, kbSchedGVR, ns, cluster)
	if err != nil {
		return nil, err
	}
	out := make([]KBScheduleView, 0, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		v := KBScheduleView{Name: it.GetName(), Component: it.GetLabels()["apps.kubeblocks.io/component-name"]}
		if v.Component == "" {
			v.Component = strings.TrimSuffix(strings.TrimPrefix(it.GetName(), cluster+"-"), "-backup-schedule")
		}
		v.PolicyName, _, _ = unstructured.NestedString(it.Object, "spec", "backupPolicyName")
		v.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
		items, _, _ := unstructured.NestedSlice(it.Object, "spec", "schedules")
		for _, si := range items {
			sm, ok := si.(map[string]any)
			if !ok {
				continue
			}
			item := KBScheduleItem{}
			item.Name, _ = sm["name"].(string)
			item.Method, _ = sm["backupMethod"].(string)
			item.Cron, _ = sm["cronExpression"].(string)
			item.Enabled, _, _ = unstructured.NestedBool(sm, "enabled")
			item.Retention, _ = sm["retentionPeriod"].(string)
			if item.Method == "" {
				item.Method = item.Name
			}
			v.Items = append(v.Items, item)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	return out, nil
}

// KBUpdateBackupSchedule 更新备份计划：只改 enabled/cron/retention（按项名对位），
// 其余字段（parameters 等）原样保留——直接整表覆盖会把 operator 填的参数抹掉。
func KBUpdateBackupSchedule(ctx context.Context, c *kube.Client, ns, cluster, component string, items []KBScheduleItem) error {
	name := cluster + "-" + component + "-backup-schedule"
	cli := c.Dynamic.Resource(kbSchedGVR).Namespace(ns)
	obj, err := cli.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取备份计划失败: %s", kbErrText(err))
	}
	cur, _, _ := unstructured.NestedSlice(obj.Object, "spec", "schedules")
	if len(cur) == 0 {
		return fmt.Errorf("组件 %s 没有可用的备份计划项", component)
	}
	byName := map[string]KBScheduleItem{}
	for _, it := range items {
		key := it.Method
		if key == "" {
			key = it.Name
		}
		byName[key] = it
	}
	updated := make([]any, 0, len(cur))
	for _, si := range cur {
		sm, ok := si.(map[string]any)
		if !ok {
			continue
		}
		key, _ := sm["backupMethod"].(string)
		if key == "" {
			key, _ = sm["name"].(string)
		}
		if it, hit := byName[key]; hit {
			sm["enabled"] = it.Enabled
			if it.Cron != "" {
				if !kbCronOK(it.Cron) {
					return fmt.Errorf("cron 表达式不合法（需 5 段，空格分隔）: %s", it.Cron)
				}
				sm["cronExpression"] = it.Cron
			}
			if it.Retention != "" {
				sm["retentionPeriod"] = it.Retention
			}
		}
		updated = append(updated, sm)
	}
	if err := unstructured.SetNestedSlice(obj.Object, updated, "spec", "schedules"); err != nil {
		return err
	}
	if _, err := cli.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("更新备份计划失败: %s", kbErrText(err))
	}
	return nil
}

// kbCronOK 宽松校验：5 或 6 段（带秒）非空表达式，拦手滑，精确校验交给 apiserver
func kbCronOK(s string) bool {
	n := len(strings.Fields(s))
	return n == 5 || n == 6
}

// ---------- Backup（手动备份的增删查）----------

type KBBackupCreateInput struct {
	Component string `json:"component"` // 按组件定位 BackupPolicy
	Method    string `json:"method"`    // 空 = 策略里的第一个方法
	Retention string `json:"retention,omitempty"`
	Policy    string `json:"policy,omitempty"` // 直接指定策略名（优先于 Component）
}

// KBCreateBackup 手动触发一次备份。仓库未配置时提前报错——不拦的话 operator
// 会把 Backup 卡在 Failed，用户要等半天才能在失败原因里看到同一句话。
func KBCreateBackup(ctx context.Context, c *kube.Client, ns, cluster string, in *KBBackupCreateInput) (string, error) {
	repos, err := KBListBackupRepos(ctx, c)
	if err != nil {
		return "", err
	}
	usable := 0
	for _, r := range repos {
		// KubeBlocks 1.0.x 的 BackupRepo 就绪态是 Ready（0.9 时代叫 Available）
		if r.Phase == "Available" || r.Phase == "Ready" || r.Phase == "" {
			usable++
		}
	}
	if len(repos) == 0 {
		return "", fmt.Errorf("集群没有配置 BackupRepo（备份仓库），无法备份；请先由管理员创建备份仓库")
	}
	if usable == 0 {
		return "", fmt.Errorf("全部 BackupRepo 都不可用（phase != Available），无法备份")
	}
	policies, err := KBListBackupPolicies(ctx, c, ns, cluster)
	if err != nil {
		return "", err
	}
	var policy *KBBackupPolicyView
	if in.Policy != "" {
		for i := range policies {
			if policies[i].Name == in.Policy {
				policy = &policies[i]
			}
		}
		if policy == nil {
			return "", fmt.Errorf("集群 %s 没有备份策略 %s", cluster, in.Policy)
		}
	} else {
		for i := range policies {
			if policies[i].Component == in.Component {
				policy = &policies[i]
			}
		}
		if policy == nil {
			return "", fmt.Errorf("组件 %s 没有备份策略（BackupPolicy）", in.Component)
		}
	}
	method := in.Method
	if method == "" && len(policy.Methods) > 0 {
		method = policy.Methods[0].Name
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kbBackupGVR.Group + "/" + kbBackupGVR.Version,
		"kind":       "Backup",
		"metadata": map[string]any{
			"generateName": "backup-" + cluster + "-",
			"namespace":    ns,
			"labels":       map[string]any{"app.kubernetes.io/managed-by": "kube-console"},
		},
		"spec": map[string]any{
			"backupPolicyName": policy.Name,
			"backupMethod":     method,
		},
	}}
	if in.Retention != "" {
		obj.Object["spec"].(map[string]any)["retentionPeriod"] = in.Retention
	}
	created, err := c.Dynamic.Resource(kbBackupGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("创建备份失败: %s", kbErrText(err))
	}
	return created.GetName(), nil
}

// KBListBackups 该集群的备份记录：按 BackupPolicy 归属（spec.backupPolicyName → 集群策略集合），
// 这条链对手工建的 Backup 也成立，比猜标签可靠。
func KBListBackups(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBBackupView, error) {
	policies, err := KBListBackupPolicies(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	mine := map[string]string{} // policyName → component
	for _, p := range policies {
		mine[p.Name] = p.Component
	}
	// 组件名兜底：策略列表取不到（无权限）时按备份命名约定 <cluster>-* 前缀收
	fallback := len(mine) == 0
	list, err := c.Dynamic.Resource(kbBackupGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrorsIsNotFoundOrNoMatch(err) {
			return []KBBackupView{}, nil
		}
		return nil, err
	}
	out := []KBBackupView{}
	for i := range list.Items {
		it := &list.Items[i]
		pn, _, _ := unstructured.NestedString(it.Object, "spec", "backupPolicyName")
		if _, ok := mine[pn]; !ok && !fallback && !kbBackupOwner(it.GetName(), cluster) {
			continue
		}
		out = append(out, backupView(it))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt+" "+out[i].Name > out[j].CreatedAt+" "+out[j].Name })
	return out, nil
}

func backupView(it *unstructured.Unstructured) KBBackupView {
	v := KBBackupView{
		Name:      it.GetName(),
		Namespace: it.GetNamespace(),
		Method:    str(it, "spec", "backupMethod"),
		Policy:    str(it, "spec", "backupPolicyName"),
		Retention: str(it, "spec", "retentionPeriod"),
		Phase:     str(it, "status", "phase"),
		FailureReason: str(it, "status", "failureReason"),
		Repo:      str(it, "status", "backupRepoName"),
		TotalSize: str(it, "status", "totalSize"),
		Expiration: str(it, "status", "expiration"),
		StartedAt: kbTime(str(it, "status", "startTimestamp")),
		CompletedAt: kbTime(str(it, "status", "completionTimestamp")),
	}
	if ts := it.GetCreationTimestamp(); !ts.IsZero() {
		v.CreatedAt = ts.Format("2006-01-02 15:04:05")
	}
	return v
}

// KBGetBackup 备份详情（含各 action 的执行明细）
func KBGetBackup(ctx context.Context, c *kube.Client, ns, name string) (*KBBackupDetail, error) {
	obj, err := c.Dynamic.Resource(kbBackupGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	d := &KBBackupDetail{KBBackupView: backupView(obj)}
	if acts, ok, _ := unstructured.NestedSlice(obj.Object, "status", "actions"); ok {
		for _, a := range acts {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			ai := KBBackupAction{}
			ai.Name, _ = am["name"].(string)
			ai.Phase, _ = am["phase"].(string)
			ai.TargetPod, _ = am["targetPodName"].(string)
			ai.TotalSize, _ = am["totalSize"].(string)
			ai.FailureReason, _ = am["failureReason"].(string)
			ai.StartAt = kbTime(str2(am["startTimestamp"]))
			ai.CompletedAt = kbTime(str2(am["completionTimestamp"]))
			d.Actions = append(d.Actions, ai)
		}
	}
	d.TimeRange, _, _ = unstructured.NestedMap(obj.Object, "status", "timeRange")
	if vs, ok, _ := unstructured.NestedSlice(obj.Object, "status", "volumeSnapshots"); ok {
		d.VolumeSnapshots = len(vs)
	}
	return d, nil
}

func str2(v any) string {
	s, _ := v.(string)
	return s
}

// KBDeleteBackup 删除备份记录（retentionPolicy=Retain 时只是删记录，底层对象存储由仓库策略管理）
func KBDeleteBackup(ctx context.Context, c *kube.Client, ns, name string) error {
	err := c.Dynamic.Resource(kbBackupGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && apierrors.IsNotFound(err) {
		return fmt.Errorf("备份不存在")
	}
	return err
}

// ---------- Restore ----------

type KBRestoreInput struct {
	// Name 新集群名（组件结构与源集群一致）
	Name string `json:"name"`
	// Component 用哪个组件的备份做恢复（备份按组件生成；多组件集群其余组件会是空数据）
	Component string `json:"component"`
	// BackupName 备份名；BackupNamespace 备份所在 ns（默认与目标同 ns）
	BackupName      string `json:"backupName"`
	BackupNamespace string `json:"backupNamespace,omitempty"`
	// VolumeRestorePolicy 恢复时卷策略：Parallel（并行）/ Sequential（串行），默认 Parallel
	VolumeRestorePolicy string `json:"volumeRestorePolicy,omitempty"`
	// TerminationPolicy 新集群回收策略，默认 Delete
	TerminationPolicy string `json:"terminationPolicy,omitempty"`
}

// KBRestoreToNewCluster 用备份拉起一个新集群：复制源 Cluster 的 spec（组件结构、资源、存储类
// 都保持），清掉身份类元数据后按 1.0 官方注解协议声明恢复来源，数据面由 operator 完成。
func KBRestoreToNewCluster(ctx context.Context, c *kube.Client, ns, srcCluster string, in *KBRestoreInput) (string, error) {
	if in.Name == "" || in.BackupName == "" || in.Component == "" {
		return "", fmt.Errorf("新集群名、组件与备份名都不能为空")
	}
	if in.Name == srcCluster {
		return "", fmt.Errorf("新集群名不能与源集群相同")
	}
	if !kbNameRe.MatchString(in.Name) {
		return "", fmt.Errorf("集群名不合法（须为 RFC 1123 小写名）: %s", in.Name)
	}
	// 备份存在性前置校验，避免注解下发后 operator 才发现并卡在启动阶段
	backupNS := in.BackupNamespace
	if backupNS == "" {
		backupNS = ns
	}
	if _, err := c.Dynamic.Resource(kbBackupGVR).Namespace(backupNS).Get(ctx, in.BackupName, metav1.GetOptions{}); err != nil {
		return "", fmt.Errorf("备份 %s/%s 不可用: %s", backupNS, in.BackupName, kbErrText(err))
	}
	src, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Get(ctx, srcCluster, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("读取源集群失败: %s", kbErrText(err))
	}
	// 校验组件归属（防串集群备份）
	comps, _, _ := unstructured.NestedSlice(src.Object, "spec", "componentSpecs")
	found := false
	for _, sp := range comps {
		if sm, ok := sp.(map[string]any); ok && sm["name"] == in.Component {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("源集群 %s 里没有组件 %s", srcCluster, in.Component)
	}
	spec, ok := src.Object["spec"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("源集群 spec 缺失，无法复制")
	}
	if tp := in.TerminationPolicy; tp != "" {
		spec["terminationPolicy"] = tp
	}
	vrp := in.VolumeRestorePolicy
	if vrp == "" {
		vrp = "Parallel"
	}
	restore := map[string]any{"name": in.BackupName, "namespace": backupNS, "volumeRestorePolicy": vrp}
	payload, err := json.Marshal(map[string]any{in.Component: restore})
	if err != nil {
		return "", err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kbClusterGVR.Group + "/" + kbClusterGVR.Version,
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      in.Name,
			"namespace": ns,
			"annotations": map[string]any{
				RestoreAnnotation: string(payload),
			},
		},
		"spec": spec,
	}}
	if _, err := c.Dynamic.Resource(kbClusterGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("集群 %s/%s 已存在", ns, in.Name)
		}
		return "", fmt.Errorf("创建恢复集群失败: %s", kbErrText(err))
	}
	return in.Name, nil
}

// KBListRestores 恢复任务记录（operator 执行恢复时创建，看进度用）。
// 归属判定：spec.backup.name 落在该集群备份集合内，或名字前缀匹配。
func KBListRestores(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBRestoreView, error) {
	backups, err := KBListBackups(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, b := range backups {
		mine[b.Name] = true
	}
	list, err := c.Dynamic.Resource(kbRestoreGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrorsIsNotFoundOrNoMatch(err) {
			return []KBRestoreView{}, nil
		}
		return nil, err
	}
	out := []KBRestoreView{}
	for i := range list.Items {
		it := &list.Items[i]
		bn := str(it, "spec", "backup", "name")
		if !mine[bn] && !strings.HasPrefix(it.GetName(), cluster+"-") {
			continue
		}
		v := KBRestoreView{
			Name:        it.GetName(),
			Backup:      bn,
			Phase:       str(it, "status", "phase"),
			Message:     str(it, "status", "message"),
			Duration:    str(it, "status", "duration"),
			CreatedAt:   kbTime(str(it, "status", "startTimestamp")),
			CompletedAt: kbTime(str(it, "status", "completionTimestamp")),
		}
		if ts := it.GetCreationTimestamp(); !ts.IsZero() {
			v.CreatedAt = ts.Format("2006-01-02 15:04:05")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt+" "+out[i].Name > out[j].CreatedAt+" "+out[j].Name })
	return out, nil
}

// apierrorsIsNotFoundOrNoMatch CRD 未安装（404/RESTMapping 缺失）统一按空列表降级
func apierrorsIsNotFoundOrNoMatch(err error) bool {
	return apierrors.IsNotFound(err) || metaNoMatch(err)
}
