#!/usr/bin/env bash
# Sourced by release.sh. Application-only deployment keeps the public edge up.

online_http_ok() {
  local status
  status=$(curl --silent --show-error --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' "$PUBLIC_ORIGIN$1") || return 1
  [[ "$status" == 200 ]]
}

online_public_ok() {
  health && online_http_ok /api/status && online_http_ok /
}

online_schema() {
  # Read only the applied migration names. This path never runs money or task
  # fingerprint checks against a database that is accepting live writes.
  docker exec "$MYSQL_CID" sh -eu -c '
    : "${MYSQL_ROOT_PASSWORD:?missing database credential}"
    MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names \
      "${MYSQL_DATABASE:-laiai}" -e "SELECT version FROM schema_migrations ORDER BY version"
  ' > "$RUN_DIR/schema.txt" 2>/dev/null || die 'Cannot read applied schema versions.'
  chmod 600 "$RUN_DIR/schema.txt"
}

online_deploy_stage() {
  local helper="$TOOL_DIR/online.py" names online_core online_web candidate_core_cid candidate_web_cid
  [[ -f "$helper" ]] || die 'The online release helper is missing.'
  cmp -s "$SOURCE_DIR/infra/Caddyfile" "$CADDYFILE" \
    || die 'Edge Caddy configuration changed; online application-only release cannot apply it.'
  online_schema
  state schema "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations" exact
  state carryover-eligible "$PLAN" "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations"
  find_containers
  [[ $(docker inspect --format '{{.Image}}' "$CORE_CID") == "$(get snapshot.core.image)" ]] \
    || die 'Stable core changed since preflight; start a new release.'
  [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] \
    || die 'Public Caddy changed since preflight; start a new release.'
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ \
    | python3 "$helper" same "$RUN_DIR/config/original.json" \
    || die 'Public routes changed since preflight; start a new release.'

  names=$(python3 "$helper" overlay "$PLAN" "$RUN_DIR/images.json" "$SOURCE_DIR" "$RUN_DIR/online-compose.yml")
  read -r online_core online_web <<< "$names"
  DC+=(-f "$RUN_DIR/online-compose.yml")
  "${DC[@]}" config --format json 2>/dev/null \
    | python3 "$helper" check-compose "$PLAN" "$RUN_DIR/images.json" "$RELEASE_ID" "$COMPOSE_PROJECT" "$RUN_DIR" \
    || die 'Online candidate Compose differs from the verified production service.'
  python3 "$helper" route "$RUN_DIR/config/original.json" "$RUN_DIR/config/online-candidate.json" "$RELEASE_ID"

  # The existing edge and core serve every request during backup, build and
  # candidate warmup. No host ports are bound by the candidates.
  "${DC[@]}" up -d --no-build --no-deps --pull never --wait --wait-timeout 180 \
    "$online_core" "$online_web" >/dev/null
  candidate_core_cid=$("${DC[@]}" ps -q "$online_core")
  candidate_web_cid=$("${DC[@]}" ps -q "$online_web")
  [[ -n "$candidate_core_cid" && -n "$candidate_web_cid" ]] || die 'Candidate container is missing.'
  [[ $(docker inspect --format '{{.Image}}' "$candidate_core_cid") == "$(state get "$RUN_DIR/images.json" core)" ]] \
    || die 'Candidate core image differs from the pinned build.'
  [[ $(docker inspect --format '{{.Image}}' "$candidate_web_cid") == "$(state get "$RUN_DIR/images.json" caddy)" ]] \
    || die 'Candidate web image differs from the pinned build.'
  docker exec "$candidate_web_cid" wget -q -O /dev/null "http://$online_core:8080/healthz" \
    || die 'Candidate web cannot reach candidate core.'
  docker exec "$candidate_web_cid" wget -q -O /dev/null "http://$online_core:8080/api/status" \
    || die 'Candidate core status failed on the private network.'
  [[ $(docker inspect --format '{{.State.Health.Status}}' "$candidate_core_cid") == healthy ]] \
    || die 'Candidate core lost health before the switch.'
  [[ $(docker inspect --format '{{.State.Health.Status}}' "$candidate_web_cid") == healthy ]] \
    || die 'Candidate frontend lost health before the switch.'
  capacity
  online_public_ok || die 'Previous public service became unhealthy during candidate warmup; no route was changed.'

  mark online-switch-started
  if ! reload_caddy "$RUN_DIR/config/online-candidate.json" || ! online_public_ok; then
    if reload_caddy "$RUN_DIR/config/original.json" && online_public_ok; then
      die 'Candidate route failed; original public routes restored without stopping services.'
    fi
    die 'Candidate public probe failed and route restoration needs immediate operator attention; all containers remain running.'
  fi
  if ! docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ \
    | python3 "$helper" same "$RUN_DIR/config/online-candidate.json"; then
    reload_caddy "$RUN_DIR/config/original.json" && online_public_ok \
      && die 'Candidate routes differed from the reviewed configuration; previous routes restored.'
    die 'Candidate route mismatch and restoration needs immediate operator attention.'
  fi
  printf '%s\n' "$RELEASE_ID" > "$STATE_DIR/.active-release.tmp"
  mv "$STATE_DIR/.active-release.tmp" "$STATE_DIR/active-release"
  mark online-candidate-live
  mark deployed
  mark open
  printf '%s\n' "$RELEASE_ID" > "$STATE_DIR/current"
  log 'Online switch complete: public edge and previous core stayed up; new core and frontend serve traffic.'
}

online_verify_stage() {
  local helper="$TOOL_DIR/online.py" names online_core online_web web_cid
  require_mark deployed
  [[ ! -f "$RUN_DIR/rolled-back.ok" ]] || die 'This online release was rolled back.'
  find_containers
  [[ $(docker inspect --format '{{.Image}}' "$CORE_CID") == "$(get snapshot.core.image)" ]] \
    || die 'Previous stable core image changed.'
  [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] \
    || die 'Public edge image changed.'
  [[ $(docker inspect --format '{{.State.Health.Status}}' "$CORE_CID") == healthy ]] \
    || die 'Online stable core is unhealthy.'
  for role in MYSQL REDIS; do
    local cid="${role}_CID" lower="${role,,}"
    [[ $(docker inspect --format '{{.Image}}' "${!cid}") == "$(get "snapshot.$lower.image")" ]] \
      || die "Data container image changed: $lower"
  done
  names=$(python3 "$helper" names "$RELEASE_ID")
  read -r online_core online_web <<< "$names"
  compose new
  DC+=(-f "$RUN_DIR/online-compose.yml")
  local core_cid
  core_cid=$("${DC[@]}" ps -q "$online_core")
  [[ -n "$core_cid" && $(docker inspect --format '{{.State.Health.Status}}' "$core_cid") == healthy ]] \
    || die 'Online candidate core is not healthy.'
  [[ $(docker inspect --format '{{.Image}}' "$core_cid") == "$(state get "$RUN_DIR/images.json" core)" ]] \
    || die 'Online candidate core image changed.'
  web_cid=$("${DC[@]}" ps -q "$online_web")
  [[ -n "$web_cid" && $(docker inspect --format '{{.State.Health.Status}}' "$web_cid") == healthy ]] \
    || die 'Online frontend is not healthy.'
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ \
    | python3 "$helper" same "$RUN_DIR/config/online-candidate.json" \
    || die 'Public edge is not using the online candidate route.'
  online_schema
  state schema "$RUN_DIR/schema.txt" "$SOURCE_DIR/migrations" exact
  online_public_ok || die 'Online public health, status or frontend probe failed.'
  mark verify
}

online_rollback_stage() {
  local helper="$TOOL_DIR/online.py"
  [[ -f "$RUN_DIR/online-candidate-live.ok" ]] || die 'No live online candidate to roll back.'
  [[ ! -f "$RUN_DIR/rolled-back.ok" ]] || die 'This release was already rolled back.'
  [[ -f "$STATE_DIR/active-release" && $(cat "$STATE_DIR/active-release") == "$RELEASE_ID" ]] \
    || die 'Another release owns the runtime; refusing stale rollback.'
  find_containers
  [[ $(docker inspect --format '{{.Image}}' "$CADDY_CID") == "$(get snapshot.caddy.image)" ]] \
    || die 'Public edge changed; refusing automatic rollback.'
  for role in MYSQL REDIS; do
    local cid="${role}_CID" lower="${role,,}"
    [[ $(docker inspect --format '{{.Image}}' "${!cid}") == "$(get "snapshot.$lower.image")" ]] \
      || die "Data image changed: $lower"
  done
  images_present "$(get snapshot.core.image)" "$(get snapshot.caddy.image)"
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ \
    | python3 "$helper" same "$RUN_DIR/config/online-candidate.json" \
    || die 'Public routes changed after this release; refusing stale rollback.'
  if ! reload_caddy "$RUN_DIR/config/original.json" || ! online_public_ok; then
    reload_caddy "$RUN_DIR/config/online-candidate.json" \
      || die 'Previous route failed and candidate route restoration needs operator attention.'
    die 'Previous route failed; candidate route restored without stopping either core.'
  fi
  docker exec "$CADDY_CID" wget -q -O - http://127.0.0.1:2019/config/ \
    | python3 "$helper" same "$RUN_DIR/config/original.json" \
    || die 'Previous public route was not restored exactly.'
  mark rolled-back
  if [[ -f "$STATE_DIR/current" && $(cat "$STATE_DIR/current") == "$RELEASE_ID" ]]; then
    rm -f -- "$STATE_DIR/current"
  fi
  log 'Previous routes restored online; both core containers and the public edge remained running.'
}
