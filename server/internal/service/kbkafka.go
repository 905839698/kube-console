// kbkafka.go Kafka 的 topic 管理、消费组与消息预览。
//
// Kafka 没有 HTTP 管理口，只有二进制协议，因此所有操作都进 broker 容器跑官方脚本
// （实测 1.0.2 的 kafka addon：容器名 kafka，客户端口 kafka-client=9092，监听器默认 PLAINTEXT）。
//
// 安全边界：脚本名走正则白名单，topic / 配置键值做字符校验后作为**独立 argv** 传给 sh
// （脚本里以 "$@" 引用），不拼进 shell 字符串——不给注入留口子。
// SASL/TLS 开启时这些命令会失败，脚本 stderr 会原样回给前端。
package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
)

var (
	kbKafkaTopicRe   = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,248}$`)
	kbKafkaConfigKey = regexp.MustCompile(`^[a-z0-9.]{1,128}$`)
	// 配置值允许常见字符，但禁掉空白与 shell/分隔符，避免被解释成多参数
	kbKafkaConfigVal = regexp.MustCompile(`^[A-Za-z0-9._:/+=,-]{0,256}$`)
	kbKafkaScriptRe  = regexp.MustCompile(`^kafka-[a-z-]+\.sh$`)
)

// kbKafkaMaxMessages 单次消息预览条数上限（stdout 另有 512KB 截断）
const kbKafkaMaxMessages = 200

// KBKafkaAdmin broker 容器入口信息
type KBKafkaAdmin struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Bootstrap string `json:"bootstrap"`
}

// KBKafkaAdminFor 找一个可当 admin client 的容器（组件容器名统一叫 kafka）
func KBKafkaAdminFor(ctx context.Context, c *kube.Client, ns, cluster string) (KBKafkaAdmin, error) {
	list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", kbPodClusterLabel, cluster),
	})
	if err != nil {
		return KBKafkaAdmin{}, err
	}
	for i := range list.Items {
		p := &list.Items[i]
		env := map[string]string{}
		ports := map[string]int32{}
		for _, ct := range p.Spec.Containers {
			if !strings.Contains(ct.Name, "kafka") {
				continue
			}
			for _, e := range ct.Env {
				if e.Value != "" {
					env[e.Name] = e.Value
				}
			}
			for _, pt := range ct.Ports {
				if pt.Name != "" {
					ports[pt.Name] = pt.ContainerPort
				}
			}
			return KBKafkaAdmin{Pod: p.Name, Container: ct.Name, Bootstrap: kbKafkaBootstrap(env, ports)}, nil
		}
	}
	return KBKafkaAdmin{}, fmt.Errorf("该集群没有 kafka 容器（未装 Kafka 引擎或 Pod 未起）")
}

// kbKafkaBootstrap 客户端地址：优先命名端口 kafka-client，其次 KAFKA_CFG_LISTENERS 里的
// CLIENT://:PORT，最后退到 9092
func kbKafkaBootstrap(env map[string]string, ports map[string]int32) string {
	if p, ok := ports["kafka-client"]; ok && p > 0 {
		return "localhost:" + strconv.Itoa(int(p))
	}
	for _, seg := range strings.Split(env["KAFKA_CFG_LISTENERS"], ",") {
		kv := strings.SplitN(strings.TrimSpace(seg), "://", 2)
		if len(kv) != 2 || !strings.EqualFold(kv[0], "CLIENT") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(kv[1], ":")); err == nil && n > 0 {
			return "localhost:" + strconv.Itoa(n)
		}
	}
	return "localhost:9092"
}

// kbKafkaRun 执行一个 kafka 脚本。command -v 兜底：bitnami 镜像在 /opt/bitnami/kafka/bin，
// 社区镜像在 /opt/kafka/bin
func kbKafkaRun(ctx context.Context, c *kube.Client, ns string, ad KBKafkaAdmin, script string, args ...string) (string, error) {
	if !kbKafkaScriptRe.MatchString(script) {
		return "", fmt.Errorf("不支持的 Kafka 脚本: %s", script)
	}
	// echo 恒成功，所以兜底不能写成 `a || echo b || echo c`；必须逐档判断文件是否存在
	runner := kbKafkaRunner(script)
	out, errOut, err := kbExecInPod(ctx, c, ns, ad.Pod, ad.Container, nil, kbWrapShell(runner, args...)...)
	if err != nil {
		return out, fmt.Errorf("%s", kafkaErrText(err, errOut, out))
	}
	return out, nil
}

// kbKafkaRunner 脚本路径三档兜底：PATH 里找 → bitnami 路径 → 社区镜像路径。
//
// 开头必须先清掉一切名字含 JMX 的环境变量：broker 容器（bitnami 镜像）导出了 JMX_PORT /
// KAFKA_JMX_OPTS 等，kafka-run-class.sh 会把这套 JMX 参数带给**客户端** JVM，
// 于是 kafka-topics.sh 也去 bind 5555 —— 端口早被同 Pod 的 broker 进程占着，
// 结果就是 AgentConfigurationError / BindException: Address already in use。
// CLI 工具根本不需要 JMX，清掉即可。
func kbKafkaRunner(script string) string {
	return fmt.Sprintf(`for v in $(env | cut -d= -f1 | grep -i jmx); do unset "$v"; done
