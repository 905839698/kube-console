// 容器内文件日志采集：向工作负载 Pod 注入 Fluent Bit Sidecar + 共享 emptyDir 卷，
// tail 指定日志文件后写入 Elasticsearch（复用「日志源配置」的连接信息），
// 采集文档沿用 fluentd 字段约定（kubernetes.namespace_name / pod_name / container_name），供日志检索直接查询。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

const (
	// LogSidecarName Sidecar 容器名（同时作为"已开启采集"的判定标记）
	LogSidecarName = "fluent-bit"
	// LogShareVolumeName 应用容器与 Sidecar 共享的 emptyDir 卷名
	LogShareVolumeName = "kc-log-share"
	// LogConfVolumeName 挂载 fluent-bit 配置的卷名
	LogConfVolumeName  = "kc-fluent-bit-conf"
	logConfMountPath   = "/fluent-bit/etc-console"
	logCollectionAnn   = "kube-console.io/log-collection"
	defaultFluentImage = "fluent/fluent-bit:3.2"
	// LogCollectionField / LogFormatField 采集文档标记字段：
	// 检索侧据此把 Sidecar 日志与 fluentd 标准输出日志分开
	LogCollectionField = "kube-console.log-collection"
	LogFormatField     = "kube-console.log-format"
	// DefaultIndexPrefix 日志源未配置前缀时的兜底
	DefaultIndexPrefix = "logstash-"
)

// LogCollectionInput 采集配置表单
type LogCollectionInput struct {
	Container string `json:"container"` // 目标应用容器名
	LogPath   string `json:"logPath"`   // 容器内日志文件路径，支持 glob（/var/log/app/*.log）
	Format    string `json:"format"`    // text | json
	Image     string `json:"image"`     // fluent-bit 镜像（空用默认）
}

// LogCollectionStatus GET 返回：是否已开启 + 当前配置
type LogCollectionStatus struct {
	Enabled bool                `json:"enabled"`
	Config  *LogCollectionInput `json:"config,omitempty"`
}

// logCollectionOwner 取工作负载 PodTemplate 及回写函数（仅支持多容器常驻负载）
func logCollectionOwner(ctx context.Context, c *kube.Client, kind, ns, name string) (
	tpl *corev1.PodTemplateSpec, ownerKind string, uid types.UID, update func(context.Context) error, err error) {
	switch kind {
	case "deployments":
		obj, e := c.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return nil, "", "", nil, e
		}
		return &obj.Spec.Template, "Deployment", obj.UID, func(ctx context.Context) error {
			_, e := c.Clientset.AppsV1().Deployments(ns).Update(ctx, obj, metav1.UpdateOptions{})
			return e
		}, nil
	case "statefulsets":
		obj, e := c.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return nil, "", "", nil, e
		}
		return &obj.Spec.Template, "StatefulSet", obj.UID, func(ctx context.Context) error {
			_, e := c.Clientset.AppsV1().StatefulSets(ns).Update(ctx, obj, metav1.UpdateOptions{})
			return e
		}, nil
	case "daemonsets":
		obj, e := c.Clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return nil, "", "", nil, e
		}
		return &obj.Spec.Template, "DaemonSet", obj.UID, func(ctx context.Context) error {
			_, e := c.Clientset.AppsV1().DaemonSets(ns).Update(ctx, obj, metav1.UpdateOptions{})
			return e
		}, nil
	default:
		return nil, "", "", nil, fmt.Errorf("仅 Deployment/StatefulSet/DaemonSet 支持日志采集")
	}
}

