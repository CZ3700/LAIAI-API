#!/usr/bin/env sh
# Fixed C_a/P business binaries, isolated MySQL and loopback-only Ark.
set -eu
umask 077
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$ROOT/scripts/lib/ark_mutation_compat.py" "$@"
