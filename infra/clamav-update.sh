#!/usr/bin/env bash
# Refresh signatures with the immutable ID of the running core image.
# --image permits initialization from a pinned release candidate before cutover.
set -Eeuo pipefail
db_dir=/var/lib/clamav
die() { printf 'clamav-update: %s\n' "$*" >&2; exit 1; }
(( EUID == 0 )) || die 'run as root'
[[ -d "$db_dir" && ! -L "$db_dir" ]] || die 'signature directory missing or symlinked'
[[ "$(realpath -- "$db_dir")" == "$db_dir" ]] || die 'signature directory path changed'
[[ "$(stat -c %u -- "$db_dir")" == 10001 ]] || die 'signature directory must belong to uid 10001'
mode=$(stat -c %a -- "$db_dir")
(( (8#$mode & 0022) == 0 )) || die 'signature directory is writable by group or others'
docker info --format '{{.ServerVersion}}' >/dev/null || die 'Docker unavailable'
if (( $# == 0 )); then
  mapfile -t containers < <(docker ps --filter label=com.docker.compose.project=infra --filter label=com.docker.compose.service=core --format '{{.ID}}')
  (( ${#containers[@]} > 0 )) || die 'no running core container'
  image=''
  for container in "${containers[@]}"; do
    current=$(docker inspect --format '{{.Image}}' "$container")
    [[ -z "$image" || "$image" == "$current" ]] || die 'core replicas run different image IDs'
    image=$current
  done
elif (( $# == 2 )) && [[ "$1" == --image ]]; then
  image=$2
else
  die 'usage: clamav-update.sh [--image sha256:<immutable image ID>]'
fi
[[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'invalid immutable image ID'
[[ "$(docker image inspect --format '{{.Id}}' "$image")" == "$image" ]] || die 'image not present locally'
docker run --rm --pull=never --read-only --cap-drop=ALL \
  --security-opt no-new-privileges --user 10001:10001 \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount "type=bind,src=$db_dir,dst=$db_dir" \
  --entrypoint /usr/bin/freshclam "$image" \
  --config-file=/app/freshclam.conf --datadir="$db_dir" --user=laiai --stdout
[[ -s "$db_dir/main.cvd" || -s "$db_dir/main.cld" ]] || die 'main signatures missing'
[[ -s "$db_dir/daily.cvd" || -s "$db_dir/daily.cld" ]] || die 'daily signatures missing'
# EICAR is harmless; exit 1 = detected, exit 0 = missed, exit 2 = scanner error.
eicar='X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*'
set +e
printf '%s' "$eicar" | docker run --rm --pull=never -i --network none \
  --read-only --cap-drop=ALL --security-opt no-new-privileges \
  --user 10001:10001 --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount "type=bind,src=$db_dir,dst=$db_dir,readonly" \
  --entrypoint /usr/bin/clamscan "$image" \
  --no-summary --database="$db_dir" - >/dev/null 2>&1
scan_status=${PIPESTATUS[1]}
set -e
(( scan_status == 1 )) || die "EICAR check failed (scanner exit $scan_status)"
printf 'clamav-update: signatures refreshed and EICAR detected\n'
