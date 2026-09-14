# 前后端分开部署说明

本文档描述 kube-console 前后端分离部署（前端静态站 + 后端 Go 服务分开构建、分开发布）的完整步骤。适用形态：

- 物理机/虚机 + Nginx（最常见）
- Docker Compose / 双容器
- Kubernetes（仓库已带 manifest，见 `deploy/`）

## 1. 架构与硬约束

```
                 ┌─────────────────────────────┐
  浏览器 ───────▶│  Nginx（:443 或 :80）        │
                 │                             │
                 │  /          → web/dist 静态  │ ──▶ 前端（纯静态文件）
                 │  /api       → 10.x.x.x:8080 │ ──▶ 后端（Go 单二进制）
                 └─────────────────────────────┘
```

- **后端**：Go + Gin 单二进制，监听 `:8080`，所有接口在 `/api` 前缀下，内含 WebSocket（Pod 终端、CI 运行推送）与 SSE（CI 日志流）
- **前端**：Vue3 + Vite 构建出的**纯静态文件**（`web/dist`），代码里请求全部走相对路径 `/api`（`web/src/api/http.ts` 的 `baseURL: '/api'`），登录态存 localStorage（Bearer token）

**硬约束：后端未开启 CORS，前端请求是相对路径——两边必须同源。** 所以"分开部署"的正确姿势不是让浏览器直连两个域名，而是：**前端静态文件与 `/api` 反向代理挂在同一个 Nginx（同一域名/端口）下**，由 Nginx 把 `/api` 转发到后端机器。Pod 终端（WS）、CI 日志（SSE）都走这个入口，Nginx 需透传升级头并关闭 SSE 缓冲（下文配置已含）。

## 2. 构建产物

| 产物 | 来源 | 构建命令（均在仓库根目录执行） |
| --- | --- | --- |
| 后端二进制 | `server/`（Go 1.25+） | `cd server && CGO_ENABLED=0 go build -o kube-console-server ./cmd/server` |
| 后端镜像 | `server/Dockerfile` | `docker build -f server/Dockerfile -t kube-console-server:latest .` |
| 前端静态文件 | `web/`（Node 18+） | `cd web && npm ci && npm run build` → 产物在 `web/dist` |
| 前端镜像（K8s 用） | `web/Dockerfile`（多阶段：vite 构建 + nginx，含 `deploy/03-web/nginx.conf`） | `docker build -f web/Dockerfile -t kube-console-web:latest .` |

> 后端镜像（`server/Dockerfile`）为多阶段构建，最终镜像含 **git**——CI 模块的 `/ci/repo/refs`（GitLab 分支/Tag 下拉）依赖它，不要精简掉。
> 交叉编译 Linux 二进制：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ...`。

## 3. 后端部署

### 3.1 启动参数

```bash
./kube-console-server [-config configs/config.yaml] [-addr :8080]
```

- `-config` 缺省时自动探测 `configs/config.yaml`（相对工作目录）
- 配置文件每项都可用 `KC_` 前缀环境变量覆盖，**环境变量优先级更高**，生产建议用环境变量注入密钥类配置：

| 环境变量 | 含义 | 示例 |
| --- | --- | --- |
| `KC_SERVER_PORT` | 监听端口 | `8080` |
| `KC_DB_DRIVER` | sqlite / mysql / postgres | `postgres` |
| `KC_DB_DSN` | 数据库连接串 | `host=10.0.0.1 user=kc password=xxx dbname=kube_console port=5432 sslmode=disable TimeZone=Asia/Shanghai` |
| `KC_JWT_SECRET` | JWT 签名密钥（**生产必改**） | 64 位随机串 |
| `KC_JWT_EXPIRE_IN` | token 有效期（小时） | `24` |
| `KC_ADMIN_USERNAME` / `KC_ADMIN_PASSWORD` | 首个管理员账号 | `admin` / 强密码 |
| `KC_K8S_DEBUG_IMAGE` | 无 shell 容器注入调试镜像 | `harbor.xxx/library/busybox:1.36`（内网集群须可拉取） |
| `KC_CI_ENABLED` | 关闭内置 CI 模块 | `false` |
| `KC_CI_RUN_TIMEOUT` / `KC_CI_PVC_SIZE` | CI 运行超时 / 工作区 PVC 大小 | `2h` / `10Gi` |
| `KC_DEBUG` | 调试日志 | `1` |

### 3.2 数据库

- **sqlite**（默认）：数据单文件（`KC_DB_DSN` 指向的路径，默认 `./data/kube-console.db`）。零依赖，单副本够用；**目录必须持久化**（容器场景挂卷）
- **MySQL / PostgreSQL**：生产建议。DSN 示例见 `configs/config.example.yaml`
- **副本数保持 1**：CI 后台同步循环在进程内运行，多副本会重复消费（未做 leader election）

### 3.3 systemd 示例（Linux 裸机）

```ini
[Unit]
Description=kube-console-server
After=network.target

