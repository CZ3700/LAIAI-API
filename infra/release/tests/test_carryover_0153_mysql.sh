#!/usr/bin/env bash
# Real isolated MySQL 8.4 test; no production data, ports or volumes.
set -Eeuo pipefail
ROOT=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
mkdir -p "$ROOT/.cache"
WORK=$(mktemp -d "$ROOT/.cache/carryover-0153-test.XXXXXX")
NAME="laiai-carryover-0153-$(basename "$WORK" | tr '[:upper:]' '[:lower:]')"
MYSQL_IMAGE=${MYSQL_SMOKE_IMAGE:-mysql:8.4}
cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  case "$(realpath -- "$WORK")" in "$ROOT"/.cache/carryover-0153-test.*) rm -rf -- "$WORK";; esac
}
trap cleanup EXIT

docker run --rm -d --name "$NAME" \
  -e MYSQL_ROOT_PASSWORD=carryover-0153-test-only -e MYSQL_DATABASE=laiai \
  "$MYSQL_IMAGE" >/dev/null
ready=0
for ((i=0; i<90; i++)); do
  if docker exec -e MYSQL_PWD=carryover-0153-test-only "$NAME" \
    mysql -uroot -Nse 'SELECT 1' >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
test "$ready" = 1 || { echo 'Isolated MySQL did not become ready' >&2; exit 1; }
sql() {
  docker exec -i -e MYSQL_PWD=carryover-0153-test-only "$NAME" \
    mysql -uroot --batch --skip-column-names laiai
}
helper() { python3 "$ROOT/infra/release/carryover_0153.py" "$1" "$NAME" "${@:2}"; }

for table in video_task_states video_attempts video_execution_attempts \
  video_response_inbox video_receipt_applications video_query_receipts \
  video_quota_reservations video_usage_evidence video_billing_snapshots \
  video_billing_entries video_reconciliation_items video_adjustment_proposals \
  video_adjustment_reviews video_adjustment_applications video_events \
  video_outbox video_request_contexts; do
  printf 'CREATE TABLE `%s` (id BIGINT PRIMARY KEY, payload VARBINARY(255) NULL);\n' "$table"
done | sql
sql <<'SQL'
CREATE TABLE tasks (
 id BIGINT PRIMARY KEY, org_id BIGINT NOT NULL, user_id BIGINT NULL,
 provider VARCHAR(32) NOT NULL, status VARCHAR(32) NOT NULL DEFAULT 'accepted',
 submitted_at DATETIME(3) NOT NULL
);
CREATE TABLE video_operations (
 id VARCHAR(64) PRIMARY KEY, status VARCHAR(16) NOT NULL,
 created_at DATETIME(3) NOT NULL, json_payload JSON NULL,
 binary_payload BLOB NULL, nullable_payload BLOB NULL
);
CREATE TABLE video_callback_hints (
 id BIGINT PRIMARY KEY AUTO_INCREMENT, task_id VARCHAR(64) NOT NULL,
 body_hash CHAR(64) NOT NULL, body_excerpt TEXT NOT NULL,
 received_at DATETIME(3) NOT NULL, processed_at DATETIME(3) NULL,
 result VARCHAR(64) NOT NULL DEFAULT '',
 UNIQUE KEY uk_video_callback_hint (task_id,body_hash)
);
CREATE TABLE video_task_parameters (
 account_ref VARCHAR(64) NOT NULL, public_model VARCHAR(128) NOT NULL,
 org_id BIGINT NOT NULL, task_id VARCHAR(64) NOT NULL
);
CREATE TABLE video_quota_policies (id BIGINT PRIMARY KEY, scope VARCHAR(32) NOT NULL);
CREATE TABLE api_keys (id BIGINT PRIMARY KEY, model_access_mode VARCHAR(32) NOT NULL);
CREATE TABLE org_group_grants (id BIGINT PRIMARY KEY, default_slot INT NULL);
CREATE TABLE video_asset_operations (operation_id VARCHAR(64) PRIMARY KEY, created_at DATETIME(3) NOT NULL);
CREATE TABLE video_assets (asset_id VARCHAR(64) PRIMARY KEY, created_at DATETIME(3) NOT NULL);
INSERT INTO video_operations VALUES
 ('op1','pending','2026-11-10 06:00:00.123','{"b":2,"a":1}',X'00090AFF',NULL);
INSERT INTO video_callback_hints(task_id,body_hash,body_excerpt,received_at) VALUES
 ('task1',REPEAT('a',64),'{"id":"task1","status":"queued"}','2026-11-10 06:00:00.123');
SQL

MANIFEST="$WORK/columns.json"
sql <<'SQL'
INSERT INTO video_callback_hints(task_id,body_hash,body_excerpt,received_at) VALUES
 ('task1',REPEAT('b',64),'{"id":"task1","status":"running"}','2026-11-10 06:00:01.123');
