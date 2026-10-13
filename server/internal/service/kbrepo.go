// kbrepo.go BackupRepo（集群级备份仓库）管理。表单字段不写死在控制台里：
// 存储参数（bucket / region / endpoint…）与凭证字段清单都长在 StorageProvider 的
// parametersSchema 上，按 provider 现取现渲染，addon 换 provider 不用改代码。
//
// 1.0.2 实测三点，都做成服务端前置校验（"仓库建得出来、备份必失败"是最难查的问题）：
//   - spec 只有 storageProviderRef 与 pvReclaimPolicy 必填，且 storageProviderRef 带 CEL
//     不可变校验，换 provider 只能删了重建；
//   - accessMethod=Mount 依赖该 provider 的 csiDriverName 已安装（本集群 s3 provider 因缺
//     ru.yandex.s3.csi 而 NotReady），只有 Tool 走 datasafed 不需要 CSI；
//   - 凭证不进 spec，走 spec.credential 指向的 Secret，键名由 provider 的
//     parametersSchema.credentialFields 规定（s3 是 accessKeyId / secretAccessKey）。
package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kube-console/server/internal/kube"
)

var kbProviderGVR = schema.GroupVersionResource{
	Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "storageproviders",
}

const (
	kbRepoMount = "Mount"
	kbRepoTool  = "Tool"
)

// ---------- StorageProvider（仓库表单的数据源）----------

// KBProviderParam provider 参数 schema 里的一项。Credential=true 表示它是凭证字段，
// 值要落在 Secret 里，不能进 spec.config。
type KBProviderParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required"`
	Credential  bool   `json:"credential"`
}

type KBProviderView struct {
	Name      string            `json:"name"`
	Phase     string            `json:"phase"`
	CSIDriver string            `json:"csiDriver,omitempty"`
	Reason    string            `json:"reason,omitempty"` // 非 Ready 的原因（status.conditions）
	Params    []KBProviderParam `json:"params"`
}