[Service]
WorkingDirectory=/opt/kube-console
ExecStart=/opt/kube-console/kube-console-server -config /opt/kube-console/configs/config.yaml
Restart=always
RestartSec=3
Environment=KC_JWT_SECRET=<换成随机串>
Environment=KC_ADMIN_PASSWORD=<换成强密码>
# Environment=KC_DB_DRIVER=postgres
# Environment=KC_DB_DSN=<DSN>

[Install]
WantedBy=multi-user.target
```

### 3.4 Docker 示例

```bash
docker run -d --name kube-console-server \
  -p 8080:8080 \
  -v /opt/kube-console/data:/app/data \
  -e KC_JWT_SECRET=<随机串> -e KC_ADMIN_PASSWORD=<强密码> \
  kube-console-server:latest
```

### 3.5 就绪检查

`GET /api/auth/login`（返回 4xx/405 即进程存活）可作探针，与 `deploy/02-deployment/server.yaml` 中一致。

## 4. 前端部署（Nginx 静态 + /api 反代）

### 4.1 构建

```bash
cd web
npm ci
npm run build        # 产物: web/dist/
```

把 `web/dist/` 整个目录拷到 Nginx 机器（或打 tar 包/上传对象存储）。

### 4.2 Nginx 配置（关键项已注释）

> 仓库内唯一来源是 [`deploy/03-web/nginx.conf`](../deploy/03-web/nginx.conf)（物理机版把 `proxy_pass` 改成后端机器 IP 即可），以下内容与其一致：

```nginx
# WebSocket 升级头映射（Pod 终端 / CI 运行推送依赖）
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80;
    server_name kube-console.example.com;
    # 前端静态文件
    root /opt/kube-console/web/dist;
    index index.html;

    # 上传（容器文件上传等）放宽请求体
    client_max_body_size 100m;

    # 后端 API 反向代理（跨机器则改为 后端IP:8080）
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;

        # WebSocket 透传
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        # SSE（CI 日志流）：必须关缓冲，否则日志不实时
        proxy_buffering off;
        proxy_cache off;

        # 终端/日志是长连接：读超时长开
        proxy_read_timeout  3600s;
        proxy_send_timeout  3600s;
    }

    # Vue Router history 模式：非 /api 路径回落 index.html
    location / {
        try_files $uri $uri/ /index.html;
    }

    gzip on;
    gzip_types text/css application/javascript application/json image/svg+xml;
}
```

要点：

1. **`/api/` 与 `location /` 分开写**，其余路径（含 `/`）全部回落 `index.html`，保证前端刷新不 404
2. **SSE 必须 `proxy_buffering off`**，否则 CI 日志会攒批吐出
3. **WS 三件套**：`proxy_http_version 1.1` + `Upgrade` + `Connection` 头，缺一个终端就连不上
4. 前端/后端**跨机器**时 `proxy_pass` 指向后端机器 IP 即可，对浏览器仍是同源；后端自身**不要**再暴露公网端口
5. 需要 HTTPS 时在本 Nginx 终结 TLS（证书配到 `listen 443 ssl`），后端保持纯 HTTP 内网可达

### 4.3 开发环境（对照参考）

本地开发用 `cd web && npm run dev`（:5173），vite 自带 `/api` 代理到 `http://localhost:8080`（含 `ws: true`），行为与生产 Nginx 一致。

## 5. Kubernetes 部署

仓库已带 manifest（`deploy/`），按序 apply：

manifest 已随仓库维护（`deploy/`），Nginx 配置唯一来源是 `deploy/03-web/nginx.conf`（`web/Dockerfile` 打进前端镜像，物理机部署也可直接复用）：

```bash
# 先构建并推送两个镜像，替换 manifest 里的占位地址
docker build -f server/Dockerfile -t <仓库>/kube-console-server:latest . && docker push ...
docker build -f web/Dockerfile    -t <仓库>/kube-console-web:latest .    && docker push ...

# 按序 apply（03-web 是前端 Deployment）
kubectl apply -f deploy/01-namespace/namespace.yaml
kubectl apply -f deploy/02-deployment/server.yaml    # 后端 Deployment + Secret（PostgreSQL DSN 在 Secret 的 db-dsn 键）
kubectl apply -f deploy/03-web/web.yaml              # 前端 Deployment（nginx 镜像内置 dist + 反代配置）
kubectl apply -f deploy/04-service/service-gateway.yaml    # 前后端 Service + Gateway + HTTPRoute
# 或一次性: kubectl apply -R -f deploy/
```