for v in KAFKA_OPTS KAFKA_CLIENT_OPTS KAFKA_JVM_PERFORMANCE_OPTS EXTRA_JVM_ARGUMENTS JAVA_TOOL_OPTIONS; do
  val=$(printenv "$v") || continue
  case "$val" in
    *jmxremote*|*rmi.server.hostname*)
      export "$v=$(printf '%%s' "$val" | sed -e 's/-D[a-z.]*jmxremote[^ ]*//g' -e 's/-Djava\.rmi\.server\.hostname=[^ ]*//g')" ;;
  esac
done
p=%s
b=$(command -v "$p" 2>/dev/null)
[ -n "$b" ] || b=/opt/bitnami/kafka/bin/$p
[ -f "$b" ] || b=/opt/kafka/bin/$p
exec "$b" "$@"`, script)
}

// kbKafkaNoise 与 Kafka 操作本身无关的噪音行：JMX agent、log4j 告警、Guava 弃用提示等。
// 判定报错时要把它们剔掉，否则一次端口冲突之类的无关告警会盖住真正的失败原因。
func kbKafkaNoise(line string) bool {
	l := strings.ToLower(line)
	for _, k := range []string{"jmx", "rmi", "agentconfigurationerror", "bindexception", "address already in use",
		"log4j:", "warning: ", "sun.management", "terminally deprecated"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// kafkaErrText 取一句能说明问题的报错：stderr 本身就是错误流，剔掉噪音后取最后一行；
// stdout 里只有 Java 打出的 ERROR/Exception 行才算报错。
func kafkaErrText(err error, stderr, stdout string) string {
	pick := func(src string, needKeyword bool) string {
		lines := strings.Split(src, "\n")
		for i := len(lines) - 1; i >= 0; i-- { // 真正的报错通常在末尾
			ln := strings.TrimSpace(lines[i])
			if ln == "" || kbKafkaNoise(ln) {
				continue
			}
			if needKeyword && !strings.Contains(ln, "Exception") && !strings.Contains(ln, "Error") && !strings.Contains(ln, "error") {
				continue
			}
			return ln
		}
		return ""
	}
	if msg := pick(stderr, false); msg != "" {
		return msg
	}
	if msg := pick(stdout, true); msg != "" {
		return msg
	}
	// 只剩噪音时也要说清是谁报的错，别给一句空话
	return strings.TrimSpace(err.Error())
}

func kbKafkaBase(ad KBKafkaAdmin) []string {
	return []string{"--bootstrap-server", ad.Bootstrap}
}

// ---------- topic ----------

type KBKafkaPartition struct {
	Partition int      `json:"partition"`
	Leader    string   `json:"leader"`
	Replicas  []string `json:"replicas"`
	ISR       []string `json:"isr"`
	Offline   bool     `json:"offline"`
}

type KBKafkaTopic struct {
	Name              string             `json:"name"`
	PartitionCount    int                `json:"partitionCount"`
	ReplicationFactor string             `json:"replicationFactor"`
	Configs           map[string]string  `json:"configs"`
	Partitions        []KBKafkaPartition `json:"partitions"`
}

// kbParseKafkaTopics 解析 `kafka-topics.sh --describe`：头行给 Topic/PartitionCount/
// ReplicationFactor/Configs，随后每个分区一行（都是 "Key: value" 制表符分隔）
func kbParseKafkaTopics(out string) []KBKafkaTopic {
	list := []KBKafkaTopic{}
	idx := map[string]int{}
	for _, ln := range strings.Split(out, "\n") {
		f := kbParseKVLine(ln)
		name := f["Topic"]
		if name == "" || !strings.Contains(ln, ":") {
			continue
		}
		if _, ok := idx[name]; !ok {
			idx[name] = len(list)
			list = append(list, KBKafkaTopic{Name: name, Configs: map[string]string{}})
		}
		t := &list[idx[name]]
		if v := f["PartitionCount"]; v != "" {
			t.PartitionCount, _ = strconv.Atoi(v)
		}
		if v := f["ReplicationFactor"]; v != "" {
			t.ReplicationFactor = v
		}
		if v := f["Configs"]; v != "" && v != "None" {
			for _, kv := range strings.Split(v, ",") {
				if p := strings.SplitN(kv, "=", 2); len(p) == 2 {
					t.Configs[p[0]] = p[1]
				}
			}
		}
		if f["Partition"] != "" {
			pn, _ := strconv.Atoi(f["Partition"])
			t.Partitions = append(t.Partitions, KBKafkaPartition{
				Partition: pn,
				Leader:    f["Leader"],
				Replicas:  kbSplitNonEmpty(f["Replicas"], ","),
				ISR:       kbSplitNonEmpty(f["Isr"], ","),
				Offline:   strings.EqualFold(f["Leader"], "none"),
			})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for i := range list {
		sort.Slice(list[i].Partitions, func(a, b int) bool { return list[i].Partitions[a].Partition < list[i].Partitions[b].Partition })
	}
	return list
}

// kbKVFRe 匹配「键: 值」，值可以为空
var kbKVFRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_-]*):[ \t]*([^ \t]*)`)

