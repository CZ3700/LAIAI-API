#!/usr/bin/env bash
set -Eeuo pipefail
set +x
[[ ${1:-} =~ ^[0-9a-f]{40}$ ]] || exit 2
HERE=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# This public repository's Actions metadata can be read without gh or an API
# token. Observe the exact main push run; state.py records its current status.
curl --fail --silent --show-error --connect-timeout 10 --max-time 45 \
  -H 'Accept: application/vnd.github+json' \
  -H 'User-Agent: LAIAI-release-observed-ci' \
  "https://api.github.com/repos/CZ3700/LAIAI-API/actions/runs?head_sha=$1&branch=main&event=push&per_page=100" \
  | python3 "$HERE/state.py" ci "$1"
