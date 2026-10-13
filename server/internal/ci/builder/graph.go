// Package builder 镜像构建器：用户上传本地构建产物（Java jar / Go 二进制 /
// 前端静态 tar.gz），平台按应用类型生成约定 Dockerfile，经托管流水线
// （artifact-fetch 从平台拉回制品 → build-image 构建推送）产出镜像。
//
// 与流水线设计器的关系：构建器是"表单驱动的流水线生成器"。每个构建器
// 对应一条托管流水线（fetch → image 两节点），DSL 由本包生成并版本化保存，
// 运行历史 / 日志 / 制品登记复用流水线既有链路（runtime.StartRun）。
package builder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/model"
)

// 约定产物名：artifact-fetch 落盘与 build-image 上下文里的固定文件名。
// 上传时原文件名只用于展示与后缀校验，workspace 内一律用固定名——
// Dockerfile 的 COPY 源因此可以完全由应用类型决定（对用户透明）。
const (
	storedJava = "app.jar"
	storedGo   = "app"
	storedWeb  = "web.tar.gz"

	// dockerfileName 生成到构建上下文（workspace 根）的约定 Dockerfile 名
	dockerfileName = "Dockerfile.ci"
)

// StoredArtifactName 该应用类型的 workspace 固定产物名。
func StoredArtifactName(appType string) string {
	switch appType {
	case model.CIBuilderJava:
		return storedJava
	case model.CIBuilderWeb:
		return storedWeb
	default:
		return storedGo
	}
}

// DefaultBaseImage 平台约定基础镜像（构建器未填 baseImage 时按类型取）。
// 前缀跟 ci.imageRegistry 走——换仓库只改一处；需要预推到该仓库，
// 清单见 docs/images.md。
func DefaultBaseImage(imageRegistry, appType string) string {
	p := strings.TrimRight(imageRegistry, "/")
	switch appType {
	case model.CIBuilderJava:
		return p + "/eclipse-temurin:17-jre"
	case model.CIBuilderWeb:
		return p + "/nginx:1.27-alpine"
	default:
		return p + "/alpine:3.19"
	}
}

// DefaultPort EXPOSE 端口默认值（构建器 port=0 时）。
func DefaultPort(appType string) int {
	if appType == model.CIBuilderWeb {
		return 80
	}
	return 8080
}

// GraphInput BuildGraph 的输入（全部是生成时已确定的值，图里不留占位）。
type GraphInput struct {
	PipelineName  string
	Builder       *model.CIImageBuilder
	Tag           string // 本次构建镜像 tag（固定值；构建器没有 git-clone，无 build_tag 可引用）
	Token         string // 制品下载令牌（平台侧 ci_builder_tokens 校验）
	ServerURL     string // 平台集群内地址（cfg.CI.ServerURL）
	ImageRegistry string // 约定基础镜像缺省前缀
}

// BuildGraph 生成托管流水线的 DSL：artifact-fetch → build-image。
// 出错返回可读原因（调用方在保存版本前拿到，不落到校验器再报错）。
func BuildGraph(in GraphInput) (*dsl.Graph, error) {
	b := in.Builder
	if b == nil {
		return nil, fmt.Errorf("缺少构建器数据")
	}
	if b.FileName == "" || b.FileSHA256 == "" {
		return nil, fmt.Errorf("尚未上传构建产物")
	}
	if strings.TrimSpace(in.ServerURL) == "" {
		return nil, fmt.Errorf("未配置 ci.serverUrl：任务 Pod 无法回调平台下载上传的制品")
	}
	if strings.TrimSpace(in.Tag) == "" {
		return nil, fmt.Errorf("缺少镜像 tag")
	}

	base := strings.TrimSpace(b.BaseImage)
	if base == "" {
		base = DefaultBaseImage(in.ImageRegistry, b.AppType)
	}
	port := b.Port
	if port <= 0 {
		port = DefaultPort(b.AppType)
	}

	df, err := dockerfileFor(b.AppType, base, port, b.JavaOpts, b.EntryArgs)
	if err != nil {
		return nil, err
	}

	fetch := map[string]interface{}{
		"url":      artifactDownloadURL(in.ServerURL, in.Token),
		"fileName": StoredArtifactName(b.AppType),
		"sha256":   b.FileSHA256,
		"extract":  b.AppType == model.CIBuilderWeb,
	}
	// 注意：不用 configFiles 把产物搬到上下文根——build-image 模板的
	// 「源已在上下文内则跳过」分支按 ./<src> 判存在，context=workspace 根时
	// ./upload/… 恒存在，搬运会被静默跳过，Dockerfile COPY 根级路径就报
	// "failed to compute cache key: /dist: not found"。fetch 落盘路径是确定的
	// （upload/<fileName>），Dockerfile 直接按 upload/ 路径 COPY。
	image := map[string]interface{}{
		"dockerfile":   dockerfileName,
		"context":      ".",
		"registry":     b.Registry,
		"repository":   b.Repository,
		"tag":          in.Tag,
		"credential":   strconv.FormatUint(uint64(b.CredentialID), 10),
		"generateSBOM": true,
		"preShell":     preShellFor(b.AppType, df),
	}

	g := &dsl.Graph{
		Name:    in.PipelineName,
		Version: 1,
		Nodes: []dsl.Node{
			{ID: "fetch", Type: "artifact-fetch", Params: fetch, Position: &dsl.Position{X: 0, Y: 0}},
			{ID: "image", Type: "build-image", Params: image, Position: &dsl.Position{X: 260, Y: 0}},
		},
		Edges: []dsl.Edge{{Source: "fetch", Target: "image"}},
	}
	return g, nil
}