// kbParseKVLine 解析 "Topic: x\tPartition: 0\tLeader: none\tIsr: " 这类一行多组键值。
// 必须按「键:」正则切：早先用空白分词 + 「下一段当值」的写法，遇到空值（Isr: 后直接换列）
// 会把下一列的键名当成本列的值吃掉。
func kbParseKVLine(line string) map[string]string {
	m := map[string]string{}
	// 制表符分列时先按列切：这样才拿得到空值（"Isr:" 后面什么都没有）
	if segs := strings.Split(line, "	"); len(segs) > 1 {
		for _, seg := range segs {
			k, v, ok := strings.Cut(strings.TrimSpace(seg), ":")
			if ok && strings.TrimSpace(k) != "" {
				m[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if len(m) > 0 {
			return m
		}
	}
	for _, g := range kbKVFRe.FindAllStringSubmatch(line, -1) {
		m[g[1]] = g[2]
	}
	return m
}

func kbSplitNonEmpty(s, sep string) []string {
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// KBKafkaTopics 列出全部 topic 与分区分布
func KBKafkaTopics(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBKafkaTopic, error) {
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	out, err := kbKafkaRun(ctx, c, ns, ad, "kafka-topics.sh", append(kbKafkaBase(ad), "--describe")...)
	if err != nil {
		return nil, err
	}
	return kbParseKafkaTopics(out), nil
}

// KBKafkaTopicInput 建 topic 请求
type KBKafkaTopicInput struct {
	Name              string            `json:"name"`
	Partitions        int               `json:"partitions"`
	ReplicationFactor int               `json:"replicationFactor"`
	Configs           map[string]string `json:"configs"`
}

// kbKafkaTopicArgs 校验并拼建 topic 的参数
func kbKafkaTopicArgs(in *KBKafkaTopicInput) ([]string, error) {
	if !kbKafkaTopicRe.MatchString(in.Name) {
		return nil, fmt.Errorf("topic 名不合法（只允许字母数字 . _ -，长度 ≤248）")
	}
	if in.Partitions < 0 || in.Partitions > 1000 {
		return nil, fmt.Errorf("分区数需在 1-1000")
	}
	if in.ReplicationFactor < 0 || in.ReplicationFactor > 10 {
		return nil, fmt.Errorf("副本数需在 1-10")
	}
	args := []string{"--topic", in.Name}
	if in.Partitions > 0 {
		args = append(args, "--partitions", strconv.Itoa(in.Partitions))
	}
	if in.ReplicationFactor > 0 {
		args = append(args, "--replication-factor", strconv.Itoa(in.ReplicationFactor))
	}
	for _, k := range kbSortedKeys(in.Configs) {
		if !kbKafkaConfigKey.MatchString(k) || !kbKafkaConfigVal.MatchString(in.Configs[k]) {
			return nil, fmt.Errorf("topic 配置键值不合法: %s=%s", k, in.Configs[k])
		}
		args = append(args, "--config", k+"="+in.Configs[k])
	}
	return args, nil
}

// kbSortedKeys 让生成的 argv 稳定（便于测试与审计比对）
func kbSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KBKafkaCreateTopic 建 topic
func KBKafkaCreateTopic(ctx context.Context, c *kube.Client, ns, cluster string, in *KBKafkaTopicInput) (string, error) {
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return "", err
	}
	targs, err := kbKafkaTopicArgs(in)
	if err != nil {
		return "", err
	}
	args := append(kbKafkaBase(ad), append([]string{"--create"}, targs...)...)
	out, err := kbKafkaRun(ctx, c, ns, ad, "kafka-topics.sh", args...)
	return strings.TrimSpace(out), err
}

// KBKafkaDeleteTopic 删除 topic（不可撤销，前端二次确认）
func KBKafkaDeleteTopic(ctx context.Context, c *kube.Client, ns, cluster, topic string) (string, error) {
	if !kbKafkaTopicRe.MatchString(topic) {
		return "", fmt.Errorf("topic 名不合法")
	}
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return "", err
	}
	out, err := kbKafkaRun(ctx, c, ns, ad, "kafka-topics.sh",
		append(kbKafkaBase(ad), "--delete", "--topic", topic)...)
	return strings.TrimSpace(out), err
}

// KBKafkaAlterConfigs 改 topic 配置：set 逐项 --add-config，unset 一次 --delete-config
func KBKafkaAlterConfigs(ctx context.Context, c *kube.Client, ns, cluster, topic string, set map[string]string, unset []string) (string, error) {
	if !kbKafkaTopicRe.MatchString(topic) {
		return "", fmt.Errorf("topic 名不合法")
	}
	if len(set) == 0 && len(unset) == 0 {
		return "", fmt.Errorf("没有要改的配置")
	}
	args := []string{"--entity-type", "topics", "--entity-name", topic, "--alter"}
	for _, k := range kbSortedKeys(set) {
		if !kbKafkaConfigKey.MatchString(k) || !kbKafkaConfigVal.MatchString(set[k]) {
			return "", fmt.Errorf("配置键值不合法: %s=%s", k, set[k])
		}
		args = append(args, "--add-config", k+"="+set[k])
	}
	if len(unset) > 0 {
		for _, k := range unset {
			if !kbKafkaConfigKey.MatchString(k) {
				return "", fmt.Errorf("配置键不合法: %s", k)
			}
		}
		args = append(args, "--delete-config", strings.Join(unset, ","))
	}
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return "", err
	}
	out, err := kbKafkaRun(ctx, c, ns, ad, "kafka-configs.sh", append(kbKafkaBase(ad), args...)...)
	return strings.TrimSpace(out), err
}

// ---------- 消费组 ----------

type KBKafkaGroup struct {
	Group     string `json:"group"`
	State     string `json:"state"`
	Members   int    `json:"members"`
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Current   string `json:"currentOffset"`
	LogEnd    string `json:"logEndOffset"`
	Lag       string `json:"lag"`
}

// KBKafkaGroups 消费组概览 + 分区 lag 明细
func KBKafkaGroups(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBKafkaGroup, error) {
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return nil, err
	}
	out, err := kbKafkaRun(ctx, c, ns, ad, "kafka-consumer-groups.sh",
		append(kbKafkaBase(ad), "--describe", "--all-groups")...)
	if err != nil {
		return nil, err
	}
	return kbParseKafkaGroups(out), nil
}

