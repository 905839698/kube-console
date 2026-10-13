// loginject.go 日志采集的 webhook 注入模式（GitOps 兼容）。
//
// 模板模式（旧，构造器仍在 logsidecar.go 供两模式共用）把 fluent-bit Sidecar 写进
// 工作负载模板——与 GitOps 正面冲突：ArgoCD 视为漂移，selfHeal/下一次 sync 直接
// 回滚掉采集。注入模式把策略存库（LogCollectionPolicy），Pod 创建那一刻由准入
// webhook 注入，工作负载模板零改动：ArgoCD 永远 Synced，自愈与 sync 都不会碰它。
//
// 准入通道（HTTPS 端口、证书、路径里的集群 token）复用 nacos 注入的基础设施；
// MWC 独立（kube-console-log-inject），生命周期与 nacos 接入互不牵连。
// 回调方式（service/url）沿用该集群 Nacos 接入里配置的 Webhook 通道——那是
// "apiserver 怎么回调控制台"的集群级配置，两个功能语义相同，不重复配置。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gorm.io/gorm"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

const (
	// LogInjectMWCName 日志采集注入的 MutatingWebhookConfiguration
	LogInjectMWCName = "kube-console-log-inject"
	// LogInjectNSLabel 命名空间打标：MWC 的 namespaceSelector 据此收窄触发范围
	LogInjectNSLabel = "kube-console.io/log-collection"
	// logInjectReconcileInterval MWC 周期维护：证书轮换后回填 caBundle、回调配置变更后跟进
	logInjectReconcileInterval = 5 * time.Minute
)

// LogInjectService 日志采集注入：策略存库 + Pod 准入注入
type LogInjectService struct {
	db       *gorm.DB
	clusters *ClusterManager
	nacos    *NacosService // webhook 证书（caBundle）与 HTTPS 端口的提供方
	k8s      *K8sService   // RestartWorkload：开关采集后滚动重启存量 Pod
}

func NewLogInjectService(db *gorm.DB, clusters *ClusterManager, nacos *NacosService, k8s *K8sService) *LogInjectService {
	return &LogInjectService{db: db, clusters: clusters, nacos: nacos, k8s: k8s}
}

// Start 周期维护 MWC：策略所在集群的 caBundle/回调配置变更后自动跟进（首轮立即）
func (s *LogInjectService) Start() {
	go func() {
		s.reconcileAll()
		ticker := time.NewTicker(logInjectReconcileInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.reconcileAll()
		}
	}()
}

// reconcileAll 对每个有采集策略的集群重推一次 MWC（幂等）
func (s *LogInjectService) reconcileAll() {
	var clusters []string
	if err := s.db.Model(&model.LogCollectionPolicy{}).Distinct().Pluck("cluster_name", &clusters).Error; err != nil {
		return
	}
	for _, cluster := range clusters {
		client, err := s.clusters.Client(cluster)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := s.ensureLogInjectConfig(ctx, client, cluster); err != nil {
			fmt.Printf("log-inject: 维护集群 %s 的 MutatingWebhookConfiguration 失败: %v\n", cluster, err)
		}
		cancel()
	}
}