SQL
if helper prepare "$MANIFEST" >"$WORK/rejected.log" 2>&1; then
  echo 'Duplicate callbacks were accepted before migration' >&2; exit 1
fi
test ! -e "$MANIFEST"
sql <<'SQL'
DELETE FROM video_callback_hints WHERE body_hash=REPEAT('b',64);
SQL
helper prepare "$MANIFEST"
before=$(helper fingerprint "$MANIFEST" before)

for migration in 0151_callback_hint_revision 0152_video_user_task_limits 0153_video_group_asset_projects; do
  sql < "$ROOT/migrations/$migration.sql"
done
after=$(helper fingerprint "$MANIFEST" after)
test "$before" = "$after" || { echo 'Additive migration changed historical fingerprint' >&2; exit 1; }

sql <<'SQL'
UPDATE video_operations SET next_dispatch_at='2026-11-10 06:10:00.123';
UPDATE video_callback_hints SET revision=2, retry_at='2026-11-10 06:10:00.123', attempts=1;
SQL
test "$(helper fingerprint "$MANIFEST" after)" = "$before"
sql <<'SQL'
UPDATE video_operations SET binary_payload=X'00FF';
SQL
test "$(helper fingerprint "$MANIFEST" after)" != "$before"
sql <<'SQL'
UPDATE video_operations SET binary_payload=X'00090AFF', json_payload='{"different":true}';
SQL
test "$(helper fingerprint "$MANIFEST" after)" != "$before"
sql <<'SQL'
UPDATE video_operations SET json_payload='{"b":2,"a":1}', created_at='2026-11-10 06:00:01.123';
SQL
test "$(helper fingerprint "$MANIFEST" after)" != "$before"
sql <<'SQL'
UPDATE video_operations SET created_at='2026-11-10 06:00:00.123', nullable_payload=X'';
SQL
test "$(helper fingerprint "$MANIFEST" after)" != "$before"
sql <<'SQL'
UPDATE video_operations SET nullable_payload=NULL;
UPDATE video_callback_hints SET body_excerpt='different';
SQL
test "$(helper fingerprint "$MANIFEST" after)" != "$before"
sql <<'SQL'
UPDATE video_callback_hints SET body_excerpt='{"id":"task1","status":"queued"}';
UPDATE video_operations SET next_dispatch_at=NULL;
SQL
test "$(helper fingerprint "$MANIFEST" after)" = "$before"

BASELINE="$WORK/baseline.json"
helper baseline "$BASELINE"
helper guard "$BASELINE"
sql <<'SQL'
INSERT INTO video_provider_model_quotas
 (account_ref,foundation_model,official_create_task_rpm,official_concurrent_requests)
 VALUES ('account','seedance-2.5',20,10);
SQL
helper guard "$BASELINE"  # Automatic official snapshot is rollback-neutral.
sql <<'SQL'
UPDATE video_provider_model_quotas SET manual_create_task_rpm=10,manual_version=1;
SQL
if helper guard "$BASELINE" >"$WORK/rejected.log" 2>&1; then
  echo 'Manual provider ceiling did not block rollback' >&2; exit 1
fi
sql <<'SQL'
UPDATE video_provider_model_quotas SET manual_create_task_rpm=NULL,manual_version=0;
INSERT INTO video_provider_model_send_gates
 (account_ref,foundation_model,next_allowed_at,updated_at)
 VALUES ('account','seedance-2.5',DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 1 MINUTE),UTC_TIMESTAMP(3));
SQL
if helper guard "$BASELINE" >"$WORK/rejected.log" 2>&1; then
  echo 'Pending provider pace did not block rollback' >&2; exit 1
fi
sql <<'SQL'
UPDATE video_provider_model_send_gates SET next_allowed_at=DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 SECOND);
SQL
helper guard "$BASELINE"
sql <<'SQL'
INSERT INTO video_asset_operations VALUES ('asset-op-new','2020-01-01 00:00:00.000');
SQL
if helper guard "$BASELINE" >"$WORK/rejected.log" 2>&1; then
  echo 'New asset operation with an old host timestamp did not block rollback' >&2; exit 1
fi
sql <<'SQL'
DELETE FROM video_asset_operations WHERE operation_id='asset-op-new';
INSERT INTO tasks(id,org_id,user_id,provider,submitted_at)
 VALUES (2,1,1,'ark_official','2020-01-01 00:00:00.000');
SQL
if helper guard "$BASELINE" >"$WORK/rejected.log" 2>&1; then
  echo 'New video admission with an old host timestamp did not block rollback' >&2; exit 1
fi
echo 'PASS: isolated MySQL 8.4 0150->0153 historical rows and rollback guards'
