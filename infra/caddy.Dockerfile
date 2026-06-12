# Caddy 入口镜像：Console 构建产物烤进镜像（构建上下文 = 仓库根目录）。
# 纪律：前端产物绝不 bind mount（禁止移植黑名单——宿主机 rm -rf 导致容器挂载变空的生产 404 事故类）；
# Caddyfile 是配置，保留 ro 挂载（改配置 reload 即可，不需要重建镜像）。
ARG NPM_REGISTRY=https://registry.npmjs.org

FROM node:22-alpine AS webbuilder
ARG NPM_REGISTRY
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund --registry=${NPM_REGISTRY}
COPY web/ ./
RUN npm run build

FROM caddy:2-alpine
COPY --from=webbuilder /src/web/dist /srv/console