- 两个镜像地址在 manifest 里是占位 `harbor.example.com/library/...`，构建推送后替换
- 密钥（`KC_JWT_SECRET` / `KC_ADMIN_PASSWORD` / `db-dsn`）走 `02` 内联的 Secret，**apply 前先改成真值**
- 入口用 **Gateway API**（集群已装 Higress，`04` 里 GatewayClass 为 `higress`）：Gateway 监听 80（hostname 占位 `kube-console.example.com`，改真实域名），HTTPRoute 按路径分发 `/api → kube-console-server:8080`、`/ → kube-console-web:80`；WebSocket 由 Higress 原生透传，SSE 流式转发无需额外配置；HTTPS 开启步骤见 manifest 尾部注释
- 不用网关时把 `kube-console-web` 服务改 NodePort 直接暴露，前端镜像内 `/api` 反代自动兜底（`proxy_pass` 指向集群内 Service DNS）

## 5.1 Nacos Pod 注入 Webhook 部署说明

启用「平台管理 → Nacos 管理」的 Pod 自动注入（Admission Webhook）时，kube-apiserver 需要能回调控制台的 HTTPS 端口（默认 9443，自签证书自动生成并持久化到 `certDir`）。两种回调模式：

**URL 模式（控制台在集群外，物理机/Nginx 部署）**：
- 前提：kube-apiserver 所在网络可访问控制台主机的 9443 端口（防火墙放行）
- 连接配置里 Webhook URL 填 `https://<控制台IP或域名>:9443/inject`（路径会自动追加 `/<cluster>` 区分集群）
- 9443 由控制台进程直接监听（自签 TLS），**不需要**经 Nginx 反代；caBundle 自动回填到 MutatingWebhookConfiguration

**Service 模式（控制台部署在集群内，deploy/ manifests）**：
- 前提：无额外网络要求（apiserver → ClusterIP 天然可达），且 server Service 已包含 9443 端口（`deploy/04-service` 已内置）
- 连接配置选 Service 模式，填控制台所在 ns（默认 `kube-console`）与 Service 名（默认 `kube-console-server`）、端口 9443

**安全与降级**：
- Webhook 仅对带 `nacos-injection=enabled` 标签的命名空间生效（同步器自动打标，可关）
- `failurePolicy: Ignore`：控制台宕机不影响业务 Pod 创建（只是暂不注入）
- 证书持久化在 `certDir`（K8s 部署挂 `/data` 卷），删除证书文件重启会重新自签，需等同步器刷新各集群的 caBundle

## 6. 部署后验证清单

| # | 验证项 | 入口 | 验证什么 |
| --- | --- | --- | --- |
| 1 | 登录 | 浏览器打开域名，admin 登录 | 同源反代基本链路 |
| 2 | 注册集群 | 集群管理 → 添加（粘贴 kubeconfig） | 后端到目标集群 apiserver 的出向网络 |
| 3 | 资源浏览 | 工作负载 / Pod 列表 | 常规 REST |
| 4 | **Pod 终端** | Pod → 终端，输入 `echo ok` | **WebSocket 透传**（Nginx 升级头） |
| 5 | **CI 实时日志** | CI 流水线 → 运行 → 看日志流 | **SSE**（proxy_buffering off） |
| 6 | 日志流滚动 | Pod → 日志 | 长连接/流式响应 |
| 7 | 刷新页面 | 任意二级页面 F5 | SPA history 回落 |
| 8 | 文件上传 | Pod 详情 → 文件 → 上传 | client_max_body_size |

## 7. 常见问题

| 现象 | 原因 / 处理 |
| --- | --- |
| 浏览器报 CORS / 跨域拒绝 | 前端直连了后端域名。必须同源：静态与 `/api` 挂同一 Nginx |
| 终端连上即断 / 一直转圈 | Nginx 缺 WS 升级头（`Upgrade`/`Connection`），或 `proxy_read_timeout` 太短 |
| CI 日志"卡住"最后一次性刷出 | `proxy_buffering` 未关 |
| 页面刷新 404 | 缺 `try_files ... /index.html` 回落 |
| 大文件上传 413 | `client_max_body_size` 未放宽 |
| 登录后部分接口 401 | token 超 24h 过期（`KC_JWT_EXPIRE_IN` 可调），重新登录即可；多浏览器不共享 token 属预期（localStorage） |
| 后端日志里出现乱码集群名 | 客户端 `X-Cluster` 头未做 URL 编码（本前端已处理；第三方接入时注意） |