// artifactDownloadURL 任务 Pod 回调平台的制品下载地址（公开路由 + 令牌收口）。
func artifactDownloadURL(serverURL, token string) string {
	return strings.TrimRight(serverURL, "/") + "/api/ci/artifact/" + token
}

// dockerfileFor 平台约定 Dockerfile（按应用类型三种形态）。
// 用户看不到也不需要写 Dockerfile；内容由构建器配置（基础镜像/端口/JVM 参数）决定。
// dockerfileFor 平台约定 Dockerfile。entryArgs（java/go 可选）以 exec 形式生成
// CMD、由运行时自动追加在 ENTRYPOINT 之后——数组形态不经 shell，参数里的
// 引号/反斜杠/反引号一律拒绝（既防 Dockerfile JSON 被破坏，也防注入）。
// java 带 JAVA_OPTS 时 ENTRYPOINT 走 sh -c，此时追加 "--" 占位 $0、脚本内
// "$@" 承接 CMD 参数（docker 会把 CMD 数组拼在 ENTRYPOINT 数组之后）。
func dockerfileFor(appType, baseImage string, port int, javaOpts, entryArgs string) (string, error) {
	for _, v := range []string{baseImage} {
		if err := checkNoInjection(v, "基础镜像"); err != nil {
			return "", err
		}
	}
	args := strings.Fields(entryArgs)
	for _, a := range args {
		if strings.ContainsAny(a, "\"\\`$") {
			return "", fmt.Errorf("启动参数 %q 含非法字符（引号/反斜杠/反引号/$ 不允许）", a)
		}
	}
	var cmdLine string
	if len(args) > 0 {
		quoted := make([]string, 0, len(args))
		for _, a := range args {
			quoted = append(quoted, `"`+a+`"`)
		}
		cmdLine = "CMD [" + strings.Join(quoted, ",") + "]\n"
	}

	var sb strings.Builder
	switch appType {
	case model.CIBuilderJava:
		opts := strings.TrimSpace(javaOpts)
		if strings.ContainsAny(opts, "\"\n\r\\") {
			return "", fmt.Errorf("JVM 参数不能包含换行、双引号或反斜杠")
		}
		sb.WriteString("FROM " + baseImage + "\n")
		sb.WriteString("ENV TZ=Asia/Shanghai\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY upload/app.jar /app/app.jar\n")
		switch {
		case opts != "" && len(args) > 0:
			sb.WriteString("ENV JAVA_OPTS=" + opts + "\n")
			sb.WriteString(`ENTRYPOINT ["sh","-c","java $JAVA_OPTS -jar /app/app.jar \"$@\"","--"]` + "\n")
		case opts != "":
			sb.WriteString("ENV JAVA_OPTS=" + opts + "\n")
			sb.WriteString(`ENTRYPOINT ["sh","-c","java $JAVA_OPTS -jar /app/app.jar"]` + "\n")
		default:
			sb.WriteString(`ENTRYPOINT ["java","-jar","/app/app.jar"]` + "\n")
		}
		sb.WriteString(cmdLine)
	case model.CIBuilderWeb:
		sb.WriteString("FROM " + baseImage + "\n")
		sb.WriteString("COPY upload/dist/ /usr/share/nginx/html/\n")
		sb.WriteString("EXPOSE 80\n")
	default: // go
		sb.WriteString("FROM " + baseImage + "\n")
		sb.WriteString("ENV TZ=Asia/Shanghai\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY upload/app /app/app\n")
		sb.WriteString("USER 65534\n")
		sb.WriteString(`ENTRYPOINT ["/app/app"]` + "\n")
		sb.WriteString(cmdLine)
	}
	if appType != model.CIBuilderWeb {
		if port <= 0 || port > 65535 {
			return "", fmt.Errorf("端口非法: %d", port)
		}
		sb.WriteString(fmt.Sprintf("EXPOSE %d\n", port))
	}
	return sb.String(), nil
}

