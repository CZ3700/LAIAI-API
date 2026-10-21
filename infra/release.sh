#!/usr/bin/env bash
# Production release coordinator. Application images are built on this host, never pulled.
set -Eeuo pipefail
set +x
umask 077
TOOL_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/release" && pwd)
export TOOL_DIR
COMMAND='' CONFIG=/etc/laiai/release.conf RELEASE_ID='' YES=0 STAGE='' EVIDENCE=''
PHASE=initialization RUN_DIR=''
# Shared interface consumed by the sourced backup module.
export OLD_CORE_IMAGE OLD_CADDY_IMAGE RELEASE_CONFIG_DIR

usage() {
  cat <<'EOF'
Usage: bash infra/release.sh [--config FILE] COMMAND [--release ID] [--yes]
  compare      Compare the running/live checkout with GitHub main (no deployment).
  preflight    Pin main, capture the production baseline and check prerequisites.
  backup       Encrypt database/configuration backups and archive old images.
  build        Build the pinned core and Caddy images sequentially.
  deploy       Enter maintenance, stop writes, final backup, migrate, backfill, switch.
  open         Verify the candidate and approved acceptance, then remove maintenance.
  release      preflight -> backup -> build -> deploy -> open (same pinned plan).
  rollback     Restore recorded application images/configuration, never restore DB.
  verify       Check running candidate, funds invariants and HTTPS health.
  status       Show a release's recorded stages, without changing services.
  approve      Record reviewed external evidence for a release stage:
               --stage before-maintenance|before-open|before-rollback --evidence FILE
All mutating commands require a trusted root-owned configuration and an exclusive lock.
--yes removes the interactive prompt; it does not bypass any validation or approval.
EOF
}

die() {
  [[ -z "${ACTIVE_RUN_DIR:-}" ]] || printf '%s\n' "$PHASE" > "$ACTIVE_RUN_DIR/failed-stage"
  printf 'ERROR: %s\n' "$*" >&2; exit 1
}
log() { printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*"; }
state() { python3 "$TOOL_DIR/state.py" "$@"; }
get() { state get "$PLAN" "$1"; }
mark() { printf '%s\n' "$(date -u +%FT%TZ)" > "$RUN_DIR/$1.ok"; }
require_mark() { [[ -f "$RUN_DIR/$1.ok" ]] || die "Missing successful stage: $1"; }
failed() {
  local code=$?
  trap - ERR
  [[ -z "${ACTIVE_RUN_DIR:-}" ]] || printf '%s\n' "$PHASE" > "$ACTIVE_RUN_DIR/failed-stage"
  printf 'FAILED at %s (exit %s). No automatic database rollback. Check release status.\n' "$PHASE" "$code" >&2
  exit "$code"
}
trap failed ERR
trap 'exit 130' INT
trap 'exit 143' TERM

while (($#)); do
  case "$1" in
    --config|--release|--stage|--evidence)
      (($# >= 2)) || die "Missing argument: $1"
      case "$1" in
        --config) CONFIG=$2;; --release) RELEASE_ID=$2;; --stage) STAGE=$2;; --evidence) EVIDENCE=$2;;
      esac; shift 2;;
    --yes) YES=1; shift;;
    -h|--help|help) usage; exit 0;;
    --*) die "Unknown option: $1";;
    *) [[ -z "$COMMAND" ]] || die 'Only one command is allowed'; COMMAND=$1; shift;;
  esac
done
case "$COMMAND" in compare|preflight|backup|build|deploy|open|release|rollback|verify|status|approve) ;; *) usage; exit 2;; esac
[[ $(id -u) == 0 ]] || die 'Run on the production Linux host as root.'
[[ $(uname -s) == Linux ]] || die 'Production execution requires Linux.'

