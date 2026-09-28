package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// kafka-topics.sh --describe 的真实排版（制表符分隔）
const kafkaTopicsOut = `Topic: order-events	PartitionCount: 3	ReplicationFactor: 2	Configs: cleanup.policy=delete,retention.ms=604800000
Topic: order-events	Partition: 0	Leader: 1	Replicas: 1,3	Isr: 1,3
Topic: order-events	Partition: 1	Leader: 3	Replicas: 3,2	Isr: 3,2
Topic: order-events	Partition: 2	Leader: none	Replicas: 2,1	Isr: 	Elapsed: 100
Topic: pay-logs	PartitionCount: 1	ReplicationFactor: 1	Configs: None
Topic: pay-logs	Partition: 0	Leader: 2	Replicas: 2	Isr: 2
`

func TestKBParseKafkaTopics(t *testing.T) {
	got := kbParseKafkaTopics(kafkaTopicsOut)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 个 topic，实际 %d", len(got))
	}
	if got[0].Name != "order-events" || got[1].Name != "pay-logs" {
		t.Fatalf("topic 名/顺序不对: %v", []string{got[0].Name, got[1].Name})
	}
	oe := got[0]
	if oe.PartitionCount != 3 || oe.ReplicationFactor != "2" {
		t.Errorf("概要不对: %+v", oe)
	}
	if oe.Configs["cleanup.policy"] != "delete" || oe.Configs["retention.ms"] != "604800000" {
		t.Errorf("Configs 没拆开: %+v", oe.Configs)
	}
	if len(oe.Partitions) != 3 {
		t.Fatalf("分区明细 %d 条", len(oe.Partitions))
	}
	if oe.Partitions[0].Leader != "1" || !reflect.DeepEqual(oe.Partitions[0].Replicas, []string{"1", "3"}) {
		t.Errorf("分区 0 解析错: %+v", oe.Partitions[0])
	}
	// Isr 为空的行不能被吞掉，Leader=none 要标成 offline
	if oe.Partitions[2].Offline != true || len(oe.Partitions[2].ISR) != 0 {
		t.Errorf("offline/ISR 判定错: %+v", oe.Partitions[2])
	}
	if len(got[1].Configs) != 0 {
		t.Errorf("Configs: None 应留空，实际 %+v", got[1].Configs)
	}
}

func TestKBParseKafkaGroups(t *testing.T) {
	out := `GROUP          COORDINATOR (HOST)           PORT  NODE_ID  STATE        MEMBER_TYPE  #MEMBERS  ASSIGNMENT_STRATEGY  PROTOCOL_TYPE  PROTOCOL  GROUP_STATE
order-workers  10.0.0.11:9092 (10.0.0.11)  9092  1        Stable       Classic      2           range                consumer       range       -
GROUP          TOPIC       PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG  CONSUMER-ID                                   HOST            CLIENT-ID
order-workers  order-events  0          120             133             13   consumer-1-abc /10.0.0.5                      10.0.0.5        consumer-1
order-workers  order-events  1          88              88              0    -                                             -               -
Note: This group has no committed offset.
`
	rows := kbParseKafkaGroups(out)
	if len(rows) != 3 {
		t.Fatalf("应有 1 条概要 + 2 条明细，实际 %d: %+v", len(rows), rows)
	}
	sum := rows[0]
	if sum.Group != "order-workers" || sum.State != "Stable" || sum.Members != 2 {
		t.Errorf("概要解析错: %+v", sum)
	}
	d := rows[1]
	if d.Topic != "order-events" || d.Partition != 0 || d.Current != "120" || d.LogEnd != "133" || d.Lag != "13" {
		t.Errorf("明细解析错: %+v", d)
	}
	if rows[2].Lag != "0" {
		t.Errorf("第二条 lag 应为 0，实际 %+v", rows[2])
	}
}

