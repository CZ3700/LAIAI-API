#!/usr/bin/env bash
# LAIAI-NEXT 一键建站部署 —— 新服务器从零到可用站点，一条命令搞定。
#
#   cd infra && ./deploy.sh                      裸 HTTP :80（首次验证/内网）
#   ./deploy.sh --domain api.example.com         配域名，Caddy 自动 ACME 签 TLS
#   ./deploy.sh --prod                           生产：第三方镜像 digest 锁定（自动 pull+取）
#   ./deploy.sh --cn                             国内机器：Go/npm 走国内镜像源加速构建
#   ./deploy.sh --domain x --prod --cn           组合
#
# 做的事：① 检查 docker/openssl ② 首次自动生成全套密钥写 .env（chmod 600）
#         ③ --prod 自动锁镜像 digest ④ build + up ⑤ 等 core 健康 ⑥ 打印访问地址 + admin token
#
# 🚨 资金红线：**绝不覆盖已存在的 .env**。LAIAI_DATA_KEYS 是渠道凭证的信封加密密钥，
#    重新生成 = 所有已加密数据无法解密（变砖）。重复运行本脚本安全——已有 .env 原样复用。
set -euo pipefail

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)" # 切到 infra/ 目录
ENV_FILE=".env"

# ── 解析参数 ──
DOMAIN="" ; PROD=0 ; CN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain 需要一个域名}"; shift 2 ;;
    --domain=*) DOMAIN="${1#*=}"; shift ;;
    --prod) PROD=1; shift ;;
    --cn) CN=1; shift ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数：$1（--help 看用法）" >&2; exit 2 ;;
  esac
done

say() { printf '\033[1;36m▸ %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
die() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# ── 依赖检查 ──
command -v docker >/dev/null 2>&1 || die "未装 docker。参考 https://docs.docker.com/engine/install/"
command -v openssl >/dev/null 2>&1 || die "未装 openssl（生成密钥必需）：apt-get install -y openssl"
if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose"
else
  die "未装 docker compose 插件（docker compose v2 或 docker-compose）"
fi

# upsert_env KEY VALUE：安全项（domain/镜像 digest/构建源）写入 .env——存在则替换，否则追加。
# 只用于非密钥项；密钥仅在首次生成 .env 时一次性写入，绝不 upsert 覆盖。
upsert_env() {
  local key="$1" val="$2"
  if grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
    # 用 | 作 sed 分隔符（digest 含 /，值不会含 |）
    sed -i "s|^${key}=.*|${key}=${val}|" "$ENV_FILE"
  else
    printf '%s=%s\n' "$key" "$val" >> "$ENV_FILE"
  fi
}

# ── 首次：生成全套密钥（幂等：.env 已存在则跳过，绝不重生成）──
if [ -f "$ENV_FILE" ]; then
  say "复用现有 .env（不重新生成密钥）"
else
  # 变砖保险丝（对抗 review CONFIRMED）：.env 不在但数据卷还在（服务器迁移/误删 .env）时，
  # 重新生成 DATA_KEYS 会让旧库里所有信封加密的渠道凭证永远解不开，且脚本还会"成功"启动。
  if docker volume ls -q 2>/dev/null | grep -qE '(^|_)mysql-data$'; then
    die "检测到已有数据卷 mysql-data，但 infra/.env 缺失。此时生成新密钥会让旧库里所有信封加密的渠道凭证永远无法解密（平台变砖）。
     → 正确做法：恢复原 .env 备份到 infra/.env 后重跑本脚本。
     → 确要丢弃旧数据重来：先 docker volume rm <project>_mysql-data（删卷）再跑。"
  fi
  # hex 密钥：纯 [0-9a-f]，在 DSN/YAML/命令行零转义麻烦
  MYSQL_ROOT_PASSWORD=$(openssl rand -hex 24)
  MYSQL_PASSWORD=$(openssl rand -hex 24)
  REDIS_PASSWORD=$(openssl rand -hex 24)
  LAIAI_AUTH_SECRET=$(openssl rand -hex 32)   # 64 hex ≥ 32 字符
  LAIAI_ADMIN_TOKEN=$(openssl rand -hex 32)
  # 信封加密密钥必须是 base64 的 32 字节（core 强制），格式 k1:<base64>
  DATA_KEY=$(openssl rand -base64 32)
  umask 077 # 确保 .env 只 owner 可读
  cat > "$ENV_FILE" <<EOF
# LAIAI-NEXT 部署配置 —— 由 deploy.sh 自动生成于首次部署。
# 🚨 妥善保管：LAIAI_DATA_KEYS 丢失 = 所有渠道凭证无法解密。绝不重新生成覆盖。
MYSQL_ROOT_PASSWORD=${MYSQL_ROOT_PASSWORD}
MYSQL_PASSWORD=${MYSQL_PASSWORD}
REDIS_PASSWORD=${REDIS_PASSWORD}
LAIAI_AUTH_SECRET=${LAIAI_AUTH_SECRET}
LAIAI_ADMIN_TOKEN=${LAIAI_ADMIN_TOKEN}
LAIAI_DATA_KEYS=k1:${DATA_KEY}
LAIAI_DATA_KEY_ACTIVE=k1
LAIAI_INVOCATION_RETENTION_DAYS=30
LAIAI_TRUSTED_PROXIES=
LAIAI_DOMAIN=:80
EOF
  chmod 600 "$ENV_FILE"
fi

# ── 域名（非密钥，可安全 upsert）──
if [ -n "$DOMAIN" ]; then
  upsert_env LAIAI_DOMAIN "$DOMAIN"
  say "域名 → $DOMAIN（Caddy 将自动签发 TLS）"
fi

# ── 国内镜像源加速构建（非密钥）──
if [ "$CN" = 1 ]; then
  upsert_env GOPROXY "https://goproxy.cn,direct"
  upsert_env NPM_REGISTRY "https://registry.npmmirror.com"
  say "构建源 → 国内镜像（goproxy.cn / npmmirror）"
fi

# ── 生产 digest 锁定：自动 pull 第三方镜像并取真实 digest 写入 .env ──
COMPOSE_FILES="-f docker-compose.yml"
if [ "$PROD" = 1 ]; then
  COMPOSE_FILES="-f docker-compose.yml -f docker-compose.prod.yml"
  for spec in "MYSQL_IMAGE mysql:8.4" "REDIS_IMAGE redis:7-alpine"; do
    var="${spec%% *}"; tag="${spec#* }"
    cur=$(grep "^${var}=" "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)
    if [ -z "$cur" ] || printf '%s' "$cur" | grep -q "REPLACE"; then
      say "锁定 ${tag} digest…"
      docker pull "$tag" >/dev/null 2>&1 || warn "拉取 ${tag} 失败，尝试用本地已有镜像"
      # set -e 下 inspect 对无 RepoDigest 的镜像会非零退出并提前中断——|| true 让友好的 die 能兜住
      digest=$(docker image inspect "$tag" --format '{{index .RepoDigests 0}}' 2>/dev/null || true)
      [ -n "$digest" ] || die "取 ${tag} digest 失败——本地无该镜像的 RepoDigest（离线/未成功拉取）。联网后重试，或手动在 .env 设 ${var}=<image>@sha256:<digest>。"
      upsert_env "$var" "$digest"
    fi
  done
fi

# ── 起栈 ──
say "构建并启动（首次编译 core+caddy 约 2-3 分钟）…"
# shellcheck disable=SC2086
$COMPOSE $COMPOSE_FILES up -d --build

# ── 等 core 健康（直读容器健康态，不依赖 Caddy 路由）──
say "等待 core 就绪…"
cid=""
for _ in $(seq 1 20); do
  # shellcheck disable=SC2086
  cid=$($COMPOSE $COMPOSE_FILES ps -q core | head -1 || true)
  [ -n "$cid" ] && break
  sleep 1
done
[ -n "$cid" ] || die "core 容器未起来，查日志：$COMPOSE $COMPOSE_FILES logs core"

healthy=0
for _ in $(seq 1 60); do # 最多等 ~180s（含首次迁移；compose healthcheck start_period 90s）
  run=$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || echo "?")
  case "$run" in
    exited|dead) die "core 启动失败。查日志：$COMPOSE $COMPOSE_FILES logs core" ;;
  esac
  # core 在 compose 里定义了 healthcheck（wget /healthz）→ 直接读健康态
  status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$cid" 2>/dev/null || echo "")
  if [ "$status" = healthy ]; then healthy=1; break; fi
  sleep 3
