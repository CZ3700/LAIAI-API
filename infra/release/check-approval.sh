#!/usr/bin/env bash
set -Eeuo pipefail
set +x
HERE=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 "$HERE/state.py" approval-check "${2:?plan required}" "${1:?stage required}"
