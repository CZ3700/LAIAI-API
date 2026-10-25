#!/bin/sh
# Inspect the installed compiler, not an automatically downloaded replacement.
# Keep the minimum in sync with go.work and both modules' go directives.
set -eu

version=$(GOTOOLCHAIN=local go env GOVERSION)
if ! printf '%s\n' "$version" | awk '
    NR != 1 { exit 1 }
    !/^go[0-9]+\.[0-9]+\.[0-9]+$/ { exit 1 }
    {
        split(substr($0, 3), v, ".")
        if (v[1] > 1 || (v[1] == 1 && v[2] > 26) ||
            (v[1] == 1 && v[2] == 26 && v[3] >= 10)) {
            accepted = 1
        }
    }
    END { if (!accepted || NR != 1) exit 1 }
'; then
    printf '%s\n' 'Go compiler must be a stable release >= go1.26.10; automatic toolchain downloads cannot satisfy this check.' >&2
    exit 1
fi
printf 'Go toolchain verified: %s (minimum go1.26.10, GOTOOLCHAIN=local)\n' "$version"
