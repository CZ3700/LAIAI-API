#!/usr/bin/env bash
# Sourced by release.sh; freeze actual deployed files + runtime values, not an
# assumed old checkout. Secret-bearing output remains in RUN_DIR/config (0700).
RELEASE_RUNTIME_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)

freeze_rollback_runtime() (
  set -Eeuo pipefail
  set +x
  umask 077
  local scratch working item
  local -a paths command
  : "${RUN_DIR:?}" "${COMPOSE_PROJECT:?}" "${LIVE_ENV:?}"
  for item in "${CORE_CID:-}" "${CADDY_CID:-}" "${MYSQL_CID:-}" "${REDIS_CID:-}"; do
    [[ "$item" =~ ^[a-f0-9]{12,64}$ ]] || die 'Verified runtime container IDs are required.'
  done
  [[ -d "$RUN_DIR" && ! -L "$RUN_DIR" ]] || die 'A protected release directory is required.'
  mkdir -p -- "$RUN_DIR/config"
  chmod 700 -- "$RUN_DIR/config"
  scratch=$(mktemp -d "$RUN_DIR/.runtime.XXXXXX")
  trap 'rm -f -- "$scratch/source.json" "$scratch/paths.bin" "$scratch/compose.json"; rmdir -- "$scratch"' EXIT
  if ! docker inspect "$CORE_CID" | python3 "$RELEASE_RUNTIME_DIR/freeze-runtime.py" metadata "$COMPOSE_PROJECT" > "$scratch/source.json" 2>/dev/null; then
    die 'Cannot verify the current Compose source file chain.'
  fi
  python3 "$RELEASE_RUNTIME_DIR/freeze-runtime.py" paths "$scratch/source.json" > "$scratch/paths.bin"
  mapfile -d '' -t paths < "$scratch/paths.bin"
  ((${#paths[@]} >= 2)) || die 'Current Compose source chain is empty.'
  working=${paths[0]}
  command=(docker compose --project-name "$COMPOSE_PROJECT" --project-directory "$working" --env-file "$LIVE_ENV")
  [[ -z "${VIDEO_ENV:-}" ]] || command+=(--env-file "$VIDEO_ENV")
  for item in "${paths[@]:1}"; do command+=(-f "$item"); done
  if ! "${command[@]}" config --format json > "$scratch/compose.json" 2>/dev/null; then
    die 'Cannot render the actual deployed Compose file chain.'
  fi
  if ! docker inspect "$CORE_CID" "$CADDY_CID" "$MYSQL_CID" "$REDIS_CID" \
    | python3 "$RELEASE_RUNTIME_DIR/freeze-runtime.py" freeze "$scratch/compose.json" "$scratch/source.json" \
      "$RUN_DIR/config/rollback-compose.json" "$RUN_DIR/config/rollback-runtime-manifest.json" 2>/dev/null; then
    die 'Cannot freeze the original runtime configuration for rollback.'
  fi
  if ! docker compose --project-name "$COMPOSE_PROJECT" --project-directory "$working" \
    -f "$RUN_DIR/config/rollback-compose.json" config --quiet >/dev/null 2>&1; then
    die 'Frozen rollback configuration is not valid Compose input.'
  fi
  log 'Original runtime configuration frozen privately; actual environment, commands, images, volumes and networks retained.'
)
