package service

import (
	"strings"
	"testing"

	"kube-console/server/internal/model"
)

func TestLogMountDir(t *testing.T) {
	cases := []struct {
		path string
		want string
		bad  bool
	}{
		{"/var/log/app/*.log", "/var/log/app", false},
		{"/var/log/app.log", "/var/log", false},
		{"/data/logs/sub/rolling-*.log", "/data/logs/sub", false},
		{"relative/app.log", "", true},
		{"/*.log", "", true},
	}
	for _, c := range cases {
		got, err := logMountDir(c.path)
		if c.bad {
			if err == nil {
				t.Errorf("logMountDir(%q) 应报错，得到 %q", c.path, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("logMountDir(%q) = %q, %v; want %q", c.path, got, err, c.want)
		}
	}
}

func TestEsCollectTarget(t *testing.T) {
	// 默认前缀 + 集群内 service：text 走行日志索引，json 走 JSON 索引
	line := &model.LogSource{Namespace: "logging", Service: "es-http", Port: 9200, IndexPrefix: "k8s-"}
	host, port, tls, index, err := esCollectTarget(line, "dev", "text")
	if err != nil || host != "es-http.logging.svc.cluster.local" || port != 9200 || tls || index != "k8s-dev" {
		t.Fatalf("text 应写行日志索引 k8s-dev，得到 %q %q %d %v %v", host, index, port, tls, err)
	}
	_, _, _, index, err = esCollectTarget(line, "dev", "json")
	if err != nil || index != "logstash-dev" {
		t.Fatalf("json 应写 JSON 索引 logstash-dev，得到 %q %v", index, err)
	}
	// 两个前缀都配置时互不干扰
	line.JsonIndexPrefix = "appjson-"
	if _, _, _, index, _ = esCollectTarget(line, "dev", "json"); index != "appjson-dev" {
		t.Errorf("json 前缀应为 appjson-dev，得到 %q", index)
	}
	if _, _, _, index, _ = esCollectTarget(line, "dev", "text"); index != "k8s-dev" {
		t.Errorf("行日志前缀不受 json 影响，得到 %q", index)
	}
	// 未配置任何前缀时两者同兜底 logstash-
	if _, _, _, index, _ = esCollectTarget(&model.LogSource{Namespace: "l", Service: "s", Port: 1}, "dev", "text"); index != "logstash-dev" {
		t.Errorf("空前缀兜底 logstash-dev，得到 %q", index)
	}
	// 直连 https + {namespace} 占位符
	host, port, tls, index, err = esCollectTarget(&model.LogSource{
		Namespace: "x", Service: "y", Port: 1, DirectURL: "https://10.0.0.8:9200", IndexPrefix: "logs-{namespace}-",
	}, "Prod", "text")
	if err != nil || host != "10.0.0.8" || port != 9200 || !tls || index != "logs-prod" {
		t.Fatalf("direct 模式解析错误: %q %d %v %q %v", host, port, tls, index, err)
	}
}

func TestLogIndexPatterns(t *testing.T) {
	src := &model.LogSource{IndexPrefix: "k8s-", JsonIndexPrefix: "logstash-"}
	cases := []struct {
		source, ns string
		want       string
	}{
		{"", "", "k8s-*,logstash-*"},
		{"", "dev", "k8s-*,logstash-*"},
		{"stdout", "dev", "k8s-*"},
		{"file", "dev", "k8s-*"},
		{"json", "dev", "logstash-*"},
		{"json", "", "logstash-*"},
	}
	for _, c := range cases {
		if got := strings.Join(logIndexPatterns(src, c.source, c.ns), ","); got != c.want {
			t.Errorf("patterns(%q,%q)=%q want %q", c.source, c.ns, got, c.want)
		}
	}
	// 两前缀同源（都兜底）时去重；{namespace} 占位符按命名空间展开
	if got := strings.Join(logIndexPatterns(&model.LogSource{}, "", "dev"), ","); got != "logstash-*" {
		t.Errorf("两前缀相同时应去重，得到 %q", got)
	}
	ph := &model.LogSource{IndexPrefix: "k8s-{namespace}-"}
	if got := strings.Join(logIndexPatterns(ph, "stdout", "Dev"), ","); got != "k8s-dev*" {
		t.Errorf("占位符应展开为 k8s-dev*，得到 %q", got)
	}
	if got := strings.Join(logIndexPatterns(ph, "stdout", ""), ","); got != "k8s-*" {
		t.Errorf("不限命名空间应展开为 k8s-*，得到 %q", got)
	}
}

func TestFluentBitConfEnvSyntax(t *testing.T) {
	// fluent-bit 仅插值 ${VAR}；写成 $VAR 会把字面量当凭证发出导致 ES 401
	conf := fluentBitConf(&LogCollectionInput{Container: "app", LogPath: "/var/log/app/*.log", Format: "text"},
		"dev", "api", "logstash-dev", "es-http.logging.svc.cluster.local", 9200, false, true)
	for _, want := range []string{"${KC_ES_USER}", "${KC_ES_PASS}", "${HOSTNAME}"} {
		if !strings.Contains(conf, want) {
			t.Errorf("配置缺少 %s:\n%s", want, conf)
		}
	}
	for _, bad := range []string{"$KC_ES_", "$HOSTNAME\n"} {
		if strings.Contains(conf, bad) {
			t.Errorf("配置含未加花括号的 %s:\n%s", bad, conf)
		}
	}
}

// json 格式必须保留原始行到 log：检索侧只渲染 log/message/msg，INPUT Parser 会把它抹掉
func TestFluentBitConfJsonKeepsRawLog(t *testing.T) {
	conf := fluentBitConf(&LogCollectionInput{Container: "apisix", LogPath: "/var/log/access.json", Format: "json"},
		"prd-public-service", "apisix", "k8s-prd-public-service", "es-http.logging.svc.cluster.local", 9200, false, false)
	if !strings.Contains(conf, "    Key_Name        log\n") || !strings.Contains(conf, "    Preserve_Key    On\n") {
		t.Errorf("json 格式缺少 filter-parser 保留 log 原文:\n%s", conf)
	}
	// filter-parser 合法属性仅 Key_Name/Parser/Preserve_Key/Reserve_Data/Unescape_key，
	// 写错名字 fluent-bit 会直接启动失败
	if strings.Contains(conf, "Preserve_Data") {
		t.Errorf("Preserve_Data 不是 filter-parser 的属性:\n%s", conf)
	}
	if strings.Contains(conf, "    Parser") && strings.Contains(conf, "[INPUT]") {
		input := conf[strings.Index(conf, "[INPUT]"):strings.Index(conf, "[FILTER]")]
		if strings.Contains(input, "json-parser") {
			t.Errorf("INPUT 不应直接 Parser，原始行会丢失:\n%s", input)
		}
	}
	// 来源筛选依赖写入格式标记
	if !strings.Contains(conf, "kube-console.log-format json") {
		t.Errorf("配置缺少格式标记字段:\n%s", conf)
	}
}

func TestSourceFilters(t *testing.T) {
	must, mustNot := sourceFilters("stdout")
	if len(must) != 0 || len(mustNot) != 1 {
		t.Errorf("stdout 应只排除采集标记，得到 must=%d mustNot=%d", len(must), len(mustNot))
	}
	must, mustNot = sourceFilters("file")
	if len(must) != 1 || len(mustNot) != 1 {
		t.Errorf("file 需采集标记且排除 json，得到 must=%d mustNot=%d", len(must), len(mustNot))
	}
	must, mustNot = sourceFilters("json")
	if len(must) != 1 || len(mustNot) != 0 {
		t.Errorf("json 只按格式标记过滤，得到 must=%d mustNot=%d", len(must), len(mustNot))
	}
	must, mustNot = sourceFilters("")
	if must != nil || mustNot != nil {
		t.Errorf("全部来源不加标记筛选，得到 %v %v", must, mustNot)
	}
}
