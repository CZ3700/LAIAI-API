#!/usr/bin/env sh
# Build genuine pre-runtime/new binaries; hand durable receipts across expand
# migration. Only synthetic in-process Ark responses and an owned Docker DB.
set -eu
umask 077
OLD_SHA=ceecb121093610b8727808d63f1917492832f610
if [ "${1:-}" = --help ] && [ "$#" -eq 1 ]; then
    printf '%s\n' 'Usage: ARK_RUNTIME_REPORT_DIR=<artifact-root> sh scripts/ark-runtime-compat.sh' \
        'Optional ARK_RUNTIME_MYSQL_IMAGE (default mysql:8.4). Requires Go >=1.26.10, Docker, Python 3, git, tar.' \
        "Requires local git object $OLD_SHA; old source is immutable except the identical test harness." \
        'No external DSN or model credentials accepted; this is v1-only rolling compatibility, not V2 or capacity evidence.'
    exit 0
fi
[ "$#" -eq 0 ] || { echo 'No arguments accepted; use --help.' >&2; exit 2; }
if [ -n "${LAIAI_DSN:-}" ] || [ -n "${ARK_TEST_MYSQL_DSN:-}" ]; then
    echo 'Refusing inherited database DSN; the runner creates an isolated database.' >&2
    exit 2
fi
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
for tool in docker go python3 git tar; do
    command -v "$tool" >/dev/null 2>&1 || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
