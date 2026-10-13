package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// promScalar 造一条标量查询结果（value[1] 是字符串，与 Prometheus 一致）
func promScalar(v any) string {
	b, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{
		"resultType": "vector",
		"result":     []any{map[string]any{"metric": map[string]string{}, "value": []any{float64(time.Now().Unix()), fmt.Sprint(v)}}},
	}})
	return string(b)
}

// 集群总览的 PV 口径不能混用：数量与总容量只有 kube-state-metrics 的 PV 容量是对的，
// kubelet 的 volume stats 报的是卷所在**文件系统**的容量（local-path / 共享盘会把整块节点盘
// 算进来，实测 17 个 20 Gi 的卷各上报 187 Gi，求和 3178 Gi 而真值 464 Gi）。
// 这里按指标名分别回值，一旦有人把总容量换回 kubelet 口径，数值就会立刻对不上。
func TestOverviewPVUsesKubeStateCapacity(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q == `count(kube_persistentvolume_capacity_bytes)`:
			fmt.Fprint(w, promScalar(17))
		case q == `sum(kube_persistentvolume_capacity_bytes) / 1024 / 1024 / 1024`:
			fmt.Fprint(w, promScalar(464))
		case q == `sum(kubelet_volume_stats_used_bytes) / sum(kubelet_volume_stats_capacity_bytes) * 100`:
			fmt.Fprint(w, promScalar(32.6))
		default:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer srv.Close()

	m := NewMonitorService(nil)
	c := &kube.Client{Config: &rest.Config{Host: srv.URL}}
	res, err := m.Overview(context.Background(), c, &model.Cluster{}, "1h")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if res.PvCount != 17 {
		t.Errorf("PV 数量应取 kube-state 的 PV 数（17），实际 %d", res.PvCount)
	}
	if res.PvTotalGi != 464 {
		t.Errorf("PV 总容量应为 464 Gi，实际 %v（拿成 kubelet 挂载点口径就会对不上）", res.PvTotalGi)
	}
	if diff := res.PvUsedPct - 32.6; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("已用率仍应走 kubelet volume stats（32.6），实际 %v", res.PvUsedPct)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range seen {
		// 允许 kubelet 容量只出现在"已用率"这一条除法里；单独求和就是回到老错误
		if q == `sum(kubelet_volume_stats_capacity_bytes) / 1024 / 1024 / 1024` ||
			q == `count(kubelet_volume_stats_capacity_bytes)` {
			t.Errorf("PV 数量/总容量不该用 kubelet volume stats 口径: %s", q)
		}
	}
}
