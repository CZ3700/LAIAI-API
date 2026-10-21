#!/usr/bin/env bash
# Offline integration tests: real tar/gzip/OpenSSL, deterministic fake Docker.
set -Eeuo pipefail
umask 077
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
export BACKUP_MODULE
BACKUP_MODULE=$(cd -- "$SCRIPT_DIR/.." && pwd -P)/backup.sh
TEST_PARENT=$(cd -- "${TMPDIR:-/tmp}" && pwd -P)
TEST_ROOT=$(mktemp -d "$TEST_PARENT/laiai-backup-test.XXXXXX")
cleanup() {
  [[ ${TEST_ROOT%/*} == "$TEST_PARENT" && ${TEST_ROOT##*/} == laiai-backup-test.* ]] || return 1
  rm -rf -- "$TEST_ROOT"
}
trap cleanup EXIT
trap 'printf "FAIL: backup test case %s\n" "${case_count:-setup}" >&2; if [[ -f $TEST_ROOT/output.txt ]]; then cat -- "$TEST_ROOT/output.txt" >&2; fi' ERR
export TEST_OPENSSL
TEST_OPENSSL=$(command -v openssl)
mkdir -p -- "$TEST_ROOT/bin"
printf 'test image layer\n' > "$TEST_ROOT/image-payload.txt"
export TEST_IMAGE_ARCHIVE="$TEST_ROOT/image.tar"
tar --create --file "$TEST_IMAGE_ARCHIVE" --directory "$TEST_ROOT" image-payload.txt

cat > "$TEST_ROOT/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ $1 == image && $2 == inspect ]]; then
  printf '%s\n' "${@: -1}"
elif [[ $1 == exec ]]; then
  printf '%s\n' 'CREATE TABLE private_data (value TEXT);' "INSERT INTO private_data VALUES ('BACKUP_TEST_PRIVATE_VALUE_ABC123');"
  [[ ${TEST_DOCKER_MODE:-} == missing-marker ]] || printf '%s\n' '-- Dump completed on 2026-10-21 00:00:00'
  if [[ ${TEST_DOCKER_MODE:-} == failed-dump ]]; then
    printf 'BACKUP_TEST_PRIVATE_VALUE_ABC123\n' >&2
    exit 7
  fi
elif [[ $1 == image && $2 == save ]]; then
  if [[ ${TEST_DOCKER_MODE:-} == failed-image ]]; then
    printf 'incomplete archive'
    exit 8
  fi
  if [[ ${TEST_DOCKER_MODE:-} == bad-image ]]; then
    printf 'invalid archive'
  else
    cat -- "$TEST_IMAGE_ARCHIVE"
  fi
else
  printf 'unexpected fake Docker invocation\n' >&2
  exit 90
fi
MOCK
cat > "$TEST_ROOT/bin/openssl" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${TEST_OPENSSL_MODE:-} == bad-decryption && " $* " == *' -d '* ]]; then
  printf 'not valid gzip content'
  exit 0
fi
exec "$TEST_OPENSSL" "$@"
MOCK
chmod 700 "$TEST_ROOT/bin/docker" "$TEST_ROOT/bin/openssl"
export PATH="$TEST_ROOT/bin:$PATH"
export MYSQL_CID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export OLD_CORE_IMAGE=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
export OLD_CADDY_IMAGE=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
export RUN_DIR LIVE_DIR LIVE_ENV VIDEO_ENV CADDYFILE BACKUP_KEY_FILE TEST_DOCKER_MODE TEST_OPENSSL_MODE
case_count=0