// preShellFor 生成 build-image 的 preShell：主步骤（构建）前在构建上下文目录里
// 把约定 Dockerfile 写出来。前端类型顺带做两件事：
//   - 把「多包了一层目录」的静态包拍平（tar dist/ 打包方式很常见）
//   - 缺 index.html 时打出明确 WARN（构建继续，但用户能在日志里看到）
//
// heredoc 定界符用 KCEOF，内容里的 $ 不带括号（Tekton 只替换 $(...) 引用）。
func preShellFor(appType, dockerfile string) string {
	var sb strings.Builder
	sb.WriteString("set -e\n")
	if appType == model.CIBuilderGo {
		// curl 下载落盘是 644，COPY 保留源权限——二进制没执行位容器起不来，补上
		sb.WriteString("chmod 755 upload/" + storedGo + "\n")
	}
	if appType == model.CIBuilderWeb {
		sb.WriteString("if [ -d upload/dist ] && [ ! -f upload/dist/index.html ]; then\n")
		sb.WriteString("  for d in upload/dist/*/; do\n")
		sb.WriteString("    [ -f \"$d/index.html\" ] || continue\n")
		sb.WriteString("    mv \"$d\" upload/dist-flat && rm -rf upload/dist && mv upload/dist-flat upload/dist && break\n")
		sb.WriteString("  done\n")
		sb.WriteString("fi\n")
		sb.WriteString("[ -f upload/dist/index.html ] || echo \"WARN: 静态包里没有 index.html，请确认打包方式为 tar -czf web.tar.gz -C <dist目录> .\"\n")
	}
	sb.WriteString("cat > " + dockerfileName + " <<'KCEOF'\n")
	sb.WriteString(dockerfile)
	sb.WriteString("KCEOF\n")
	return sb.String()
}

var (
	dockerTagRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
	regNameRe   = regexp.MustCompile(`^[a-zA-Z0-9._:-]+$`)            // registry host[:port]
	repoRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,255}$`) // docker repository 路径（小写）
	// imageTokenRe 镜像地址常用字符白名单（天然排除空白/引号/$/反引号/反斜杠）
	imageTokenRe = regexp.MustCompile(`^[a-zA-Z0-9._/@:-]+$`)
)

// checkNoInjection 基础镜像等值会拼进 Dockerfile / heredoc：按白名单收口，
// 杜绝换行（提前终止 heredoc）与引号/$（Dockerfile 指令与 shell 注入）。
func checkNoInjection(v, what string) error {
	if v == "" {
		return nil
	}
	if !imageTokenRe.MatchString(v) {
		return fmt.Errorf("%s %q 含非法字符（仅允许字母数字与 . _ / @ : -）", what, v)
	}
	if v == "KCEOF" {
		return fmt.Errorf("%s 不能为 KCEOF（与生成的 Dockerfile 定界符冲突）", what)
	}
	return nil
}

// ValidateImageFields registry/repository/tag 的形态校验（保存构建器时前置，
// 错误直接打到表单字段，不留到构建日志里才暴露）
func ValidateImageFields(registry, repository, tag string) error {
	if registry == "" || !regNameRe.MatchString(registry) {
		return fmt.Errorf("registry %q 非法（如 harbor.example.com[:8443]，仅允许字母数字 . _ - :）", registry)
	}
	if repository == "" || !repoRe.MatchString(repository) {
		return fmt.Errorf("repository %q 非法（docker 仓库路径，小写字母数字 . _ / -，如 library/demo）", repository)
	}
	if tag != "" && !dockerTagRe.MatchString(tag) {
		return fmt.Errorf("tag %q 非法（Docker tag：字母数字开头，可含 . _ -，≤128 字符）", tag)
	}
	return nil
}
