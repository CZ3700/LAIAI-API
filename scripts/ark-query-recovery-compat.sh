#!/usr/bin/env sh
# Four real committed query-recovery handoffs; synthetic loopback HTTP only.
set -eu
umask 077
export PYTHONDONTWRITEBYTECODE=1
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 -B "$ROOT/scripts/lib/ark_query_recovery_compat.py" "$@"
