#!/bin/sh
# 一键部署 entrypoint：先应用迁移（带重试等 MySQL 就绪），再启动 core。
# migrate 失败 30 次（约 60s）则退出 —— 宁可容器明确失败，不要静默跳过 schema。
set -e

i=0
until /app/migrate -dir /app/migrations; do
  i=$((i+1))
  if [ "$i" -ge 30 ]; then
    echo "FATAL: migrations failed after 30 attempts" >&2
    exit 1
  fi
  echo "migrate: database not ready, retrying ($i/30)..."
  sleep 2
done

exec /app/core
