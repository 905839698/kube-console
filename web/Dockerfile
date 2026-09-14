# 前端镜像构建（仓库根目录执行）
# 构建: docker build -f web/Dockerfile -t kube-console-web:latest .
# 产物: nginx 容器，/usr/share/nginx/html 为构建产物，反代配置来自 deploy/03-web/nginx.conf
FROM registry3.nhdc.cloud/proxy/library/node:20 AS builder
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM registry3.nhdc.cloud/proxy/library/nginx:1.27
COPY --from=builder /src/dist /usr/share/nginx/html
COPY deploy/03-web/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
