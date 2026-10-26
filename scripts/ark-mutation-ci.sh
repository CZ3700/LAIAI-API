#!/usr/bin/env sh
# Full matrix only. The business runner remains independently usable for bring-up.
set -eu
umask 077
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$ROOT/scripts/lib/ark_mutation_ci.py" "$@"
