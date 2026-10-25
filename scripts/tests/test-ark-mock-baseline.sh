#!/usr/bin/env sh
# Runner failure paths use command doubles only: no Docker/DB/model calls.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/laiai-mock-runner-test.XXXXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM
mkdir "$TMP/bin"
cat >"$TMP/bin/go" <<'SH'
#!/bin/sh
set -eu
case "${1:-}" in
    env) echo go1.26.10 ;;
    run) [ "${FAKE_MODE:-}" != migration_fail ] ;;
    test)
        [ "${FAKE_MODE:-}" != test_fail ] || exit 1
        python3 - <<'PY'
import json, os
from pathlib import Path
Path(os.environ['ARK_MOCK_BASELINE_REPORT']).write_text(json.dumps({'passed':True,'status':'passed','verified_scope_count':1,'task_count':10,'official_calls':0}))
name='TestMySQLMockArkWorkerBaseline'
print(json.dumps({'Test':name, 'Action':'run'}))
print(json.dumps({'Test':name, 'Action':'skip' if os.environ['FAKE_MODE']=='skip' else 'pass'}))
PY
        ;;
    *) exit 2 ;;
esac
SH
cat >"$TMP/bin/docker" <<'SH'
#!/bin/sh
set -eu
case "${1:-}" in
    run)
        shift
        while [ "$#" -gt 0 ]; do
            case "$1" in
                --name) shift; printf '%s\n' "$1" >"$FAKE_STATE/name" ;;
                --label) shift; printf '%s\n' "${1#*=}" >"$FAKE_STATE/owner" ;;
            esac
            shift
        done
        touch "$FAKE_STATE/created"
        [ "$FAKE_MODE" != create_partial_fail ] || exit 1
        echo synthetic-container ;;
    inspect)
        [ -f "$FAKE_STATE/created" ] || exit 1
        case "$3" in
            *Labels*) if [ "$FAKE_MODE" = owner_mismatch ]; then echo another-owner; else cat "$FAKE_STATE/owner"; fi ;;
            *Health*) echo healthy ;;
            *Image*) echo sha256:synthetic ;;
            *Id*) echo synthetic-container ;;
            *) exit 2 ;;
        esac ;;
    port) echo 127.0.0.1:33306 ;;
    rm)
        [ "$2" = -fv ] && [ "$3" = "$(cat "$FAKE_STATE/name")" ] || exit 3
        [ "$FAKE_MODE" != owner_mismatch ] || exit 4
        touch "$FAKE_STATE/removed" ;;
    *) exit 2 ;;
esac
SH
chmod +x "$TMP/bin/go" "$TMP/bin/docker"
for mode in success migration_fail test_fail skip create_partial_fail owner_mismatch; do
    mkdir "$TMP/$mode"
    export FAKE_STATE="$TMP/$mode" FAKE_MODE="$mode"
    if env -u LAIAI_DSN -u ARK_TEST_MYSQL_DSN PATH="$TMP/bin:$PATH" \
        ARK_BASELINE_REPORT_DIR="$TMP/$mode/artifacts" \
        sh "$ROOT/scripts/ark-mock-baseline.sh" >"$TMP/$mode/output" 2>&1; then
        rc=0
    else
        rc=$?
    fi
    if [ "$mode" = success ]; then [ "$rc" -eq 0 ]; else [ "$rc" -ne 0 ]; fi
    if [ "$mode" = owner_mismatch ]; then
        [ ! -e "$FAKE_STATE/removed" ]
    else
        [ -e "$FAKE_STATE/removed" ]
    fi
    RC="$rc" python3 - <<'PY'
import json, os
from pathlib import Path
p=Path(os.environ['FAKE_STATE'])/'artifacts'
runs=list(p.glob('run-*'))
assert len(runs)==1
run=runs[0]
meta=json.loads((run/'run.json').read_text())
assert meta['exit_code']==int(os.environ['RC'])
assert not (run/'mysql.env').exists()
report=json.loads((run/'report.json').read_text())
if os.environ['FAKE_MODE'] in ('migration_fail','test_fail','create_partial_fail'):
    assert report['passed'] is False and report['status']=='runner_failed'
assert 'root:' not in (Path(os.environ['FAKE_STATE'])/'output').read_text()
PY
    printf 'PASS runner scenario: %s\n' "$mode"
done
mkdir "$TMP/inherited"
if LAIAI_DSN='do-not-connect' ARK_BASELINE_REPORT_DIR="$TMP/inherited" \
    sh "$ROOT/scripts/ark-mock-baseline.sh" >"$TMP/inherited/output" 2>&1; then
    echo 'Inherited DSN was not rejected' >&2
    exit 1
fi
[ -z "$(find "$TMP/inherited" -name run.json -print)" ]
if sh "$ROOT/scripts/ark-mock-baseline.sh" unexpected >"$TMP/argument-output" 2>&1; then
    echo 'Unknown argument was not rejected' >&2
    exit 1
fi
printf '%s\n' 'PASS runner scenario: external DSN and unknown argument refused before Docker'
