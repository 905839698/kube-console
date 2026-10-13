// 容器内文件日志采集：向工作负载 Pod 注入 Fluent Bit Sidecar + 共享 emptyDir 卷，
// tail 指定日志文件后写入 Elasticsearch（复用「日志源配置」的连接信息），
// 采集文档沿用 fluentd 字段约定（kubernetes.namespace_name / pod_name / container_name），供日志检索直接查询。
package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
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
	// LogESSecretName 是**每命名空间一个**的 ES 凭据 Secret，同 ns 内所有采集 Sidecar 共用。
	// 早期实现按工作负载各建一份（<name>-fluent-bit-es）：同一密码被复制 N 份，
	// 且任何对该 ns 有 get secret 权限的人都能读走平台 ES 账号。
	LogESSecretName = "kc-es-credentials"
	// LegacyESSecretSuffix 旧版按工作负载命名的后缀（保存/关闭时清理）
	LegacyESSecretSuffix = "-fluent-bit-es"
	logESKey             = "pass"
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

// buildFluentBitCM 工作负载专属的 fluent-bit 配置 CM（Sidecar 以卷挂载）
func buildFluentBitCM(in *LogCollectionInput, ns, workload string, src *model.LogSource) (*corev1.ConfigMap, error) {
	host, port, tlsOn, index, err := esCollectTarget(src, ns, in.Format)
	if err != nil {
		return nil, err
	}
	cm := &corev1.ConfigMap{
		Data: map[string]string{
			"fluent-bit.conf": fluentBitConf(in, ns, workload, index, host, port, tlsOn, src.Username != ""),
			// Parsers_File 声明了就必须存在，text 格式也带上（json-parser 闲置无害）
			"parsers.conf": "[PARSER]\n    Name      json-parser\n    Format    json\n",
		},
	}
	return cm, nil
}

// buildLogESSecret 命名空间共享的 ES 凭据 Secret；无账号时返回 nil（调用方删除遗留）
func buildLogESSecret(src *model.LogSource, ns string) *corev1.Secret {
	if src.Username == "" {
		return nil
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      LogESSecretName,
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "kube-console"},
		},
		StringData: map[string]string{logESKey: src.Password},
	}
}

// buildLogSidecar 采集 Sidecar 容器（admission 注入与配置共用同一份构造）
func buildLogSidecar(in *LogCollectionInput, dir string, esUser string) corev1.Container {
	sidecar := corev1.Container{
		Name:    LogSidecarName,
		Image:   in.Image,
		Command: []string{"/fluent-bit/bin/fluent-bit", "-c", logConfMountPath + "/fluent-bit.conf"},
		// HOSTNAME 供配置里的 ${HOSTNAME} 插值（写 pod_name）；ES 凭证经 env 插值进 fluent-bit.conf
		Env: []corev1.EnvVar{
			{Name: "HOSTNAME", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			}},
			{Name: "KC_ES_USER", Value: esUser},
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
	return sidecar
}

// stripLogSidecarFromSpec 摘除模板模式注入的 Sidecar、共享卷与配置卷（旧模式迁移/关闭用）
func stripLogSidecarFromSpec(spec *corev1.PodSpec) {
	filtered := spec.Containers[:0]
	for _, ct := range spec.Containers {
		if ct.Name != LogSidecarName {
			filtered = append(filtered, ct)
		}
	}
	spec.Containers = filtered
	for i := range spec.Containers {
		spec.Containers[i].VolumeMounts = stripMounts(spec.Containers[i].VolumeMounts)
	}
	spec.Volumes = filterVolumes(spec.Volumes)
}

// GetLogCollection 查询采集状态（Sidecar 容器存在即视为开启）
// collectorsLeftIn 命名空间内是否仍有带 fluent-bit Sidecar 的工作负载。
// 任一类负载查询失败一律按"还有"处理（fail-safe）——误删会让其它 Sidecar 起不来。
func collectorsLeftIn(ctx context.Context, c *kube.Client, ns string) (bool, error) {
	var firstErr error
	dl, err := c.Clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		firstErr = err
	} else {
		for _, d := range dl.Items {
			if hasLogSidecar(d.Spec.Template.Spec.Containers) {
				return true, nil
			}
		}
	}
	sl, err := c.Clientset.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		for _, st := range sl.Items {
			if hasLogSidecar(st.Spec.Template.Spec.Containers) {
				return true, nil
			}
		}
	}
	wl, err := c.Clientset.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		for _, w := range wl.Items {
			if hasLogSidecar(w.Spec.Template.Spec.Containers) {
				return true, nil
			}
		}
	}
	if firstErr != nil {
		return true, firstErr
	}
	return false, nil
}

func hasLogSidecar(containers []corev1.Container) bool {
	for _, ct := range containers {
		if ct.Name == LogSidecarName {
			return true
		}
	}
	return false
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
