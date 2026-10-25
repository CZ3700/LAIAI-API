# core 控制面镜像（构建上下文 = 仓库根目录）
# 纪律：确定性构建（-trimpath、无时间戳）；生产部署按 digest pin（红线 7）
ARG GOPROXY=https://proxy.golang.org,direct
# Verified linux/amd64 compiler: go1.26.10. Releases may supply another digest,
# but the compiler below must still meet the security baseline.
ARG GO_IMAGE=docker.io/library/golang@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca
ARG ALPINE_IMAGE=alpine:3.21

FROM ${GO_IMAGE} AS builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY} GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY scripts/check-go-toolchain.sh ./scripts/check-go-toolchain.sh
RUN sh scripts/check-go-toolchain.sh
COPY core/go.mod core/go.sum ./core/
RUN cd core && go mod download
COPY core/ ./core/
RUN cd core && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/core ./cmd/core && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/schema-doctor ./cmd/schema-doctor && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/backfill-funds ./cmd/backfill-funds

FROM ${ALPINE_IMAGE}
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 laiai
COPY --from=builder /out/core /out/migrate /out/schema-doctor /out/backfill-funds /app/
COPY migrations/ /app/migrations/
COPY infra/entrypoint.sh /app/entrypoint.sh
# Release checkouts use umask 077; COPY retains their private file modes.
# Normalize only runtime code, never loosen the host's release/secret files.
RUN chmod 0755 /app/entrypoint.sh && \
    find /app/migrations -type d -exec chmod 0755 {} + && \
    find /app/migrations -type f -exec chmod 0644 {} +
USER laiai
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
