package builder

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"kube-console/server/internal/ci/node"
	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline"
	"kube-console/server/internal/ci/pipeline/compiler"
	"kube-console/server/internal/model"
)

// realRegistry 加载真实内嵌节点插件（与 server 装配一致）——
// 托管 DSL 必须能通过线上 validator/compiler 全链路，不能用桩。
func realRegistry(t *testing.T) nodetype.Registry {
	t.Helper()
	reg := node.NewRegistry()
	sub, err := fs.Sub(node.NodesFS, "nodes")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	if err := node.NewLoader(reg).WithGlobals(map[string]interface{}{
		"imageRegistry": "harbor.test/library",
	}).LoadEmbedded(sub); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg
}

func baseBuilder(appType string) *model.CIImageBuilder {
	return &model.CIImageBuilder{
		ID: 7, Name: "demo-app", AppType: appType,
		Registry: "harbor.test", Repository: "library/demo", CredentialID: 3,
		FileName: "demo.bin", FileSHA256: strings.Repeat("a", 64),
	}
}

func TestBuildGraphValidateCompile(t *testing.T) {
	reg := realRegistry(t)
	svc := pipeline.NewService(reg, "ci-demo")
	names := map[string]string{
		model.CIBuilderJava: "demo.jar",
		model.CIBuilderGo:   "demo",
		model.CIBuilderWeb:  "web-dist.tar.gz",
	}
	for _, appType := range model.CIBuilderAppTypes {
		t.Run(appType, func(t *testing.T) {
			b := baseBuilder(appType)
			b.FileName = names[appType]
			g, err := BuildGraph(GraphInput{
				PipelineName: "bld-demo-app", Builder: b, Tag: "upload-20261009120000",
				Token: strings.Repeat("f", 48), ServerURL: "http://kc.kube-console.svc:8080",
				ImageRegistry: "harbor.test/library",
			})
			if err != nil {
				t.Fatalf("BuildGraph: %v", err)
			}
			if res := svc.Validate(g); !res.Valid {
				t.Fatalf("validate: %v", res.Errors)
			}
			spec, err := svc.Compile(g)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if len(spec.Spec.Tasks) != 2 {
				t.Fatalf("expect fetch+image tasks, got %d", len(spec.Spec.Tasks))
			}
			yamlText, err := compiler.ToYAML(spec)
			if err != nil {
				t.Fatalf("yaml: %v", err)
			}
			// 约定 Dockerfile 名与平台制品回调地址必须进入编译产物
			if !strings.Contains(yamlText, "filename=Dockerfile.ci") ||
				!strings.Contains(yamlText, "http://kc.kube-console.svc:8080/api/ci/artifact/") {
				t.Fatalf("compiled yaml missing expected refs:\n%s", yamlText)
			}
		})
	}
}

func TestBuildGraphDockerfilePerType(t *testing.T) {
	cases := []struct {
		appType string
		mustHas []string
	}{
		{model.CIBuilderJava, []string{"COPY upload/app.jar /app/app.jar", `ENTRYPOINT ["java","-jar","/app/app.jar"]`, "harbor.test/library/eclipse-temurin:17-jre", "EXPOSE 8080"}},
		{model.CIBuilderGo, []string{"COPY upload/app /app/app", "chmod 755 upload/app", `ENTRYPOINT ["/app/app"]`, "harbor.test/library/alpine:3.19", "USER 65534"}},
		{model.CIBuilderWeb, []string{"COPY upload/dist/ /usr/share/nginx/html/", "harbor.test/library/nginx:1.27-alpine", "EXPOSE 80"}},
	}
	for _, tc := range cases {
		t.Run(tc.appType, func(t *testing.T) {
			g, err := BuildGraph(GraphInput{
				PipelineName: "bld-x", Builder: baseBuilder(tc.appType), Tag: "t1",
				Token: "tok", ServerURL: "http://kc:8080", ImageRegistry: "harbor.test/library",
			})
			if err != nil {
				t.Fatalf("BuildGraph: %v", err)
			}
			image := g.Nodes[1]
			pre, _ := image.Params["preShell"].(string)
			for _, want := range tc.mustHas {
				if !strings.Contains(pre, want) {
					t.Fatalf("%s: preShell missing %q:\n%s", tc.appType, want, pre)
				}
			}
			if image.Params["dockerfile"] != "Dockerfile.ci" {
				t.Fatalf("dockerfile param: %v", image.Params["dockerfile"])
			}
			fetch := g.Nodes[0]
			if tc.appType == model.CIBuilderWeb {
				if fetch.Params["extract"] != true {
					t.Fatal("web must extract")
				}
			}
			// 回归：不得再用 configFiles 搬运——build-image 的「已在上下文则跳过」
			// 分支在 context=workspace 根时恒命中，COPY 根级路径会报 /dist not found
			if _, ok := image.Params["configFiles"]; ok {
				t.Fatalf("configFiles must NOT be set (silent-skip pitfall in build-image): %v", image.Params["configFiles"])
			}
			if image.Params["tag"] != "t1" {
				t.Fatalf("tag must be baked: %v", image.Params["tag"])
			}
		})
	}
}

