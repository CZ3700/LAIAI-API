#!/usr/bin/env bash
# Sourced by release.sh (Bash, set -Eeuo pipefail).
# Inputs: MYSQL_CID (already verified live container), RUN_DIR, die(), log().
# Outputs: data-<phase>.json, schema-<phase>.txt and schema.txt (mode 0600).
# Does not create schema, write business rows, log SQL errors, or start workers.
RELEASE_DATA_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)

check_data() (
  set -Eeuo pipefail
  umask 077
  local phase=${1:?check_data requires a phase} scratch rc=0
  case "$phase" in preflight|drained|post-migration|post-backfill) ;; *) die 'invalid data-check phase';; esac
  : "${MYSQL_CID:?verified MySQL container required}" "${RUN_DIR:?release run directory required}"
  [[ -d "$RUN_DIR" && ! -L "$RUN_DIR" ]] || die 'release run directory is missing or a symlink'
  [[ "$MYSQL_CID" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || die 'invalid MySQL container identity'
  scratch=$(mktemp -d "$RUN_DIR/.data-${phase}.XXXXXX")
  trap 'rm -f -- "$scratch/rows.tsv" "$scratch/report.json" "$scratch/schema.txt"; rmdir -- "$scratch"' EXIT
  # A failed re-check must not leave an earlier successful gate at this path.
  printf '{"schema_version":1,"phase":"%s","passed":false,"error":"check_not_completed"}\n' "$phase" > "$scratch/report.json"
  mv -f -- "$scratch/report.json" "$RUN_DIR/data-${phase}.json"
  # MYSQL_PWD is set inside the existing container; its value never enters the
  # host command line, result artifacts or SQL input. SQL stderr is suppressed:
  # raw database errors may include sensitive values and are not a release log.
  if ! docker exec -i "$MYSQL_CID" sh -c \
    'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --raw --skip-column-names --default-character-set=utf8mb4 "${MYSQL_DATABASE:-laiai}"' \
    < "$RELEASE_DATA_DIR/checks.sql" > "$scratch/rows.tsv" 2>/dev/null; then
    die "read-only database check failed at $phase; no release gate passed"
  fi
  python3 - "$phase" "$scratch/rows.tsv" "$scratch/report.json" "$scratch/schema.txt" <<'PY' || rc=$?
import datetime
import hashlib
import json
import pathlib
import re
import sys

phase, source, output, schema_output = sys.argv[1:]
expected = {
    "check": {"ledger_posted_vs_journal", "ledger_held_vs_active_holds",
              "journal_missing_account", "hold_missing_account", "invalid_hold_state_or_amount",
              "task_missing_hold", "task_hold_cross_org", "membership_missing_org_or_user"},
    "warning": {"channel_credential_missing_parent"},
    "drain": {"active_holds", "nonterminal_tasks", "unresolved_video_operations",
              "unsettled_video_billing", "active_video_reservations"},
    "presence": {"video_operations", "video_task_states", "video_quota_reservations"},
    "snapshot": {"ledger_accounts_rows", "ledger_journal_rows", "ledger_holds_rows", "tasks_rows"},
    "amount": {"posted_balance_sum", "held_amount_sum", "journal_delta_sum", "hold_amount_sum", "captured_amount_sum"},
}
try:
    buckets = {kind: {} for kind in expected}
    fingerprints = {table: {"hash": hashlib.sha256(("laiai-funds-v1\0" + table + "\0").encode()), "rows": 0}
                    for table in ("ledger_accounts", "ledger_journal", "ledger_holds")}
    versions = []
    for line in pathlib.Path(source).open(encoding="utf-8"):
        parts = line.rstrip("\n").split("\t")
        if len(parts) != 3:
            raise ValueError("malformed row")
        kind, label, value = parts
        if kind == "fund":
            if label not in fingerprints or not re.fullmatch(r"[a-f0-9]{64}", value):
                raise ValueError("malformed row fingerprint")
            fingerprints[label]["hash"].update((value + "\n").encode("ascii"))
            fingerprints[label]["rows"] += 1
            continue
        if kind == "schema":
            if value or not re.fullmatch(r"[0-9]{4}_[A-Za-z0-9_]+", label) or label in versions:
                raise ValueError("malformed schema version")
            versions.append(label)
            continue
        if kind not in expected or label not in expected[kind] or label in buckets[kind]:
            raise ValueError("unexpected or duplicate metric")
        if kind == "amount":
            if not re.fullmatch(r"-?[0-9]+\.[0-9]{8}", value):
                raise ValueError("malformed decimal")
            buckets[kind][label] = value  # Keep exact fixed-point values, never float.
        else:
            if not re.fullmatch(r"[0-9]+", value):
                raise ValueError("malformed count")
            buckets[kind][label] = int(value)
            if kind == "presence" and int(value) not in (0, 1):
                raise ValueError("malformed table presence")
    if not versions or versions != sorted(versions):
        raise ValueError("missing or unordered applied migrations")
    if any(set(buckets[kind]) != labels for kind, labels in expected.items()):
        raise ValueError("incomplete check result")
    if any(item["rows"] != buckets["snapshot"][table + "_rows"] for table, item in fingerprints.items()):
        raise ValueError("incomplete funds fingerprint")
    failures = [name for name, count in buckets["check"].items() if count]
    if phase != "preflight":
        failures += [name for name, count in buckets["drain"].items() if count]
    report = {
        "schema_version": 1, "phase": phase,
        "checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "passed": not failures, "blocking_checks": sorted(failures),
        "checks": buckets["check"], "warnings": buckets["warning"],
        "in_flight": buckets["drain"], "optional_tables": buckets["presence"],
        "snapshot": {**buckets["snapshot"], **buckets["amount"]},
        "funds_fingerprint": {table: {"rows": item["rows"], "sha256": item["hash"].hexdigest()}
                              for table, item in fingerprints.items()},
        "migrations": versions,
    }
    pathlib.Path(output).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    pathlib.Path(schema_output).write_text("\n".join(versions) + "\n", encoding="utf-8")
except (OSError, UnicodeError, ValueError, OverflowError):
    print("database check output is incomplete or malformed; no gate passed", file=sys.stderr)
    sys.exit(2)
sys.exit(3 if failures else 0)
PY
  if [[ "$rc" != 0 && "$rc" != 3 ]]; then
    die "database verification could not complete at $phase"
  fi
  # Rename within RUN_DIR so interrupted checks cannot publish a partial result.
  # Preserve a failed well-formed report for diagnosis, but return nonzero.
  install -m 600 "$scratch/schema.txt" "$RUN_DIR/schema-${phase}.txt"
  install -m 600 "$scratch/schema.txt" "$RUN_DIR/schema.txt"
  mv -f -- "$scratch/report.json" "$RUN_DIR/data-${phase}.json"
  if [[ "$rc" == 3 ]]; then
    die "database gate failed at $phase; inspect data-${phase}.json aggregate checks"
  fi
  log "database checks passed: $phase (eight invariants; orphan credentials reported separately)"
)