// kbParseKafkaGroups kafka-consumer-groups.sh --describe --all-groups 输出两张表：
// 概要表头 `GROUP COORDINATOR ... STATE #MEMBERS`，明细表头 `GROUP TOPIC PARTITION
// CURRENT-OFFSET LOG-END-OFFSET LAG CONSUMER-ID HOST CLIENT-ID`。按表头列数区分。
func kbParseKafkaGroups(out string) []KBKafkaGroup {
	rows := []KBKafkaGroup{}
	detail := false
	for _, ln := range strings.Split(out, "\n") {
		cols := strings.Fields(ln)
		if len(cols) == 0 {
			continue
		}
		if cols[0] == "GROUP" {
			detail = len(cols) > 1 && cols[1] == "TOPIC"
			continue
		}
		if strings.HasPrefix(cols[0], "Note") || strings.HasPrefix(cols[0], "Error") {
			continue
		}
		g := KBKafkaGroup{Group: cols[0]}
		if detail {
			// 前六列取值都不含空格，可按位取：GROUP TOPIC PARTITION CURRENT-OFFSET LOG-END-OFFSET LAG
			if len(cols) > 1 {
				g.Topic = cols[1]
			}
			if len(cols) > 2 {
				g.Partition, _ = strconv.Atoi(cols[2])
			}
			if len(cols) > 3 {
				g.Current = cols[3]
			}
			if len(cols) > 4 {
				g.LogEnd = cols[4]
			}
			if len(cols) > 5 {
				g.Lag = cols[5]
			}
		} else {
			// 概要表的列数随版本变，且 COORDINATOR 的值本身带空格（"ip:9092 (ip)"）——
			// 只有状态是固定枚举，按它定位再取后一列的成员数最稳
			for i, col := range cols {
				if !kbGroupStates[col] {
					continue
				}
				g.State = col
				for _, after := range cols[i+1:] { // STATE 与 #MEMBERS 之间还有 MEMBER_TYPE
					if n, err := strconv.Atoi(after); err == nil {
						g.Members = n
						break
					}
				}
				break
			}
		}
		rows = append(rows, g)
	}
	return rows
}

