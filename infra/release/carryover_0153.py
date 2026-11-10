#!/usr/bin/env python3
"""Read-only MySQL guards for the exact 0150 -> 0153 in-flight release.

Raw business rows stay inside a private Docker pipe. Only column names, zero
counts, a database-time cutoff and SHA-256 digests enter release state.
"""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


CHANGED_TABLES = {
    "video_operations": ("next_dispatch_at",),
    "video_callback_hints": ("revision", "retry_at", "attempts"),
}
UNCHANGED_TABLES = (
    "tasks", "video_task_states", "video_attempts", "video_execution_attempts",
    "video_response_inbox", "video_receipt_applications", "video_query_receipts",
    "video_quota_reservations", "video_usage_evidence", "video_billing_snapshots",
    "video_billing_entries", "video_reconciliation_items", "video_adjustment_proposals",
    "video_adjustment_reviews", "video_adjustment_applications", "video_events",
    "video_outbox", "video_request_contexts",
)
MYSQL = '''
    : "${MYSQL_ROOT_PASSWORD:?missing database credential}"
    MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --raw \
      --skip-column-names --default-character-set=utf8mb4 \
      "${MYSQL_DATABASE:-laiai}" -e "$1"
'''
DUMP = '''
    : "${MYSQL_ROOT_PASSWORD:?missing database credential}"
    MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction \
      --compact --skip-comments --skip-extended-insert --order-by-primary \
      --no-create-info --skip-triggers --default-character-set=utf8mb4 \
      --set-gtid-purged=OFF --no-tablespaces --tz-utc "${MYSQL_DATABASE:-laiai}" "$@"
'''
IDENTITY = re.compile(r"[a-z][a-z0-9_]*\Z")
CONTAINER = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*\Z")
DATABASE_TIME = re.compile(r"20[0-9]{2}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}\Z")


class GuardError(Exception):
    pass


def docker_args(container, script, *args):
    if not CONTAINER.fullmatch(container):
        raise GuardError("invalid MySQL container identity")
    return ["docker", "exec", container, "sh", "-eu", "-c", script, "carryover", *args]


def mysql_rows(container, query):
    result = subprocess.run(docker_args(container, MYSQL, "SET time_zone = '+00:00'; " + query),
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False)
    if result.returncode:
        raise GuardError("read-only database query failed")
    try:
        return result.stdout.decode("ascii").splitlines()
    except UnicodeError as exc:
        raise GuardError("non-ASCII database guard output") from exc


def stream_hash(command, digest):
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    assert process.stdout is not None
    try:
        while chunk := process.stdout.read(65536):
            digest.update(chunk)
    finally:
        process.stdout.close()
    if process.wait() != 0:
        raise GuardError("read-only business fingerprint failed")


def columns(container, table):
    if table not in CHANGED_TABLES:
        raise GuardError("unreviewed table")
    rows = mysql_rows(container, "SELECT COLUMN_NAME FROM information_schema.COLUMNS "
                      "WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '" + table +
                      "' ORDER BY ORDINAL_POSITION")
    if not rows or len(rows) > 64 or len(rows) != len(set(rows)) or \
            any(not IDENTITY.fullmatch(name) for name in rows) or "id" not in rows:
        raise GuardError("invalid historical column manifest")
    return rows


def save_private(path, data):
    target = Path(path)
    if not target.parent.is_dir() or target.is_symlink() or target.exists():
        raise GuardError("private guard state already exists or has invalid parent")
    temporary = target.with_suffix(target.suffix + ".tmp")
    if temporary.exists() or temporary.is_symlink():
        raise GuardError("private guard temporary state already exists")
    try:
        with temporary.open("x", encoding="ascii") as out:
            out.write(json.dumps(data, sort_keys=True) + "\n")
        temporary.chmod(0o600)
        temporary.replace(target)
    finally:
        if temporary.exists():
            temporary.unlink()


