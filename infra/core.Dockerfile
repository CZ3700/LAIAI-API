# core 控制面镜像（构建上下文 = 仓库根目录）
# 纪律：确定性构建（-trimpath、无时间戳）；生产部署按 digest pin（红线 7）
ARG GOPROXY=https://proxy.golang.org,direct

FROM golang:1.26-alpine AS builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY core/go.mod core/go.sum ./core/
RUN cd core && go mod download
COPY core/ ./core/
RUN cd core && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/core ./cmd/core && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 laiai
COPY --from=builder /out/core /out/migrate /app/
COPY migrations/ /app/migrations/
COPY infra/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
USER laiai
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
