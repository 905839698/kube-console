# 自举流水线：kube-console 构建并部署它自己

本目录是「用本项目内置的 CI 构建本项目自己」的全部材料：

| 文件 | 用途 |
| --- | --- |
| `bootstrap-pipeline.json` | 流水线 DSL（设计器导出的 graph JSON），版本化的流水线定义事实来源 |
| `buildkitd.yaml` | 集群内共享 BuildKit 构建器（build-image 节点的硬依赖，本仓库原先没有这份清单） |

流水线链路：**git-clone → web-test（npm ci + vitest + vite build）→ server-test（go test）→ web-image / server-image（BuildKit 构建并推送，tag = `<分支>-<北京时间>-<短SHA>`）→ deploy-gate（仅 master）→ deploy-web → deploy-server（kubectl set image + 自动 wait）**。

导入后编译产物是 `tekton.dev/v1` 的 Pipeline CR，由已安装的 Tekton 执行；运行历史、WS 状态推送、SSE 日志、部署记录与一键回滚由平台（CI / CD → CI 流水线）提供。

## 前提

1. 目标集群已装 **Tekton Pipelines（v1 API）**。未装时 CI 页面会显示横幅，流水线只能浏览不能执行
2. `ci.imageRegistry`（默认 `harbor.cqyxpt.site:8443/library`）下已预推好工具镜像，清单见 `docs/images.md`（git / buildkit / syft / kubectl / golang:1.25 / node:20 等）
3. 平台管理员的 `ci.enabled` 已开启（KC 配置）
4. Harbor 推送凭证已在 平台管理 → 凭证 创建（账密或 DockerConfig 形态；Harbor 机器人账号用账密形态）
5. （仅镜像构建器功能需要）配置 `ci.serverUrl` = **任务 Pod 在集群内回连平台后端**的地址（不是浏览器访问地址；按 deploy/04 清单即 `http://kube-console-server.kube-console:8080`，env `KC_CI_SERVER_URL`）——artifact-fetch 凭令牌从这里回拉用户上传的制品；并预推约定基础镜像（`eclipse-temurin:17-jre` / `nginx:1.27-alpine`，`alpine:3.19` 已在清单，见 `docs/images.md`）

## 部署 buildkitd

```bash
kubectl apply -f deploy/06-ci/buildkitd.yaml
```

注意：

- 清单里命名空间是 `kube-console`，与流水线 run 的执行命名空间必须一致（build-image 默认连 `tcp://buildkitd:1234`，短域名只在同 ns 解析）。若 CI 项目建在其他 ns，先把 `buildkitd.yaml` 与流水线里各节点的 namespace 一起改掉
- buildkitd 需要 privileged（rootful 模式）；缓存盘 20Gi，StorageClass 留空取集群默认

## 创建自举流水线

1. **CI / CD → CI 流水线 → 新建**：所属项目选择运行命名空间为 `kube-console`（即控制台所在 ns，平台会在这里自动建 ci-bot/ci-deploy RBAC 与项目工作区 PVC），流水线名随意（如 `kube-console-bootstrap`）
2. 进入设计器 → **导入 JSON** → 选择 `deploy/06-ci/bootstrap-pipeline.json` → **保存**
3. 替换三处环境相关占位：
   - `git-clone` 节点 `url`：改成真实的 GitLab 仓库地址；私有仓库再选 Git 凭证
   - 两个 `build-image` 节点 `credential`：选择 Harbor 推送凭证（JSON 里是 `REPLACE_WITH_HARBOR_CREDENTIAL_ID` 占位）
   - `registry` / `repository`：当前指向 `harbor.cqyxpt.site:8443/library/kube-console-{web,server}`，不同环境按需改
4. **保存版本**，然后用「验证 / 编译预览」确认能编译出 Tekton YAML（本 JSON 已用 `server` 模块的 ParseGraph → Validate → Compile 全链路验证通过）

也可以不走设计器，直接调 API：`POST /ci/pipelines` 建流水线后 `POST /ci/pipelines/:id/versions` 提交本 JSON 的 `graphJson`。

## 触发

- **手动**：流水线页 → 运行（分支 `master`）
- **GitLab push 自动触发**：流水线触发器里创建 webhook，把生成的回调地址（`/api/ci/webhook/<token>`）填到 GitLab 项目的 Push events。分支过滤、事件去重由平台完成
- 定时触发也支持（cron），自举场景一般用不到

运行行为：**每次构建双镜像但只有 master 分支自动部署**——其他分支 push 走到条件节点即止（镜像照常推送，可手动 set image 验证）。两个 `k8s-deploy` 成功后自动落发布记录，出问题可在 CI 部署记录里一键回滚镜像。

## 已知限制与说明

- **同流水线串行、不同流水线并行**：工作区 PVC 按流水线分了子目录（`pl-<流水线ID>`），同项目的不同流水线可同时执行；同一条流水线的并发触发会排队。注意依赖缓存 `kc-cache` 是项目级共享的，多流水线并行时共用（npm/go 缓存自身并发安全）
- **历史盘根残留**：改为按流水线分目录之前的工作区内容会留在 PVC 根部不再被使用，需要时手动进 `ci-ws-p*` 盘清理（新目录由 kubelet 在首次运行时自动创建）
- **无路径过滤**：`web/`、`server/` 任一变更都全量走完整流水线。平台 DSL 的 condition 只能比较触发参数与任务结果，表达不了「本次 commit 改了哪些文件」；真嫌慢再拆两条流水线各自挂 webhook
- **tag 触发不会自动部署**：tag_push 时 `GIT_BRANCH` 为空，`deploy-gate`（`GIT_BRANCH == master`）判 false。发版走 master push；确需 tag 部署时把条件改成按 `GIT_COMMIT` 或临时手动 run
- **节点固定**：CI 运行的全部 Pod（任务 + affinity-assistant）可通过服务端配置 `ci.nodeSelector` / `ci.tolerations`（或 env `KC_CI_NODE_SELECTOR` / `KC_CI_TOLERATIONS`，JSON 格式）钉到指定节点。注意：工作区 PVC（local-path）的 PV 钉在首次消费的节点上，改配置后**存量项目的盘不会自动迁移**——在项目行「缓存」→ 清空依赖缓存（删盘重建）让 PV 重建到新节点
- **冷启动**：控制台自己没部署起来时本流水线无法存在——首次构建用本地 `docker build -f server/Dockerfile -t <仓库>/kube-console-server:latest .`（web 同理，仓库根目录执行）先把平台跑起来，之后全部交给这条流水线
- **go-build 的 `goproxy` 参数**：已填内网 Nexus goproxy（`10.107.2.251:8081`，与 `server/Dockerfile` 内的 `go env -w GOPROXY` 同一地址）。工具镜像 `golang:1.25` 需在 docs/images.md 的预推清单内
- go-build 节点模板（`server/internal/ci/node/nodes/go-build/`）本次补了 Go 1.24/1.25 选项与 `goproxy` 参数——本项目 go.mod 要求 1.25.3，原选项只到 1.23