def load_manifest(path):
    value = json.loads(Path(path).read_text(encoding="ascii"))
    if set(value) != {"version", "columns"} or value["version"] != 1 or \
            set(value["columns"]) != set(CHANGED_TABLES):
        raise GuardError("invalid historical column manifest")
    for table, names in value["columns"].items():
        if not isinstance(names, list) or not names or len(names) > 64 or "id" not in names or \
                len(names) != len(set(names)) or any(not isinstance(n, str) or not IDENTITY.fullmatch(n) for n in names) or \
                any(n in names for n in CHANGED_TABLES[table]):
            raise GuardError("invalid historical column manifest")
    return value["columns"]


def prepare(container, path):
    duplicates = mysql_rows(container, "SELECT COUNT(*) FROM (SELECT task_id FROM "
                            "video_callback_hints GROUP BY task_id HAVING COUNT(*) > 1) AS duplicated")
    if len(duplicates) != 1 or duplicates[0] != "0":
        raise GuardError("duplicate callback hints remain after old core stopped")
    historical = {}
    for table, additions in CHANGED_TABLES.items():
        historical[table] = columns(container, table)
        if any(name in historical[table] for name in additions):
            raise GuardError("0153 expansion already began")
    save_private(path, {"version": 1, "columns": historical})


def row_query(table, names):
    # Each nullable cell has an explicit tag; every non-null byte is hex, so
    # NULL, empty, binary, tabs and newlines cannot collide. PK order is fixed.
    cells = ["IF(`{0}` IS NULL,'N;',CONCAT('V',HEX(CAST(`{0}` AS BINARY)),';'))".format(name)
             for name in names]
    return "SELECT CONCAT(" + ",".join(cells) + ") FROM `" + table + "` ORDER BY `id`"


def fingerprint(container, path, phase):
    if phase not in ("before", "after"):
        raise GuardError("invalid fingerprint phase")
    historical = load_manifest(path)
    for table, names in historical.items():
        expected = names if phase == "before" else names + list(CHANGED_TABLES[table])
        if columns(container, table) != expected:
            raise GuardError("business table schema differs from reviewed expansion")
    digest = hashlib.sha256(b"laiai-business-0153-v1\0")
    stream_hash(docker_args(container, DUMP, *UNCHANGED_TABLES), digest)
    for table in CHANGED_TABLES:
        digest.update(b"\0" + table.encode("ascii") + b"\0")
        query = "SET time_zone = '+00:00'; " + row_query(table, historical[table])
        stream_hash(docker_args(container, MYSQL, query), digest)
    return digest.hexdigest()


FEATURE_SQL = (
    ("user_task_limits", "SELECT COUNT(*) FROM video_user_task_limits"),
    ("user_send_gates", "SELECT COUNT(*) FROM video_user_send_gates"),
    # The five-minute ListModelRateLimit job may write official-only snapshots
    # immediately after startup. Old core can ignore those read-only facts.
    ("provider_manual_ceilings", "SELECT COUNT(*) FROM video_provider_model_quotas "
     "WHERE manual_version > 0 OR manual_create_task_rpm IS NOT NULL "
     "OR manual_concurrent_requests IS NOT NULL OR manual_create_task_rpm_4k IS NOT NULL "
     "OR manual_concurrent_requests_4k IS NOT NULL"),
    ("provider_send_gates_pending", "SELECT COUNT(*) FROM video_provider_model_send_gates "
     "WHERE next_allowed_at > UTC_TIMESTAMP(3) OR next_allowed_at_4k > UTC_TIMESTAMP(3)"),
    ("group_asset_projects", "SELECT COUNT(*) FROM video_group_asset_projects"),
    ("deferred_operations", "SELECT COUNT(*) FROM video_operations WHERE next_dispatch_at IS NOT NULL"),
    ("user_money_budgets", "SELECT COUNT(*) FROM video_quota_policies WHERE scope = 'user'"),
    ("inherited_keys", "SELECT COUNT(*) FROM api_keys WHERE model_access_mode = 'inherit_group'"),
    ("default_group_grants", "SELECT COUNT(*) FROM org_group_grants WHERE default_slot = 1"),
)