// Enable 开启/更新采集：策略入库 + 配置 CM/凭据 Secret + 命名空间打标 + MWC 维护 + 滚动重启。
// 幂等：重复保存即更新策略与配置。
func (s *LogInjectService) Enable(ctx context.Context, client *kube.Client, clusterName, kind, ns, name string, in *LogCollectionInput, src *model.LogSource) error {
	if in.Container == "" || in.LogPath == "" {
		return fmt.Errorf("目标容器与日志路径不能为空")
	}
	if in.Format == "" {
		in.Format = "text"
	}
	if in.Format != "text" && in.Format != "json" {
		return fmt.Errorf("日志格式仅支持 text / json")
	}
	if in.Image == "" {
		in.Image = defaultFluentImage
	}
	if in.Container == LogSidecarName {
		return fmt.Errorf("目标容器不能叫 %s", LogSidecarName)
	}
	dir, err := logMountDir(in.LogPath)
	if err != nil {
		return err
	}
	if _, _, _, _, err = esCollectTarget(src, ns, in.Format); err != nil {
		return err
	}
	// 回调通道是注入的前提，放最前面校验：它失败时后面的策略/CM/标签都不该落下去
	if _, err = s.deliveryFor(ctx, clusterName); err != nil {
		return err
	}

	// 目标容器必须存在；共享目录不能和应用现有挂载打架（与模板模式同一口径，提前到开启时校验）
	tpl, ownerKind, uid, update, err := logCollectionOwner(ctx, client, kind, ns, name)
	if err != nil {
		return err
	}
	spec := &tpl.Spec
	appIdx := -1
	for i, ct := range spec.Containers {
		if ct.Name == in.Container {
			appIdx = i
		}
	}
	if appIdx < 0 {
		return fmt.Errorf("容器 %s 不存在于该工作负载", in.Container)
	}
	for _, m := range spec.Containers[appIdx].VolumeMounts {
		if m.MountPath == dir && m.Name != LogShareVolumeName {
			return fmt.Errorf("目录 %s 已被卷 %s 挂载，无法再共享给采集 Sidecar；请把日志写到未挂载卷的目录或调整日志路径", dir, m.Name)
		}
	}

	// 策略入库（webhook 按此注入）
	var policy model.LogCollectionPolicy
	err = s.db.Where("cluster_name = ? AND namespace = ? AND workload = ?", clusterName, ns, name).First(&policy).Error
	policy.ClusterName, policy.Namespace, policy.Workload = clusterName, ns, name
	policy.Container, policy.LogPath, policy.Format, policy.Image, policy.ESUser = in.Container, in.LogPath, in.Format, in.Image, src.Username
	if err == nil {
		err = s.db.Save(&policy).Error
	} else {
		err = s.db.Create(&policy).Error
	}
	if err != nil {
		return fmt.Errorf("保存采集策略失败: %w", err)
	}

	// fluent-bit 配置 CM（工作负载专属）+ 命名空间共享的 ES 凭据 Secret
	cm, err := buildFluentBitCM(in, ns, name, src)
	if err != nil {
		return err
	}
	cm.Name, cm.Namespace = name+"-fluent-bit", ns
	cm.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: ownerKind, Name: name, UID: uid}}
	cm.Labels = map[string]string{"app.kubernetes.io/managed-by": "kube-console"}
	cmCli := client.Clientset.CoreV1().ConfigMaps(ns)
	if _, err = cmCli.Get(ctx, cm.Name, metav1.GetOptions{}); err == nil {
		_, err = cmCli.Update(ctx, cm, metav1.UpdateOptions{})
	} else if apierrors.IsNotFound(err) {
		_, err = cmCli.Create(ctx, cm, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("写入 fluent-bit 配置失败: %w", err)
	}
	secCli := client.Clientset.CoreV1().Secrets(ns)
	if sec := buildLogESSecret(src, ns); sec != nil {
		if _, err = secCli.Get(ctx, LogESSecretName, metav1.GetOptions{}); err == nil {
			_, err = secCli.Update(ctx, sec, metav1.UpdateOptions{})
		} else if apierrors.IsNotFound(err) {
			_, err = secCli.Create(ctx, sec, metav1.CreateOptions{})
		}
		if err != nil {
			return fmt.Errorf("写入 ES 凭证 Secret 失败: %w", err)
		}
	} else {
		_ = secCli.Delete(ctx, LogESSecretName, metav1.DeleteOptions{})
	}

	// 旧模板模式的 Sidecar 摘掉（迁移到注入模式：模板回到 git 原样），随本次重启生效
	if hasLogSidecar(spec.Containers) {
		stripLogSidecarFromSpec(spec)
		delete(tpl.Annotations, logCollectionAnn)
		if err := update(ctx); err != nil {
			return fmt.Errorf("摘除模板内旧 Sidecar 失败: %w", err)
		}
	}

	if err := s.labelNamespace(ctx, client, ns, true); err != nil {
		return err
	}
	if err := s.ensureLogInjectConfig(ctx, client, clusterName); err != nil {
		return err
	}
	// 存量 Pod 靠滚动重启才有机会过一遍 webhook（新 Pod 注入 Sidecar）
	return s.k8s.RestartWorkload(ctx, client, kind, ns, name)
}