// logMountDir 由日志路径推导需共享的目录：glob 取通配符之前的目录，普通文件取其父目录。
// 容器内路径是 POSIX 语义，不能用 filepath（Windows 上分隔符不同）
func logMountDir(logPath string) (string, error) {
	bad := fmt.Errorf("无法从日志路径推导挂载目录（如 /var/log/app/*.log 挂载 /var/log/app）")
	if !strings.HasPrefix(logPath, "/") {
		return "", fmt.Errorf("日志路径必须为容器内绝对路径")
	}
	var dir string
	if i := strings.Index(logPath, "*"); i >= 0 {
		prefix := logPath[:i] // 通配符前的部分
		if strings.HasSuffix(prefix, "/") {
			dir = prefix // /a/b/*.log → /a/b
		} else {
			dir = prefix[:strings.LastIndex(prefix, "/")+1] // /a/b/fi*.log → /a/b/
		}
	} else {
		p := strings.TrimRight(logPath, "/") // 普通文件取其父目录
		dir = p[:strings.LastIndex(p, "/")+1]
	}
	dir = strings.TrimRight(dir, "/")
	if dir == "" || dir == "/" {
		return "", bad
	}
	return dir, nil
}

// 两条索引：行日志（标准输出 + Sidecar 单行文本）走 IndexPrefix，
// JSON 结构化采集走 JsonIndexPrefix，避免任意 JSON 字段污染行日志索引的 mapping
func lineIndexPrefix(src *model.LogSource) string {
	if src.IndexPrefix != "" {
		return src.IndexPrefix
	}
	return DefaultIndexPrefix
}

func jsonIndexPrefix(src *model.LogSource) string {
	if src.JsonIndexPrefix != "" {
		return src.JsonIndexPrefix
	}
	return DefaultIndexPrefix
}

// collectIndex 采集写入的索引名：前缀含 {namespace} 则展开，否则拼 -<ns>
func collectIndex(prefix, ns string) string {
	base := strings.TrimRight(prefix, "-*")
	idx := base
	if strings.Contains(base, "{namespace}") {
		idx = strings.ReplaceAll(base, "{namespace}", ns)
	} else {
		idx = base + "-" + ns
	}
	idx = strings.ToLower(strings.TrimRight(idx, "-"))
	if idx == "" {
		idx = strings.TrimRight(DefaultIndexPrefix, "-*")
	}
	return idx
}

// esCollectTarget 从集群日志源推导 fluent-bit 输出地址与写入索引。
// 写入索引须能被检索侧对应前缀的通配模式命中
func esCollectTarget(src *model.LogSource, ns, format string) (host string, port int, tlsOn bool, index string, err error) {
	prefix := lineIndexPrefix(src)
	if format == "json" {
		prefix = jsonIndexPrefix(src)
	}
	index = collectIndex(prefix, ns)
	if src.DirectURL != "" {
		u, e := url.Parse(src.DirectURL)
		if e != nil || u.Host == "" {
			return "", 0, false, "", fmt.Errorf("日志源直连地址不合法: %s", src.DirectURL)
		}
		host = u.Hostname()
		port = 80
		if u.Scheme == "https" {
			port, tlsOn = 443, true
		}
		if p := u.Port(); p != "" {
			fmt.Sscanf(p, "%d", &port)
		}
		return host, port, tlsOn, index, nil
	}
	// Sidecar 在集群内：走 ES Service DNS（与检索侧"集群内直连"路径一致）
	host = fmt.Sprintf("%s.%s.svc.cluster.local", src.Service, src.Namespace)
	port = src.Port
	if port == 0 {
		port = 9200
	}
	return host, port, false, index, nil
}