def feature_counts(container, task_max=None):
    queries = list(FEATURE_SQL)
    if task_max is not None:
        if not isinstance(task_max, int) or task_max < 0:
            raise GuardError("invalid pre-start task high-water mark")
        queries.append(("new_video_tasks", "SELECT COUNT(*) FROM tasks WHERE provider = 'ark_official' "
                        "AND id > " + str(task_max)))
    # All subqueries share one InnoDB consistent statement snapshot.
    query = "SELECT " + ",".join("(" + sql + ")" for _, sql in queries)
    rows = mysql_rows(container, query)
    if len(rows) != 1:
        raise GuardError("incomplete feature rollback check")
    values = rows[0].split("\t")
    if len(values) != len(queries) or any(not re.fullmatch(r"[0-9]+", v) for v in values):
        raise GuardError("malformed feature rollback check")
    return {name: int(value) for (name, _), value in zip(queries, values)}


def asset_id_digest(container, table):
    identity = {"video_asset_operations": "operation_id", "video_assets": "asset_id"}
    if table not in identity:
        raise GuardError("unreviewed asset identity table")
    digest = hashlib.sha256(b"laiai-asset-identities-0153-v1\0" + table.encode("ascii") + b"\0")
    query = "SET time_zone = '+00:00'; SELECT HEX(CAST(`" + identity[table] + "` AS BINARY)) " \
            "FROM `" + table + "` ORDER BY `" + identity[table] + "`"
    stream_hash(docker_args(container, MYSQL, query), digest)
    return digest.hexdigest()


def require_unused(container, task_max=None, asset_hashes=None):
    active = [name for name, count in feature_counts(container, task_max).items() if count]
    if asset_hashes is not None:
        for table in ("video_asset_operations", "video_assets"):
            if asset_id_digest(container, table) != asset_hashes[table]:
                active.append("new_or_removed_" + table)
    if active:
        raise GuardError("old-core rollback unsafe; active 0153 facts: " + ", ".join(active))


def baseline(container, path):
    require_unused(container)
    rows = mysql_rows(container, "SELECT COALESCE(MAX(id),0), "
                      "DATE_FORMAT(UTC_TIMESTAMP(3), '%Y-%m-%d %H:%i:%s.%f') FROM tasks")
    fields = rows[0].split("\t") if len(rows) == 1 else []
    if len(fields) != 2 or not re.fullmatch(r"[0-9]+", fields[0]) or not re.fullmatch(
            r"20[0-9]{2}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}", fields[1]):
        raise GuardError("invalid database-time baseline")
    assets = {table: asset_id_digest(container, table)
              for table in ("video_asset_operations", "video_assets")}
    save_private(path, {"version": 1, "task_max_id": int(fields[0]),
                        "recorded_at": fields[1][:23], "asset_id_hashes": assets})


def guard(container, path):
    value = json.loads(Path(path).read_text(encoding="ascii"))
    hashes = value.get("asset_id_hashes")
    if set(value) != {"version", "task_max_id", "recorded_at", "asset_id_hashes"} or \
            value["version"] != 1 or not isinstance(value["task_max_id"], int) or \
            value["task_max_id"] < 0 or not isinstance(value["recorded_at"], str) or \
            not DATABASE_TIME.fullmatch(value["recorded_at"]) or not isinstance(hashes, dict) or \
            set(hashes) != {"video_asset_operations", "video_assets"} or \
            any(not isinstance(h, str) or not re.fullmatch(r"[0-9a-f]{64}", h) for h in hashes.values()):
        raise GuardError("invalid core start baseline")
    require_unused(container, value["task_max_id"], hashes)


def main():
    if len(sys.argv) < 3:
        raise GuardError("missing carryover guard command")
    command, container = sys.argv[1:3]
    if command == "prepare" and len(sys.argv) == 4:
        prepare(container, sys.argv[3])
    elif command == "fingerprint" and len(sys.argv) == 5:
        print(fingerprint(container, sys.argv[3], sys.argv[4]))
    elif command == "baseline" and len(sys.argv) == 4:
        baseline(container, sys.argv[3])
    elif command == "guard" and len(sys.argv) == 4:
        guard(container, sys.argv[3])
    else:
        raise GuardError("invalid carryover guard command")


if __name__ == "__main__":
    try:
        main()
    except (GuardError, OSError, ValueError, TypeError, KeyError, UnicodeError) as exc:
        print("0153 carryover refused: " + str(exc), file=sys.stderr)
        sys.exit(2)
