#!/usr/bin/env sh
# Exercise orchestration/error handling only; commands below never use Docker,
# a database or an official model. Real binary compatibility is a separate run.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/laiai-runtime-runner-test.XXXXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM
mkdir "$TMP/bin"
cat >"$TMP/bin/go" <<'SH'
#!/bin/sh
set -eu
case "${1:-}" in
    env) echo go1.26.10 ;;
    run) [ "$FAKE_MODE" != migration_fail ] ;;
    test)
        [ "$FAKE_MODE" != build_fail ] || exit 1
        shift
        while [ "$#" -gt 0 ]; do
            if [ "$1" = -o ]; then shift; printf 'synthetic binary from %s\n' "$PWD" >"$1"; exit 0; fi
            shift
        done
        exit 2 ;;
    tool)
        [ "$2" = test2json ] || exit 2
        [ "$FAKE_MODE" != test_fail ] || exit 1
        [ "$FAKE_MODE" != old_after_fail ] || [ "$ARK_RUNTIME_COMPAT_PHASE" != old_after ] || exit 1
        python3 - <<'PY'
import json, os
from pathlib import Path
phase=os.environ['ARK_RUNTIME_COMPAT_PHASE']; mode=os.environ['FAKE_MODE']
new=phase=='new_verify'; name='report.json' if new else phase+'.json'
if mode!='missing_report' or not new:
    r={'passed':True,'phase':phase,'tasks':[{}]*(8 if new else 4),'mock_posts':0 if new else 4,
       'mock_gets':0 if new else 2,'official_calls':0,'provider_calls':0 if new else 6,
       'new_settlements':2 if new else 0,'ledger_drift':0}
    if new and mode=='new_calls': r['provider_calls']=1
    if new and mode=='duplicate_settlements': r['new_settlements']=3
    (Path(os.environ['ARK_RUNTIME_COMPAT_DIR'])/name).write_text(json.dumps(r))
name='TestMySQLRuntimeBinaryHandoff'
print(json.dumps({'Test':name,'Action':'run'}))
print(json.dumps({'Test':name,'Action':'skip' if mode=='skip' else 'pass'}))
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
for mode in success build_fail migration_fail test_fail old_after_fail skip missing_report new_calls duplicate_settlements create_partial_fail owner_mismatch; do
    mkdir "$TMP/$mode"
    export FAKE_STATE="$TMP/$mode" FAKE_MODE="$mode"
    if env -u LAIAI_DSN -u ARK_TEST_MYSQL_DSN PATH="$TMP/bin:$PATH" \
        ARK_RUNTIME_REPORT_DIR="$TMP/$mode/artifacts" \
        sh "$ROOT/scripts/ark-runtime-compat.sh" >"$TMP/$mode/output" 2>&1; then
        rc=0
    else
        rc=$?
    fi
    if [ "$mode" = success ]; then [ "$rc" -eq 0 ]; else [ "$rc" -ne 0 ]; fi
    case "$mode" in
        owner_mismatch|build_fail) [ ! -e "$FAKE_STATE/removed" ] ;;
        *) [ -e "$FAKE_STATE/removed" ] ;;
    esac
    RC="$rc" python3 - <<'PY'
import json, os
from pathlib import Path
state=Path(os.environ['FAKE_STATE']); runs=list((state/'artifacts').glob('run-*'))
assert len(runs)==1
p=runs[0]; run=json.loads((p/'run.json').read_text()); report=json.loads((p/'report.json').read_text())
assert run['exit_code']==int(os.environ['RC'])
assert not (p/'mysql.env').exists()
assert run['old_source_commit']=='ceecb121093610b8727808d63f1917492832f610'
assert run['old_overlay_files']==['core/internal/video/runtime_handoff_test.go']
for key in ('old_source_with_harness_sha256','new_source_sha256','identical_harness_sha256'):
    assert len(run[key])==64
if os.environ['FAKE_MODE']!='build_fail':
    assert run['old_test_binary_sha256']!=run['new_test_binary_sha256']
assert report['passed']==(os.environ['FAKE_MODE']=='success')
assert 'root:' not in (state/'output').read_text()
PY
    printf 'PASS runtime runner scenario: %s\n' "$mode"
done
mkdir "$TMP/inherited"
for key in LAIAI_DSN ARK_TEST_MYSQL_DSN; do
    if env "$key=do-not-connect" ARK_RUNTIME_REPORT_DIR="$TMP/inherited" sh "$ROOT/scripts/ark-runtime-compat.sh" >"$TMP/inherited/output" 2>&1; then
        echo 'Inherited DSN was not rejected' >&2; exit 1
    fi
done
[ -z "$(find "$TMP/inherited" -name run.json -print)" ]
if sh "$ROOT/scripts/ark-runtime-compat.sh" unexpected >"$TMP/argument-output" 2>&1; then
    echo 'Unknown argument was not rejected' >&2; exit 1
fi
printf '%s\n' 'PASS runtime runner scenario: inherited DSNs and arguments refused before Docker'
