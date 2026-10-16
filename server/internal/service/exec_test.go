package service

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func pod(hostPID bool, privileged bool, runAsUser *int64, image string) *corev1.Pod {
	sc := &corev1.SecurityContext{RunAsUser: runAsUser}
	if privileged {
		p := true
		sc.Privileged = &p
	}
	return &corev1.Pod{Spec: corev1.PodSpec{
		HostPID:    hostPID,
		Containers: []corev1.Container{{Name: "shell", Image: image, SecurityContext: sc}},
	}}
}

func ptr64(v int64) *int64 { return &v }

// 复用判断必须同时看 hostPID / privileged / 显式 root：只比镜像会把旧规格残留的
// Pod 一直复用，nsenter 永远报 EACCES 且不自愈。
func TestNodeShellPodMatches(t *testing.T) {
	root := ptr64(0)
	nonroot := ptr64(65532)
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"规格齐备", pod(true, true, root, "busybox:1.36"), true},
		{"缺 hostPID", pod(false, true, root, "busybox:1.36"), false},
		{"缺 privileged", pod(true, false, root, "busybox:1.36"), false},
		{"以镜像自带非 root 起步", pod(true, true, nonroot, "busybox:1.36"), false},
		{"未显式指定 uid", pod(true, true, nil, "busybox:1.36"), false},
		{"镜像不符", pod(true, true, root, "busybox:1.35"), false},
	}
	for _, c := range cases {
		if got := nodeShellPodMatches(c.pod, "busybox:1.36"); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestFatalWaitingReason(t *testing.T) {
	for _, r := range []string{"ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull"} {
		if !fatalWaitingReason(r) {
			t.Errorf("%s 应视为硬失败", r)
		}
	}
	for _, r := range []string{"ContainerCreating", "PodInitializing", ""} {
		if fatalWaitingReason(r) {
			t.Errorf("%s 不应视为硬失败", r)
		}
	}
}
