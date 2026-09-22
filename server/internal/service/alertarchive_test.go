// 可观测性扩展单测：告警归档状态机、事件归档变更检测、AM 配置编解码
package service

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/model"
)

// newTestDB 内存 sqlite + 迁移告警相关表
func newObsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}, &model.AlertmanagerConfig{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func amAlert(fp string, labels map[string]string, startsAt string) AMAlert {
	return AMAlert{
		Fingerprint: fp,
		Labels:      labels,
		StartsAt:    startsAt,
		Status: struct {
			State       string   `json:"state"`
			SilencedBy  []string `json:"silencedBy"`
			InhibitedBy []string `json:"inhibitedBy"`
		}{State: "active"},
	}
}

// 场景：新告警记触发 → 消失判恢复 → 同指纹再现新开记录
func TestSyncAlertEventsLifecycle(t *testing.T) {
	db := newObsTestDB(t)
	started := "2026-09-12T08:00:00Z"
	now1 := time.Now()

	// 1. 首次拉到告警 → 记触发
	alerts := []AMAlert{amAlert("fp1", map[string]string{"alertname": "PodCrashLooping", "severity": "critical", "namespace": "prod"}, started)}
	if err := syncAlertEvents(db, "c1", alerts, now1); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var ev model.AlertEvent
	if err := db.Where("cluster = ? AND fingerprint = ?", "c1", "fp1").First(&ev).Error; err != nil {
		t.Fatalf("应记录触发事件: %v", err)
	}
	if ev.State != model.AlertStateFiring || ev.AlertName != "PodCrashLooping" || ev.Severity != "critical" || ev.Namespace != "prod" {
		t.Errorf("触发记录字段不符: %+v", ev)
	}

	// 2. 告警仍在 → 不新开记录
	if err := syncAlertEvents(db, "c1", alerts, now1.Add(time.Minute)); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var cnt int64
	db.Model(&model.AlertEvent{}).Where("cluster = ?", "c1").Count(&cnt)
	if cnt != 1 {
		t.Fatalf("持续 firing 不应新开记录，当前 %d 条", cnt)
	}

	// 3. 告警从 AM 列表消失 → 判定恢复
	if err := syncAlertEvents(db, "c1", nil, now1.Add(2*time.Minute)); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if err := db.Where("cluster = ? AND fingerprint = ?", "c1", "fp1").First(&ev).Error; err != nil {
		t.Fatalf("记录应保留: %v", err)
	}
	// 短暂消失未过恢复宽限（10min）不判恢复——防 AM 重启/查询异常的单次
	// 空列表批量误判 resolved
	if ev.State != model.AlertStateFiring {
		t.Fatalf("未过宽限不应判恢复: %+v", ev)
	}

	// 4. 持续消失超过宽限 → 判定恢复
	if err := syncAlertEvents(db, "c1", nil, now1.Add(15*time.Minute)); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if err := db.Where("cluster = ? AND fingerprint = ?", "c1", "fp1").First(&ev).Error; err != nil {
		t.Fatalf("记录应保留: %v", err)
	}
	if ev.State != model.AlertStateResolved || ev.ResolvedAt == nil {
		t.Fatalf("应判定恢复: %+v", ev)
	}

	// 5. 同指纹再次 firing → 新开一条
	if err := syncAlertEvents(db, "c1", alerts, now1.Add(16*time.Minute)); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	db.Model(&model.AlertEvent{}).Where("cluster = ?", "c1").Count(&cnt)
	if cnt != 2 {
		t.Fatalf("重复触发应新开记录，当前 %d 条", cnt)
	}
	var openCount int64
	db.Model(&model.AlertEvent{}).Where("cluster = ? AND state = ?", "c1", model.AlertStateFiring).Count(&openCount)
	if openCount != 1 {
		t.Fatalf("应恰好一条开放记录，当前 %d 条", openCount)
	}
}

// 多集群互不干扰 + 无指纹告警跳过
func TestSyncAlertEventsClusterIsolation(t *testing.T) {
	db := newObsTestDB(t)
	now := time.Now()
	alerts := []AMAlert{
		amAlert("fp-a", map[string]string{"alertname": "A"}, "2026-09-12T08:00:00Z"),
		{Labels: map[string]string{"alertname": "NoFP"}}, // 无指纹 → 跳过
	}
	if err := syncAlertEvents(db, "cA", alerts, now); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	// cB 的恢复判定不应影响 cA
	if err := syncAlertEvents(db, "cB", nil, now); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var open int64
	db.Model(&model.AlertEvent{}).Where("cluster = ? AND state = ?", "cA", model.AlertStateFiring).Count(&open)
	if open != 1 {
		t.Fatalf("cA 的开放记录应保留，当前 %d 条", open)
	}
}

func TestEventHash(t *testing.T) {
	ts := metav1.Time{Time: time.Unix(1700000000, 0).UTC()}
	ev := corev1.Event{Count: 3, LastTimestamp: ts}
	if eventHash(ev) != "3|1700000000" {
		t.Errorf("eventHash = %q", eventHash(ev))
	}
	// count 变化 → hash 变化
	ev2 := ev
	ev2.Count = 4
	if eventHash(ev) == eventHash(ev2) {
		t.Error("count 变化后指纹应不同")
	}
}

func TestBuildEventBulk(t *testing.T) {
	ts := metav1.Time{Time: time.Unix(1700000000, 0).UTC()} // 2023-11-14（索引日期）
	events := []corev1.Event{
		{
			ObjectMeta: metav1.ObjectMeta{UID: "u1", Namespace: "prod"},
			Type: "Warning", Reason: "OOMKilled", Count: 2, LastTimestamp: ts,
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "app-1"},
		},
		{ObjectMeta: metav1.ObjectMeta{UID: ""}}, // 无 UID → 跳过
	}
	seen := map[string]string{}
	body, changed, next := buildEventBulk("c1", "kc-events-", events, seen)
	if changed != 1 {
		t.Fatalf("首轮应写入 1 条，实际 %d", changed)
	}
	if !strings.Contains(body, `"kc-events-2023.11.14"`) || !strings.Contains(body, `"c1-u1"`) {
		t.Errorf("bulk 请求体不符: %s", body)
	}
	if next["u1"] == "" {
		t.Error("指纹表应记录 u1")
	}

	// 同一轮数据再来一次 → 无变更
	_, changed2, _ := buildEventBulk("c1", "kc-events-", events, next)
	if changed2 != 0 {
		t.Fatalf("未变化的事件不应重写，实际 %d", changed2)
	}

	// count 增加 → 重写
	events[0].Count = 3
	body3, changed3, _ := buildEventBulk("c1", "kc-events-", events, next)
	if changed3 != 1 || !strings.Contains(body3, `"count":3`) {
		t.Errorf("count 变化应重写，changed=%d body=%s", changed3, body3)
	}
}

