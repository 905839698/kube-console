package service

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

func kbPod(name, cluster, comp, role, node string, ready []bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "app",
			Labels: map[string]string{
				kbPodClusterLabel:   cluster,
				kbPodComponentLabel: comp,
				kbPodRoleLabel:      role,
			},
		},
		Spec: corev1.PodSpec{NodeName: node},
	}
	names := []string{"mysql", "redis", "postgresql", "mongodb"}
	for i, r := range ready {
		ct := corev1.Container{Name: names[i%len(names)]}
		st := corev1.ContainerStatus{Name: ct.Name, Ready: r, RestartCount: int32(i)}
		if ct.Name == "mysql" {
			st.Image = "mysql:8.0.39"
		}
		p.Spec.Containers = append(p.Spec.Containers, ct)
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, st)
	}
	p.Status.Phase = corev1.PodRunning
	p.Status.PodIP = "10.1.2.3"
	return p
}

// Pod 归属用 app.kubernetes.io/instance=<集群>；角色取 kubeblocks.io/role；就绪是「就绪容器/总容器」
func TestKBPodsListsClusterInstances(t *testing.T) {
	cs := fake.NewSimpleClientset(
		kbPod("db-mysql-1", "db", "mysql", "secondary", "n2", []bool{true, false}),
		kbPod("db-mysql-0", "db", "mysql", "primary", "n1", []bool{true, true}),
		kbPod("other-x-0", "other", "mysql", "primary", "n1", []bool{true}),
	)
	pods, err := KBPods(context.Background(), &kube.Client{Clientset: cs}, "app", "db")
	if err != nil {
		t.Fatalf("KBPods: %v", err)
	}
	if len(pods) != 2 {
		t.Fatalf("只应看到本集群的 2 个 Pod，实际 %d", len(pods))
	}
	if pods[0].Name != "db-mysql-0" || pods[0].Role != "primary" {
		t.Errorf("应按名字排序且带角色，实际 %s/%s", pods[0].Name, pods[0].Role)
	}
	if pods[0].Ready != "2/2" || pods[0].Node != "n1" || pods[0].PodIP != "10.1.2.3" {
		t.Errorf("Pod 摘要不对: %+v", pods[0])
	}
	if pods[1].Ready != "1/2" || pods[1].Restarts != 1 {
		t.Errorf("未就绪与重启数没统计出来: %+v", pods[1])
	}
	if len(pods[1].Containers) != 2 || !pods[1].Containers[0].Ready {
		t.Errorf("容器明细缺失: %+v", pods[1].Containers)
	}
}

// 查询终端只给「有对应镜像 CLI 且容器名对得上」的组件；命令里只引用 $ENV，不出现明文
func TestKBCLIForEngines(t *testing.T) {
	cases := []struct {
		engine, container string
		ok                bool
		must              []string
		mustNot           []string
	}{
		{"mysql", "mysql", true, []string{"MYSQL_ROOT_PASSWORD"}, []string{"-e "}},
		{"mysql", "kbagent", false, nil, nil},
		{"redis", "redis", true, []string{"REDIS_PASSWORD", "redis-cli"}, nil},
		{"postgresql", "postgresql", true, []string{"PGUSER_SUPERUSER", "psql"}, nil},
		{"mongodb", "mongodb", true, []string{"MONGODB_PASSWORD", "mongosh"}, nil},
		{"kafka", "kafka-broker", false, nil, nil},
		{"elasticsearch", "elasticsearch", false, nil, nil},
	}
	for _, c := range cases {
		cmd, ok := kbCLIFor(c.engine, c.container)
		if ok != c.ok {
			t.Errorf("%s/%s ok=%v 期望 %v", c.engine, c.container, ok, c.ok)
			continue
		}
		for _, m := range c.must {
			if !strings.Contains(cmd, m) {
				t.Errorf("%s 命令缺 %s: %s", c.engine, m, cmd)
			}
		}
		for _, m := range c.mustNot {
			if strings.Contains(cmd, m) {
				t.Errorf("%s 命令不该含 %s: %s", c.engine, m, cmd)
			}
		}
	}
}

// 主节点排在前面：查询终端默认连 primary，避免把写打到副本
func TestKBCLIsPrefersPrimary(t *testing.T) {
	cs := fake.NewSimpleClientset(
		kbPod("db-mysql-1", "db", "mysql", "secondary", "n2", []bool{true}),
		kbPod("db-mysql-0", "db", "mysql", "primary", "n1", []bool{true}),
	)
	entries, err := KBCLIs(context.Background(), &kube.Client{Clientset: cs}, "app", "db", "mysql")
	if err != nil {
		t.Fatalf("KBCLIs: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("一个组件应只有一个入口，实际 %d", len(entries))
	}
	if entries[0].Pod != "db-mysql-0" || entries[0].Container != "mysql" {
		t.Errorf("入口不对: %+v", entries[0])
	}
	if entries[0].Command == "" {
		t.Error("命令不能为空")
	}
}
