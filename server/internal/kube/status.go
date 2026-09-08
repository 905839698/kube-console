package kube

import (
	corev1 "k8s.io/api/core/v1"
)

// PodStatusInfo Pod 状态摘要（kubectl 风格）
type PodStatusInfo struct {
	Status       string // Running | Pending | Completed | Failed | Terminating | CrashLoopBackOff | Unknown
	Reason       string // 具体原因（如 CrashLoopBackOff / ImagePullBackOff）
	RestartCount int32
	Ready        bool
}

// PodStatus 计算 Pod 的状态分类
func PodStatus(pod *corev1.Pod) PodStatusInfo {
	info := PodStatusInfo{RestartCount: restartCount(pod)}

	// 删除中
	if pod.DeletionTimestamp != nil {
		info.Status = "Terminating"
		info.Reason = "terminating"
		return info
	}

	// 已就绪（所有容器 running 且 ready）
	if allReady(pod) {
		info.Status = "Running"
		info.Ready = true
		return info
	}

	// 按容器状态归类
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff":
				info.Status = "CrashLoopBackOff"
			case "ErrImagePull", "ImagePullBackOff":
				info.Status = "ImagePullBackOff"
			default:
				info.Status = "Waiting"
			}
			info.Reason = cs.State.Waiting.Reason
			return info
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "Error" {
			info.Status = "Error"
			info.Reason = cs.State.Terminated.Reason
			return info
		}
	}

	switch pod.Status.Phase {
	case corev1.PodPending:
		info.Status = "Pending"
	case corev1.PodSucceeded:
		info.Status = "Completed"
	case corev1.PodFailed:
		info.Status = "Failed"
	case corev1.PodRunning:
		// 已创建但未就绪（如 readiness 探针未通过）
		info.Status = "Running"
		info.Reason = "not ready"
	default:
		info.Status = "Unknown"
	}
	return info
}

// ContainerStatusInfo 容器状态摘要
type ContainerStatusInfo struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
	State        string `json:"state"` // Running | Waiting | Terminated
	Reason       string `json:"reason"`
	Message      string `json:"message"`
}

// ContainerStatuses 计算容器状态列表（含未启动的 Init 容器之外的普通容器）
func ContainerStatuses(pod *corev1.Pod) []ContainerStatusInfo {
	statuses := make([]ContainerStatusInfo, 0, len(pod.Status.ContainerStatuses))
	for _, cs := range pod.Status.ContainerStatuses {
		s := ContainerStatusInfo{Name: cs.Name, Image: cs.Image, Ready: cs.Ready, RestartCount: cs.RestartCount}
		switch {
		case cs.State.Running != nil:
			s.State = "Running"
		case cs.State.Waiting != nil:
			s.State = "Waiting"
			s.Reason = cs.State.Waiting.Reason
			s.Message = cs.State.Waiting.Message
		case cs.State.Terminated != nil:
			s.State = "Terminated"
			s.Reason = cs.State.Terminated.Reason
			s.Message = cs.State.Terminated.Message
		}
		statuses = append(statuses, s)
	}
	return statuses
}

func allReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			// 容器全部就绪
			ready := 0
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
			}
			return ready == len(pod.Spec.Containers)
		}
	}
	return false
}

func restartCount(pod *corev1.Pod) int32 {
	var n int32
	for _, cs := range pod.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

// WorkloadReplicaStatus 工作负载副本状态
type WorkloadReplicaStatus struct {
	Replicas      int32 `json:"replicas"`
	ReadyReplicas int32 `json:"readyReplicas"`
	Updated       int32 `json:"updated"`
	Available     int32 `json:"available"`
}