trusted_file() {
  local path=$1 mode
  [[ "$path" = /* && -f "$path" && ! -L "$path" ]] || die "Required regular absolute file: $path"
  [[ $(stat -c %u -- "$path") == 0 ]] || die "File must be root-owned: $path"
  mode=$(stat -c %a -- "$path")
  (( (8#$mode & 0022) == 0 )) || die "File is writable by another user: $path"
}
secret_file() {
  trusted_file "$1"
  local mode; mode=$(stat -c %a -- "$1")
  (( (8#$mode & 0077) == 0 )) || die "Secret/configuration must be mode 0600 or 0400: $1"
}
secret_file "$CONFIG"
# This is trusted operator code, not an untrusted dotenv or downloaded file.
# shellcheck disable=SC1090
source "$CONFIG"
set -Eeuo pipefail
set +x
: "${LIVE_DIR:=/opt/laiai-next}" "${STATE_DIR:=/var/lib/laiai-release}"
: "${SOURCE_REPOSITORY:=https://github.com/CZ3700/LAIAI-API.git}" "${COMPOSE_PROJECT:=infra}"
: "${LIVE_ENV:=$LIVE_DIR/infra/.env}" "${VIDEO_ENV:=}" "${CADDYFILE:=$LIVE_DIR/infra/Caddyfile}"
: "${PUBLIC_ORIGIN:=https://laiai.ai}" "${BACKUP_KEY_FILE:=/root/.laiai-release-backup.key}"
: "${MIN_FREE_MB:=8192}" "${MIN_AVAILABLE_MB:=1024}" "${HOOK_TIMEOUT:=600}"
: "${CI_CHECK_HOOK:=$TOOL_DIR/check-github-ci.sh}" "${CHECK_HOOK:=$TOOL_DIR/check-approval.sh}"
: "${GO_IMAGE:=}" "${ALPINE_IMAGE:=}" "${NODE_IMAGE:=}" "${CADDY_BASE_IMAGE:=}"
export LIVE_DIR STATE_DIR SOURCE_REPOSITORY COMPOSE_PROJECT LIVE_ENV VIDEO_ENV CADDYFILE PUBLIC_ORIGIN
export BACKUP_KEY_FILE MIN_FREE_MB MIN_AVAILABLE_MB HOOK_TIMEOUT CI_CHECK_HOOK CHECK_HOOK
export GO_IMAGE ALPINE_IMAGE NODE_IMAGE CADDY_BASE_IMAGE
[[ "$COMPOSE_PROJECT" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || die 'Invalid Compose project.'
[[ "$PUBLIC_ORIGIN" =~ ^https://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]] || die 'PUBLIC_ORIGIN must be an HTTPS origin.'
[[ "$SOURCE_REPOSITORY" == https://github.com/CZ3700/LAIAI-API.git || "$SOURCE_REPOSITORY" == git@github.com:CZ3700/LAIAI-API.git ]] || die 'Repository must be CZ3700/LAIAI-API without embedded credentials.'
for path in "$LIVE_DIR" "$STATE_DIR"; do [[ "$path" = /* && "$path" != / && ! -L "$path" ]] || die 'Invalid directory'; done
for number in "$MIN_FREE_MB" "$MIN_AVAILABLE_MB" "$HOOK_TIMEOUT"; do [[ "$number" =~ ^[1-9][0-9]*$ ]] || die 'Invalid positive resource/timeout limit'; done
for tool in git docker python3 openssl tar gzip flock curl sha256sum timeout realpath; do command -v "$tool" >/dev/null || die "Missing tool: $tool"; done
[[ $(realpath -m -- "$STATE_DIR") != "$(realpath -- "$LIVE_DIR")" && $(realpath -m -- "$STATE_DIR") != "$(realpath -- "$LIVE_DIR")"/* ]] || die 'STATE_DIR must be outside the live source tree.'
mkdir -p -- "$STATE_DIR"
[[ $(stat -c %u -- "$STATE_DIR") == 0 ]] || die 'State directory is not root-owned.'
chmod 700 -- "$STATE_DIR"
exec 9>"$STATE_DIR/.lock"
flock -n 9 || die 'Another release command is running.'
if [[ -n "$RELEASE_ID" ]]; then
  [[ "$RELEASE_ID" =~ ^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}$ ]] || die 'Invalid release ID.'
  RUN_DIR="$STATE_DIR/releases/$RELEASE_ID"
  [[ -d "$RUN_DIR" && ! -L "$RUN_DIR" ]] || die 'Release does not exist.'
fi
confirm() {
  ((YES)) && return
  [[ -t 0 ]] || die 'Use --yes for an unattended run after reviewing the pinned plan.'
  local answer
  read -r -p "Execute $COMMAND for ${RELEASE_ID:-main}? Type yes: " answer
  [[ "$answer" == yes ]] || die 'Cancelled.'
}
load_plan() {
  [[ -n "$RUN_DIR" ]] || die 'Specify --release ID from preflight.'
  PLAN="$RUN_DIR/plan.json"; trusted_file "$PLAN"
  ACTIVE_RUN_DIR=$RUN_DIR
  TARGET=$(get commit)
  [[ "$TARGET" =~ ^[0-9a-f]{40}$ ]] || die 'Invalid planned commit.'
  SOURCE_DIR="$STATE_DIR/sources/$TARGET"
}
verify_config() {
  [[ "$(state config-hash)" == "$(get config_hash)" ]] || die 'Configuration/tool/evidence hooks changed; create a fresh preflight plan.'
}
remote_main() {
  local line
  line=$(git ls-remote --exit-code "$SOURCE_REPOSITORY" refs/heads/main 2>/dev/null) || die 'Cannot read remote main using the configured Git identity.'
  MAIN_SHA=${line%%[[:space:]]*}
  [[ "$MAIN_SHA" =~ ^[0-9a-f]{40}$ ]] || die 'Remote main did not resolve to a full commit.'
}
fetch_main() {
  [[ -d "$STATE_DIR/repository.git" ]] || git init --bare --quiet "$STATE_DIR/repository.git"
  git --git-dir="$STATE_DIR/repository.git" fetch --quiet --no-tags "$SOURCE_REPOSITORY" \
    +refs/heads/main:refs/remotes/origin/main 2>/dev/null || die 'Unable to fetch main.'
  [[ $(git --git-dir="$STATE_DIR/repository.git" rev-parse refs/remotes/origin/main) == "$MAIN_SHA" ]] || die 'main moved during preflight; retry.'
}
find_containers() {
  local role; local -a ids
  for role in core caddy mysql redis; do
    mapfile -t ids < <(docker ps -aq --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" --filter "label=com.docker.compose.service=$role")
    ((${#ids[@]} == 1)) || die "Expected exactly one $role container in project $COMPOSE_PROJECT."
    printf -v "${role^^}_CID" '%s' "${ids[0]}"
  done
}
snapshot() {
  find_containers
  docker inspect "$CORE_CID" "$CADDY_CID" "$MYSQL_CID" "$REDIS_CID" | state inspect "$@"
  # A failed application startup must not prevent rollback. Running Caddy still
  # has to expose a verifiable admin configuration; only a stopped one may skip it.
  if [[ ${2:-} == allow-stopped-apps && $(state get "$1" caddy.running) == False ]]; then
    return
  fi
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ | state caddy-hash "$1"
}
compare_versions() {
  remote_main; fetch_main; find_containers
  local live running
  live=$(git -C "$LIVE_DIR" rev-parse HEAD)
  running=$(docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$CORE_CID")
  [[ "$running" =~ ^[0-9a-f]{40}$ ]] || running=$live
  log "main: $MAIN_SHA"
  log "Running version: $running (legacy images without a revision label use the live checkout baseline)"
  log "Live checkout: $live"
  if git --git-dir="$STATE_DIR/repository.git" cat-file -e "$running^{commit}" 2>/dev/null; then
    git --git-dir="$STATE_DIR/repository.git" rev-list --left-right --count "$running...$MAIN_SHA"
    git --git-dir="$STATE_DIR/repository.git" diff --stat "$running" "$MAIN_SHA"
    git --git-dir="$STATE_DIR/repository.git" merge-base --is-ancestor "$running" "$MAIN_SHA" || die 'Running commit is not an ancestor of main; automatic downgrade/divergence is forbidden.'
  else die 'Running commit is absent from the fetched main history.'; fi
  local dirty
  dirty=$(git -C "$LIVE_DIR" status --porcelain --untracked-files=all)
  # Existing, untracked release records are data; all other dirt blocks source-based deployment.
  dirty=$(printf '%s\n' "$dirty" | sed '/^?? infra\/releases\/[^/]*\.json$/d; /^$/d')
  [[ -z "$dirty" ]] || die 'Live checkout has unreviewed changes; they will not be overwritten.'
}
capacity() {
  local free available docker_root docker_free
  free=$(df -Pm "$STATE_DIR" | awk 'NR==2 {print $4}')
  docker_root=$(docker info --format '{{.DockerRootDir}}')
  [[ -d "$docker_root" ]] || die 'Cannot inspect Docker storage filesystem.'
  docker_free=$(df -Pm "$docker_root" | awk 'NR==2 {print $4}')
  available=$(awk '/^MemAvailable:/ {print int($2/1024)}' /proc/meminfo)
  log "Available archive disk=${free} MiB; Docker disk=${docker_free} MiB; RAM=${available} MiB; configured minimum=${MIN_FREE_MB}/${MIN_AVAILABLE_MB} MiB"
  ((free >= MIN_FREE_MB && docker_free >= MIN_FREE_MB && available >= MIN_AVAILABLE_MB)) || die 'Insufficient release/build capacity. No automatic cache or volume cleanup is performed.'
}
ci_check() {
  trusted_file "$CI_CHECK_HOOK"; [[ -x "$CI_CHECK_HOOK" ]] || die 'CI checker is not executable.'
  timeout "$HOOK_TIMEOUT" "$CI_CHECK_HOOK" "$TARGET" > "$RUN_DIR/ci.txt" 2>/dev/null || die 'The exact main commit has no verified successful CI; see configured CI checker.'
}
external_check() {
  trusted_file "$CHECK_HOOK"; [[ -x "$CHECK_HOOK" ]] || die 'Acceptance checker is not executable.'
  timeout "$HOOK_TIMEOUT" "$CHECK_HOOK" "$1" "$PLAN" >/dev/null 2>&1 || die "Missing/failed external evidence for $1. Record it with approve, or use the configured acceptance checker."
}
checkout_source() {
  if [[ ! -d "$SOURCE_DIR" ]]; then
    mkdir -p "$STATE_DIR/sources"
    git clone --quiet --no-checkout "$STATE_DIR/repository.git" "$SOURCE_DIR"
    git -C "$SOURCE_DIR" checkout --quiet --detach "$TARGET"
  fi
  [[ $(git -C "$SOURCE_DIR" rev-parse HEAD) == "$TARGET" ]] || die 'Source commit mismatch.'
  [[ -z $(git -C "$SOURCE_DIR" status --porcelain --untracked-files=all) ]] || die 'Candidate source tree is dirty.'
  for path in infra/core.Dockerfile infra/caddy.Dockerfile core/cmd/backfill-funds/main.go core/cmd/schema-doctor/main.go; do
    [[ -f "$SOURCE_DIR/$path" ]] || die "main does not yet include required release functionality: $path"
  done
}
images_present() {
  local image
  for image in "$@"; do
    [[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'Not a local immutable image ID.'
    [[ $(docker image inspect --format '{{.Id}}' "$image" 2>/dev/null) == "$image" ]] || die 'A recorded image is missing; do not rebuild or pull a replacement.'
  done
}
compose() {
  local old=${1:-new} base
  [[ "$old" == old ]] && base="$RUN_DIR/config/rollback-compose.json" || base="$SOURCE_DIR/infra/docker-compose.yml"
  if [[ "$old" == old ]]; then
    (cd "$RUN_DIR/config" && sha256sum -c rollback-compose.sha256 >/dev/null)
  fi
  state compose "$PLAN" "$RUN_DIR/images.json" "$RUN_DIR/$old-compose.yml" "$old"
  DC=(docker compose --project-name "$COMPOSE_PROJECT" --project-directory "$LIVE_DIR/infra" --env-file "$LIVE_ENV")
  [[ -z "$VIDEO_ENV" || "$old" == old ]] || DC+=(--env-file "$VIDEO_ENV")
  DC+=(-f "$base" -f "$RUN_DIR/$old-compose.yml")
  "${DC[@]}" config --format json 2>/dev/null | state check-compose "$PLAN" "$old"
}
adapt_caddy() {
  mkdir -p "$RUN_DIR/config"
  # Read the active runtime configuration, including prior reloads and rendered env values.
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ > "$RUN_DIR/config/original.json" 2>/dev/null
  state maintenance "$RUN_DIR/config/original.json" "$RUN_DIR/config/maintenance.json"
  cp "$RUN_DIR/config/maintenance.json" "$RUN_DIR/config/active.json"
}
reload_caddy() {
  timeout 60 docker exec -i "$CADDY_CID" caddy validate --config /dev/stdin < "$1" >/dev/null 2>&1 || return 1
  timeout 60 docker exec -i "$CADDY_CID" caddy reload --config /dev/stdin < "$1" >/dev/null 2>&1
}
health() { curl --fail --silent --show-error --connect-timeout 10 --max-time 30 "$PUBLIC_ORIGIN/healthz" >/dev/null; }
maintenance_visible() {
  local status
  status=$(curl --silent --show-error --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' "$PUBLIC_ORIGIN/api/status") || return 1
  [[ "$status" == 503 ]]
}
restore_ingress() {
  # Preserve the inode of the bind-mounted active configuration.
  if cat "$RUN_DIR/config/original.json" > "$RUN_DIR/config/active.json" && reload_caddy "$RUN_DIR/config/active.json" && health; then
    return
  fi
  if cat "$RUN_DIR/config/maintenance.json" > "$RUN_DIR/config/active.json" && reload_caddy "$RUN_DIR/config/active.json" && maintenance_visible; then
    die 'Opening failed; maintenance was restored and publicly verified. Inspect health before retrying.'
  fi
  # An unverified maintenance reload must not leave a failed candidate public.
  if docker stop -t 10 "$CADDY_CID" >/dev/null 2>&1; then
    die 'Opening failed and maintenance could not be confirmed; Caddy was stopped to close ingress. Operator recovery is required.'
  fi
  die 'Opening failed; neither maintenance nor stopping Caddy could be confirmed. Inspect and close public ingress immediately.'
}
same_live() {
  snapshot "$RUN_DIR/latest-snapshot.json"
  state same "$RUN_DIR/initial-snapshot.json" "$RUN_DIR/latest-snapshot.json"
}
require_current_main() { remote_main; [[ "$MAIN_SHA" == "$TARGET" ]] || die 'main changed after preflight. Create a fresh release; do not deploy a different build.'; }

# shellcheck source=release/backup.sh
source "$TOOL_DIR/backup.sh"
# shellcheck source=release/check-data.sh
source "$TOOL_DIR/check-data.sh"
# shellcheck source=release/rollback-snapshot.sh
source "$TOOL_DIR/rollback-snapshot.sh"

preflight() {
  PHASE=preflight
  state compose-version "$(docker compose version --short)"
  compare_versions
  secret_file "$LIVE_ENV"; [[ -z "$VIDEO_ENV" ]] || secret_file "$VIDEO_ENV"
  trusted_file "$CADDYFILE"; secret_file "$BACKUP_KEY_FILE"
  [[ $(stat -c %a -- "$BACKUP_KEY_FILE") == 600 ]] || die 'Backup key must have mode 0600.'
  local first_line_length
  first_line_length=$(awk 'NR == 1 { n = length($0) } END { print n + 0 }' "$BACKUP_KEY_FILE")
  ((first_line_length >= 32)) || die 'Backup key first line must contain at least 32 characters.'
  trusted_file "$CHECK_HOOK"; trusted_file "$CI_CHECK_HOOK"
  if [[ -z "$RUN_DIR" ]]; then
    RELEASE_ID="$(date -u +%Y%m%dT%H%M%SZ)-${MAIN_SHA:0:12}"
    RUN_DIR="$STATE_DIR/releases/$RELEASE_ID"
    mkdir -p "$STATE_DIR/releases"
    mkdir "$RUN_DIR"
    PLAN="$RUN_DIR/plan.json"
    snapshot "$RUN_DIR/initial-snapshot.json"
    state plan "$PLAN" "$RUN_DIR/initial-snapshot.json" "$MAIN_SHA"
  fi
  load_plan; verify_config; require_current_main; same_live
  [[ ! -f "$RUN_DIR/maintenance-started.ok" ]] || die 'A deployment already started; use status/verify/open/rollback.'
  log "Release ID: $RELEASE_ID"
  capacity; checkout_source; ci_check
  local ref
  for ref in "$GO_IMAGE" "$ALPINE_IMAGE" "$NODE_IMAGE" "$CADDY_BASE_IMAGE"; do
    [[ "$ref" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]] || die 'All four base images need verified repository@sha256 references.'
  done
  check_data preflight
  state schema "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations" subset
  if [[ ! -f "$RUN_DIR/build.ok" ]]; then
    adapt_caddy
    freeze_rollback_runtime
    # Use existing image IDs only to validate planned mounts before a build exists.
    state images "$RUN_DIR/images.json" "$(get snapshot.core.image)" "$(get snapshot.caddy.image)"
  fi
  compose new
  health
  mark preflight
  log 'Preflight passed. No production services or database were changed.'
}
backup_stage() {
  PHASE=backup
  load_plan; require_mark preflight; verify_config; same_live; capacity
  [[ ! -e "$RUN_DIR/backup" ]] || die 'Backup stage already exists; do not overwrite it.'
  local parent=$RUN_DIR
  RELEASE_CONFIG_DIR="$parent/config"
  OLD_CORE_IMAGE=$(get snapshot.core.image); OLD_CADDY_IMAGE=$(get snapshot.caddy.image)
  mkdir "$parent/backup"
  RUN_DIR="$parent/backup"; release_backup; RUN_DIR=$parent
  mark backup
}
build_stage() {
  PHASE=build
  load_plan; require_mark preflight; require_mark backup; verify_config; same_live; checkout_source; ci_check; capacity
  [[ ! -f "$RUN_DIR/build.ok" ]] || die 'Build is immutable. Use the existing images or a fresh release.'
  local ref core_tag caddy_tag
  for ref in "$GO_IMAGE" "$ALPINE_IMAGE" "$NODE_IMAGE" "$CADDY_BASE_IMAGE"; do
    [[ "$ref" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]] || die 'All four base images need verified repository@sha256 references.'
  done
  core_tag="laiai/core:$TARGET-$RELEASE_ID"; caddy_tag="laiai/caddy:$TARGET-$RELEASE_ID"
  if docker image inspect "$core_tag" >/dev/null 2>&1 || docker image inspect "$caddy_tag" >/dev/null 2>&1; then
    die 'Candidate tags already exist; partial builds require a fresh release plan.'
  fi
  docker build --platform linux/amd64 --build-arg GO_IMAGE="$GO_IMAGE" --build-arg ALPINE_IMAGE="$ALPINE_IMAGE" \
    --label "org.opencontainers.image.revision=$TARGET" -f "$SOURCE_DIR/infra/core.Dockerfile" -t "$core_tag" "$SOURCE_DIR" \
    > "$RUN_DIR/build-core.log" 2>&1 || die 'Core build failed; inspect its private build log.'
  capacity; health
  docker build --platform linux/amd64 --build-arg NODE_IMAGE="$NODE_IMAGE" --build-arg CADDY_IMAGE="$CADDY_BASE_IMAGE" \
    --label "org.opencontainers.image.revision=$TARGET" -f "$SOURCE_DIR/infra/caddy.Dockerfile" -t "$caddy_tag" "$SOURCE_DIR" \
    > "$RUN_DIR/build-caddy.log" 2>&1 || die 'Caddy/frontend build failed; inspect its private build log.'
  CORE_IMAGE=$(docker image inspect --format '{{.Id}}' "$core_tag")
  CADDY_IMAGE=$(docker image inspect --format '{{.Id}}' "$caddy_tag")
  images_present "$CORE_IMAGE" "$CADDY_IMAGE"
  state images "$RUN_DIR/images.json" "$CORE_IMAGE" "$CADDY_IMAGE"
  docker run --rm --pull never --network none --read-only --entrypoint sh "$CORE_IMAGE" \
    -ec 'test -x /app/core; test -x /app/migrate; test -x /app/schema-doctor; test -x /app/backfill-funds'
  docker run --rm --pull never --network none --read-only --entrypoint sh "$CADDY_IMAGE" \
    -ec 'test -s /srv/console/index.html; test -d /srv/console/assets'
  docker image save -o "$RUN_DIR/candidate-images.tar" "$CORE_IMAGE" "$CADDY_IMAGE"
  (cd "$RUN_DIR" && sha256sum candidate-images.tar > candidate-images.tar.sha256 && sha256sum -c candidate-images.tar.sha256 >/dev/null)
  compose new; mark build
  log 'Both candidate images built and archived; services remain unchanged.'
}
maintenance_enter() {
  printf '%s\n' "$RELEASE_ID" > "$STATE_DIR/.active-release.tmp"
  mv "$STATE_DIR/.active-release.tmp" "$STATE_DIR/active-release"
  mark maintenance-started
  reload_caddy "$RUN_DIR/config/maintenance.json"
  # A marker is retained even if the subsequent public probe fails.
  mark maintenance
  maintenance_visible || die 'Maintenance response is not visible at the public entry.'
}
run_tool() {
  local name=$1; shift
  "${DC[@]}" run --rm --no-deps --pull never --entrypoint "/app/$name" core "$@" 2>/dev/null
}
verify_stage() {
  PHASE=verify
  load_plan; verify_config; find_containers
  images_present "$(state get "$RUN_DIR/images.json" core)" "$(state get "$RUN_DIR/images.json" caddy)"
  [[ $(docker inspect --format '{{.Image}}' "$CORE_CID") == "$(state get "$RUN_DIR/images.json" core)" ]] || die 'Running core is not the planned image.'
  [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(state get "$RUN_DIR/images.json" caddy)" ]] || die 'Running Caddy is not the planned image.'
  [[ $(docker inspect --format '{{.State.Health.Status}}' "$CORE_CID") == healthy ]] || die 'Candidate core is unhealthy.'
  [[ $(docker inspect --format '{{.Image}}' "$MYSQL_CID") == "$(get snapshot.mysql.image)" ]] || die 'MySQL image changed.'
  [[ $(docker inspect --format '{{.Image}}' "$REDIS_CID") == "$(get snapshot.redis.image)" ]] || die 'Redis image changed.'
  snapshot "$RUN_DIR/running-snapshot.json"
  state running "$PLAN" "$RUN_DIR/running-snapshot.json" "$RUN_DIR/images.json"
  if [[ -f "$RUN_DIR/open.ok" ]]; then
    state caddy-match "$RUN_DIR/running-snapshot.json" "$RUN_DIR/config/original.json"
  else
    state caddy-match "$RUN_DIR/running-snapshot.json" "$RUN_DIR/config/maintenance.json"
  fi
  check_data preflight
  state schema "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations" exact
  health; mark verify
}
open_stage() {
  PHASE=open
  load_plan; require_mark deployed; require_mark maintenance
  [[ ! -f "$RUN_DIR/rolled-back.ok" ]] || die 'This release was rolled back.'
  [[ -f "$STATE_DIR/active-release" && $(cat "$STATE_DIR/active-release") == "$RELEASE_ID" ]] || die 'Another release owns the runtime; refusing stale open.'
  verify_stage; PHASE=open
  if [[ -f "$RUN_DIR/open.ok" ]]; then log 'Release is already open; verified without reloading its routes.'; return; fi
  external_check before-open
  restore_ingress; mark open
  printf '%s\n' "$RELEASE_ID" > "$STATE_DIR/current"
  log 'Release opened. Perform business acceptance within the approved test budget.'
}
deploy_stage() {
  PHASE=deploy
  load_plan; require_mark preflight; require_mark backup; require_mark build
  [[ ! -f "$RUN_DIR/maintenance-started.ok" ]] || die 'A deployment already started. Inspect status; use open/rollback or manual recovery, not repeated migration.'
  verify_config; same_live; require_current_main; ci_check; checkout_source; capacity
  images_present "$(state get "$RUN_DIR/images.json" core)" "$(state get "$RUN_DIR/images.json" caddy)"
  (cd "$RUN_DIR/backup" && sha256sum -c checksums.sha256 >/dev/null)
  (cd "$RUN_DIR" && sha256sum -c candidate-images.tar.sha256 >/dev/null)
  compose new; check_data preflight; external_check before-maintenance
  maintenance_enter
  # Existing workers may finish after ingress is closed; do not erase unknown tasks/holds.
  check_data drained
  "${DC[@]}" stop -t 60 core >/dev/null
  [[ $(docker inspect --format '{{.State.Running}}' "$CORE_CID") == false ]] || die 'Old core did not stop.'
  check_data drained
  cp "$RUN_DIR/data-drained.json" "$RUN_DIR/funds-before.json"
  local parent=$RUN_DIR
  RELEASE_CONFIG_DIR="$parent/config"
  OLD_CORE_IMAGE=$(get snapshot.core.image); OLD_CADDY_IMAGE=$(get snapshot.caddy.image)
  mkdir "$parent/final-backup"
  RUN_DIR="$parent/final-backup"; release_backup; RUN_DIR=$parent
  mark final-backup
  date -u +%FT%TZ > "$RUN_DIR/backfill-cutoff"
  # DDL can commit even when the migration process fails: mark before the first call.
  mark migration-started
  run_tool migrate -dir /app/migrations > "$RUN_DIR/migrate.log"
  run_tool migrate -dir /app/migrations >> "$RUN_DIR/migrate.log"
  run_tool schema-doctor -dir /app/migrations > "$RUN_DIR/doctor.log"
  check_data post-migration
  state schema "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations" exact
  state funds "$RUN_DIR/funds-before.json" "$RUN_DIR/data-post-migration.json"
  mark migrated
  local output iteration complete=0 cutoff
  local complete_pattern='^complete: [0-9]+ source rows projected before '
  local batch_pattern='^batch limit reached: [0-9]+ source rows projected;'
  cutoff=$(cat "$RUN_DIR/backfill-cutoff")
  for ((iteration=0; iteration<100; iteration++)); do
    output=$(run_tool backfill-funds -before "$cutoff" -limit 100 -batches 100)
    printf '%s\n' "$output" >> "$RUN_DIR/backfill.log"
    if [[ "$output" =~ $complete_pattern ]]; then complete=1; break; fi
    [[ "$output" =~ $batch_pattern ]] || die 'Unrecognized backfill result.'
  done
  ((complete == 1)) || die 'Backfill iteration limit reached; remain in maintenance.'
  output=$(run_tool backfill-funds -before "$cutoff" -limit 100 -batches 100)
  [[ "$output" == "complete: 0 source rows projected before $cutoff" ]] || die 'Backfill replay was not empty.'
  check_data post-backfill
  state funds "$RUN_DIR/funds-before.json" "$RUN_DIR/data-post-backfill.json"
  mark backfilled
  "${DC[@]}" up -d --no-build --no-deps --pull never --wait --wait-timeout 180 --scale core=1 core >/dev/null
  "${DC[@]}" up -d --no-build --no-deps --pull never caddy >/dev/null
  mark deployed
  verify_stage
  open_stage
}
rollback_stage() {
  PHASE=rollback
  load_plan; verify_config; require_mark maintenance-started
  [[ ! -f "$RUN_DIR/rolled-back.ok" ]] || die 'Already rolled back.'
  [[ -f "$STATE_DIR/active-release" && $(cat "$STATE_DIR/active-release") == "$RELEASE_ID" ]] || die 'Another release owns the runtime; refusing stale rollback.'
  snapshot "$RUN_DIR/rollback-snapshot.json" allow-stopped-apps
  state rollback-guard "$PLAN" "$RUN_DIR/rollback-snapshot.json" "$RUN_DIR/images.json"
  find_containers
  local actual attempt ready=0
  actual=$(docker inspect --format '{{.Image}}' "$CORE_CID")
  [[ "$actual" == "$(get snapshot.core.image)" || "$actual" == "$(state get "$RUN_DIR/images.json" core)" ]] || die 'Running core belongs to another release; refusing stale rollback.'
  if [[ ! -f "$RUN_DIR/migration-started.ok" ]]; then
    [[ "$actual" == "$(get snapshot.core.image)" && $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] || die 'Pre-migration containers unexpectedly changed.'
  else
    external_check before-rollback
  fi
  # Finish all ownership, data and frozen-configuration checks before restarting
  # either application. Restoring an image archive never starts its services.
  check_data preflight
  compose old
  if ! docker image inspect "$(get snapshot.core.image)" "$(get snapshot.caddy.image)" >/dev/null 2>&1; then
    (cd "$RUN_DIR/backup" && sha256sum -c checksums.sha256 >/dev/null)
    docker image load -i "$RUN_DIR/backup/images.tar" >/dev/null
  fi
  images_present "$(get snapshot.core.image)" "$(get snapshot.caddy.image)"
  if [[ $(state get "$RUN_DIR/rollback-snapshot.json" caddy.running) == False ]]; then
    # The previous Caddy image starts directly with maintenance, never with its
    # saved public routes. Preserve the inode in case this file is already bound.
    cat "$RUN_DIR/config/maintenance.json" > "$RUN_DIR/config/active.json"
    "${DC[@]}" up -d --no-build --no-deps --pull never caddy >/dev/null
    find_containers
    [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] || die 'Recovery Caddy image mismatch.'
    for ((attempt=0; attempt<30; attempt++)); do
      if reload_caddy "$RUN_DIR/config/active.json" && maintenance_visible; then ready=1; break; fi
      sleep 2
    done
    ((ready)) || die 'Original Caddy did not recover with publicly verified maintenance; core was not changed.'
  fi
  maintenance_enter
  if [[ ! -f "$RUN_DIR/migration-started.ok" ]]; then
    docker start "$CORE_CID" >/dev/null
    ready=0
    for ((attempt=0; attempt<90; attempt++)); do
      if [[ $(docker inspect --format '{{.State.Health.Status}}' "$CORE_CID") == healthy ]]; then ready=1; break; fi
      sleep 2
    done
    ((ready)) || die 'Original core did not recover.'
    restore_ingress; mark rolled-back
    log 'Original service resumed before any migration; no database changes were rolled back.'
    return
  fi
  check_data drained
  "${DC[@]}" stop -t 60 core >/dev/null
  check_data drained
  cat "$RUN_DIR/config/maintenance.json" > "$RUN_DIR/config/active.json"
  "${DC[@]}" up -d --no-build --no-deps --pull never --wait --wait-timeout 180 --scale core=1 core >/dev/null
  "${DC[@]}" up -d --no-build --no-deps --pull never caddy >/dev/null
  find_containers
  [[ $(docker inspect --format '{{.Image}}' "$CORE_CID") == "$(get snapshot.core.image)" ]] || die 'Rollback core image mismatch.'
  [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] || die 'Rollback Caddy image mismatch.'
  check_data preflight; health
  restore_ingress; mark rolled-back
  log 'Recorded application images/configuration restored. Database was not restored or downgraded.'
}

case "$COMMAND" in
  compare) compare_versions;;
  status)
    load_plan; printf 'release=%s\ncommit=%s\n' "$RELEASE_ID" "$TARGET"
    find "$RUN_DIR" -maxdepth 1 -name '*.ok' -printf '%f\n' | sort
    [[ ! -f "$RUN_DIR/failed-stage" ]] || { printf 'last failure: '; cat "$RUN_DIR/failed-stage"; };;
  preflight) preflight;;
  approve)
    load_plan; verify_config
    case "$STAGE" in before-maintenance|before-open|before-rollback) ;; *) die 'Unknown approval stage.';; esac
    secret_file "$EVIDENCE"; confirm
    state approve "$PLAN" "$STAGE" "$EVIDENCE"
    log "Evidence recorded for $STAGE; built-in checks remain mandatory.";;
  backup) confirm; backup_stage;; build) confirm; build_stage;; deploy) confirm; deploy_stage;;
  open) confirm; open_stage;; verify) verify_stage;; rollback) confirm; rollback_stage;;
  release)
    confirm
    if [[ -n "$RUN_DIR" && -f "$RUN_DIR/deployed.ok" ]]; then
      open_stage
    else
      preflight
      [[ -f "$RUN_DIR/backup.ok" ]] || backup_stage
      [[ -f "$RUN_DIR/build.ok" ]] || build_stage
      deploy_stage
    fi;;
esac
