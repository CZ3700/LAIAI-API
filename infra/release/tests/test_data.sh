#!/usr/bin/env bash
# No Docker daemon, database, upstream request, or production access is used.
set -Eeuo pipefail
umask 077
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
SCRATCH=$(mktemp -d)
trap 'rm -rf -- "$SCRATCH"' EXIT
mkdir -p "$SCRATCH/bin" "$SCRATCH/run"
cat > "$SCRATCH/bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == exec && "$2" == -i && "$3" == fixture-mysql && "$4" == sh && "$5" == -c ]]
[[ "$6" == *'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql'* ]]
cat > "$SQL_CAPTURE"
if [[ "${DOCKER_FAIL:-0}" == 1 ]]; then
  printf '%s\n' 'SECRET_DATABASE_ERROR_MUST_NOT_LEAK' >&2
  exit 42
fi
cat "$DATA_FIXTURE"
SH
chmod +x "$SCRATCH/bin/docker"
export PATH="$SCRATCH/bin:$PATH"
export SQL_CAPTURE="$SCRATCH/sql.txt" DATA_FIXTURE="$SCRATCH/base.tsv"
python3 - "$DATA_FIXTURE" <<'PY'
import pathlib, sys
rows = []
for check in ("ledger_posted_vs_journal", "ledger_held_vs_active_holds", "journal_missing_account",
              "hold_missing_account", "invalid_hold_state_or_amount", "task_missing_hold",
              "task_hold_cross_org", "membership_missing_org_or_user"):
    rows.append(("check", check, "0"))
rows += [("warning", "channel_credential_missing_parent", "8")]
rows += [("drain", key, "0") for key in ("active_holds", "nonterminal_tasks", "unresolved_video_operations",
                                         "unsettled_video_billing", "active_video_reservations")]
rows += [("presence", key, "0") for key in ("video_operations", "video_task_states", "video_quota_reservations")]
rows += [("snapshot", key + "_rows", "1") for key in ("ledger_accounts", "ledger_journal", "ledger_holds", "tasks")]
rows += [("amount", key, "0.00000000") for key in ("posted_balance_sum", "held_amount_sum", "journal_delta_sum",
                                                  "hold_amount_sum", "captured_amount_sum")]
rows += [("fund", key, "a" * 64) for key in ("ledger_accounts", "ledger_journal", "ledger_holds")]
rows += [("schema", "0001_identity", ""), ("schema", "0048_model_price_per_call", "")]
pathlib.Path(sys.argv[1]).write_text("".join("\t".join(row) + "\n" for row in rows))
PY
export MYSQL_CID=fixture-mysql RUN_DIR="$SCRATCH/run"
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }
source "$HERE/../check-data.sh"

check_data preflight > "$SCRATCH/result.log" 2>&1
check_data drained >> "$SCRATCH/result.log" 2>&1
check_data post-migration >> "$SCRATCH/result.log" 2>&1
check_data post-backfill >> "$SCRATCH/result.log" 2>&1
python3 - "$RUN_DIR" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
a = json.loads((p / "data-drained.json").read_text())
assert a["passed"] and a["warnings"]["channel_credential_missing_parent"] == 8
assert set(a["optional_tables"].values()) == {0}
assert all(v["rows"] == 1 and len(v["sha256"]) == 64 for v in a["funds_fingerprint"].values())
assert (p / "schema.txt").read_text().splitlines() == a["migrations"]
assert all(v == "0.00000000" for k,v in a["snapshot"].items() if k.endswith("_sum"))
PY
grep -q 'START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY' "$SQL_CAPTURE"
if grep -Eiq '^[[:space:]]*(CREATE|ALTER|DROP|INSERT|UPDATE|DELETE|REPLACE|TRUNCATE)[[:space:]]' "$SQL_CAPTURE"; then
  die 'data query must not mutate the database'
fi

mutate_fixture() {
  python3 - "$SCRATCH/base.tsv" "$SCRATCH/changed.tsv" "$@" <<'PY'
import pathlib, sys
base, target, mode, *args = sys.argv[1:]
text = pathlib.Path(base).read_text()
if mode == "metric":
    kind, label, value = args
    lines = text.splitlines()
    text = "\n".join("\t".join((kind, label, value)) if line.startswith(kind + "\t" + label + "\t") else line for line in lines) + "\n"
elif mode == "missing":
    text = "\n".join(line for line in text.splitlines() if not line.startswith(args[0] + "\t")) + "\n"
elif mode == "duplicate":
    text += text.splitlines()[0] + "\n"
pathlib.Path(target).write_text(text)
PY
  export DATA_FIXTURE="$SCRATCH/changed.tsv"
}
expect_reject() {
  local phase=$1
  if (check_data "$phase") > "$SCRATCH/rejected.log" 2>&1; then
    die "accepted an invalid fixture at $phase"
  fi
}
for check in ledger_posted_vs_journal ledger_held_vs_active_holds journal_missing_account \
  hold_missing_account invalid_hold_state_or_amount task_missing_hold task_hold_cross_org membership_missing_org_or_user; do
  mutate_fixture metric check "$check" 1
  expect_reject preflight
done
for outstanding in active_holds nonterminal_tasks unresolved_video_operations unsettled_video_billing active_video_reservations; do
  mutate_fixture metric drain "$outstanding" 1
  check_data preflight >> "$SCRATCH/result.log" 2>&1
  expect_reject drained
  expect_reject post-migration
  expect_reject post-backfill
done
mutate_fixture missing schema
expect_reject preflight
mutate_fixture missing fund
expect_reject preflight
mutate_fixture duplicate
expect_reject preflight
mutate_fixture metric amount posted_balance_sum 0.000000001
expect_reject preflight

# Equal aggregates must not hide a changed underlying financial fact row.
mutate_fixture metric fund ledger_journal "$(printf '%064d' 1)"
check_data preflight >> "$SCRATCH/result.log" 2>&1
python3 - "$RUN_DIR" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
a = json.loads((p / "data-drained.json").read_text())
b = json.loads((p / "data-preflight.json").read_text())
assert a["snapshot"] == b["snapshot"]
assert a["funds_fingerprint"] != b["funds_fingerprint"]
PY
export DOCKER_FAIL=1
expect_reject preflight
if grep -q SECRET_DATABASE_ERROR_MUST_NOT_LEAK "$SCRATCH/rejected.log"; then
  die 'database error leaked'
fi
printf '%s\n' 'PASS: data-check fixtures (all phases, eight invariants, drain blockers, legacy tables, malformed output, stable fingerprints, redacted failures)'
