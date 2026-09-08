# 后端镜像构建（可选，用于部署到集群）
# 构建: docker build -f server/Dockerfile -t kube-console-server:latest .
# 前端构建产物由 web/dist 提供，可另行打包为静态文件镜像
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -o /kube-console-server ./cmd/server

FROM alpine:3.20
# git: CI 的 /ci/repo/refs 用 git ls-remote 拉取分支/Tag
RUN apk add --no-cache ca-certificates tzdata git
WORKDIR /app
COPY --from=builder /kube-console-server /app/kube-console-server
EXPOSE 8080
ENTRYPOINT ["/app/kube-console-server"]