new_case() {
  case_count=$((case_count + 1))
  local root="$TEST_ROOT/case $case_count"
  RUN_DIR="$root/run records"
  LIVE_DIR="$root/source path"
  LIVE_ENV="$root/live configuration.env"
  VIDEO_ENV="$root/video configuration.env"
  CADDYFILE="$LIVE_DIR/infra/Caddyfile"
  BACKUP_KEY_FILE="$root/backup key"
  mkdir -p -- "$RUN_DIR" "$LIVE_DIR/infra/mysql" "$LIVE_DIR/infra/redis"
  chmod 700 -- "$RUN_DIR"
  printf '%064d\n' 42 > "$BACKUP_KEY_FILE"
  chmod 600 -- "$BACKUP_KEY_FILE"
  local file
  for file in "$LIVE_ENV" "$VIDEO_ENV" "$CADDYFILE" "$LIVE_DIR/infra/docker-compose.yml" "$LIVE_DIR/infra/docker-compose.prod.yml" "$LIVE_DIR/infra/mysql/my.cnf" "$LIVE_DIR/infra/redis/redis.conf"; do
    printf 'BACKUP_TEST_PRIVATE_VALUE_ABC123\n' > "$file"
  done
  TEST_DOCKER_MODE=ok
  TEST_OPENSSL_MODE=ok
}

run_backup() {
  bash -c '
    set -Eeuo pipefail
    die() { printf "ERROR: %s\n" "$*" >&2; exit 1; }
    log() { printf "%s\n" "$*"; }
    source "$BACKUP_MODULE"
    release_backup
  ' > "$TEST_ROOT/output.txt" 2>&1
}

assert_private() {
  if grep -Fq 'BACKUP_TEST_PRIVATE_VALUE_ABC123' "$TEST_ROOT/output.txt"; then
    printf 'FAIL: private content appeared in output\n' >&2
    exit 1
  fi
  if grep -RaqF 'BACKUP_TEST_PRIVATE_VALUE_ABC123' "$RUN_DIR"; then
    printf 'FAIL: plaintext database/configuration persisted\n' >&2
    exit 1
  fi
}

expect_failure() {
  if run_backup; then
    printf 'FAIL: expected backup failure\n' >&2
    exit 1
  fi
  [[ ! -e $RUN_DIR/backup.ok ]]
  assert_private
}

new_case
run_backup
[[ $(cat -- "$RUN_DIR/backup.ok") == verified ]]
[[ $(stat -c '%a' -- "$RUN_DIR/database.sql.gz.enc") == 600 ]]
(cd -- "$RUN_DIR" && sha256sum --check --status checksums.sha256)
assert_private
# Verify archived configuration paths with spaces survive the stream pipeline.
"$TEST_OPENSSL" enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" -in "$RUN_DIR/configuration.tar.gz.enc" | gzip -dc | tar --list --ignore-zeros --file - > "$TEST_ROOT/config-members.txt"
grep -Fxq "${LIVE_ENV#/}" "$TEST_ROOT/config-members.txt"
grep -Fxq "${VIDEO_ENV#/}" "$TEST_ROOT/config-members.txt"
before=$(sha256sum -- "$RUN_DIR/checksums.sha256")
if run_backup; then
  printf 'FAIL: existing backup was overwritten\n' >&2
  exit 1
fi
[[ $before == "$(sha256sum -- "$RUN_DIR/checksums.sha256")" ]]
assert_private

new_case
TEST_DOCKER_MODE=failed-dump
expect_failure
new_case
TEST_DOCKER_MODE=missing-marker
expect_failure
new_case
TEST_DOCKER_MODE=failed-image
expect_failure
new_case
TEST_DOCKER_MODE=bad-image
expect_failure
new_case
TEST_OPENSSL_MODE=bad-decryption
expect_failure
new_case
chmod 644 -- "$BACKUP_KEY_FILE"
expect_failure
new_case
printf '\n%064d\n' 42 > "$BACKUP_KEY_FILE"
expect_failure
new_case
rm -- "$LIVE_DIR/infra/mysql/my.cnf"
expect_failure
printf 'PASS: encrypted backup success, repeat refusal, and %s failure cases\n' "$((case_count - 1))"
