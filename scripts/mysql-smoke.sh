#!/usr/bin/env sh
# mysql-smoke.sh —— 真 MySQL 迁移冒烟（P1-7）
#
# 在一次性 MySQL 8.4 容器上验证 migrations/ 的真实行为（SQLite 单测验不出 MySQL DDL 语义）：
#   1) 全量 migrate 一次成功；
#   2) -status 0 pending（到 head）；
#   3) 再 migrate 一次幂等（up to date）；
#   4) schema-doctor 在线漂移体检 OK（无 pending / 无 unknown / 期望表齐全）。
# 用完即焚。CI 与本地部署前都应跑。
#
# 依赖：docker、go。用法：scripts/mysql-smoke.sh   （从仓库根运行）
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PORT=${MYSQL_SMOKE_PORT:-13306}
PW=smokepw
CT=laiai-mysql-smoke
IMG=${MYSQL_SMOKE_IMAGE:-mysql:8.4}
DSN="root:${PW}@tcp(127.0.0.1:${PORT})/laiai?parseTime=true&charset=utf8mb4&loc=UTC"

cleanup() { docker rm -f "$CT" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

echo ">> 启动一次性 MySQL ($IMG) 于 :$PORT"
docker run -d --name "$CT" \
  -e MYSQL_ROOT_PASSWORD="$PW" -e MYSQL_DATABASE=laiai \
  -p "${PORT}:3306" "$IMG" >/dev/null

echo ">> 等待 MySQL 健康"
i=0
until docker exec "$CT" mysqladmin ping -h127.0.0.1 -uroot -p"$PW" --silent >/dev/null 2>&1; do
  i=$((i+1)); [ "$i" -gt 60 ] && { echo "MySQL 启动超时" >&2; docker logs "$CT" | tail -20 >&2; exit 1; }
  sleep 2
done

cd "$ROOT/core"

echo ">> 1/4 全量 migrate"
LAIAI_DSN="$DSN" go run ./cmd/migrate -dir ../migrations

echo ">> 2/4 status 须 0 pending"
out=$(LAIAI_DSN="$DSN" go run ./cmd/migrate -dir ../migrations -status)
echo "$out"
echo "$out" | grep -q "pending (0)" || { echo "FAIL: 仍有 pending 迁移" >&2; exit 1; }

echo ">> 3/4 再 migrate 须幂等"
out=$(LAIAI_DSN="$DSN" go run ./cmd/migrate -dir ../migrations)
echo "$out"
echo "$out" | grep -q "up to date" || { echo "FAIL: 重跑非幂等" >&2; exit 1; }

echo ">> 4/4 schema-doctor 在线漂移体检"
LAIAI_DSN="$DSN" go run ./cmd/schema-doctor -dir ../migrations

echo ">> PASS: MySQL 迁移冒烟通过"
