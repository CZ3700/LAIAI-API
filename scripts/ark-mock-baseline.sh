#!/usr/bin/env sh
# A fresh Docker MySQL database and an in-memory provider: no production DSN,
# official credentials, external model call, or shared fixture database.
set -eu
umask 077

if [ "${1:-}" = --help ] && [ "$#" -eq 1 ]; then
    printf '%s\n' 'Usage: ARK_BASELINE_REPORT_DIR=<artifact-root> sh scripts/ark-mock-baseline.sh' \
        'Optional: ARK_BASELINE_MYSQL_IMAGE (default mysql:8.4).' \
        'Requires installed Go >=1.26.10, Docker, Python 3; creates a unique report subdirectory.'
    exit 0
fi
[ "$#" -eq 0 ] || { echo 'No command-line arguments accepted; use --help.' >&2; exit 2; }
if [ -n "${LAIAI_DSN:-}" ] || [ -n "${ARK_TEST_MYSQL_DSN:-}" ]; then
    echo 'Refusing inherited database DSN; this runner creates its own isolated database.' >&2
    exit 2
fi
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
for tool in docker go python3 git; do
    command -v "$tool" >/dev/null 2>&1 || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
export GOTOOLCHAIN=local
sh "$ROOT/scripts/check-go-toolchain.sh"
REPORT_ROOT=${ARK_BASELINE_REPORT_DIR:-"$ROOT/.cache/ark-mock-baseline"}
mkdir -p "$REPORT_ROOT"
REPORT_ROOT=$(CDPATH= cd -- "$REPORT_ROOT" && pwd)
RUN_DIR=$(mktemp -d "$REPORT_ROOT/run-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXXXX")
RUN_ID=$(python3 -c 'import secrets; print(secrets.token_hex(12))')
CT="laiai-ark-mock-$RUN_ID"
LABEL='ai.laiai.ark-mock-baseline.run'
IMAGE=${ARK_BASELINE_MYSQL_IMAGE:-mysql:8.4}
CT_ATTEMPTED=0
COMPLETED=0
PHASE=setup
export ROOT RUN_DIR RUN_ID IMAGE

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
            echo 'Container ownership label mismatch; refusing cleanup.' >&2
        fi
    fi
    # The path was created inside this unique directory by this invocation.
    rm -f "$RUN_DIR/mysql.env"
    BASELINE_EXIT=$rc BASELINE_PHASE=$PHASE BASELINE_REMOVED=$removed python3 - <<'PY'
import datetime, json, os
from pathlib import Path
p = Path(os.environ['RUN_DIR'])
report = p / 'report.json'
if not report.exists():
    report.write_text(json.dumps({'schema_version': 1, 'passed': False, 'status': 'runner_failed',
        'phase': os.environ['BASELINE_PHASE'], 'official_calls': 0, 'paid_calls': 0}, indent=2)+'\n')
elif os.environ['BASELINE_EXIT'] != '0':
    value = json.loads(report.read_text())
    value['test_report_passed'] = value.get('passed')
    value['passed'] = False
    value['runner_status'] = 'failed'
    value['runner_phase'] = os.environ['BASELINE_PHASE']
    report.write_text(json.dumps(value, indent=2)+'\n')
run = json.loads((p/'run.json').read_text()) if (p/'run.json').exists() else {}
run.update(exit_code=int(os.environ['BASELINE_EXIT']), finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
           container_removed=os.environ['BASELINE_REMOVED']=='true', phase=os.environ['BASELINE_PHASE'])
(p/'run.json').write_text(json.dumps(run, indent=2)+'\n')
PY
    printf 'Baseline artifacts: %s\n' "$RUN_DIR"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

PW=$(python3 -c 'import secrets; print(secrets.token_hex(24))')
printf 'MYSQL_ROOT_PASSWORD=%s\nMYSQL_ROOT_HOST=%%\nMYSQL_DATABASE=ark_test\n' "$PW" >"$RUN_DIR/mysql.env"
ARK_MOCK_BASELINE_COMMIT=$(git -C "$ROOT" rev-parse HEAD)
export ARK_MOCK_BASELINE_COMMIT
python3 - <<'PY'
import datetime, hashlib, json, os, subprocess
from pathlib import Path
p=Path(os.environ['RUN_DIR'])
root=Path(os.environ['ROOT'])
files=['core/internal/video/mock_ark_baseline_test.go','core/internal/video/mock_ark_provider_test.go','scripts/ark-mock-baseline.sh']
fingerprint=hashlib.sha256()
for name in files:
    fingerprint.update(name.encode()+b'\0'+(root/name).read_bytes()+b'\0')