// Get 采集状态：策略优先；无策略时回退读模板注解（旧模板模式遗留）
func (s *LogInjectService) Get(ctx context.Context, client *kube.Client, clusterName, kind, ns, name string) (*LogCollectionStatus, error) {
	out := &LogCollectionStatus{}
	var policy model.LogCollectionPolicy
	if err := s.db.Where("cluster_name = ? AND namespace = ? AND workload = ?", clusterName, ns, name).
		First(&policy).Error; err == nil {
		out.Enabled = true
		out.Config = &LogCollectionInput{Container: policy.Container, LogPath: policy.LogPath, Format: policy.Format, Image: policy.Image}
		if out.Config.Image == "" {
			out.Config.Image = defaultFluentImage
		}
		return out, nil
	}
	tpl, _, _, _, err := logCollectionOwner(ctx, client, kind, ns, name)
	if err != nil {
		return nil, err
	}
	if hasLogSidecar(tpl.Spec.Containers) {
		out.Enabled = true
	}
	if raw := tpl.Annotations[logCollectionAnn]; raw != "" {
		var in LogCollectionInput
		if json.Unmarshal([]byte(raw), &in) == nil {
			out.Config = &in
		}
	}
	return out, nil
}

// Remove 关闭采集：删策略 + 摘模板遗留 Sidecar + 回收配置资源 + 命名空间/集群级清理。
// 存量 Pod 上的 Sidecar 要等滚动重启才会消失，所以最后照例重启一次。
func (s *LogInjectService) Remove(ctx context.Context, client *kube.Client, clusterName, kind, ns, name string) error {
	s.db.Where("cluster_name = ? AND namespace = ? AND workload = ?", clusterName, ns, name).
		Delete(&model.LogCollectionPolicy{})

	tpl, _, _, update, err := logCollectionOwner(ctx, client, kind, ns, name)
	if err != nil {
		return err
	}
	if hasLogSidecar(tpl.Spec.Containers) {
		stripLogSidecarFromSpec(&tpl.Spec)
		delete(tpl.Annotations, logCollectionAnn)
		if err = update(ctx); err != nil {
			return err
		}
	}
	_ = client.Clientset.CoreV1().ConfigMaps(ns).Delete(ctx, name+"-fluent-bit", metav1.DeleteOptions{})
	_ = client.Clientset.CoreV1().Secrets(ns).Delete(ctx, name+LegacyESSecretSuffix, metav1.DeleteOptions{})

	// 共享凭据 Secret 只在该 ns 再无采集者（模板 Sidecar 或策略）时回收
	var nsPolicies int64
	s.db.Model(&model.LogCollectionPolicy{}).Where("cluster_name = ? AND namespace = ?", clusterName, ns).Count(&nsPolicies)
	if left, e := collectorsLeftIn(ctx, client, ns); e == nil && !left && nsPolicies == 0 {
		_ = client.Clientset.CoreV1().Secrets(ns).Delete(ctx, LogESSecretName, metav1.DeleteOptions{})
	}
	if err := s.labelNamespace(ctx, client, ns, nsPolicies > 0); err != nil {
		return err
	}
	// 集群最后一个策略关掉时回收 MWC
	var clusterPolicies int64
	s.db.Model(&model.LogCollectionPolicy{}).Where("cluster_name = ?", clusterName).Count(&clusterPolicies)
	if clusterPolicies == 0 {
		_ = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().
			Delete(ctx, LogInjectMWCName, metav1.DeleteOptions{})
	}
	return s.k8s.RestartWorkload(ctx, client, kind, ns, name)
}