func fluentBitConf(in *LogCollectionInput, ns, workload, index, host string, port int, tlsOn, hasAuth bool) string {
	parser := ""
	// json 不用 INPUT Parser（会丢掉原始行，检索侧只认 log/message 就成空行）；
	// 改用 filter-parser 把 log 拆成字段并保留原文
	if in.Format == "json" {
		parser = `
[FILTER]
    Name            parser
    Match           kc.**
    Key_Name        log
    Parser          json-parser
    Preserve_Key    On
    Reserve_Data    On
`
	}
	auth := ""
	if hasAuth {
		// fluent-bit 只插值 ${VAR} 形式，写成 $VAR 会把字面量当用户名发给 ES（401）
		auth = "    HTTP_User         ${KC_ES_USER}\n    HTTP_Passwd       ${KC_ES_PASS}\n"
	}
	tls := "Off"
	if tlsOn {
		tls = "On"
	}
	return fmt.Sprintf(`# kube-console 自动生成：容器内文件日志 -> Elasticsearch（请勿手工编辑，重新保存会覆盖）
[SERVICE]
    Flush               2
    Daemon              Off
    Log_Level           info
    Parsers_File        /fluent-bit/etc-console/parsers.conf

[INPUT]
    Name                tail
    Path                %s
    Tag                 kc.%s.%s
    Refresh_Interval    5
    Skip_Long_Lines     On
    DB                  /tmp/kc-fluent-bit-registry.db
%s
[FILTER]
    Name    modify
    Match   kc.**
    Add     kubernetes.namespace_name %s
    Add     kubernetes.pod_name ${HOSTNAME}
    Add     kubernetes.container_name %s
    Add     %s %s
    Add     %s %s

[OUTPUT]
    Name              es
    Match             *
    Host              %s
    Port              %d
    Index             %s
    Type              _doc
    TLS               %s
    TLS.verify        Off
    Replace_Dots      Off
%s`, in.LogPath, ns, workload, parser,
		ns, in.Container, LogCollectionField, workload, LogFormatField, in.Format,
		host, port, index, tls, auth)
}

func ensureVolume(spec *corev1.PodSpec, name string, src corev1.VolumeSource) {
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == name {
			spec.Volumes[i].VolumeSource = src
			return
		}
	}
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: name, VolumeSource: src})
}

// EnableLogCollection 注入/更新 fluent-bit Sidecar（幂等，覆盖旧配置），触发 Pod 模板滚动更新
func (s *K8sService) EnableLogCollection(ctx context.Context, c *kube.Client, kind, ns, name string, in *LogCollectionInput, src *model.LogSource) error {
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
	host, port, tlsOn, index, err := esCollectTarget(src, ns, in.Format)
	if err != nil {
		return err
	}

	tpl, ownerKind, uid, update, err := logCollectionOwner(ctx, c, kind, ns, name)
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
	app := &spec.Containers[appIdx]
	for _, m := range app.VolumeMounts {
		if m.MountPath == dir && m.Name != LogShareVolumeName {
			return fmt.Errorf("目录 %s 已被卷 %s 挂载，无法再共享给采集 Sidecar；请把日志写到未挂载卷的目录或调整日志路径", dir, m.Name)
		}
	}
	app.VolumeMounts = append(stripMounts(app.VolumeMounts), corev1.VolumeMount{Name: LogShareVolumeName, MountPath: dir})
	ensureVolume(spec, LogShareVolumeName, corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}})

	cmName := name + "-fluent-bit"
	secretName := name + "-fluent-bit-es"
	ensureVolume(spec, LogConfVolumeName, corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}},
	})

	meta, _ := json.Marshal(in)
	if tpl.Annotations == nil {
		tpl.Annotations = map[string]string{}
	}
	tpl.Annotations[logCollectionAnn] = string(meta)

	ownerRefs := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: ownerKind, Name: name, UID: uid}}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns, OwnerReferences: ownerRefs,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "kube-console"}},
		Data: map[string]string{"fluent-bit.conf": fluentBitConf(in, ns, name, index, host, port, tlsOn, src.Username != "")},
	}
	// Parsers_File 声明了就必须存在，text 格式也带上（json-parser 闲置无害）
	cm.Data["parsers.conf"] = "[PARSER]\n    Name      json-parser\n    Format    json\n"
	cmCli := c.Clientset.CoreV1().ConfigMaps(ns)
	if _, err = cmCli.Get(ctx, cmName, metav1.GetOptions{}); err == nil {
		_, err = cmCli.Update(ctx, cm, metav1.UpdateOptions{})
	} else if apierrors.IsNotFound(err) {
		_, err = cmCli.Create(ctx, cm, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("写入 fluent-bit 配置失败: %w", err)
	}

	// ES 密码只落 Secret 并经 env 注入（配置里 ${KC_ES_PASS} 插值）；无账号时清理旧 Secret
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, OwnerReferences: ownerRefs},
		StringData: map[string]string{"pass": src.Password},
	}
	secCli := c.Clientset.CoreV1().Secrets(ns)
	if src.Username == "" {
		_ = secCli.Delete(ctx, secretName, metav1.DeleteOptions{})
	} else {
		if _, err = secCli.Get(ctx, secretName, metav1.GetOptions{}); err == nil {
			_, err = secCli.Update(ctx, sec, metav1.UpdateOptions{})
		} else if apierrors.IsNotFound(err) {
			_, err = secCli.Create(ctx, sec, metav1.CreateOptions{})
		}
		if err != nil {
			return fmt.Errorf("写入 ES 凭证 Secret 失败: %w", err)
		}
	}

	sidecar := corev1.Container{
		Name:    LogSidecarName,
		Image:   in.Image,
		Command: []string{"/fluent-bit/bin/fluent-bit", "-c", logConfMountPath + "/fluent-bit.conf"},
		// HOSTNAME 供配置里的 ${HOSTNAME} 插值（写 pod_name）；ES 凭证经 env 插值进 fluent-bit.conf
		Env: []corev1.EnvVar{
			{Name: "HOSTNAME", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			}},
			{Name: "KC_ES_USER", Value: src.Username},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: LogShareVolumeName, MountPath: dir, ReadOnly: true},
			{Name: LogConfVolumeName, MountPath: logConfMountPath, ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		},
	}
	if src.Username != "" {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{Name: "KC_ES_PASS", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "pass"},
		}})
	}
	replaced := false
	for i, ct := range spec.Containers {
		if ct.Name == LogSidecarName {
			spec.Containers[i] = sidecar
			replaced = true
		}
	}
	if !replaced {
		spec.Containers = append(spec.Containers, sidecar)
	}
	return update(ctx)
}