func TestBuildGraphJavaOpts(t *testing.T) {
	b := baseBuilder(model.CIBuilderJava)
	b.JavaOpts = "-Xmx512m -XX:+UseG1GC"
	g, err := BuildGraph(GraphInput{PipelineName: "p", Builder: b, Tag: "t", Token: "x", ServerURL: "http://kc", ImageRegistry: "r"})
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := g.Nodes[1].Params["preShell"].(string)
	if !strings.Contains(pre, "ENV JAVA_OPTS=-Xmx512m -XX:+UseG1GC") ||
		!strings.Contains(pre, `ENTRYPOINT ["sh","-c","java $JAVA_OPTS -jar /app/app.jar"]`) {
		t.Fatalf("java opts not rendered:\n%s", pre)
	}
}

func TestBuildGraphEntrypointArgs(t *testing.T) {
	// go：ENTRYPOINT 裸二进制 + CMD 数组追加（不经 shell）
	b := baseBuilder(model.CIBuilderGo)
	b.EntryArgs = "serve --conf=/app/config.yaml --port 8080"
	g, err := BuildGraph(GraphInput{PipelineName: "p", Builder: b, Tag: "t", Token: "x", ServerURL: "http://kc", ImageRegistry: "r"})
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := g.Nodes[1].Params["preShell"].(string)
	if !strings.Contains(pre, `ENTRYPOINT ["/app/app"]`) ||
		!strings.Contains(pre, `CMD ["serve","--conf=/app/config.yaml","--port","8080"]`) {
		t.Fatalf("go entry args not rendered as CMD array:\n%s", pre)
	}
	// java：JVM 参数 + 启动参数组合走 sh -c "$@" 透传（"--" 占位 $0）
	bj := baseBuilder(model.CIBuilderJava)
	bj.JavaOpts = "-Xmx256m"
	bj.EntryArgs = "--spring.profiles.active=prod"
	gj, err := BuildGraph(GraphInput{PipelineName: "p", Builder: bj, Tag: "t", Token: "x", ServerURL: "http://kc", ImageRegistry: "r"})
	if err != nil {
		t.Fatal(err)
	}
	prej, _ := gj.Nodes[1].Params["preShell"].(string)
	if !strings.Contains(prej, `ENTRYPOINT ["sh","-c","java $JAVA_OPTS -jar /app/app.jar \"$@\"","--"]`) ||
		!strings.Contains(prej, `CMD ["--spring.profiles.active=prod"]`) {
		t.Fatalf("java opts+args form wrong:\n%s", prej)
	}
	// 非法字符拒绝（数组不经 shell，出现这些只会是破坏 Dockerfile/注入企图）
	bad := baseBuilder(model.CIBuilderGo)
	bad.EntryArgs = `x"; rm -rf /`
	if _, err := BuildGraph(GraphInput{PipelineName: "p", Builder: bad, Tag: "t", Token: "x", ServerURL: "http://kc", ImageRegistry: "r"}); err == nil {
		t.Fatal("quote in entry args must fail")
	}
}

func TestBuildGraphRejectsBadInput(t *testing.T) {
	// 未上传制品（sha 缺失）
	b := baseBuilder("java")
	b.FileSHA256 = ""
	if _, err := BuildGraph(GraphInput{Builder: b, Tag: "t", Token: "x", ServerURL: "http://kc"}); err == nil {
		t.Fatal("missing sha must fail")
	}
	// 缺 serverUrl
	if _, err := BuildGraph(GraphInput{Builder: baseBuilder("go"), Tag: "t", Token: "x", ServerURL: " "}); err == nil {
		t.Fatal("missing serverURL must fail")
	}
	// 基础镜像带空格（Dockerfile/heredoc 注入面）
	bb := baseBuilder("go")
	bb.BaseImage = "alpine:3.19 rm -rf /"
	if _, err := BuildGraph(GraphInput{Builder: bb, Tag: "t", Token: "x", ServerURL: "http://kc"}); err == nil {
		t.Fatal("injected baseImage must fail")
	}
	// 非法 tag / 大写仓库名
	if err := ValidateImageFields("harbor.test", "library/demo", "bad tag"); err == nil {
		t.Fatal("space in tag must fail")
	}
	if err := ValidateImageFields("harbor.test", "Library/Demo", "ok1"); err == nil {
		t.Fatal("uppercase repository must fail")
	}
}

func TestPipelineNameFor(t *testing.T) {
	if got := pipelineNameFor("订单中心 Demo App"); got != "bld-demo-app" {
		t.Fatalf("pipelineNameFor: %q", got)
	}
	if got := pipelineNameFor("中文"); got != "bld-builder" {
		t.Fatalf("all-non-ascii should fall back: %q", got)
	}
	rfc1123 := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if got := pipelineNameFor("My App!! 2026"); !rfc1123.MatchString(got) {
		t.Fatalf("sanitized pipeline name must be rfc1123: %q", got)
	}
}