// labelNamespace 打/摘命名空间标签（MWC namespaceSelector 用）
func (s *LogInjectService) labelNamespace(ctx context.Context, client *kube.Client, ns string, on bool) error {
	obj, err := client.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if obj.Labels == nil {
		obj.Labels = map[string]string{}
	}
	if on {
		obj.Labels[LogInjectNSLabel] = "enabled"
	} else {
		delete(obj.Labels, LogInjectNSLabel)
	}
	_, err = client.Clientset.CoreV1().Namespaces().Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// ---------- MutatingWebhookConfiguration 维护 ----------

// logDelivery apiserver 回调控制台的方式（复用 Nacos 接入里的集群级配置）
type logDelivery struct {
	mode    string // service | url
	url     string
	svcNS   string
	svcName string
	port    int32
}

func (s *LogInjectService) deliveryFor(ctx context.Context, clusterName string) (*logDelivery, error) {
	var cfg model.NacosConfig
	if err := s.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("该集群尚未配置 Webhook 回调通道：请先在「Nacos 接入」保存一次连接配置（apiserver 回调方式为集群级配置，日志采集注入与 Pod 注入共用）")
	}
	if strings.EqualFold(cfg.WebhookMode, "service") {
		port := int32(cfg.WebhookServicePort)
		if port == 0 {
			port = 9443
		}
		return &logDelivery{mode: "service", svcNS: cfg.WebhookServiceNS, svcName: cfg.WebhookServiceName, port: port}, nil
	}
	u := strings.TrimSuffix(cfg.WebhookURL, "/")
	if !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("Nacos 接入的 Webhook URL 必须以 https:// 开头（当前 %q）", cfg.WebhookURL)
	}
	return &logDelivery{mode: "url", url: u}, nil
}

// ensureLogInjectConfig 维护日志采集注入的 MWC（caBundle 自动回填；幂等）
func (s *LogInjectService) ensureLogInjectConfig(ctx context.Context, client *kube.Client, clusterName string) error {
	caBundle, err := s.nacos.WebhookCABundle()
	if err != nil {
		return err
	}
	token, err := webhookClusterToken(s.db, clusterName)
	if err != nil {
		return err
	}
	path := "/log-inject/" + token
	ignore := admissionregistrationv1.Ignore
	none := admissionregistrationv1.SideEffectClassNone
	scopeAll := admissionregistrationv1.AllScopes
	var timeout int32 = 5
	webhook := admissionregistrationv1.MutatingWebhook{
		Name: "log-sidecar.kube-console.io",
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
			Rule: admissionregistrationv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}, Scope: &scopeAll,
			},
		}},
		FailurePolicy:           &ignore,
		SideEffects:             &none,
		AdmissionReviewVersions: []string{"v1"},
		TimeoutSeconds:          &timeout,
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{LogInjectNSLabel: "enabled"},
		},
	}
	webhook.ClientConfig.CABundle = caBundle
	d, err := s.deliveryFor(ctx, clusterName)
	if err != nil {
		return err
	}
	if d.mode == "service" {
		webhook.ClientConfig.Service = &admissionregistrationv1.ServiceReference{
			Namespace: d.svcNS, Name: d.svcName, Path: &path, Port: &d.port,
		}
	} else {
		full := d.url + path
		webhook.ClientConfig.URL = &full
	}
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: LogInjectMWCName},
		Webhooks:   []admissionregistrationv1.MutatingWebhook{webhook},
	}
	existing, err := client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, LogInjectMWCName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, mwc, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Webhooks = mwc.Webhooks
	_, err = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ---------- Admission 处理器 ----------