// GetLogCollection 查询采集状态（Sidecar 容器存在即视为开启）
func (s *K8sService) GetLogCollection(ctx context.Context, c *kube.Client, kind, ns, name string) (*LogCollectionStatus, error) {
	tpl, _, _, _, err := logCollectionOwner(ctx, c, kind, ns, name)
	if err != nil {
		return nil, err
	}
	out := &LogCollectionStatus{}
	for _, ct := range tpl.Spec.Containers {
		if ct.Name == LogSidecarName {
			out.Enabled = true
		}
	}
	if raw := tpl.Annotations[logCollectionAnn]; raw != "" {
		var in LogCollectionInput
		if json.Unmarshal([]byte(raw), &in) == nil {
			out.Config = &in
		}
	}
	return out, nil
}

// RemoveLogCollection 摘除 Sidecar、共享卷与配置资源（保留应用其他卷）
func (s *K8sService) RemoveLogCollection(ctx context.Context, c *kube.Client, kind, ns, name string) error {
	tpl, _, _, update, err := logCollectionOwner(ctx, c, kind, ns, name)
	if err != nil {
		return err
	}
	spec := &tpl.Spec
	filterContainers := spec.Containers[:0]
	for _, ct := range spec.Containers {
		if ct.Name != LogSidecarName {
			filterContainers = append(filterContainers, ct)
		}
	}
	spec.Containers = filterContainers
	for i := range spec.Containers {
		spec.Containers[i].VolumeMounts = stripMounts(spec.Containers[i].VolumeMounts)
	}
	spec.Volumes = filterVolumes(spec.Volumes)
	delete(tpl.Annotations, logCollectionAnn)

	_ = c.Clientset.CoreV1().ConfigMaps(ns).Delete(ctx, name+"-fluent-bit", metav1.DeleteOptions{})
	_ = c.Clientset.CoreV1().Secrets(ns).Delete(ctx, name+"-fluent-bit-es", metav1.DeleteOptions{})
	return update(ctx)
}

func stripMounts(ms []corev1.VolumeMount) []corev1.VolumeMount {
	out := ms[:0]
	for _, m := range ms {
		if m.Name != LogShareVolumeName && m.Name != LogConfVolumeName {
			out = append(out, m)
		}
	}
	return out
}

func filterVolumes(vols []corev1.Volume) []corev1.Volume {
	out := []corev1.Volume{}
	for _, v := range vols {
		if v.Name != LogShareVolumeName && v.Name != LogConfVolumeName {
			out = append(out, v)
		}
	}
	return out
}