// kbGroupStates Kafka 的 ConsumerGroupState 枚举
var kbGroupStates = map[string]bool{
	"Empty": true, "Assigning": true, "Stable": true, "Dead": true,
	"PreparingRebalance": true, "CompletingRebalance": true, "Unknown": true,
}

// ---------- 消息预览 ----------

// KBKafkaConsumeInput 消息预览请求
type KBKafkaConsumeInput struct {
	Topic         string `json:"topic"`
	Count         int    `json:"count"`
	FromBeginning bool   `json:"fromBeginning"`
	Partition     *int   `json:"partition"`
	AtOffset      *int64 `json:"atOffset"`
}

// KBKafkaConsume 有界拉取若干条消息（8s 超时、≤200 条）
func KBKafkaConsume(ctx context.Context, c *kube.Client, ns, cluster string, in *KBKafkaConsumeInput) (string, error) {
	if !kbKafkaTopicRe.MatchString(in.Topic) {
		return "", fmt.Errorf("topic 名不合法")
	}
	n := in.Count
	if n <= 0 {
		n = 20
	}
	if n > kbKafkaMaxMessages {
		return "", fmt.Errorf("单次最多预览 %d 条", kbKafkaMaxMessages)
	}
	ad, err := KBKafkaAdminFor(ctx, c, ns, cluster)
	if err != nil {
		return "", err
	}
	args := append(kbKafkaBase(ad),
		"--topic", in.Topic,
		"--max-messages", strconv.Itoa(n),
		"--timeout-ms", "8000",
		"--property", "print.timestamp=true",
		"--property", "print.key=true",
		"--property", "print.partition=true")
	if in.FromBeginning {
		args = append(args, "--from-beginning")
	}
	if in.Partition != nil {
		args = append(args, "--partition", strconv.Itoa(*in.Partition))
		if in.AtOffset != nil {
			args = append(args, "--offset", strconv.FormatInt(*in.AtOffset, 10))
		} else {
			args = append(args, "--offset", "-1") // -1 = log-end-offset（只等增量）
		}
	}
	return kbKafkaRun(ctx, c, ns, ad, "kafka-console-consumer.sh", args...)
}