func TestKBKafkaBootstrap(t *testing.T) {
	cases := []struct {
		env   map[string]string
		ports map[string]int32
		want  string
	}{
		{nil, map[string]int32{"kafka-client": 9092}, "localhost:9092"},
		{map[string]string{"KAFKA_CFG_LISTENERS": "CONTROLLER://:9093,INTERNAL://:9094,CLIENT://:9095"}, nil, "localhost:9095"},
		{map[string]string{"KAFKA_CFG_LISTENERS": "INTERNAL://:9094"}, nil, "localhost:9092"},
		{nil, nil, "localhost:9092"},
	}
	for _, c := range cases {
		if got := kbKafkaBootstrap(c.env, c.ports); got != c.want {
			t.Errorf("env=%v ports=%v -> %s，期望 %s", c.env, c.ports, got, c.want)
		}
	}
}

func TestKBKafkaTopicArgs(t *testing.T) {
	args, err := kbKafkaTopicArgs(&KBKafkaTopicInput{Name: "a.b-c_1", Partitions: 3, ReplicationFactor: 2,
		Configs: map[string]string{"cleanup.policy": "delete", "retention.bytes": "-1"}})
	if err != nil {
		t.Fatalf("合法输入报错: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--topic a.b-c_1", "--partitions 3", "--replication-factor 2", "--config cleanup.policy=delete", "--config retention.bytes=-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("参数缺 %q: %s", want, joined)
		}
	}
	// 顺序稳定（map 遍历随机会让审计/测试不稳）
	if args[len(args)-2] != "--config" || args[len(args)-1] != "retention.bytes=-1" {
		t.Errorf("配置未按键排序: %v", args)
	}
	for _, bad := range []*KBKafkaTopicInput{
		{Name: "bad;rm -rf"},
		{Name: "a b"},
		{Name: ""},
		{Name: "ok", Partitions: 5000},
		{Name: "ok", ReplicationFactor: 99},
		{Name: "ok", Configs: map[string]string{"retention.ms": "1 2"}},
		{Name: "ok", Configs: map[string]string{"a=b": "1"}},
	} {
		if _, err := kbKafkaTopicArgs(bad); err == nil {
			t.Errorf("非法输入未报错: %+v", bad)
		}
	}
}

func TestKBESIndexNameRe(t *testing.T) {
	for _, ok := range []string{"kibana", ".kibana_1", "app-2026.09.24", "a+b"} {
		if !kbESIndexRe.MatchString(ok) {
			t.Errorf("%s 应合法", ok)
		}
	}
	for _, bad := range []string{"a b", "a/b", `a?x=1`, "a&b", "*", "", strings.Repeat("x", 256)} {
		if kbESIndexRe.MatchString(bad) {
			t.Errorf("%q 应被拒", bad)
		}
	}
}

// settings 压平成 index.number_of_shards 这种点路径；mapping 只收叶子字段
func TestKBESFlatten(t *testing.T) {
	var set map[string]any
	_ = json.Unmarshal([]byte(`{"index":{"number_of_shards":"1","routing":{"allocation":{"include":{"rack":"1"}}},"blocks":{"read_only":false}}}`), &set)
	out := map[string]string{}
	flattenES(set, "", out)
	if out["index.number_of_shards"] != "1" {
		t.Errorf("点路径没压出来: %+v", out)
	}
	if out["index.blocks.read_only"] != "false" {
		t.Errorf("布尔没转字符串: %+v", out)
	}
	if out["index.routing.allocation.include.rack"] != "1" {
		t.Errorf("深层没压平: %+v", out)
	}

	var mp map[string]any
	_ = json.Unmarshal([]byte(`{"properties":{"ts":{"type":"date"},"n":{"type":"integer"},"user":{"properties":{"id":{"type":"keyword"},"addr":{"properties":{"city":{"type":"text"}}}}}}}`), &mp)
	fields := []KBESField{}
	flattenESMapping(mp, "", &fields)
	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Type
	}
	if got["ts"] != "date" || got["n"] != "integer" || got["user.id"] != "keyword" || got["user.addr.city"] != "text" {
		t.Errorf("字段展平错: %+v", got)
	}
	if _, ok := got["user"]; ok {
		t.Errorf("对象容器不该作为字段出现: %+v", got)
	}
}

