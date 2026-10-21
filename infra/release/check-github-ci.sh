#!/usr/bin/env bash
set -Eeuo pipefail
set +x
[[ ${1:-} =~ ^[0-9a-f]{40}$ ]] || exit 2
command -v gh >/dev/null || { echo 'GitHub CLI is required for the default CI checker.' >&2; exit 1; }
HERE=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# gh uses its existing secure authentication; no token is accepted as a CLI argument.
gh api --method GET repos/CZ3700/LAIAI-API/actions/runs \
  -f head_sha="$1" -f branch=main -f event=push -f per_page=100 \
  | python3 "$HERE/state.py" ci "$1"