// KBStorageProviders 列出可用存储 provider 与其参数清单。未装 KubeBlocks 时返回空列表。
func KBStorageProviders(ctx context.Context, c *kube.Client) ([]KBProviderView, error) {
	list, err := c.Dynamic.Resource(kbProviderGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrorsIsNotFoundOrNoMatch(err) {
			return []KBProviderView{}, nil
		}
		return nil, err
	}
	out := make([]KBProviderView, 0, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		v := KBProviderView{Name: it.GetName()}
		v.CSIDriver, _, _ = unstructured.NestedString(it.Object, "spec", "csiDriverName")
		v.Phase, _, _ = unstructured.NestedString(it.Object, "status", "phase")
		for _, cd := range kbConditionsOf(it) {
			if cd.Status != "True" && cd.Reason+cd.Message != "" {
				v.Reason = strings.TrimSpace(cd.Reason + " " + cd.Message)
				break
			}
		}
		v.Params = kbProviderParams(it)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// kbProviderParams 把 parametersSchema.openAPIV3Schema 摊平成表单字段。
// required 里既含普通参数也含凭证字段（bucket/region/accessKeyId/secretAccessKey），
// 靠 credentialFields 区分归属。
func kbProviderParams(it *unstructured.Unstructured) []KBProviderParam {
	schemaObj, ok, _ := unstructured.NestedMap(it.Object,
		"spec", "parametersSchema", "openAPIV3Schema")
	if !ok {
		return nil
	}
	cred := map[string]bool{}
	if fields, fOK, _ := unstructured.NestedStringSlice(it.Object,
		"spec", "parametersSchema", "credentialFields"); fOK {
		for _, f := range fields {
			cred[f] = true
		}
	}
	required := map[string]bool{}
	if req, rOK, _ := unstructured.NestedStringSlice(schemaObj, "required"); rOK {
		for _, r := range req {
			required[r] = true
		}
	}
	props, pOK, _ := unstructured.NestedMap(schemaObj, "properties")
	if !pOK {
		return nil
	}
	out := make([]KBProviderParam, 0, len(props))
	for name, raw := range props {
		p, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		pp := KBProviderParam{Name: name, Required: required[name], Credential: cred[name]}
		pp.Type, _ = p["type"].(string)
		pp.Description, _ = p["description"].(string)
		if d, has := p["default"]; has {
			pp.Default = fmt.Sprint(d)
		}
		out = append(out, pp)
	}
	// 必填的在前，凭证字段沉底（表单上它们是"选 Secret"而不是输入框）
	sort.Slice(out, func(i, j int) bool {
		if out[i].Credential != out[j].Credential {
			return !out[i].Credential
		}
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ---------- 仓库视图 / 详情 ----------

type KBRepoDetail struct {
	KBRepoView
	PVReclaimPolicy string            `json:"pvReclaimPolicy"`
	VolumeCapacity  string            `json:"volumeCapacity,omitempty"`
	SecretName      string            `json:"secretName,omitempty"`
	SecretNamespace string            `json:"secretNamespace,omitempty"`
	Config          map[string]string `json:"config,omitempty"`
	Conditions      []KBCondition     `json:"conditions,omitempty"`
}

// kbRepoViewOf 仓库列表项（列表与详情共用，避免两处字段口径漂开）
func kbRepoViewOf(obj *unstructured.Unstructured) KBRepoView {
	v := KBRepoView{
		Name:         obj.GetName(),
		Phase:        kbNestedString(obj, "status", "phase"),
		Default:      kbNestedBool(obj, "status", "isDefault"),
		Provider:     kbNestedString(obj, "spec", "storageProviderRef"),
		AccessMethod: kbNestedString(obj, "spec", "accessMethod"),
		PathPrefix:   kbNestedString(obj, "spec", "pathPrefix"),
		StorageClass: kbNestedString(obj, "status", "generatedStorageClassName"),
	}
	if cfg, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "config"); ok {
		v.Config = cfg
	}
	if ts := obj.GetCreationTimestamp(); !ts.IsZero() {
		v.CreatedAt = ts.Format("2006-01-02 15:04:05")
	}
	return v
}

func kbRepoDetailOf(obj *unstructured.Unstructured) *KBRepoDetail {
	return &KBRepoDetail{
		KBRepoView:      kbRepoViewOf(obj),
		PVReclaimPolicy: kbNestedString(obj, "spec", "pvReclaimPolicy"),
		VolumeCapacity:  kbNestedString(obj, "spec", "volumeCapacity"),
		Conditions:      kbConditionsOf(obj),
	}
}

func kbNestedString(u *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedString(u.Object, fields...)
	return v
}

func kbNestedBool(u *unstructured.Unstructured, fields ...string) bool {
	v, _, _ := unstructured.NestedBool(u.Object, fields...)
	return v
}

// KBGetBackupRepo 单个仓库详情（编辑表单回填 + 状态条件）
func KBGetBackupRepo(ctx context.Context, c *kube.Client, name string) (*KBRepoDetail, error) {
	obj, err := c.Dynamic.Resource(kbRepoGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("备份仓库 %s 不存在", name)
		}
		return nil, err
	}
	d := kbRepoDetailOf(obj)
	if sec, ok, _ := unstructured.NestedMap(obj.Object, "spec", "credential"); ok {
		d.SecretName, _ = sec["name"].(string)
		d.SecretNamespace, _ = sec["namespace"].(string)
	}
	return d, nil
}

// ---------- 写入 ----------

type KBRepoInput struct {
	Name            string            `json:"name"`
	Provider        string            `json:"provider"`
	AccessMethod    string            `json:"accessMethod"`
	PVReclaimPolicy string            `json:"pvReclaimPolicy"`
	PathPrefix      string            `json:"pathPrefix"`
	VolumeCapacity  string            `json:"volumeCapacity"`
	SecretName      string            `json:"secretName"`
	SecretNamespace string            `json:"secretNamespace"`
	Config          map[string]string `json:"config"`
}

// KBCreateBackupRepo 建仓库（集群级资源，凭证只按引用写入、控制台不留存明文）
func KBCreateBackupRepo(ctx context.Context, c *kube.Client, in KBRepoInput) error {
	prov, err := kbLoadProvider(ctx, c, in.Provider)
	if err != nil {
		return err
	}
	if in, err = kbCheckRepo(ctx, c, in, prov); err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kbRepoGVR.Group + "/" + kbRepoGVR.Version,
		"kind":       "BackupRepo",
		"metadata":   map[string]any{"name": strings.TrimSpace(in.Name)},
		"spec":       kbRepoSpec(in),
	}}
	if _, err := c.Dynamic.Resource(kbRepoGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("备份仓库 %s 已存在", in.Name)
		}
		return err
	}
	return nil
}

