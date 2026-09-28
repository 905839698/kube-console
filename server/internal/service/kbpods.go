// kbpods.go KubeBlocks 集群的 Pod 视图与「进容器查询」入口（受限 exec 的公共底座）。
//
// Pod 归属：实测 operator 1.0.2 给实例 Pod 打的标签是
// app.kubernetes.io/instance=<集群>、apps.kubeblocks.io/component-name=<组件>、
// kubeblocks.io/role=primary|secondary，用第一个标签就能拿到整个集群的 Pod。
package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
)

const (
	kbPodClusterLabel   = "app.kubernetes.io/instance"
	kbPodComponentLabel = "apps.kubeblocks.io/component-name"
	kbPodRoleLabel      = "kubeblocks.io/role"
)

type KBContainer struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	Image    string `json:"image"`
}

type KBPod struct {
	Name       string        `json:"name"`
	Component  string        `json:"component"`
	Role       string        `json:"role"`
	Node       string        `json:"node"`
	Phase      string        `json:"phase"`
	PodIP      string        `json:"podIP"`
	Ready      string        `json:"ready"` // "3/4"
	Restarts   int32         `json:"restarts"`
	StartTime  string        `json:"startTime"`
	Containers []KBContainer `json:"containers"`
}

// KBPods 列出集群的全部实例 Pod（含 sidecar 容器就绪与重启数）
func KBPods(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBPod, error) {
	if cluster == "" {
		return nil, fmt.Errorf("集群名不能为空")
	}
	list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", kbPodClusterLabel, cluster),
	})
	if err != nil {
		return nil, err
	}
	out := make([]KBPod, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		row := KBPod{
			Name:      p.Name,
			Component: p.Labels[kbPodComponentLabel],
			Role:      p.Labels[kbPodRoleLabel],
			Node:      p.Spec.NodeName,
			Phase:     string(p.Status.Phase),
			PodIP:     p.Status.PodIP,
		}
		if ts := p.Status.StartTime; ts != nil && !ts.IsZero() {
			row.StartTime = ts.Format("2006-01-02 15:04:05")
		}
		ready := 0
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			row.Restarts += cs.RestartCount
			row.Containers = append(row.Containers, KBContainer{Name: cs.Name, Ready: cs.Ready, Restarts: cs.RestartCount, Image: cs.Image})
		}
		row.Ready = fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers))
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ---------- 受限 exec：跑一条固定命令并收集输出 ----------

// kbExecMaxOutput 单次输出上限（ topic 列表 / 消息预览 / 查询结果都在这里收口）
const kbExecMaxOutput = 512 << 10

type kbLimitWriter struct {
	buf     *bytes.Buffer
	max     int
	trimmed bool
}

func (w *kbLimitWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.max {
		w.trimmed = true
		return len(p), nil
	}
	if remain := w.max - w.buf.Len(); len(p) > remain {
		w.buf.Write(p[:remain])
		w.trimmed = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// kbExecInPod 在容器里执行一条命令（argv 直接传给 exec，不经外层 shell 展开）。
// 命令模板全部由服务端拼装，用户提供的名字/URL 只作为独立 argv 传入，避免 shell 注入。
func kbExecInPod(ctx context.Context, c *kube.Client, ns, pod, container string, stdin io.Reader, command ...string) (string, string, error) {
	var out, errb bytes.Buffer
	lw := &kbLimitWriter{buf: &out, max: kbExecMaxOutput}
	ew := &kbLimitWriter{buf: &errb, max: 64 << 10}
	err := ExecOnce(ctx, c, ns, pod, container, stdin, lw, ew, command...)
	stdout := out.String()
	if lw.trimmed {
		stdout += "\n…（输出超过 512KB 已截断）"
	}
	return stdout, errb.String(), err
}

// kbWrapShell 用 sh -c 执行一段服务端拼好的脚本，用户输入通过 "$1" 之类的位置参数传入
// （脚本本身在容器内展开 $ENV，明文凭证不出 Pod）
func kbWrapShell(script string, args ...string) []string {
	cmd := []string{"sh", "-c", script, "kb"}
	return append(cmd, args...)
}

// ---------- 镜像自带 CLI 的引导命令 ----------

// KBCLIEntry 一个组件的查询终端入口
type KBCLIEntry struct {
	Component string `json:"component"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Command   string `json:"command"`
	Role      string `json:"role,omitempty"`
}

// kbCLIFor 按引擎给出容器内的自动登录命令。用户名/密码只写成 $ENV 引用，
// 由容器自己展开；env 不存在时退化成不带认证，至少能进 CLI。
func kbCLIFor(engine, container string) (string, bool) {
	switch engine {
	case "mysql":
		if container != "mysql" {
			return "", false
		}
		return `mysql -u"${MYSQL_ROOT_USER:-root}" -p"$MYSQL_ROOT_PASSWORD" 2>/dev/null || mysql -u"${MYSQL_ROOT_USER:-root}"`, true
	case "redis":
		if container != "redis" {
			return "", false
		}
		return `if [ -n "$REDIS_PASSWORD" ]; then redis-cli -a "$REDIS_PASSWORD"; else redis-cli; fi`, true
	case "postgresql":
		if container != "postgresql" {
			return "", false
		}
		return `psql -U "${PGUSER_SUPERUSER:-postgres}" -d "${PGDATABASE:-postgres}"`, true
	case "mongodb":
		if container != "mongodb" {
			return "", false
		}
		return `if [ -n "$MONGODB_PASSWORD" ]; then mongosh -u "${MONGODB_USERNAME:-admin}" -p "$MONGODB_PASSWORD" --authenticationDatabase admin; else mongosh; fi`, true
	}
	return "", false
}

// KBCLIs 给出各组件可用的查询终端入口（优先主节点 Pod）
func KBCLIs(ctx context.Context, c *kube.Client, ns, cluster, engine string) ([]KBCLIEntry, error) {
	pods, err := KBPods(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]KBCLIEntry, 0, len(pods))
	for _, p := range pods {
		if seen[p.Component] {
			continue
		}
		for _, ct := range p.Containers {
			cmd, ok := kbCLIFor(engine, ct.Name)
			if !ok {
				continue
			}
			seen[p.Component] = true
			out = append(out, KBCLIEntry{Component: p.Component, Pod: p.Name, Container: ct.Name, Command: cmd, Role: p.Role})
			break
		}
	}
	// 主节点排在前面：写操作只能连 primary
	sort.SliceStable(out, func(i, j int) bool { return kbRoleFirst(out[i].Role) < kbRoleFirst(out[j].Role) })
	return out, nil
}

func kbRoleFirst(role string) int {
	switch strings.ToLower(role) {
	case "primary", "master", "leader":
		return 0
	case "":
		return 1
	default:
		return 2
	}
}