// HandleInject 日志采集 Sidecar 注入（/log-inject/{cluster}）
func (s *LogInjectService) HandleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cluster := resolveClusterToken(s.db, strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/log-inject"), "/"))
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	review := &admissionv1.AdmissionReview{}
	if err := json.Unmarshal(body, review); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	out, _ := json.Marshal(s.mutatePod(cluster, review))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// mutatePod 按策略给 Pod 注入采集 Sidecar（幂等；任何不确定都放行不注入——
// failurePolicy=Ignore 只兜得住 webhook 自身故障，注入出非法对象会被 apiserver 整单拒绝）
func (s *LogInjectService) mutatePod(cluster string, review *admissionv1.AdmissionReview) *admissionv1.AdmissionReview {
	resp := &admissionv1.AdmissionReview{
		TypeMeta: review.TypeMeta,
		Response: &admissionv1.AdmissionResponse{Result: &metav1.Status{}},
	}
	resp.Response.Allowed = true
	if review.Request == nil || review.Request.Resource.Resource != "pods" || cluster == "" {
		return resp
	}
	resp.Response.UID = review.Request.UID

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
		return resp
	}
	ns := review.Request.Namespace
	if ns == "" {
		ns = pod.Namespace
	}
	if ns == "" {
		return resp
	}
	workload := s.workloadNameForPod(context.Background(), cluster, ns, &pod)
	if workload == "" {
		return resp
	}
	var policy model.LogCollectionPolicy
	if err := s.db.Where("cluster_name = ? AND namespace = ? AND workload = ?", cluster, ns, workload).
		First(&policy).Error; err != nil {
		return resp
	}
	in := &LogCollectionInput{Container: policy.Container, LogPath: policy.LogPath, Format: policy.Format, Image: policy.Image}
	if in.Image == "" {
		in.Image = defaultFluentImage
	}
	dir, err := logMountDir(in.LogPath)
	if err != nil {
		return resp
	}

	// 已有 Sidecar（模板模式遗留）或目标容器缺失：不动
	appIdx := -1
	for i, ct := range pod.Spec.Containers {
		if ct.Name == LogSidecarName {
			return resp
		}
		if ct.Name == in.Container {
			appIdx = i
		}
	}
	if appIdx < 0 {
		return resp
	}
	// 目标容器在别的卷上挂了同一目录：共享不了，注入了也是坏 Pod，放弃
	for _, m := range pod.Spec.Containers[appIdx].VolumeMounts {
		if m.MountPath == dir && m.Name != LogShareVolumeName {
			return resp
		}
	}

	sidecar := buildLogSidecar(in, dir, policy.ESUser)
	confVolume := corev1.Volume{Name: LogConfVolumeName, VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: workload + "-fluent-bit"}}}}

	// JSON Patch 的 /- 追加要求字段已存在：volumes 为空时整体 add，之后再补第二个卷
	volExists := len(pod.Spec.Volumes) > 0
	var ops []map[string]interface{}
	appendOp := func(path string, value interface{}) {
		ops = append(ops, map[string]interface{}{"op": "add", "path": path, "value": value})
	}
	appendOp("/spec/containers/-", sidecar)
	if volExists {
		appendOp("/spec/volumes/-", corev1.Volume{Name: LogShareVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		appendOp("/spec/volumes/-", confVolume)
	} else {
		appendOp("/spec/volumes", []corev1.Volume{
			{Name: LogShareVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			confVolume,
		})
	}
	mountPath := fmt.Sprintf("/spec/containers/%d/volumeMounts", appIdx)
	if len(pod.Spec.Containers[appIdx].VolumeMounts) > 0 {
		mountPath += "/-"
	}
	appendOp(mountPath, corev1.VolumeMount{Name: LogShareVolumeName, MountPath: dir})

	patch, err := json.Marshal(ops)
	if err != nil {
		return resp
	}
	pt := admissionv1.PatchTypeJSONPatch
	resp.Response.PatchType = &pt
	resp.Response.Patch = patch
	return resp
}

// workloadNameForPod 从 Pod 反查工作负载名：StatefulSet/DaemonSet 的 Pod 挂在
// 工作负载上（不需要集群客户端）；Deployment 的 Pod 挂在 ReplicaSet 上，
// 要再往上查一层。查不到（裸 Pod / 链路断裂）返回空串 → 不注入。
func (s *LogInjectService) workloadNameForPod(ctx context.Context, cluster, ns string, pod *corev1.Pod) string {
	var client *kube.Client
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "DaemonSet", "StatefulSet":
			return owner.Name
		case "ReplicaSet":
			if client == nil {
				var err error
				if client, err = s.clusters.Client(cluster); err != nil {
					return ""
				}
			}
			getCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			rs, err := client.Clientset.AppsV1().ReplicaSets(ns).Get(getCtx, owner.Name, metav1.GetOptions{})
			cancel()
			if err != nil {
				return ""
			}
			for _, up := range rs.OwnerReferences {
				if up.Kind == "Deployment" {
					return up.Name
				}
			}
			return ""
		}
	}
	return ""
}
