// 容器文件浏览：通过一次性 exec 执行 ls/cat 等 POSIX 命令实现。
// 需要容器内有 sh（distroless 镜像请用「调试容器」入口）。
package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"kube-console/server/internal/kube"
)

// ExecOnce 单次 exec：不分配 TTY，stdin/stdout/stderr 直通。
func (s *K8sService) ExecOnce(ctx context.Context, c *kube.Client, namespace, pod, container string, stdin io.Reader, stdout, stderr io.Writer, command ...string) error {
	if container == "" {
		podObj, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(podObj.Spec.Containers) == 0 {
			return fmt.Errorf("Pod 没有容器")
		}
		container = podObj.Spec.Containers[0].Name
	}
	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	onceCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return executor.StreamWithContext(onceCtx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}

// FileEntry 目录条目
type FileEntry map[string]any

// FileList 列出容器内目录（ls -la 解析），返回 {path, entries:[{name,type,size,mode,mtime,linkTarget}]}
func (s *K8sService) FileList(ctx context.Context, c *kube.Client, namespace, pod, container, path string) (map[string]any, error) {
	if path == "" {
		path = "/"
	}
	var stdout, stderr strings.Builder
	if err := s.ExecOnce(ctx, c, namespace, pod, container, nil, &stdout, &stderr, "ls", "-la", path); err != nil {
		hint := stderr.String()
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(hint, "not found") && strings.Contains(err.Error(), "sh") {
			return nil, fmt.Errorf("容器内没有可用的 sh，无法浏览文件（可使用调试容器）")
		}
		return nil, fmt.Errorf("读取目录失败: %s %v", strings.TrimSpace(hint), err)
	}
	out := strings.TrimSpace(stdout.String())
	if strings.Contains(out, "No such file or directory") || strings.Contains(out, "not found") {
		return nil, fmt.Errorf("路径不存在: %s", path)
	}
	entries := make([]FileEntry, 0)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		e, ok := parseLsLine(line)
		if !ok {
			continue
		}
		entries = append(entries, e)
	}
	return map[string]any{"path": path, "entries": entries}, nil
}

// parseLsLine 解析 ls -la 的一行（兼容 GNU 与 BusyBox 的日期格式，容忍文件名空格）
func parseLsLine(line string) (FileEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return nil, false
	}
	mode := fields[0]
	if len(mode) < 10 {
		return nil, false
	}
	size := fields[4]
	// 日期在 size 之后：GNU/BusyBox 均为 3 段（如 "Mar 10 10:22"），ISO 为 2 段（"2024-03-10 10:22"）
	// 从右往左找名字：名字 = size 之后、去掉日期与时间后的剩余（保空格）
	afterSize := trimFieldsPrefix(line, 5) // 去掉 perms links owner group size
	dateFields := strings.Fields(afterSize)
	if len(dateFields) < 3 {
		return nil, false
	}
	var timeIdx int
	if isISODate(dateFields[0]) { // ISO: YYYY-MM-DD HH:MM name...
		timeIdx = 2
	} else { // "Mar 10 10:22 name..."
		timeIdx = 3
	}
	if len(dateFields) < timeIdx+1 {
		return nil, false
	}
	mtime := strings.Join(dateFields[:timeIdx], " ")
	name := strings.TrimSpace(strings.Join(dateFields[timeIdx:], " "))
	// 符号链接："name -> target"
	linkTarget := ""
	if idx := strings.Index(name, " -> "); idx >= 0 {
		linkTarget = name[idx+4:]
		name = name[:idx]
	}
	if name == "" || name == "." || name == ".." {
		return nil, false
	}
	ftype := "file"
	switch mode[0] {
	case 'd':
		ftype = "dir"
	case 'l':
		ftype = "link"
	case '-':
		ftype = "file"
	default:
		ftype = "other"
	}
	return FileEntry{"name": name, "type": ftype, "size": size, "mode": mode, "mtime": mtime, "linkTarget": linkTarget}, true
}

// trimFieldsPrefix 按空白分词去掉前 n 个字段，返回剩余原文（保空格）
func trimFieldsPrefix(line string, n int) string {
	i := 0
	for ; n > 0 && i < len(line); n-- {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		for i < len(line) && line[i] != ' ' {
			i++
		}
	}
	return strings.TrimLeft(line[i:], " ")
}

// isISODate 判断 "YYYY-MM-DD" 形态（BusyBox ls 可输出 ISO 日期；
// 旧实现 len==4 判断对 "2024-03-10"（长度 10）永不命中，走 3 段分支后
// 单 token 文件名整行被丢弃、多 token 名字字段错乱）
func isISODate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	return isAllDigits(s[:4]) && isAllDigits(s[5:7]) && isAllDigits(s[8:10])
}

func isAllDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}