// KBUpdateBackupRepo 改仓库。provider 不可改（CRD 的 CEL 校验），config 按 provider
// schema 全量覆盖、schema 外的手工键保留。
func KBUpdateBackupRepo(ctx context.Context, c *kube.Client, in KBRepoInput) error {
	obj, err := c.Dynamic.Resource(kbRepoGVR).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("备份仓库 %s 不存在", in.Name)
		}
		return err
	}
	// provider 先按已绑定的那个校验：它不可修改，表单里改成别的 provider
	// 只会撞到 CRD 的 CEL 校验，错误信息对用户毫无帮助
	if cur := kbNestedString(obj, "spec", "storageProviderRef"); in.Provider == "" || in.Provider != cur {
		return fmt.Errorf("备份仓库 %s 绑定的是 provider %s：storageProviderRef 不可修改，换存储类型请删了重建", in.Name, cur)
	}
	prov, err := kbLoadProvider(ctx, c, in.Provider)
	if err != nil {
		return err
	}
	if in, err = kbCheckRepo(ctx, c, in, prov); err != nil {
		return err
	}
	spec := kbRepoSpec(in)
	if old, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "config"); ok {
		merged := map[string]any{}
		for k, v := range old {
			merged[k] = v
		}
		for _, p := range prov.Params {
			if p.Credential {
				continue
			}
			delete(merged, p.Name) // 表单里的值是权威（清空就是取消设置）
		}
		if newCfg, isMap := spec["config"].(map[string]any); isMap {
			for k, v := range newCfg {
				merged[k] = v
			}
		}
		if len(merged) > 0 {
			spec["config"] = merged
		} else {
			delete(spec, "config")
		}
	}
	obj.Object["spec"] = spec
	if _, err := c.Dynamic.Resource(kbRepoGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

// KBDeleteBackupRepo 删仓库。正在被备份引用时由 apiserver/operator 拦截，错误原样回传。
func KBDeleteBackupRepo(ctx context.Context, c *kube.Client, name string) error {
	if !kbNameRe.MatchString(name) {
		return fmt.Errorf("仓库名不合法")
	}
	if err := c.Dynamic.Resource(kbRepoGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("备份仓库 %s 不存在", name)
		}
		return err
	}
	return nil
}

func kbRepoSpec(in KBRepoInput) map[string]any {
	spec := map[string]any{
		"storageProviderRef": in.Provider,
		"accessMethod":       in.AccessMethod,
		"pvReclaimPolicy":    in.PVReclaimPolicy,
	}
	if pp := strings.Trim(in.PathPrefix, " /"); pp != "" {
		spec["pathPrefix"] = pp
	}
	if vc := strings.TrimSpace(in.VolumeCapacity); vc != "" {
		spec["volumeCapacity"] = vc
	}
	if len(in.Config) > 0 {
		cfg := map[string]any{}
		for k, v := range in.Config {
			cfg[k] = v
		}
		spec["config"] = cfg
	}
	if in.SecretName != "" {
		cred := map[string]any{"name": in.SecretName}
		if ns := strings.TrimSpace(in.SecretNamespace); ns != "" {
			cred["namespace"] = ns
		}
		spec["credential"] = cred
	}
	return spec
}

func kbLoadProvider(ctx context.Context, c *kube.Client, name string) (*KBProviderView, error) {
	if !kbCompDefNameRe.MatchString(name) && !kbNameRe.MatchString(name) {
		return nil, fmt.Errorf("provider 名不合法: %s", name)
	}
	obj, err := c.Dynamic.Resource(kbProviderGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("StorageProvider %s 不存在，请从已安装的 provider 里选", name)
		}
		return nil, err
	}
	prov := KBProviderView{
		Name:      obj.GetName(),
		CSIDriver: kbNestedString(obj, "spec", "csiDriverName"),
		Phase:     kbNestedString(obj, "status", "phase"),
		Params:    kbProviderParams(obj),
	}
	for _, cd := range kbConditionsOf(obj) {
		if cd.Status != "True" {
			prov.Reason = strings.TrimSpace(cd.Reason + " " + cd.Message)
			break
		}
	}
	return &prov, nil
}

// kbCheckRepo 校验 + 凭证 Secret 齐键检查，返回补齐默认值后的输入
func kbCheckRepo(ctx context.Context, c *kube.Client, in KBRepoInput, prov *KBProviderView) (KBRepoInput, error) {
	in, credFields, err := kbApplyRepoInput(in, prov)
	if err != nil {
		return in, err
	}
	if len(credFields) == 0 {
		return in, nil
	}
	return in, kbCheckCredSecret(ctx, c, in.SecretNamespace, in.SecretName, credFields)
}

