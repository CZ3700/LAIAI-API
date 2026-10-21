#!/usr/bin/env bash
# Real Docker/MySQL regression. Run only on an isolated Linux test runner.
# No production configuration, credentials, volumes or exposed ports are used.
set -Eeuo pipefail
ROOT=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
WORK=$(mktemp -d)
NAME="laiai-permissions-$(basename "$WORK" | tr '[:upper:]' '[:lower:]')"
IMAGE="$NAME:ci"
MYSQL_IMAGE=${MYSQL_SMOKE_IMAGE:-mysql:8.4}
cleanup() {
  docker rm -fv "$NAME-mysql" >/dev/null 2>&1 || true
  docker network rm "$NAME" >/dev/null 2>&1 || true
  docker image rm "$IMAGE" >/dev/null 2>&1 || true
  rm -rf -- "$WORK"
}
trap cleanup EXIT

# A new checkout is essential: setting umask after checkout cannot reproduce it.
(
  umask 077
  git -c core.sharedRepository=false clone --quiet --no-local --no-hardlinks "$ROOT" "$WORK/source"
)
test "$(stat -c %a "$WORK/source/infra/entrypoint.sh")" = 600
sql_files=("$WORK/source/migrations/"*.sql)
test -f "${sql_files[0]}"
test "$(stat -c %a "${sql_files[0]}")" = 600
test "$(stat -c %a "$WORK/source/migrations")" = 700
docker build -f "$WORK/source/infra/core.Dockerfile" -t "$IMAGE" "$WORK/source"

# Read/parse the actual entrypoint, rather than checking only its execute bit.
docker run --rm --network none --read-only --entrypoint sh "$IMAGE" -ec '
  test "$(id -u)" = 10001
  test -x /app/entrypoint.sh
  sh -n /app/entrypoint.sh
'
docker run --rm --network none --read-only --entrypoint /app/schema-doctor "$IMAGE" \
  -lint -dir /app/migrations

docker network create --internal "$NAME" >/dev/null
docker run -d --name "$NAME-mysql" --network "$NAME" \
  -e MYSQL_ROOT_PASSWORD=image-permissions-test-only -e MYSQL_ROOT_HOST=% \
  -e MYSQL_DATABASE=permissions_test "$MYSQL_IMAGE" >/dev/null
ready=0
for ((i=0; i<90; i++)); do
  if docker exec -e MYSQL_PWD=image-permissions-test-only "$NAME-mysql" \
    mysql -h 127.0.0.1 -uroot -Nse 'SELECT 1' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 2
done
test "$ready" = 1 || { echo 'Isolated MySQL did not become ready' >&2; exit 1; }
DSN="root:image-permissions-test-only@tcp($NAME-mysql:3306)/permissions_test?parseTime=true"
run_tool() {
  local tool=$1; shift
  docker run --rm --network "$NAME" --read-only -e LAIAI_DSN="$DSN" \
    --entrypoint "/app/$tool" "$IMAGE" "$@"
}

# An empty database forces actual reads; -status on an applied database does not.
run_tool migrate -dir /app/migrations
expected=$(find "$WORK/source/migrations" -maxdepth 1 -name '*.sql' | wc -l)
actual=$(docker exec -e MYSQL_PWD=image-permissions-test-only "$NAME-mysql" \
  mysql -uroot -Nse 'SELECT COUNT(*) FROM permissions_test.schema_migrations')
test "$actual" = "$expected"
replayed=$(run_tool migrate -dir /app/migrations)
test "$replayed" = 'up to date'
run_tool schema-doctor -dir /app/migrations
echo 'PASS: umask 077 image supports non-root entrypoint reads and full MySQL migration/replay'
