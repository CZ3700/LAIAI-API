#!/usr/bin/env sh
# Genuine committed N/N-1 execution-input compatibility; synthetic loopback only.
set -eu
umask 077
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$ROOT/scripts/lib/ark_execution_input_compat.py" "$@"