// 页面 DSL 是文本框内容 → JSON 里是字符串；也要兼容直接传对象。
// 早先只按对象解，查询页固定报 cannot unmarshal string into map。
func TestKBESUnwrapBody(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		err  bool
	}{
		{"文本框里的 DSL", `"{\"query\":{\"match_all\":{}}}"`, `{"query":{"match_all":{}}}`, false},
		{"直接给对象", `{"query":{"term":{"a":1}}}`, `{"query":{"term":{"a":1}}}`, false},
		{"空", ``, ``, false},
		{"空字符串", `""`, ``, false},
		{"对象形态不校验（交给上层按对象解时报错）", `{"query":}`, `{"query":}`, false},
		{"字符串字面量本身残缺", `"abc`, ``, true},
	}
	for _, c := range cases {
		got, err := kbESUnwrapBody(json.RawMessage(c.raw))
		if c.err {
			if err == nil {
				t.Errorf("%s: 应报错", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 意外报错 %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s: 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
}

// 脚本里只引用 $ENV 与位置参数，不含任何用户可控拼接
func TestKBESCurlScriptIsFixed(t *testing.T) {
	for _, want := range []string{"${ELASTIC_PASSWORD", `path="$1"`, "shift", "127.0.0.1:9200"} {
		if !strings.Contains(kbESCurlScript, want) {
			t.Errorf("脚本缺 %q", want)
		}
	}
}

// echo 恒成功，兜底必须逐档判文件存在，否则社区镜像的 /opt/kafka/bin 永远取不到
func TestKBKafkaRunnerFallsBackByFileCheck(t *testing.T) {
	r := kbKafkaRunner("kafka-topics.sh")
	for _, want := range []string{"command -v", "/opt/bitnami/kafka/bin/", "/opt/kafka/bin/", `[ -f "$b" ]`, `exec "$b" "$@"`} {
		if !strings.Contains(r, want) {
			t.Errorf("runner 缺 %q: %s", want, r)
		}
	}
	if strings.Contains(r, "|| echo /opt") {
		t.Errorf("不该用 `|| echo` 做兜底: %s", r)
	}
	// 必须先清 JMX 变量，否则客户端 JVM 会去 bind 已被 broker 占用的 5555
	if !strings.Contains(r, "grep -i jmx") || !strings.Contains(r, `unset "$v"`) {
		t.Errorf("runner 未清理 JMX 环境变量: %s", r)
	}
}

// 单测内不允许 spawn 进程（sh -n 语法校验与桩脚本真跑已移除），runner 是常量文本，
// 改为钉住 JMX 清理段的关键片段，并用 RE2 复验两个 sed 表达式的匹配语义
// （这两条 BRE 无反向引用/分组，是 RE2 的子集，可机械翻译后验证）。
func TestKBKafkaRunnerClearsJMX(t *testing.T) {
	r := kbKafkaRunner("kafka-topics.sh")
	for _, want := range []string{
		// unset 一切名字含 jmx 的环境变量（broker 导出的 JMX_PORT/KAFKA_JMX_OPTS 等全灭）
		`for v in $(env | cut -d= -f1 | grep -i jmx); do unset "$v"; done`,
		// 再扫五个透传变量，case 命中才动刀
		"KAFKA_OPTS KAFKA_CLIENT_OPTS KAFKA_JVM_PERFORMANCE_OPTS EXTRA_JVM_ARGUMENTS JAVA_TOOL_OPTIONS",
		`*jmxremote*|*rmi.server.hostname*`,
		`sed -e 's/-D[a-z.]*jmxremote[^ ]*//g' -e 's/-Djava\.rmi\.server\.hostname=[^ ]*//g'`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("JMX 清理段缺失/被改: %q\n%s", want, r)
		}
	}
	jmx := regexp.MustCompile(`-D[a-z.]*jmxremote[^ ]*`)
	rmi := regexp.MustCompile(`-Djava\.rmi\.server\.hostname=[^ ]*`)
	stripped := strings.TrimSpace(jmx.ReplaceAllString(rmi.ReplaceAllString(
		"-Dcom.sun.management.jmxremote.port=5555 -Djava.rmi.server.hostname=1.2.3.4 -Xmx512m", ""), ""))
	if stripped != "-Xmx512m" {
		t.Errorf("应剔掉 JMX 参数保留其余 JVM 参数，实际: %q", stripped)
	}
	if intact := jmx.ReplaceAllString(rmi.ReplaceAllString("-Xms1g -Xmx512m", ""), ""); intact != "-Xms1g -Xmx512m" {
		t.Errorf("不含 JMX 的参数不应被改: %q", intact)
	}
}

// 报错文案要剔掉 JMX/log4j 之类无关噪音，挑真正的失败原因
func TestKafkaErrTextSkipsNoise(t *testing.T) {
	noise := `Error: JMX connector server communication error: service:jmx:rmi://kafka-broker-0:5555
jdk.internal.agent.AgentConfigurationError: java.rmi.server.ExportException: Port already in use: 5555
Caused by: java.net.BindException: Address already in use
`
	real := `Error while executing topic command : Replication factor: 3 larger than available brokers: 2
[ERROR] org.apache.kafka.common.errors.InvalidReplicationFactorException`
	if got := kafkaErrText(errors.New("command terminated with exit code 1"), "", noise+"\n"+real); !strings.Contains(got, "InvalidReplicationFactorException") {
		t.Errorf("应取真实报错，实际: %q", got)
	}
	if got := kafkaErrText(errors.New("exit status 1"), "", noise); got != "exit status 1" {
		t.Errorf("只剩噪音时应回落到 exec 错误，实际: %q", got)
	}
	if got := kafkaErrText(errors.New("boom"), "broker rejected the request", ""); !strings.Contains(got, "broker rejected") {
		t.Errorf("stderr 优先，实际: %q", got)
	}
}

// 噪音判定要盖住 JMX/RMI/log4j/端口冲突这几类
func TestKBKafkaNoise(t *testing.T) {
	for _, ln := range []string{
		"Error: JMX connector server communication error",
		"Cause: java.rmi.server.ExportException: Port already in use",
		"log4j:WARN No appenders could be found.",
		"WARNING: A terminally deprecated method in java.lang.System",
	} {
		if !kbKafkaNoise(ln) {
			t.Errorf("应判为噪音: %s", ln)
		}
	}
	for _, ln := range []string{
		"Error while executing topic command : Replication factor: 3 larger than available brokers: 2",
		"org.apache.kafka.common.errors.TimeoutException: Timed out waiting for a node assignment",
	} {
		if kbKafkaNoise(ln) {
			t.Errorf("不该判为噪音: %s", ln)
		}
	}
}

// 用户输入只能作为独立 argv（$1 起）传入，不能拼进脚本字符串
func TestKBWrapShellPassesArgsPositionally(t *testing.T) {
	argv := kbWrapShell(`exec curl "$1"`, "http://x/_cat/indices?v")
	if argv[0] != "sh" || argv[1] != "-c" || argv[3] != "kb" {
		t.Fatalf("argv 结构不对: %v", argv)
	}
	if argv[4] != "http://x/_cat/indices?v" {
		t.Errorf("参数没按位置传: %v", argv)
	}
	if strings.Contains(argv[2], "http://x") {
		t.Error("参数不该出现在脚本里")
	}
}