func TestEventIndexPrefix(t *testing.T) {
	src := &model.LogSource{EventIndexPrefix: ""}
	if got := eventIndexPrefix(src); got != "kc-events-" {
		t.Errorf("默认前缀 = %q", got)
	}
	src.EventIndexPrefix = "ev-"
	if got := eventIndexPrefix(src); got != "ev-" {
		t.Errorf("自定义前缀 = %q", got)
	}
	src.EventIndexPrefix = "ev-" // 尾部多个 - 应去重
	if eventIndexPrefix(src) != "ev-" {
		t.Errorf("前缀应规范化: %q", eventIndexPrefix(src))
	}
}

func TestAMConfigCodecGzip(t *testing.T) {
	cfg := "global:\n  resolve_timeout: 5m\n"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(cfg))
	zw.Close()

	// gzip 格式解码
	text, key, err := amConfigDecode(map[string][]byte{amConfigKeyGzip: gz.Bytes()})
	if err != nil || text != cfg || key != amConfigKeyGzip {
		t.Fatalf("gzip 解码失败: text=%q key=%q err=%v", text, key, err)
	}
	// 保持 gzip 格式编码回写
	out := map[string][]byte{amConfigKeyGzip: gz.Bytes()}
	if err := amConfigEncode(out, cfg + "\nroute:\n  receiver: default\n"); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if len(out[amConfigKeyGzip]) == 0 || out[amConfigKeyPlain] != nil {
		t.Fatal("应保持 .gz key 写回")
	}
	text2, key2, err := amConfigDecode(out)
	if err != nil || key2 != amConfigKeyGzip || !strings.Contains(text2, "receiver: default") {
		t.Fatalf("gzip 回读失败: %q %q %v", text2, key2, err)
	}
}

func TestAMConfigCodecPlain(t *testing.T) {
	cfg := "route:\n  receiver: default\n"
	// 明文格式
	text, key, err := amConfigDecode(map[string][]byte{amConfigKeyPlain: []byte(cfg)})
	if err != nil || text != cfg || key != amConfigKeyPlain {
		t.Fatalf("明文解码失败: %v", err)
	}
	// 新 Secret（无标准 key）→ 按明文补齐
	out := map[string][]byte{"unrelated": []byte("x")}
	if err := amConfigEncode(out, cfg); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if out[amConfigKeyPlain] == nil {
		t.Fatal("无标准 key 时应按明文补齐")
	}
	// 缺失标准 key → 明确报错
	if _, _, err := amConfigDecode(map[string][]byte{"other": []byte("y")}); err == nil {
		t.Fatal("缺标准 key 应报错")
	}
}

func TestSplitSecretRef(t *testing.T) {
	if ns, name, err := splitSecretRef("monitoring/alertmanager-main"); err != nil || ns != "monitoring" || name != "alertmanager-main" {
		t.Fatalf("正常引用解析失败: %v %v %v", ns, name, err)
	}
	for _, bad := range []string{"", "no-slash", "a/b/c"} {
		if _, _, err := splitSecretRef(bad); err == nil {
			t.Errorf("非法引用 %q 应报错", bad)
		}
	}
}