// kbApplyRepoInput 建/改仓库前的统一校验（缺哪个就说哪个，不留给 operator 静默失败）。
// 返回补齐默认值后的输入，落库用返回值而不是调用方那份。
// 凭证 Secret 是否齐键不在这里查（要看 apiserver），由 kbCheckCredSecret 在写入前单独做。
func kbApplyRepoInput(in KBRepoInput, prov *KBProviderView) (KBRepoInput, []string, error) {
	if !kbNameRe.MatchString(strings.TrimSpace(in.Name)) {
		return in, nil, fmt.Errorf("仓库名需是小写字母/数字/-（以字母或数字开头结尾）")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.AccessMethod == "" {
		in.AccessMethod = kbRepoMount // CRD 的 default 只对手工 kubectl 生效，这里也得按同一口径补上
	}
	switch in.AccessMethod {
	case kbRepoMount, kbRepoTool:
	default:
		return in, nil, fmt.Errorf("accessMethod 只能是 %s 或 %s", kbRepoMount, kbRepoTool)
	}
	switch in.PVReclaimPolicy {
	case "Delete", "Retain":
	default:
		return in, nil, fmt.Errorf("pvReclaimPolicy 必填（Delete = 删仓库时回收 PV，Retain = 保留）")
	}
	if in.AccessMethod == kbRepoMount {
		// Mount 要把远端对象存储挂成 PV，依赖 provider 的 CSI 驱动真的装着
		if prov.Phase != "Ready" {
			msg := "未就绪"
			if prov.Reason != "" {
				msg = prov.Reason
			}
			return in, nil, fmt.Errorf("provider %s %s，挂载方式（Mount）用不了；请改用 Tool 方式%s", prov.Name, msg, csiHint(prov))
		}
		if strings.TrimSpace(in.VolumeCapacity) == "" {
			return in, nil, fmt.Errorf("Mount 方式必须填 volumeCapacity（备份用 PVC 的容量）")
		}
		if _, err := resource.ParseQuantity(in.VolumeCapacity); err != nil {
			return in, nil, fmt.Errorf("volumeCapacity 不是合法容量: %s", in.VolumeCapacity)
		}
	}
	credFields := []string{}
	known := map[string]KBProviderParam{}
	for _, p := range prov.Params {
		if p.Credential {
			credFields = append(credFields, p.Name)
			continue
		}
		known[p.Name] = p
		if v := strings.TrimSpace(in.Config[p.Name]); v == "" && p.Required {
			return in, nil, fmt.Errorf("provider %s 的必填参数 %s 没有填%s", prov.Name, p.Name, paramHint(p))
		}
	}
	for k := range in.Config {
		if _, ok := known[k]; !ok {
			return in, nil, fmt.Errorf("参数 %s 不属于 provider %s（写错的键会被静默忽略，备份时才发现连不上）；可用参数：%s",
				k, prov.Name, strings.Join(kbParamNames(known), ", "))
		}
	}
	if len(credFields) == 0 {
		return in, nil, nil
	}
	if in.SecretName == "" || in.SecretNamespace == "" {
		return in, nil, fmt.Errorf("provider %s 需要凭证 Secret（键：%s）", prov.Name, strings.Join(credFields, " / "))
	}
	return in, credFields, nil
}

func csiHint(prov *KBProviderView) string {
	if prov.CSIDriver == "" {
		return ""
	}
	return fmt.Sprintf("（该 provider 要求 CSI 驱动 %s）", prov.CSIDriver)
}

func paramHint(p KBProviderParam) string {
	if p.Description == "" {
		return ""
	}
	return fmt.Sprintf("：%s", p.Description)
}

func kbParamNames(m map[string]KBProviderParam) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kbCheckCredSecret 凭证 Secret 必须存在且齐键。缺键的 Secret 建完仓库后 operator 只会
// 把 phase 卡在 NotReady，界面上看不出来是凭证问题，所以在这里一次说清。
func kbCheckCredSecret(ctx context.Context, c *kube.Client, ns, name string, fields []string) error {
	sec, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("凭证 Secret %s/%s 不存在", ns, name)
		}
		return err
	}
	missing := []string{}
	for _, f := range fields {
		if _, ok := sec.Data[f]; !ok {
			if _, ok2 := sec.StringData[f]; !ok2 {
				missing = append(missing, f)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("凭证 Secret %s/%s 缺少键 %s", ns, name, strings.Join(missing, " / "))
	}
	return nil
}

// KBRepoSecret 可作为某 provider 凭证的 Secret（只回名字，不回内容）
type KBRepoSecret struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// KBCredentialSecrets 列出命名空间里键齐全的凭证 Secret。
// 凭证是仓库能不能用的关键，与其让用户去猜 Secret 名，不如直接把可用的挑出来。
func KBCredentialSecrets(ctx context.Context, c *kube.Client, ns, provider string) ([]KBRepoSecret, error) {
	prov, err := kbLoadProvider(ctx, c, provider)
	if err != nil {
		return nil, err
	}
	fields := []string{}
	for _, p := range prov.Params {
		if p.Credential {
			fields = append(fields, p.Name)
		}
	}
	if len(fields) == 0 {
		return []KBRepoSecret{}, nil
	}
	list, err := c.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]KBRepoSecret, 0, len(list.Items))
	for i := range list.Items {
		sec := &list.Items[i]
		ok := true
		for _, f := range fields {
			if _, has := sec.Data[f]; !has {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, KBRepoSecret{Name: sec.Name, Namespace: sec.Namespace})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
