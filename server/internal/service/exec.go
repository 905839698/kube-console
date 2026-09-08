package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"kube-console/server/internal/kube"
)

// wsControl 前端 → 后端控制消息（resize 终端尺寸）
type wsControl struct {
	Type   string `json:"type"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	Width  uint16 `json:"width"`
	Height uint16 `json:"height"`
}

// wsTextMsg 后端 → 前端通知（notice=灰色提示，exit=会话结束）
type wsTextMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// wsInbound ws 读循环产出的原始消息
type wsInbound struct {
	mt   int
	data []byte
}

// wsWriter 把 exec 输出写到 WebSocket（gorilla 不允许并发写，加锁）
type wsWriter struct {
	ws *websocket.Conn
	mu *sync.Mutex
}

func (w wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// terminalSizeQueue 实现 remotecommand.TerminalSizeQueue
type terminalSizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// 尝试的 shell 列表：容器镜像 shell 各异（sh/bash/ash），逐个尝试直到可执行
var fallbackShells = []string{"/bin/sh", "/bin/bash", "/bin/ash", "sh", "bash", "ash"}

// debugContainerName 临时调试容器名（kubectl debug 风格，用于无 shell 的 distroless 镜像）
const debugContainerName = "debug"

// ExecPod 通过 WebSocket 桥接 Pod 内 Shell：ws binary=stdin，ws text JSON=resize，exec 输出=ws binary
// 正常模式：容器缺少 /bin/sh 时自动回退尝试 /bin/bash 等；debug 模式：注入临时调试容器后进入
func (s *K8sService) ExecPod(ctx context.Context, c *kube.Client, ws *websocket.Conn, namespace, pod, container, shell string, debug bool) error {
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

	// 单一 ws 读循环：binary=stdin 输入；text JSON=resize 控制
	msgCh := make(chan wsInbound, 128)
	go func() {
		defer close(msgCh)
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgCh <- wsInbound{mt: mt, data: data}:
			default:
				// 队列满则丢弃（输入洪水时保留最新状态）
			}
		}
	}()

	var err error
	if debug {
		err = s.execDebugShell(ctx, c, ws, msgCh, namespace, pod, container)
	} else {
		err = s.execShellFallback(ctx, c, ws, msgCh, namespace, pod, container, shell)
	}
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{Type: "exit", Message: exitMessage(err)}))
	_ = ws.Close()
	return err
}

// execShellFallback 按 shell 列表逐个尝试 exec 会话
func (s *K8sService) execShellFallback(ctx context.Context, c *kube.Client, ws *websocket.Conn, msgCh <-chan wsInbound, namespace, pod, container, shell string) error {
	shells := fallbackShells
	if shell != "" {
		shells = []string{shell}
	}

	var lastSize *remotecommand.TerminalSize
	allStartupFail := false
	for i, sh := range shells {
		err := s.execShellAttempt(ctx, c, ws, msgCh, &lastSize, namespace, pod, container, sh)
		if err == nil {
			return nil
		}
		// 仅"启动失败（shell 不存在）"继续尝试下一个；正常退出/其他错误直接返回
		isStartupFail := strings.Contains(err.Error(), "no such file or directory") ||
			strings.Contains(err.Error(), "executable file not found")
		if !isStartupFail || i == len(shells)-1 {
			if isStartupFail {
				allStartupFail = true
			}
			return friendlyShellError(err, allStartupFail)
		}
		allStartupFail = true
		// 提示前端正在切换 shell
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{
			Type:    "notice",
			Message: fmt.Sprintf("%s 不存在，尝试 %s ...", sh, shells[i+1]),
		}))
	}
	return nil
}

// friendlyShellError 全部 shell 启动失败时给出友好提示（容器可能为 distroless 无 shell）
func friendlyShellError(err error, allStartupFail bool) error {
	if allStartupFail {
		return fmt.Errorf("容器内没有可用的 shell（/bin/sh、/bin/bash、/bin/ash 均不存在），可通过「调试容器」注入 busybox 进入终端")
	}
	return err
}

// execDebugShell 注入临时调试容器并从其中进入终端
func (s *K8sService) execDebugShell(ctx context.Context, c *kube.Client, ws *websocket.Conn, msgCh <-chan wsInbound, namespace, pod, target string) error {
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{Type: "notice", Message: fmt.Sprintf("正在创建调试容器（%s）...", s.debugImage)}))
	debugContainer, err := s.EnsureDebugContainer(ctx, c, namespace, pod, target)
	if err != nil {
		return fmt.Errorf("创建调试容器失败：%v", err)
	}
	if err := s.waitDebugContainerRunning(ctx, c, namespace, pod, debugContainer); err != nil {
		return fmt.Errorf("调试容器未就绪：%v", err)
	}
	var lastSize *remotecommand.TerminalSize
	return s.execShellAttempt(ctx, c, ws, msgCh, &lastSize, namespace, pod, debugContainer, "/bin/sh")
}

// EnsureDebugContainer 确保 Pod 存在可用的临时调试容器并返回其实际名称
// K8s 规则：ephemeral container 只能新增，不能修改或移除。因此镜像一致的复用，
// 镜像配置变更时追加新名字（debug-1、debug-2...）的容器（旧容器残留无害，pod 重建后消失）
func (s *K8sService) EnsureDebugContainer(ctx context.Context, c *kube.Client, namespace, pod, target string) (string, error) {
	pc, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	existing := pc.Spec.EphemeralContainers
	// 镜像一致的 debug 容器直接复用
	for _, ec := range existing {
		if strings.HasPrefix(ec.Name, debugContainerName) && ec.Image == s.debugImage {
			return ec.Name, nil
		}
	}

	// 生成不冲突的新名字（已有容器不可修改/移除，只能追加）
	usedNames := make(map[string]bool)
	for _, ec := range existing {
		if strings.HasPrefix(ec.Name, debugContainerName) {
			usedNames[ec.Name] = true
		}
	}
	name := debugContainerName
	for i := 1; usedNames[name]; i++ {
		name = fmt.Sprintf("%s-%d", debugContainerName, i)
	}
	common := corev1.EphemeralContainerCommon{
		Name:    name,
		Image:   s.debugImage,
		Command: []string{"/bin/sh", "-c", "sleep 3600"},
		Stdin:   true,
		TTY:     true,
	}
	noEsc := false
	common.SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &noEsc}
	ec := corev1.EphemeralContainer{EphemeralContainerCommon: common}
	if target != "" {
		ec.TargetContainerName = target
	}
	pc.Spec.EphemeralContainers = append(pc.Spec.EphemeralContainers, ec)
	_, err = c.Clientset.CoreV1().Pods(namespace).UpdateEphemeralContainers(ctx, pod, pc, metav1.UpdateOptions{})
	if err != nil {
		return "", err
	}
	return name, nil
}

// waitDebugContainerRunning 等待调试容器进入 Running（需拉取镜像）
func (s *K8sService) waitDebugContainerRunning(ctx context.Context, c *kube.Client, namespace, pod, name string) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		pc, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return err
		}
		for _, st := range pc.Status.EphemeralContainerStatuses {
			if st.Name == name && st.State.Running != nil {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("等待调试容器 %s Running 超时", name)
}

// execShellAttempt 用指定 shell 发起一次 exec 会话
func (s *K8sService) execShellAttempt(ctx context.Context, c *kube.Client, ws *websocket.Conn, msgCh <-chan wsInbound, lastSize **remotecommand.TerminalSize, namespace, pod, container, shell string) error {
	execURL := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{shell},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec).URL()

	executor, err := remotecommand.NewSPDYExecutor(c.Config, "POST", execURL)
	if err != nil {
		return err
	}

	// 本次尝试的 stdin 管道与尺寸队列（初始尺寸取上次尝试记忆的值）
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	sizeCh := make(chan remotecommand.TerminalSize, 8)
	if *lastSize != nil {
		sizeCh <- **lastSize
	}

	// 消费 ws 消息直到 exec 结束
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdinW.Close()
		for {
			select {
			case <-stop:
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				switch msg.mt {
				case websocket.BinaryMessage:
					if _, werr := stdinW.Write(msg.data); werr != nil {
						return
					}
				case websocket.TextMessage:
					var ctrl wsControl
					if json.Unmarshal(msg.data, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
						size := remotecommand.TerminalSize{Width: ctrl.Cols, Height: ctrl.Rows}
						*lastSize = &size
						select {
						case sizeCh <- size:
						default:
							// 队列满则丢弃，终端会持续发送新尺寸
						}
					}
				}
			}
		}
	}()

	// 本次尝试的写锁：exec 输出（Stream 内部）与主流程通知消息（尝试间隙）不并发
	var writeMu sync.Mutex
	opts := remotecommand.StreamOptions{
		Stdin:             stdinR,
		Stdout:            wsWriter{ws: ws, mu: &writeMu},
		Stderr:            wsWriter{ws: ws, mu: &writeMu},
		Tty:               true,
		TerminalSizeQueue: &terminalSizeQueue{ch: sizeCh},
	}
	err = executor.StreamWithContext(ctx, opts)
	close(stop)
	wg.Wait()
	return err
}

// exitMessage 把 exec 结束原因转成用户可读文本
func exitMessage(err error) string {
	if err == nil {
		return "Shell 会话已结束"
	}
	return fmt.Sprintf("Shell 会话已结束：%v", err)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ------------------- 节点终端（node shell） -------------------

// EnsureNodeDebugPod 在目标节点创建 nsenter 调试 Pod（幂等：存在且镜像一致则复用）
func (s *K8sService) EnsureNodeDebugPod(ctx context.Context, c *kube.Client, nodeName string) (string, error) {
	name := "kc-node-shell-" + nodeName
	pod, err := c.Clientset.CoreV1().Pods("kube-system").Get(ctx, name, metav1.GetOptions{})
	if err == nil && pod.Spec.Containers[0].Image == s.debugImage {
		return name, nil
	}
	// 已存在但镜像不同：先删再建
	if err == nil {
		_ = c.Clientset.CoreV1().Pods("kube-system").Delete(ctx, name, metav1.DeleteOptions{})
		time.Sleep(1 * time.Second)
	}
	privileged := true
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", Labels: map[string]string{"app": "kc-node-shell"}},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			HostPID:       true,
			HostNetwork:   true,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			Tolerations:   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{{
				Name:            "shell",
				Image:           s.debugImage,
				Command:         []string{"/bin/sh", "-c", "sleep 7200"},
				SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
			}},
		},
	}
	if _, err := c.Clientset.CoreV1().Pods("kube-system").Create(ctx, newPod, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	// 等待 Running（镜像已在节点缓存时秒级；首次拉取最多 90s）
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := c.Clientset.CoreV1().Pods("kube-system").Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			for _, st := range p.Status.ContainerStatuses {
				if st.Name == "shell" && st.State.Running != nil {
					return name, nil
				}
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("节点调试 Pod 未就绪（超时）")
}

// ExecNode 通过 WebSocket 进入节点 shell：nsenter 到宿主机 1 号进程命名空间
func (s *K8sService) ExecNode(ctx context.Context, c *kube.Client, ws *websocket.Conn, nodeName string) error {
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{Type: "notice", Message: "正在创建节点调试 Pod（privileged + hostPID）..."}))
	podName, err := s.EnsureNodeDebugPod(ctx, c, nodeName)
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{Type: "exit", Message: "节点终端启动失败：" + err.Error()}))
		_ = ws.Close()
		return err
	}
	defer func() {
		// 会话结束即清理调试 Pod（避免驻留）
		delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = c.Clientset.CoreV1().Pods("kube-system").Delete(delCtx, podName, metav1.DeleteOptions{})
		cancel()
	}()

	msgCh := make(chan wsInbound, 128)
	go func() {
		defer close(msgCh)
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgCh <- wsInbound{mt: mt, data: data}:
			default:
			}
		}
	}()

	// nsenter 进入宿主机命名空间
	err = s.execNodeShellAttempt(ctx, c, ws, msgCh, nodeName, podName)
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsTextMsg{Type: "exit", Message: exitMessage(err)}))
	_ = ws.Close()
	return err
}

// execNodeShellAttempt 单次 nsenter 会话
func (s *K8sService) execNodeShellAttempt(ctx context.Context, c *kube.Client, ws *websocket.Conn, msgCh <-chan wsInbound, nodeName, podName string) error {
	execURL := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace("kube-system").Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "shell",
			Command:   []string{"/bin/sh", "-c", "nsenter -t 1 -m -u -i -n -p -- /bin/sh -l"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec).URL()

	executor, err := remotecommand.NewSPDYExecutor(c.Config, "POST", execURL)
	if err != nil {
		return err
	}
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	sizeCh := make(chan remotecommand.TerminalSize, 8)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdinW.Close()
		for {
			select {
			case <-stop:
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				switch msg.mt {
				case websocket.BinaryMessage:
					if _, werr := stdinW.Write(msg.data); werr != nil {
						return
					}
				case websocket.TextMessage:
					var ctrl wsControl
					if json.Unmarshal(msg.data, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
						size := remotecommand.TerminalSize{Width: ctrl.Cols, Height: ctrl.Rows}
						select {
						case sizeCh <- size:
						default:
						}
					}
				}
			}
		}
	}()

	var writeMu sync.Mutex
	opts := remotecommand.StreamOptions{
		Stdin:             stdinR,
		Stdout:            wsWriter{ws: ws, mu: &writeMu},
		Stderr:            wsWriter{ws: ws, mu: &writeMu},
		Tty:               true,
		TerminalSizeQueue: &terminalSizeQueue{ch: sizeCh},
	}
	err = executor.StreamWithContext(ctx, opts)
	close(stop)
	wg.Wait()
	return err
}
