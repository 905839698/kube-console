# 镜像依赖清单与全局变量

平台运行所依赖的全部镜像地址在此清单。换镜像仓库（Harbor/registry 迁移）时：**把 A 节镜像推到新仓库 → 只改一个配置变量 `ci.imageRegistry`**，无需改代码。

## 全局变量定义

| 变量 | 配置项 | 环境变量 | 默认值 | 作用范围 |
| --- | --- | --- | --- | --- |
| CI 任务镜像仓库前缀 | `ci.imageRegistry` | `KC_CI_IMAGE_REGISTRY` | `harbor.cqyxpt.site:8443/library` | A 节全部 19 类 CI 节点模板里的 `{{imageRegistry}}` 占位 |
| 终端调试容器镜像 | `k8s.debugImage` | `KC_K8S_DEBUG_IMAGE` | `busybox:1.36` | B 节 |

- `imageRegistry` 在 CI 编译期注入节点模板（`node.Loader.WithGlobals`），**优先于节点参数**——流水线里误填同名参数不会改坏镜像仓库
- 镜像**版本**不是全局变量，是各节点自己的参数（`{{goVersion}}`/`{{jdk}}`/`{{nodeVersion}}`/`{{pythonVersion}}`/`{{mavenVersion}}`，默认值见下表，可在设计器属性面板按节点调整）
- CI 拉取私有镜像用 `ci.imagePullSecret`（默认 `harbor`），与镜像地址是两个概念

## A. CI 节点任务镜像（21 处，前缀均为 `{{imageRegistry}}`）

| 节点 | 镜像（前缀省略） | 版本参数（默认值） | 用途 |
| --- | --- | --- | --- |
| git-clone | `git:2.43.4` | — | 克隆 GitLab 仓库 |
| gitops-bump | `git:2.43.4` | — | 拉清单仓库、改写镜像 Tag |
| harbor-scan | `git:2.43.4` | — | 仓库拉取（配合 Harbor API 扫描） |
| go-build | `golang:{{goVersion}}` | goVersion=1.22 | Go 编译 |
| gradle-build | `gradle:8-jdk{{jdk}}` | jdk=17 | Gradle 构建 |
| maven-build | `maven:{{mavenVersion}}-eclipse-temurin-{{jdk}}` | mavenVersion=3.9, jdk=17 | Maven 构建 |
| npm-build | `node:{{nodeVersion}}` | nodeVersion=20 | 自定义 Node 构建脚本（registry/代理、依赖安装、构建命令都写在 script 参数里） |
| python-build | `python:{{pythonVersion}}` | pythonVersion=3.11 | Python 构建 |
| build-image | `buildkit:stable` | — | 容器镜像构建（BuildKit） |
| build-image | `syft:1.0.0-sh2` | — | 构建产物 SBOM/漏洞扫描 |
| trivy-scan | `trivy:0.48.3` | — | Trivy 漏洞扫描 |
| sonar-scan | `sonar-scanner-cli:11` | — | 代码质量扫描 |
| image-verify | `cosign:2.2.3` | — | 镜像签名验证 |
| push-registry | `skopeo:latest` | — | 镜像搬运推送 |
| k8s-deploy | `kubectl:1.29` | — | kubectl 部署/回滚 |
| argocd-sync | `kubectl:1.29` | — | ArgoCD 同步触发 |
| helm-deploy | `helm:3.14` | — | Helm 安装/升级 |
| upload-artifact | `curl:8.5.0` | — | 制品上传（Nexus 等） |
| upload-artifact | `mc:RELEASE.2024-06-12T14-34-03Z` | — | 制品上传（MinIO） |
| approval | `alpine:3.19` | — | 人工审批等待 shell |
| condition | `alpine:3.19` | — | 条件分支判断 |

**换仓库时的推送清单**（去重后 18 个 + 5 类版本化镜像按所选版本）：

```
alpine:3.19  git:2.43.4  kubectl:1.29  buildkit:stable  syft:1.0.0-sh2
trivy:0.48.3  sonar-scanner-cli:11  cosign:2.2.3  skopeo:latest  helm:3.14
curl:8.5.0  mc:RELEASE.2024-06-12T14-34-03Z
golang:<goVersion>  gradle:8-jdk<jdk>  maven:<mavenVersion>-eclipse-temurin-<jdk>
node:<nodeVersion>  python:<pythonVersion>
```

## B. 平台运行时镜像（非 CI）

| 用途 | 镜像 | 配置项 / 环境变量 |
| --- | --- | --- |
| Pod 终端"注入调试容器"（distroless 无 shell 容器） | `busybox:1.36`（内网集群必须换成私有仓库可拉取的镜像） | `k8s.debugImage` / `KC_K8S_DEBUG_IMAGE` |

## C. 应用自身部署镜像（构建产物，非运行时依赖）

| 镜像 | 构建 | 基础镜像 |
| --- | --- | --- |
| `kube-console-server` | `docker build -f server/Dockerfile` | golang:1.25-alpine → alpine:3.20（**含 git**，CI 分支/Tag 接口依赖） |
| `kube-console-web` | `docker build -f web/Dockerfile` | node:20-alpine → nginx:1.27-alpine |

## 生效方式

- `imageRegistry`：改配置/环境变量后**重启后端**即对之后编译的全部流水线生效（已保存的流水线版本是 Graph DSL，编译时才渲染镜像，无需重建版本）
- 已在跑的历史 PipelineRun 不受影响（Tekton CR 已固化当时的镜像）
