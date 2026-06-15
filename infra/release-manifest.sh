#!/usr/bin/env sh
# release-manifest.sh —— LAIAI-NEXT 部署快照 + 漂移检测（P1-6）
#
# 记录"此刻实际在跑的"运行态指纹：四个容器的运行镜像 ID（sha256）与配置镜像引用、
# 以及 compose/Caddyfile/.env 的 sha256（仅哈希，**绝不**记录 .env 明文值）、git commit。
# 这些都是从真实本地状态读出，无任何伪造。
#
# 用法：
#   ./release-manifest.sh generate [out.json]      生成当前快照（默认写 stdout）
#   ./release-manifest.sh diff baseline.json        对比当前与基线，发现漂移 exit 1
#
# 典型流程：部署后 generate > releases/$(git rev-parse --short HEAD).json 留档；
# 下次部署前 diff 上一份基线，确认只有预期项变化（揪静默回滚/镜像被换）。
#
# 依赖：docker、sha256sum（或 shasum）、git；diff 子命令需要 jq。
set -eu

INFRA_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE="$INFRA_DIR/docker-compose.yml"
PROD_OVERLAY="$INFRA_DIR/docker-compose.prod.yml"
CADDYFILE="$INFRA_DIR/Caddyfile"
ENVFILE="$INFRA_DIR/.env"

# 容器名（与 docker-compose.yml 的 container_name 对齐）
C_CORE=laiai-core
C_CADDY=laiai-caddy
C_MYSQL=laiai-mysql
C_REDIS=laiai-redis

sha_of() { # 文件 sha256（缺失则 MISSING）；只输出哈希，不泄露内容
  if [ -f "$1" ]; then
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
    else shasum -a 256 "$1" | cut -d' ' -f1; fi
  else echo "MISSING"; fi
}

# 容器运行镜像 ID（sha256:...）——"实际在跑的内容"指纹；容器不存在则 ABSENT
running_image_id() { docker inspect --format '{{.Image}}' "$1" 2>/dev/null || echo "ABSENT"; }
# 容器配置镜像引用（如 mysql:8.4@sha256:...）；容器不存在则 ABSENT
configured_image_ref() { docker inspect --format '{{.Config.Image}}' "$1" 2>/dev/null || echo "ABSENT"; }

json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

generate() {
  git_commit=$(git -C "$INFRA_DIR" rev-parse HEAD 2>/dev/null || echo "unknown")
  git_dirty=false
  if ! git -C "$INFRA_DIR" diff --quiet 2>/dev/null || ! git -C "$INFRA_DIR" diff --cached --quiet 2>/dev/null; then
    git_dirty=true
  fi
  compose_sha=$(sha_of "$COMPOSE")
  overlay_sha=$(sha_of "$PROD_OVERLAY")
  caddy_sha=$(sha_of "$CADDYFILE")
  env_sha=$(sha_of "$ENVFILE")

  cat <<EOF
{
  "schema_version": 1,
  "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "git_commit": "$(json_escape "$git_commit")",
  "git_dirty": $git_dirty,
  "config_sha256": {
    "docker_compose": "$compose_sha",
    "docker_compose_prod": "$overlay_sha",
    "caddyfile": "$caddy_sha",
    "env": "$env_sha"
  },
  "services": {
    "core":  { "image_id": "$(running_image_id $C_CORE)",  "image_ref": "$(json_escape "$(configured_image_ref $C_CORE)")" },
    "caddy": { "image_id": "$(running_image_id $C_CADDY)", "image_ref": "$(json_escape "$(configured_image_ref $C_CADDY)")" },
    "mysql": { "image_id": "$(running_image_id $C_MYSQL)", "image_ref": "$(json_escape "$(configured_image_ref $C_MYSQL)")" },
    "redis": { "image_id": "$(running_image_id $C_REDIS)", "image_ref": "$(json_escape "$(configured_image_ref $C_REDIS)")" }
  }
}
EOF
}

diff_baseline() {
  baseline="$1"
  [ -f "$baseline" ] || { echo "baseline not found: $baseline" >&2; exit 2; }
  command -v jq >/dev/null 2>&1 || { echo "diff requires jq" >&2; exit 2; }
  current=$(generate)

  # 比较关键字段：四服务 image_id + 全部 config sha256 + git_commit。
  drift=0
  paths="services.core.image_id services.caddy.image_id services.mysql.image_id services.redis.image_id config_sha256.docker_compose config_sha256.docker_compose_prod config_sha256.caddyfile config_sha256.env git_commit"
  for p in $paths; do
    b=$(jq -r ".$p // \"MISSING\"" "$baseline")
    c=$(printf '%s' "$current" | jq -r ".$p // \"MISSING\"")
    if [ "$b" != "$c" ]; then
      echo "DRIFT  $p"
      echo "   baseline: $b"
      echo "   current : $c"
      drift=1
    fi
  done
  if [ "$drift" -eq 0 ]; then
    echo "OK: no drift vs baseline ($baseline)"
  fi
  exit "$drift"
}

cmd="${1:-}"
case "$cmd" in
  generate)
    shift || true
    if [ "${1:-}" != "" ]; then generate > "$1"; echo "wrote $1" >&2; else generate; fi
    ;;
  diff)
    shift || true
    diff_baseline "${1:?usage: release-manifest.sh diff baseline.json}"
    ;;
  *)
    echo "usage: $0 {generate [out.json] | diff baseline.json}" >&2
    exit 2
    ;;
esac
