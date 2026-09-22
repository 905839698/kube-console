package node

import (
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 多行参数值插入块标量：续行必须与占位符同缩进，否则标量被顶格行终止、YAML 解析失败
// （npm-build 的 script、各节点的 preShell/configFiles 都是多行输入）。
func TestRenderTemplateMultiLineIndent(t *testing.T) {
	tmpl := "steps:\n  - name: build\n    script: |\n      #!/bin/sh\n      set -e\n      {{script}}\n      echo done\n"
	out := renderTemplate(tmpl, map[string]interface{}{
		"script": "npm config set registry http://nexus/repository/npm-group/\n\nif [ -f pnpm-lock.yaml ]; then\n  pnpm install\nfi\nnpm run build",
	})

	var doc struct {
		Steps []struct {
			Script string `yaml:"script"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("渲染结果不是合法 YAML: %v\n%s", err, out)
	}
	if len(doc.Steps) != 1 {
		t.Fatalf("step 数 = %d, want 1", len(doc.Steps))
	}
	want := "#!/bin/sh\nset -e\n" +
		"npm config set registry http://nexus/repository/npm-group/\n" +
		"\n" +
		"if [ -f pnpm-lock.yaml ]; then\n  pnpm install\nfi\n" +
		"npm run build\n" +
		"echo done\n"
	if doc.Steps[0].Script != want {
		t.Errorf("脚本内容不符合预期:\n得到 %q\n期望 %q", doc.Steps[0].Script, want)
	}
}

// 单行值逐字替换，行为与改造前完全一致（其它节点的既有模板不受影响）。
func TestRenderTemplateSingleLineUnchanged(t *testing.T) {
	tmpl := "image: {{imageRegistry}}/node:{{nodeVersion}}\n"
	got := renderTemplate(tmpl, map[string]interface{}{
		"imageRegistry": "harbor.example:8443/library",
		"nodeVersion":   "20",
	})
	want := "image: harbor.example:8443/library/node:20\n"
	if got != want {
		t.Errorf("单行替换结果 = %q, want %q", got, want)
	}
}

// 值里自带缩进（if 体等）时相对缩进保持，不被打平。
func TestRenderTemplateMultiLineKeepsRelativeIndent(t *testing.T) {
	tmpl := "    script: |\n      {{script}}\n"
	out := renderTemplate(tmpl, map[string]interface{}{"script": "if true; then\n    echo deep\nfi"})
	var doc struct {
		Script string `yaml:"script"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("渲染结果不是合法 YAML: %v\n%s", err, out)
	}
	// 块标量 `|` 保留一个结尾换行（clip），比较时去掉
	if got := strings.TrimSuffix(doc.Script, "\n"); got != "if true; then\n    echo deep\nfi" {
		t.Errorf("相对缩进被破坏: %q", doc.Script)
	}
}

// npm-build 渲染：单一 script 入口落到 build step，镜像取 node:<版本>，
// 且保留 build_output 结果契约（下游 build-image 等按它登记产物）。
func TestNPMPBuildRenderScript(t *testing.T) {
	sub, err := fs.Sub(NodesFS, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := NewLoader(reg).WithGlobals(map[string]interface{}{
		"imageRegistry": "harbor.example:8443/library",
	}).LoadEmbedded(sub); err != nil {
		t.Fatal(err)
	}
	n, ok := reg.Get("npm-build")
	if !ok {
		t.Fatal("npm-build 节点未注册")
	}

	user := "npm config set registry http://nexus/repository/npm-group/\nnpm ci\nnpm run build"
	spec, err := n.RenderTask(map[string]interface{}{"nodeVersion": "20", "script": user})
	if err != nil {
		t.Fatalf("RenderTask: %v", err)
	}
	if len(spec.Steps) != 1 || spec.Steps[0].Name != "build" {
		t.Fatalf("steps = %+v, want 单个 build", spec.Steps)
	}
	if spec.Steps[0].Image != "harbor.example:8443/library/node:20" {
		t.Errorf("镜像 = %s", spec.Steps[0].Image)
	}
	script := spec.Steps[0].Script
	for _, want := range []string{
		"cd $(workspaces.source.path)",
		"npm config set registry http://nexus/repository/npm-group/",
		"npm ci",
		"npm run build",
		`: "${OUTPUT_DIR:=dist}"`,
		`if [ ! -e "$OUTPUT_DIR" ]`,
		"构建产物目录不存在",
		`printf '%s' "$OUTPUT_DIR" > $(results.build_output.path)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("脚本缺少 %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "{{") {
		t.Errorf("脚本残留未替换占位符:\n%s", script)
	}
	// 只挂运行工作区：cache 工作区是第二个 PVC，Tekton 的 Affinity Assistant
	// （coschedule=workspaces）不允许一个 TaskRun 绑两个不同 PVC
	if len(spec.Workspaces) != 1 || spec.Workspaces[0].Name != "source" {
		t.Errorf("workspaces = %+v, want 仅 source", spec.Workspaces)
	}
	for _, r := range spec.Results {
		if r.Name == "build_output" {
			return
		}
	}
	t.Errorf("缺少 build_output 结果声明: %+v", spec.Results)
}

// 产物目录是节点属性：默认 dist 写进脚本，自定义值照原样生效（下游按 build_output 取用）。
func TestNPMPBuildOutputDirParam(t *testing.T) {
	sub, err := fs.Sub(NodesFS, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := NewLoader(reg).LoadEmbedded(sub); err != nil {
		t.Fatal(err)
	}
	n, _ := reg.Get("npm-build")
	spec, err := n.RenderTask(map[string]interface{}{
		"nodeVersion": "20", "outputDir": "apps/web/build", "script": "pnpm build",
	})
	if err != nil {
		t.Fatalf("RenderTask: %v", err)
	}
	if got := spec.Steps[0].Script; !strings.Contains(got, `: "${OUTPUT_DIR:=apps/web/build}"`) {
		t.Errorf("自定义产物目录未写进脚本:\n%s", got)
	}
	// 未填时取 schema 默认值
	spec2, err := n.RenderTask(map[string]interface{}{"nodeVersion": "20", "script": "pnpm build"})
	if err != nil {
		t.Fatalf("RenderTask(默认): %v", err)
	}
	if got := spec2.Steps[0].Script; !strings.Contains(got, `: "${OUTPUT_DIR:=dist}"`) {
		t.Errorf("默认产物目录应为 dist:\n%s", got)
	}
}

// script 是必填属性：历史图的 npm-build 节点（无该参数、只有旧的
// packageManager/installCommand 等）会在保存与提交运行时报「缺少必填参数 script」，
// 而不是渲染出一个什么都不做的构建步骤。
func TestNPMPBuildScriptRequired(t *testing.T) {
	sub, err := fs.Sub(NodesFS, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := NewLoader(reg).LoadEmbedded(sub); err != nil {
		t.Fatal(err)
	}
	n, _ := reg.Get("npm-build")
	var found bool
	for _, p := range n.Meta().Properties {
		switch p.Name {
		case "script":
			found = true
			if !p.Required {
				t.Error("script 应为必填")
			}
		case "preShell", "cache", "packageManager", "installCommand", "buildCommand":
			t.Errorf("属性 %s 应已移除（改用单一 script 入口）", p.Name)
		}
	}
	if !found {
		t.Fatal("schema 缺少 script 属性")
	}
	if errs := n.Validate(map[string]interface{}{}); len(errs) == 0 {
		t.Error("未填 script 时应校验失败")
	}
}