export GOTOOLCHAIN=local GOENV=off
# Inherited workspace/overlay flags could silently compile another checkout.
unset GOWORK GOFLAGS GOOS GOARCH
sh "$ROOT/scripts/check-go-toolchain.sh"
git -C "$ROOT" cat-file -e "$OLD_SHA^{commit}" 2>/dev/null || {
    echo "Required fixed old revision is absent: $OLD_SHA. Fetch repository history before running." >&2; exit 2;
}
REPORT_ROOT=${ARK_RUNTIME_REPORT_DIR:-"$ROOT/.cache/ark-runtime-compat"}
mkdir -p "$REPORT_ROOT"
REPORT_ROOT=$(CDPATH= cd -- "$REPORT_ROOT" && pwd)
RUN_DIR=$(mktemp -d "$REPORT_ROOT/run-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXXXX")
RUN_ID=$(python3 -c 'import secrets; print(secrets.token_hex(12))')
CT="laiai-ark-runtime-$RUN_ID"
LABEL=ai.laiai.ark-runtime-compat.run
IMAGE=${ARK_RUNTIME_MYSQL_IMAGE:-mysql:8.4}
CT_ATTEMPTED=0
COMPLETED=0
PHASE=snapshot
export ROOT RUN_DIR RUN_ID IMAGE OLD_SHA
cleanup() {
    rc=$?
    trap - EXIT INT TERM
    [ "$COMPLETED" -eq 1 ] || [ "$rc" -ne 0 ] || rc=1
    removed=false
    if [ "$CT_ATTEMPTED" -eq 1 ]; then
        owner=$(docker inspect --format "{{ index .Config.Labels \"$LABEL\" }}" "$CT" 2>/dev/null || true)
        if [ "$owner" = "$RUN_ID" ]; then
            if docker rm -fv "$CT" >"$RUN_DIR/cleanup.log" 2>&1; then
                removed=true
            else
                rc=1
                echo 'Could not remove this run container; see private cleanup.log.' >&2
            fi
        elif docker inspect --format '{{.Id}}' "$CT" >/dev/null 2>&1; then
            rc=1
            echo 'Container ownership mismatch; refusing cleanup.' >&2
        fi
    fi
    rm -f "$RUN_DIR/mysql.env"
    COMPAT_EXIT=$rc COMPAT_PHASE=$PHASE COMPAT_REMOVED=$removed python3 - <<'PY'
import datetime, json, os
from pathlib import Path
p=Path(os.environ['RUN_DIR'])
report=p/'report.json'
if not report.exists():
    report.write_text(json.dumps({'passed':False,'status':'runner_failed','phase':os.environ['COMPAT_PHASE'],
        'official_calls':0},indent=2)+'\n')
elif os.environ['COMPAT_EXIT']!='0':
    r=json.loads(report.read_text()); r.update(test_report_passed=r.get('passed'),passed=False,runner_status='failed')
    report.write_text(json.dumps(r,indent=2)+'\n')
run=json.loads((p/'run.json').read_text()) if (p/'run.json').exists() else {}
run.update(exit_code=int(os.environ['COMPAT_EXIT']),phase=os.environ['COMPAT_PHASE'],
    container_removed=os.environ['COMPAT_REMOVED']=='true',finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
(p/'run.json').write_text(json.dumps(run,indent=2)+'\n')
PY
    printf 'Runtime compatibility artifacts: %s\n' "$RUN_DIR"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir "$RUN_DIR/old-source" "$RUN_DIR/new-source" "$RUN_DIR/bin"
git -C "$ROOT" archive --format=tar "$OLD_SHA" >"$RUN_DIR/old-source.tar"
tar -xf "$RUN_DIR/old-source.tar" -C "$RUN_DIR/old-source"
python3 - <<'PY'
import datetime, hashlib, json, os, shutil, subprocess
from pathlib import Path
p,root=Path(os.environ['RUN_DIR']),Path(os.environ['ROOT'])
assert not root.resolve().is_relative_to(p.parent.resolve()), 'artifact root must not contain the source root'
names=subprocess.check_output(['git','-C',str(root),'ls-files','-z','--cached','--others','--exclude-standard']).split(b'\0')
new=p/'new-source'; old=p/'old-source'
for raw in sorted(set(names)):
    if not raw: continue
    name=raw.decode(); relative=Path(name)
    assert not relative.is_absolute() and '..' not in relative.parts and '.git' not in relative.parts
    source=root/relative
    if source.resolve().is_relative_to(p.parent.resolve()): continue
    if not source.exists(): continue # tracked deletion is part of this working tree snapshot
    assert source.is_file() and not source.is_symlink(), 'unsupported source entry'
    dest=new/relative; dest.parent.mkdir(parents=True,exist_ok=True); shutil.copyfile(source,dest)
harness='core/internal/video/runtime_handoff_test.go'
assert not (old/harness).exists(), 'fixed old revision already has a different handoff harness'
shutil.copyfile(new/harness,old/harness)
def digest_tree(directory):
    h=hashlib.sha256()
    for f in sorted(directory.rglob('*')):
        if f.is_file(): h.update(f.relative_to(directory).as_posix().encode()+b'\0'+f.read_bytes()+b'\0')
    return h.hexdigest()
new_sha=subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD'],text=True).strip()
diff=subprocess.check_output(['git','-C',str(root),'diff','--binary','HEAD'])
dirty=bool(subprocess.check_output(['git','-C',str(root),'status','--porcelain']))
(p/'run.json').write_text(json.dumps({'run_id':os.environ['RUN_ID'],'old_source_commit':os.environ['OLD_SHA'],
    'new_source_commit':new_sha,'new_source_dirty':dirty,'new_tracked_diff_sha256':hashlib.sha256(diff).hexdigest(),
    'old_source_with_harness_sha256':digest_tree(old),'new_source_sha256':digest_tree(new),
    'identical_harness_sha256':hashlib.sha256((new/harness).read_bytes()).hexdigest(),
    'old_overlay_files':[harness], 'mysql_image_requested':os.environ['IMAGE'],
    'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'official_calls':0,
    'claim':'real pre-runtime binary and new v1 binary; V2 remains disabled; no capacity claim'},indent=2)+'\n')
PY

PHASE=compile
( cd "$RUN_DIR/old-source/core"; go test -p 1 -c -o "$RUN_DIR/bin/old-video.test" ./internal/video ) >"$RUN_DIR/old-build.log" 2>&1
( cd "$RUN_DIR/new-source/core"; go test -p 1 -c -o "$RUN_DIR/bin/new-video.test" ./internal/video ) >"$RUN_DIR/new-build.log" 2>&1
python3 - <<'PY'
import hashlib,json,os
from pathlib import Path
p=Path(os.environ['RUN_DIR']); r=json.loads((p/'run.json').read_text())
for name in ('old','new'):
    binary=p/'bin'/f'{name}-video.test'; assert binary.stat().st_size>0
    r[f'{name}_test_binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
assert r['old_test_binary_sha256']!=r['new_test_binary_sha256'], 'old/new binary unexpectedly identical'
(p/'run.json').write_text(json.dumps(r,indent=2)+'\n')
PY

PHASE=mysql_start
PW=$(python3 -c 'import secrets; print(secrets.token_hex(24))')
printf 'MYSQL_ROOT_PASSWORD=%s\nMYSQL_ROOT_HOST=%%\nMYSQL_DATABASE=ark_test\n' "$PW" >"$RUN_DIR/mysql.env"
CT_ATTEMPTED=1
docker run -d --name "$CT" --label "$LABEL=$RUN_ID" --env-file "$RUN_DIR/mysql.env" -p 127.0.0.1::3306 \
    --health-cmd='MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h127.0.0.1 -uroot --silent' \
    --health-interval=2s --health-timeout=2s --health-retries=60 "$IMAGE" \
    >"$RUN_DIR/container-id" 2>"$RUN_DIR/mysql-start.log"
rm -f "$RUN_DIR/mysql.env"
i=0
while [ "$(docker inspect --format '{{.State.Health.Status}}' "$CT" 2>/dev/null || true)" != healthy ]; do
    i=$((i+1))
    [ "$i" -le 90 ] || { echo 'Isolated MySQL startup timed out.' >&2; exit 1; }
    sleep 2
done
BIND=$(docker port "$CT" 3306/tcp)
PORT=$(printf '%s\n' "$BIND" | python3 -c 'import sys; host,port=sys.stdin.read().strip().rsplit(":",1); assert host=="127.0.0.1" and 0<int(port)<65536; print(port)')
LAIAI_DSN="root:$PW@tcp(127.0.0.1:$PORT)/ark_test?parseTime=true&charset=utf8mb4&loc=UTC"
ARK_TEST_MYSQL_DSN=$LAIAI_DSN
ARK_RUNTIME_COMPAT=1
ARK_RUNTIME_COMPAT_DIR=$RUN_DIR
export LAIAI_DSN ARK_TEST_MYSQL_DSN ARK_RUNTIME_COMPAT ARK_RUNTIME_COMPAT_DIR
unset PW
docker inspect --format '{{.Image}}' "$CT" >"$RUN_DIR/mysql-image-id"
PHASE=old_migration
( cd "$RUN_DIR/old-source/core"; go run ./cmd/migrate -dir ../migrations ) >"$RUN_DIR/old-migrations.log" 2>&1
PHASE=old_before
ARK_RUNTIME_COMPAT_PHASE=old_before go tool test2json -t -p github.com/laiai/laiai-next/core/internal/video \
    "$RUN_DIR/bin/old-video.test" -test.v=test2json -test.run '^TestMySQLRuntimeBinaryHandoff$' -test.count=1 -test.timeout=3m >"$RUN_DIR/old-before.jsonl" 2>&1
PHASE=expand
(
    cd "$RUN_DIR/new-source/core"
    go run ./cmd/migrate -dir ../migrations
    go run ./cmd/migrate -dir ../migrations
    go run ./cmd/schema-doctor -dir ../migrations
) >"$RUN_DIR/expand-migrations.log" 2>&1
PHASE=old_after
ARK_RUNTIME_COMPAT_PHASE=old_after go tool test2json -t -p github.com/laiai/laiai-next/core/internal/video \
    "$RUN_DIR/bin/old-video.test" -test.v=test2json -test.run '^TestMySQLRuntimeBinaryHandoff$' -test.count=1 -test.timeout=3m >"$RUN_DIR/old-after.jsonl" 2>&1
PHASE=new_verify
ARK_RUNTIME_COMPAT_PHASE=new_verify go tool test2json -t -p github.com/laiai/laiai-next/core/internal/video \
    "$RUN_DIR/bin/new-video.test" -test.v=test2json -test.run '^TestMySQLRuntimeBinaryHandoff$' -test.count=1 -test.timeout=3m >"$RUN_DIR/new-verify.jsonl" 2>&1
PHASE=verify_reports
python3 - <<'PY'
import json,os
from pathlib import Path
p=Path(os.environ['RUN_DIR']); name='TestMySQLRuntimeBinaryHandoff'
for phase in ('old-before','old-after','new-verify'):
    lines=(p/f'{phase}.jsonl').read_text().splitlines()
    events=[json.loads(line) for line in lines if line.startswith('{')]
    assert any(e.get('Test')==name and e.get('Action')=='run' for e in events), 'test did not run'
    assert any(e.get('Test')==name and e.get('Action')=='pass' for e in events), 'test did not pass'
    assert not any(e.get('Action') in ('skip','fail') for e in events), 'a test skipped/failed'
for phase in ('old_before','old_after'):
    r=json.loads((p/f'{phase}.json').read_text())
    assert r['passed'] and r['phase']==phase and len(r['tasks'])==4
    assert r['mock_posts']==4 and r['mock_gets']==2 and r['official_calls']==0 and r['ledger_drift']==0
r=json.loads((p/'report.json').read_text())
assert r['passed'] and r['phase']=='new_verify' and len(r['tasks'])==8
assert r['new_settlements']==2 and r['provider_calls']==0 and r['official_calls']==0 and r['ledger_drift']==0
print('PASS: actual old/new binaries, migration plus old-writer overlap, receipt replay twice, new HTTP=0, new settlements=2, drift=0.')
PY
PHASE=complete
COMPLETED=1
