#!/bin/sh
# Isolated checks of the build gate: no downloads or installed Go required.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT HUP INT TERM
cat > "$fixture/go" <<'EOF'
#!/bin/sh
[ "${GOTOOLCHAIN:-}" = local ] || exit 99
[ "$*" = 'env GOVERSION' ] || exit 98
[ "${TEST_GO_EXIT:-0}" = 0 ] || exit "$TEST_GO_EXIT"
printf '%s\n' "$TEST_GO_VERSION"
EOF
chmod 0755 "$fixture/go"
PATH="$fixture:$PATH"
export PATH

for version in go1.26.10 go1.26.11 go1.27.0; do
    TEST_GO_VERSION=$version sh "$root/scripts/check-go-toolchain.sh" > /dev/null
done
for version in go1.26.0 go1.26.1 go1.26.9 go1.25.99 go1.26 go1.27rc1 go1.27beta1 devel '' go1.26.10-extra; do
    if TEST_GO_VERSION=$version sh "$root/scripts/check-go-toolchain.sh" > /dev/null 2>&1; then
        printf 'unexpected compiler accepted: %s\n' "$version" >&2
        exit 1
    fi
done
if TEST_GO_EXIT=7 TEST_GO_VERSION=go1.26.10 sh "$root/scripts/check-go-toolchain.sh" > /dev/null 2>&1; then
    printf '%s\n' 'failed compiler inspection was accepted' >&2
    exit 1
fi
if TEST_GO_VERSION="go1.26.10
go1.26.9" sh "$root/scripts/check-go-toolchain.sh" > /dev/null 2>&1; then
    printf '%s\n' 'multiple compiler versions were accepted' >&2
    exit 1
fi
printf '%s\n' 'Go toolchain gate: 15 checks passed'
