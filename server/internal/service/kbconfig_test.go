package service

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kube-console/server/internal/kube"
)

// schemaInJSON 的参数在 topLevelKey 包裹下（实测 apecloud-mysql8.0-pd 形态）
func kbMySQLPD() *unstructured.Unstructured {
	schema := `{"type":"object","properties":{"MysqlParameter":{"type":"object","properties":{
		"max_connections":{"type":"integer","default":151,"minimum":1,"maximum":100000,"description":"连接数上限"},
		"character_set_server":{"type":"string","default":"utf8mb4","enum":["utf8mb4","latin1"]},
		"read_only":{"type":"boolean","default":false},
		"datadir":{"type":"string","description":"数据目录"}
	}}}}`
	return kbUD("mysql-8.0-pd", kbPDGVR, "ParametersDefinition", "", map[string]any{
		"spec": map[string]any{
			"fileName":            "my.cnf",
			"parametersSchema":    map[string]any{"schemaInJSON": schema, "topLevelKey": "MysqlParameter"},
			"dynamicParameters":   []any{"max_connections", "read_only"},
			"staticParameters":    []any{"character_set_server"},
			"immutableParameters": []any{"datadir"},
			"reloadAction":        map[string]any{"unixSignalTrigger": map[string]any{"processName": "mysqld", "signal": "SIGHUP"}},
		},
	})
}

func kbClusterForConfig() *unstructured.Unstructured {
	return kbUD("db", kbClusterGVR, "Cluster", "app", map[string]any{"spec": map[string]any{
		"componentSpecs": []any{map[string]any{"name": "mysql", "componentDef": "mysql-8.0-1.0.2", "replicas": int64(1)}},
	}})
}

func kbPCR() *unstructured.Unstructured {
	return kbUD("mysql-8.0-pcr", kbPCRGVR, "ParamConfigRenderer", "", map[string]any{
		"spec": map[string]any{"componentDef": "mysql-8.0-1.0.2", "parametersDefs": []any{"mysql-8.0-pd"}},
	})
}

func kbCompParam() *unstructured.Unstructured {
	return kbUD("db-mysql", kbCPGVR, "ComponentParameter", "app", map[string]any{
		"spec": map[string]any{
			"clusterName": "db", "componentName": "mysql",
			"configItemDetails": []any{map[string]any{
				"name": "mysql-replication-config",
				"configFileParams": map[string]any{
					"my.cnf": map[string]any{"parameters": map[string]any{
						"max_connections": int64(500), "auto_cn": "on",
					}},
				},
			}},
		},
	})
}

// 全链路：compDef → ParamConfigRenderer → ParametersDefinition schema + ComponentParameter 当前值
func TestKBGetComponentConfigResolvesChain(t *testing.T) {
	dyn := kbFakeDyn(kbClusterForConfig(), kbPCR(), kbMySQLPD(), kbCompParam())
	out, err := KBGetComponentConfig(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", "mysql")
	if err != nil {
		t.Fatalf("KBGetComponentConfig: %v", err)
	}
	if len(out.Files) != 1 || out.Files[0].Name != "my.cnf" {
		t.Fatalf("文件没解析出来: %+v", out.Files)
	}
	f := out.Files[0]
	if f.Reload != "Dynamic" {
		t.Errorf("unixSignalTrigger 应识别为 Dynamic，实际 %s", f.Reload)
	}
	byName := map[string]KBParam{}
	for _, p := range f.Params {
		byName[p.Name] = p
	}
	mc := byName["max_connections"]
	if !mc.Dynamic || mc.Default != "151" || mc.Min != "1" || mc.Max != "100000" || mc.Current != "500" {
		t.Errorf("动态参数解析不对: %+v", mc)
	}
	cs := byName["character_set_server"]
	if cs.Dynamic || cs.Default != "utf8mb4" || len(cs.Enum) != 2 {
		t.Errorf("静态参数解析不对: %+v", cs)
	}
	if !byName["datadir"].Immutable {
		t.Errorf("datadir 应标记不可变")
	}
	// schema 没声明但当前值里有的键要保留可编辑
	if p, ok := byName["auto_cn"]; !ok || p.InSchema || p.Current != "on" {
		t.Errorf("schema 外的键应保留（InSchema=false）: %+v", byName["auto_cn"])
	}
	// 整数值渲染不能出浮点
	if strings.Contains(mc.Default, ".") {
		t.Errorf("整型默认值不应带小数点: %s", mc.Default)
	}
}

// 没有渲染器/参数定义的组件降级：返回空表单 + 说明，不报错
func TestKBGetComponentConfigWithoutPD(t *testing.T) {
	dyn := kbFakeDyn(kbClusterForConfig())
	out, err := KBGetComponentConfig(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", "mysql")
	if err != nil {
		t.Fatalf("无 PD 应降级不报错: %v", err)
	}
	if len(out.Files) != 0 || out.Note == "" {
		t.Errorf("应有说明文案: %+v", out)
	}
}

// 提交校验：不可变/未知键/枚举/布尔
func TestKBValidateParams(t *testing.T) {
	dyn := kbFakeDyn(kbClusterForConfig(), kbPCR(), kbMySQLPD(), kbCompParam())
	schema, err := KBGetComponentConfig(context.Background(), &kube.Client{Dynamic: dyn}, "app", "db", "mysql")
	if err != nil {
		t.Fatalf("schema 获取失败: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{"max_connections": "800"}); err != nil {
		t.Fatalf("合法修改不应报错: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{"datadir": "/var/lib/data"}); err == nil || !strings.Contains(err.Error(), "不可变") {
		t.Errorf("不可变参数应被拦: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{"no_such_param": "1"}); err == nil || !strings.Contains(err.Error(), "no_such_param") {
		t.Errorf("未知参数应被拦: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{"character_set_server": "gbk"}); err == nil || !strings.Contains(err.Error(), "utf8mb4") {
		t.Errorf("枚举外取值应被拦: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{"read_only": "maybe"}); err == nil || !strings.Contains(err.Error(), "布尔") {
		t.Errorf("布尔参数应被拦: %v", err)
	}
	if err := KBValidateParams(schema, map[string]string{}); err == nil {
		t.Errorf("空参数应被拦")
	}
}