diff=subprocess.check_output(['git','-C',str(root),'diff','--binary','HEAD'])
dirty=bool(subprocess.check_output(['git','-C',str(root),'status','--porcelain']))
(p/'run.json').write_text(json.dumps({'run_id':os.environ['RUN_ID'], 'commit':os.environ['ARK_MOCK_BASELINE_COMMIT'],
  'source_dirty':dirty, 'baseline_sources_sha256':fingerprint.hexdigest(), 'tracked_diff_sha256':hashlib.sha256(diff).hexdigest(),
  'mysql_image_requested':os.environ['IMAGE'], 'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
  'official_calls':0, 'scope_count':1, 'task_count':10}, indent=2)+'\n')
PY

PHASE=mysql_start
CT_ATTEMPTED=1
docker run -d --name "$CT" --label "$LABEL=$RUN_ID" \
    --env-file "$RUN_DIR/mysql.env" -p 127.0.0.1::3306 \
    --health-cmd='MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h127.0.0.1 -uroot --silent' \
    --health-interval=2s --health-timeout=2s --health-retries=60 \
    "$IMAGE" >"$RUN_DIR/container-id" 2>"$RUN_DIR/mysql-start.log"
rm -f "$RUN_DIR/mysql.env"
i=0
while [ "$(docker inspect --format '{{.State.Health.Status}}' "$CT" 2>/dev/null || true)" != healthy ]; do
    i=$((i+1))
    [ "$i" -le 90 ] || { echo 'Isolated MySQL startup timed out.' >&2; exit 1; }
    sleep 2
done
BIND=$(docker port "$CT" 3306/tcp)
PORT=$(printf '%s\n' "$BIND" | python3 -c 'import sys; s=sys.stdin.read().strip(); host,port=s.rsplit(":",1); assert host=="127.0.0.1" and 0<int(port)<65536; print(port)')
LAIAI_DSN="root:$PW@tcp(127.0.0.1:$PORT)/ark_test?parseTime=true&charset=utf8mb4&loc=UTC"
ARK_TEST_MYSQL_DSN=$LAIAI_DSN
export LAIAI_DSN ARK_TEST_MYSQL_DSN
unset PW
docker inspect --format '{{.Image}}' "$CT" >"$RUN_DIR/mysql-image-id"
PHASE=migrations
(
    cd "$ROOT/core"
    go run ./cmd/migrate -dir ../migrations
    go run ./cmd/migrate -dir ../migrations
    go run ./cmd/schema-doctor -dir ../migrations
) >"$RUN_DIR/migrations.log" 2>&1
PHASE=baseline
ARK_MOCK_BASELINE=1
ARK_MOCK_BASELINE_REPORT="$RUN_DIR/report.json"
export ARK_MOCK_BASELINE ARK_MOCK_BASELINE_REPORT
(
    cd "$ROOT/core"
    go test -p 1 ./internal/video -run '^TestMySQLMockArkWorkerBaseline$' -count=1 -timeout=3m -json
) >"$RUN_DIR/go-test.jsonl" 2>&1
PHASE=verify_report
python3 - <<'PY'
import json, os
from pathlib import Path
p=Path(os.environ['RUN_DIR'])
r=json.loads((p/'report.json').read_text())
assert r.get('passed') is True and r.get('status')=='passed', 'baseline report did not pass'
assert r.get('verified_scope_count')==1 and r.get('task_count')==10 and r.get('official_calls')==0
events=[json.loads(line) for line in (p/'go-test.jsonl').read_text().splitlines() if line.startswith('{')]
name='TestMySQLMockArkWorkerBaseline'
assert any(e.get('Test')==name and e.get('Action')=='run' for e in events), 'baseline did not run'
assert any(e.get('Test')==name and e.get('Action')=='pass' for e in events), 'baseline did not pass'
assert not any(e.get('Test')==name and e.get('Action') in ('skip','fail') for e in events)
print('PASS: 10-task single-scope mock baseline; official calls=0; no 500-task capacity claim.')
PY
PHASE=complete
COMPLETED=1
