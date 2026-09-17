package handler

import "testing"

// 应用 spec 非法（如 spec.project 指向不存在的 AppProject）时 sync/health 恒为 Unknown，
// 原因只在 status.conditions 里——列表要把这条带出去，否则界面只能显示「Unknown」
func TestArgoInvalidSpecError(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want string
	}{
		{"无 conditions", map[string]any{}, ""},
		{"只有别的 condition", map[string]any{"status": map[string]any{"conditions": []any{
			map[string]any{"type": "SyncError", "message": "boom"},
		}}}, ""},
		{"命中 InvalidSpecError", map[string]any{"status": map[string]any{"conditions": []any{
			map[string]any{"type": "ComparisonError", "message": "x"},
			map[string]any{"type": "InvalidSpecError", "message": "Application referencing project qa-service-manager which does not exist"},
		}}}, "Application referencing project qa-service-manager which does not exist"},
		{"conditions 结构异常不 panic", map[string]any{"status": map[string]any{"conditions": "nope"}}, ""},
		{"conditions 元素结构异常不 panic", map[string]any{"status": map[string]any{"conditions": []any{"nope", 42}}}, ""},
	}
	for _, tc := range cases {
		if got := argoInvalidSpecError(tc.obj); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// AppProject 视图解析：sourceRepos / destinations 是 []any，断言写错会静默丢数据
func TestArgoProjectView(t *testing.T) {
	obj := map[string]any{
		"spec": map[string]any{
			"description": "团队 A",
			"sourceRepos": []any{"http://gitlab.cqyxpt.site/cec/*", "*"},
			"destinations": []any{
				map[string]any{"server": "https://kubernetes.default.svc", "namespace": "qa-*"},
				map[string]any{"name": "cluster2", "namespace": "*"},
				"garbage",
			},
		},
	}
	p := argoProjectView(obj, "team-a", "argocd")
	if p.Name != "team-a" || p.Namespace != "argocd" || p.Description != "团队 A" {
		t.Errorf("基础字段解析错误: %+v", p)
	}
	if len(p.SourceRepos) != 2 || p.SourceRepos[0] != "http://gitlab.cqyxpt.site/cec/*" {
		t.Errorf("sourceRepos 解析错误: %+v", p.SourceRepos)
	}
	if len(p.Destinations) != 2 {
		t.Fatalf("destinations 应跳过非法元素，实际 %+v", p.Destinations)
	}
	if p.Destinations[0].Server != "https://kubernetes.default.svc" || p.Destinations[0].Namespace != "qa-*" {
		t.Errorf("destination[0] 解析错误: %+v", p.Destinations[0])
	}
	if p.Destinations[1].Name != "cluster2" || p.Destinations[1].Namespace != "*" {
		t.Errorf("destination[1] 解析错误: %+v", p.Destinations[1])
	}
	// 缺 spec 时给出空切片（前端据此渲染，nil 会变成 JSON null）
	empty := argoProjectView(map[string]any{}, "default", "argocd")
	if empty.SourceRepos == nil || empty.Destinations == nil {
		t.Errorf("缺 spec 也应返回空切片而非 nil: %+v", empty)
	}
}