done

# ── 汇报 ──
echo
if [ "$healthy" = 1 ]; then
  say "部署成功 ✅"
else
  warn "core 未在超时内报告健康——可能仍在迁移或构建。查日志：$COMPOSE $COMPOSE_FILES logs -f core"
fi

DOM=$(grep '^LAIAI_DOMAIN=' "$ENV_FILE" | cut -d= -f2-)
if [ -n "$DOM" ] && [ "$DOM" != ":80" ]; then
  echo "  控制台：https://${DOM}/    （首次访问 Caddy 签 TLS 需十几秒）"
  echo "  API：   https://${DOM}/v1  （OpenAI 兼容）"
else
  ip=$(hostname -I 2>/dev/null | awk '{print $1}'); [ -n "$ip" ] || ip="<server-ip>"
  echo "  控制台：http://${ip}/       （裸 HTTP；配 --domain 后自动 TLS）"
  echo "  API：   http://${ip}/v1"
fi
ADMIN=$(grep '^LAIAI_ADMIN_TOKEN=' "$ENV_FILE" | cut -d= -f2-)
echo "  管理员令牌（控制台 Admin 页填它解锁运营后台，等同超管权限，请妥善保存）："
echo "    ${ADMIN}"
echo
echo "  下一步：① 浏览器打开控制台，注册一个账号（默认无需邮箱验证）用于日常/建 API Key；"
echo "         ② 进 Admin 页，填上面的管理员令牌解锁后台 → 配渠道/定价/支付。"
echo "  所有密钥在 infra/.env（chmod 600）。务必备份——尤其 LAIAI_DATA_KEYS，丢了渠道凭证永远解不开。"
