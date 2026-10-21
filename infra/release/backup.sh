#!/usr/bin/env bash
# Sourced by release.sh. All database/configuration plaintext stays in pipes.
# Required globals: RUN_DIR LIVE_DIR LIVE_ENV CADDYFILE BACKUP_KEY_FILE MYSQL_CID
# OLD_CORE_IMAGE OLD_CADDY_IMAGE. VIDEO_ENV is optional. Caller supplies die/log.

release_backup() (
  set -Eeuo pipefail
  set +x
  umask 077
  set -C

  local item actual first_line_length
  local database_file=database.sql.gz.enc config_file=configuration.tar.gz.enc
  local image_file=images.tar
  local -a config_paths=() config_members=()
  local -A seen_paths=()

  [[ ${RUN_DIR:-} == /* && -d $RUN_DIR && ! -L $RUN_DIR ]] || die 'Backup directory must be an existing absolute directory.'
  [[ $(stat -c '%a' -- "$RUN_DIR" 2>/dev/null) == 700 ]] || die 'Backup directory must have mode 0700.'
  [[ ${LIVE_DIR:-} == /* ]] || die 'Live source directory must be absolute.'
  [[ ${BACKUP_KEY_FILE:-} == /* && -f $BACKUP_KEY_FILE && ! -L $BACKUP_KEY_FILE ]] || die 'Backup key must be an absolute regular file.'
  [[ $(stat -c '%a' -- "$BACKUP_KEY_FILE" 2>/dev/null) == 600 ]] || die 'Backup key must have mode 0600.'
  [[ $(stat -c '%s' -- "$BACKUP_KEY_FILE" 2>/dev/null) -ge 32 ]] || die 'Backup key must contain at least 32 bytes.'
  # OpenSSL reads only the first line of a passphrase file. Never print its value.
  first_line_length=$(awk 'NR == 1 { n = length($0) } END { print n + 0 }' "$BACKUP_KEY_FILE" 2>/dev/null) || die 'Cannot read backup key.'
  [[ $first_line_length -ge 32 ]] || die 'Backup key first line must contain at least 32 characters.'
  [[ ${MYSQL_CID:-} =~ ^[a-f0-9]{12,64}$ ]] || die 'Verified MySQL container ID is required.'

  for item in "${OLD_CORE_IMAGE:-}" "${OLD_CADDY_IMAGE:-}"; do
    [[ $item =~ ^sha256:[a-f0-9]{64}$ ]] || die 'Complete previous image IDs are required.'
    actual=$(docker image inspect --format '{{.Id}}' "$item" 2>/dev/null) || die 'A previous image is unavailable.'
    [[ $actual == "$item" ]] || die 'Previous image ID verification failed.'
  done

  config_paths=("${LIVE_ENV:-}" "${CADDYFILE:-}" "$LIVE_DIR/infra/docker-compose.yml" "$LIVE_DIR/infra/mysql/my.cnf" "$LIVE_DIR/infra/redis/redis.conf")
  [[ -z ${VIDEO_ENV:-} ]] || config_paths+=("$VIDEO_ENV")
  if [[ -n ${RELEASE_CONFIG_DIR:-} ]]; then
    for item in original.json maintenance.json active.json rollback-compose.json; do
      config_paths+=("$RELEASE_CONFIG_DIR/$item")
    done
  fi
  [[ ! -f $LIVE_DIR/infra/docker-compose.prod.yml ]] || config_paths+=("$LIVE_DIR/infra/docker-compose.prod.yml")
  for item in "${config_paths[@]}"; do
    [[ $item == /* && -f $item && $item != *$'\n'* && $item != *$'\r'* ]] || die 'A required configuration file is unavailable or has an unsupported path.'
    if [[ -z ${seen_paths["$item"]+present} ]]; then
      seen_paths["$item"]=1
      config_members+=("${item#/}")
    fi
  done

  for item in "$database_file" "$config_file" "$image_file" ".database.partial.sql.gz.enc" ".configuration.partial.tar.gz.enc" ".images.partial.tar" .backup.verified.partial checksums.sha256 configuration-files.txt backup-summary.txt backup.ok; do
    [[ ! -e $RUN_DIR/$item && ! -L $RUN_DIR/$item ]] || die 'Backup output already exists; use a new run directory.'
  done

  log 'Backing up the complete application database into an encrypted stream.'
  if ! docker exec "$MYSQL_CID" sh -eu -c '
      : "${MYSQL_ROOT_PASSWORD:?missing database credential}"
      MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot \
        --single-transaction --routines --events --triggers --hex-blob \
        --default-character-set=utf8mb4 --set-gtid-purged=OFF --no-tablespaces \
        --databases "${MYSQL_DATABASE:-laiai}"
    ' 2>/dev/null | gzip -1 2>/dev/null | openssl enc -aes-256-cbc -salt -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" > "$RUN_DIR/.database.partial.sql.gz.enc" 2>/dev/null; then
    die 'Database encrypted backup failed; no completion marker was written.'
  fi
  if ! openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" -in "$RUN_DIR/.database.partial.sql.gz.enc" 2>/dev/null | gzip -t 2>/dev/null; then
    die 'Database backup decryption or compression verification failed.'
  fi
  if ! openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" -in "$RUN_DIR/.database.partial.sql.gz.enc" 2>/dev/null | gzip -dc 2>/dev/null | tail -n 1 | grep -Eq '^-- Dump completed on .+'; then
    die 'Database dump is incomplete; completion marker is missing.'
  fi
  ln -- "$RUN_DIR/.database.partial.sql.gz.enc" "$RUN_DIR/$database_file" 2>/dev/null || die 'Cannot finalize database backup without overwriting an existing file.'
  rm -- "$RUN_DIR/.database.partial.sql.gz.enc" 2>/dev/null || die 'Cannot finalize database backup record.'

  log 'Backing up runtime configuration into an encrypted stream.'
  if ! tar --create --dereference --file - --directory / -- "${config_members[@]}" 2>/dev/null | gzip -1 2>/dev/null | openssl enc -aes-256-cbc -salt -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" > "$RUN_DIR/.configuration.partial.tar.gz.enc" 2>/dev/null; then
    die 'Configuration encrypted backup failed.'
  fi
  if ! openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" -in "$RUN_DIR/.configuration.partial.tar.gz.enc" 2>/dev/null | gzip -t 2>/dev/null; then
    die 'Configuration backup decryption or compression verification failed.'
  fi
  # Read every member, including its contents, without extracting files to disk.
  if ! openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -pass "file:$BACKUP_KEY_FILE" -in "$RUN_DIR/.configuration.partial.tar.gz.enc" 2>/dev/null | gzip -dc 2>/dev/null | tar --extract --to-stdout --ignore-zeros --file - > /dev/null 2>/dev/null; then
    die 'Configuration archive content verification failed.'
  fi
  ln -- "$RUN_DIR/.configuration.partial.tar.gz.enc" "$RUN_DIR/$config_file" 2>/dev/null || die 'Cannot finalize configuration backup without overwriting an existing file.'
  rm -- "$RUN_DIR/.configuration.partial.tar.gz.enc" 2>/dev/null || die 'Cannot finalize configuration backup record.'
  printf '%s\n' "${config_paths[@]}" > "$RUN_DIR/configuration-files.txt" || die 'Cannot write configuration backup file list.'

  log 'Archiving the two previous application images for rollback.'
  if ! docker image save "$OLD_CORE_IMAGE" "$OLD_CADDY_IMAGE" > "$RUN_DIR/.images.partial.tar" 2>/dev/null; then
    die 'Previous image archive failed.'
  fi
  if ! tar --list --file "$RUN_DIR/.images.partial.tar" > /dev/null 2>/dev/null; then
    die 'Previous image archive integrity verification failed.'
  fi
  ln -- "$RUN_DIR/.images.partial.tar" "$RUN_DIR/$image_file" 2>/dev/null || die 'Cannot finalize image archive without overwriting an existing file.'
  rm -- "$RUN_DIR/.images.partial.tar" 2>/dev/null || die 'Cannot finalize previous image archive record.'

  if ! (cd -- "$RUN_DIR" && sha256sum -- "$database_file" "$config_file" "$image_file" > checksums.sha256 && sha256sum --check --status checksums.sha256) 2>/dev/null; then
    die 'Backup checksum verification failed.'
  fi
  printf 'format=laiai-release-backup-v1\ndatabase=container-MYSQL_DATABASE-or-laiai\nencryption=AES-256-CBC-PBKDF2-SHA256-600000\ncore_image=%s\ncaddy_image=%s\n' "$OLD_CORE_IMAGE" "$OLD_CADDY_IMAGE" > "$RUN_DIR/backup-summary.txt" || die 'Cannot write backup summary.'
  # This is a verified backup, not proof that the database can be rolled back.
  printf 'verified\n' > "$RUN_DIR/.backup.verified.partial" || die 'Cannot write backup completion record.'
  ln -- "$RUN_DIR/.backup.verified.partial" "$RUN_DIR/backup.ok" 2>/dev/null || die 'Cannot finalize backup completion record without overwriting an existing file.'
  # A leftover second link is harmless; all verified artifacts already exist.
  rm -- "$RUN_DIR/.backup.verified.partial" 2>/dev/null || :
  log 'Encrypted database/configuration and previous image archives verified.'
)
