"""Runner boundary/oracle tests, NOT evidence of any two-binary scenario.

No test starts Docker, a model provider, or a video business process. The CLI
tests replace only costly orchestration to exercise reporting/cleanup; actual
Ca/P handoff is proved separately by the real isolated MySQL runner.
"""
from __future__ import annotations

import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


LIB = Path(__file__).resolve().parents[1] / "lib"
sys.path.insert(0, str(LIB))
spec = importlib.util.spec_from_file_location("a2c_compat_under_test", LIB / "ark_mutation_compat.py")
rc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rc)

FIXED_CA = "64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89"
FIXED_P = "24da28583eddfaad151b9a4a054264a8275b61bb"
FIXED_FINANCIAL_KEYS = (
    "accounts", "holds", "journals", "ledger_movements", "ledger_operations",
    "ledger_events", "billing_entries", "quota_reservations", "quota_buckets",
    "key_usage", "financial_all",
)
INDEPENDENT_CASE_IDS = {
    "L1-cancel", "L1-delete", "L2-cancel-Poff", "L2-cancel-Pon", "L2-delete-Poff", "L2-delete-Pon",
    "N1-cancel-Ca", "N1-cancel-Poff", "N1-delete-Ca", "N1-delete-Poff", "N1-wall-cancel-Ca",
    "N2-cancel-Ca", "N2-cancel-Poff", "N2-delete-Ca", "N2-delete-Poff",
    "N3-cancel-Ca", "N3-cancel-Poff", "N3-delete-Ca", "N3-delete-Poff", "N3-delete-404-Ca",
    "N3-cancel-both-flags-Ca", "N4-cancel-502-Ca", "N4-delete-EOF-Poff",
    "R1-cancel-Ca", "R1-delete-Ca", "R2-cancel-Ca", "R2-delete-Ca",
    "M1-cancel", "M1-delete", "M2-cancel", "M2-delete",
    "M3-cancel-1", "M3-cancel-2", "M3-cancel-3", "M3-delete-1", "M3-delete-2", "M3-delete-3",
    "Q1-cancel-succeeded-first", "Q1-cancel-cancelled-first",
}


def financial_snapshot(terminal=None):
    # Literal independent oracle: 1280*720*24*5/1024 = 108000, at $8/M =
    # $0.864 hold. 100 returned tokens cost $0.0008; the rest is released.
    values = {
        None: ("99.13600000", "0.86400000", "0.00000000", "0.00000000"),
        "succeeded": ("99.99920000", "0.00000000", "0.00080000", "0.86320000"),
        "cancelled": ("100.00000000", "0.00000000", "0.00000000", "0.86400000"),
    }
    available, frozen, captured, released = values[terminal]
    return {
        "finance": {"credit_usd": "100.00000000", "available_usd": available, "frozen_usd": frozen,
                    "captured_usd": captured, "released_usd": released, "ledger_drift_count": 0},
        "fingerprints": {key: hashlib.sha256(("independent:" + key).encode()).hexdigest()
                         for key in FIXED_FINANCIAL_KEYS},
    }


def action_snapshot(*, applied=False, history=True):
    value = financial_snapshot()
    value.update({
        "clock": "2026-10-26T12:00:00.000Z",
        "operation": {"status": "unknown" if applied else "in_flight", "attempt_id": "attempt-one",
                      "version": "2", "lease_until": "2026-10-26T12:03:00.000Z"},
        "task": {"status": "queued", "deleted_at": None},
        "mutation_attempt": {"count": 1, "id": "attempt-one", "storage_protocol": "inbox-actions-v1",
                             "input_schema": 2, "observation_schema": 1, "delivery_schema": 0,
                             "action": "cancel_task", "target": "synthetic-upstream"},
        "inbox": {"count": 1, "attempt_id": "attempt-one", "response_received_count": 1,
                  "response_payload_ref": "payload-one", "outcome": "accepted", "http_status": 204,
                  "evidence_hmac_sha256": hashlib.sha256(b"immutable-response-evidence").hexdigest(),
                  "application_state": "applied" if applied else "pending",
                  "applied_at": "2026-10-26T12:04:00.000Z" if applied else None,
                  "applied_event_id": "event-one" if applied else "", "receipt_id": "receipt-one"},
        "legacy": {"attempt_count": 0, "receipt_present": False},
        "action_input": {"capture_status": "staged", "wrapped_key_present": True,
                         "nonce_present": True, "staging_count": 1},
        "audits": {"response_capture_status": "staged" if history else "disabled",
                   "response_staging_count": 1 if history else 0},
    })
    value["fingerprints"]["action_application_events"] = hashlib.sha256(b"real-action-events").hexdigest()
    return value


def oracle_case(*, history=True):
    case = object.__new__(rc.Case)
    case.action = "cancel"
    case.seed = {"mutation_kind": "cancel_task", "history_enabled": history}
    case.mock = SimpleNamespace(upstream_id="synthetic-upstream", status=lambda: {"delete": 1})
    case.baseline = financial_snapshot()
    return case


class OracleTests(unittest.TestCase):
    def test_fixed_revisions_inventory_and_financial_scope(self):
        self.assertEqual((rc.CA, rc.P), (FIXED_CA, FIXED_P))
        cases = rc.case_definitions()
        self.assertEqual(len(cases), 39)
        self.assertEqual({item["id"] for item in cases}, INDEPENDENT_CASE_IDS)
        self.assertEqual(tuple(rc.FINANCIAL_KEYS), FIXED_FINANCIAL_KEYS)
        self.assertEqual({c["consumer"] for c in cases if c["family"] in ("N1", "N2")}, {"Ca", "Poff"})

    def test_independent_exact_money_oracles_and_invalid_types(self):
        for terminal in (None, "succeeded", "cancelled"):
            with self.subTest(terminal=terminal):
                rc.validate_finance(financial_snapshot(terminal), terminal=terminal)
                for name in ("credit_usd", "available_usd", "frozen_usd", "captured_usd", "released_usd"):
                    wrong = financial_snapshot(terminal)
                    # Change the exact smallest ledger unit, not a rounded float.
                    original = wrong["finance"][name]
                    whole, fraction = original.split(".")
                    wrong["finance"][name] = whole + "." + str(int(fraction) + 1).zfill(8)
                    with self.assertRaisesRegex(rc.Failure, "finance_oracle_mismatch"):
                        rc.validate_finance(wrong, terminal=terminal)
        for value in (100, 100.0, True, None, "NaN", "Infinity", "-Infinity", "not-money"):
            with self.subTest(value=value):
                wrong = financial_snapshot()
                wrong["finance"]["credit_usd"] = value
                with self.assertRaises(rc.Failure):
                    rc.validate_finance(wrong)

    def test_no_missing_financial_partition_or_one_unit_drift(self):
        for key in FIXED_FINANCIAL_KEYS:
            with self.subTest(key=key):
                before, after = financial_snapshot(), financial_snapshot()
                after["fingerprints"][key] = "f" * 64
                with self.assertRaisesRegex(rc.Failure, "finance_fingerprint_changed"):
                    rc.same_finance(before, after)
                del after["fingerprints"][key]
                with self.assertRaisesRegex(rc.Failure, "finance_fingerprint_missing"):
                    rc.validate_finance(after)
        bad = financial_snapshot()
        bad["finance"]["ledger_drift_count"] = 1
        with self.assertRaisesRegex(rc.Failure, "ledger_drift"):
            rc.validate_finance(bad)

    def test_registered_contract_target_receipt_and_mixed_legacy_rejected(self):
        case = oracle_case()
        valid = action_snapshot()
        case.registered(valid, 1)
        mutations = (
            ("mutation_attempt", "count", 0), ("mutation_attempt", "storage_protocol", "inbox-v1"),
            ("mutation_attempt", "input_schema", 1), ("mutation_attempt", "observation_schema", 2),
            ("mutation_attempt", "delivery_schema", 1), ("mutation_attempt", "action", "delete_task"),
            ("mutation_attempt", "target", "other-upstream"), ("inbox", "count", 2),
            ("inbox", "attempt_id", "other-attempt"), ("inbox", "response_received_count", 2),
            ("legacy", "attempt_count", 1), ("legacy", "receipt_present", True),
        )
        for section, field, value in mutations:
            with self.subTest(section=section, field=field):
                bad = copy.deepcopy(valid)
                bad[section][field] = value
                with self.assertRaises(rc.Failure):
                    case.registered(bad, 1)

    def test_pending_requires_complete_durable_input_and_capture_policy(self):
        for history in (True, False):
            case = oracle_case(history=history)
            valid = action_snapshot(history=history)
            case.check_pending(valid, 1)
            variants = (
                ("action_input", "capture_status", "expired"), ("action_input", "wrapped_key_present", False),
                ("action_input", "nonce_present", False), ("action_input", "staging_count", 0),
                ("operation", "status", "unknown"), ("operation", "lease_until", "2026-10-26T11:59:59.999Z"),
                ("inbox", "application_state", "applied"), ("inbox", "applied_at", "2026-10-26T12:00:00.000Z"),
                ("audits", "response_capture_status", "disabled" if history else "staged"),
                ("audits", "response_staging_count", 0 if history else 1),
            )
            for section, field, value in variants:
                with self.subTest(history=history, field=field):
                    bad = copy.deepcopy(valid)
                    bad[section][field] = value
                    with self.assertRaises(rc.Failure):
                        case.check_pending(bad, 1)

    def test_applied_requires_receipt_event_and_action_fact_without_money_change(self):
        case = oracle_case()
        good = action_snapshot(applied=True)
        case.expected_action(good)
        variants = (("inbox", "applied_event_id", ""), ("inbox", "application_state", "pending"),
                    ("operation", "status", "confirmed"), ("task", "status", "cancelled"),
                    ("task", "deleted_at", "2026-10-26T12:00:00.000Z"),
                    ("fingerprints", "financial_all", "f" * 64))
        for section, field, value in variants:
            with self.subTest(field=field):
                bad = copy.deepcopy(good)
                bad[section][field] = value
                with self.assertRaises(rc.Failure):
                    case.expected_action(bad)

    def test_replay_rejects_event_receipt_attempt_or_additional_delete(self):
        # Exercise the real replay assertion loop with canned inspector facts;
        # this is an oracle unit test, not a fake business recovery.
        stable = action_snapshot(applied=True)
        variants = (("fingerprints", "action_application_events", "f" * 64),
                    ("mutation_attempt", "id", "replacement-attempt"),
                    ("inbox", "receipt_id", "replacement-receipt"),
                    ("legacy", "receipt_present", True), ("operation", "status", "confirmed"))
        for section, field, value in variants:
            with self.subTest(field=field):
                case = oracle_case()
                bad = copy.deepcopy(stable)
                bad[section][field] = value
                case.start = mock.Mock(return_value=SimpleNamespace(stop=mock.Mock()))
                case.cycle = mock.Mock()
                case.inspect = mock.Mock(return_value=bad)
                with self.assertRaises(rc.Failure):
                    case.replay(stable)
        case = oracle_case()
        children = [SimpleNamespace(stop=mock.Mock()), SimpleNamespace(stop=mock.Mock())]
        case.start = mock.Mock(side_effect=children)
        case.cycle = mock.Mock()
        case.inspect = mock.Mock(return_value=copy.deepcopy(stable))
        case.replay(stable)
        self.assertEqual([call.args[0] for call in case.start.call_args_list], ["Ca", "Poff"])
        self.assertEqual(case.cycle.call_count, 4)
        for child in children:
            child.stop.assert_called_once_with()
        case.start = mock.Mock(return_value=SimpleNamespace(stop=mock.Mock()))
        case.mock.status = lambda: {"delete": 2}
        with self.assertRaisesRegex(rc.Failure, "delete_repeated_on_replay"):
            case.replay(stable)

    def test_immutable_evidence_allows_application_progress_but_not_replacement(self):
        before = action_snapshot()
        after = action_snapshot(applied=True)
        # Application bookkeeping can progress while the registered execution
        # and observed response remain byte-for-byte the same evidence.
        after["inbox"].update({"next_attempt_at": None, "application_error": ""})
        rc.same_mutation_evidence(before, after)
        immutable_changes = (
            ("count", 2), ("receipt_id", "replacement-receipt"), ("attempt_id", "replacement-attempt"),
            ("response_payload_ref", "replacement-payload"), ("outcome", "unknown"), ("http_status", 502),
            ("response_received_count", 2), ("evidence_hmac_sha256", "f" * 64),
        )
        for field, replacement in immutable_changes:
            with self.subTest(section="inbox", field=field):
                changed = copy.deepcopy(after)
                changed["inbox"][field] = replacement
                with self.assertRaisesRegex(rc.Failure, "immutable_response_changed"):
                    rc.same_mutation_evidence(before, changed)
        for field, replacement in (("id", "replacement-attempt"), ("target", "replacement-target"),
                                   ("input_schema", 1), ("storage_protocol", "inbox-v1")):
            with self.subTest(section="mutation_attempt", field=field):
                changed = copy.deepcopy(after)
                changed["mutation_attempt"][field] = replacement
                with self.assertRaisesRegex(rc.Failure, "mutation_registration_changed"):
                    rc.same_mutation_evidence(before, changed)


def archive(entries):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w") as tar:
        for name, data in entries.items():
            item = tarfile.TarInfo(name)
            if data is None:
                item.type = tarfile.SYMTYPE
                item.linkname = "/outside"
                tar.addfile(item)
            else:
                item.size = len(data)
                item.mode = 0o644
                tar.addfile(item, io.BytesIO(data))
    return out.getvalue()


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "checkout"
        self.output = Path(self.temp.name) / "output"
        self.output.mkdir()
        harness = self.root / "core/internal/video/mutation_handoff_test.go"
        harness.parent.mkdir(parents=True)
        harness.write_bytes(b"synthetic identical harness\n")
        # This dirty production content must never enter the fixed archive.
        (self.root / "core/internal/video/worker.go").write_bytes(b"dirty local production")
        self.entries = {"core/internal/video/worker.go": b"fixed production", "migrations/0001.sql": b"fixed ddl"}

    def run_sources(self, old=None, new=None):
        archives = {FIXED_CA: archive(self.entries if old is None else old),
                    FIXED_P: archive(self.entries if new is None else new)}
        commands = []
        def run(argv, **kwargs):
            commands.append(argv)
            if "archive" in argv:
                return archives[argv[-1]]
            self.assertTrue("cat-file" in argv or "merge-base" in argv)
            return b""
        with mock.patch.object(rc, "ROOT", self.root), mock.patch.object(rc, "run_command", side_effect=run):
            result = rc.snapshot_sources(self.output, {})
        return result, commands

    def test_archive_fixed_sources_only_identical_harness_overlay_and_manifest(self):
        result, commands = self.run_sources()
        self.assertEqual([cmd[-1] for cmd in commands if "archive" in cmd], [FIXED_CA, FIXED_P])
        for role, expected in (("Ca", FIXED_CA), ("P", FIXED_P)):
            self.assertEqual(result[role]["sha"], expected)
            source = Path(result[role]["source"])
            self.assertEqual((source / "core/internal/video/worker.go").read_bytes(), b"fixed production")
            before = json.loads((self.output / (role + "-source-manifest.json")).read_text())
            after = json.loads((self.output / (role + "-overlay-manifest.json")).read_text())
            self.assertEqual(set(after) - set(before), {"core/internal/video/mutation_handoff_test.go"})
            self.assertEqual(before["migrations/0001.sql"], hashlib.sha256(b"fixed ddl").hexdigest())
            self.assertEqual(before["core/internal/video/worker.go"], after["core/internal/video/worker.go"])

    def test_archive_traversal_symlink_and_changed_historical_migration_rejected(self):
        for entries, code in (({"../outside": b"bad"}, "unsafe_archive_path"),
                              ({"link": None}, "unsafe_archive_entry")):
            with self.subTest(code=code):
                # Each failed extraction has a separate owned output directory.
                self.output = Path(self.temp.name) / code
                self.output.mkdir()
                with self.assertRaisesRegex(rc.Failure, code):
                    self.run_sources(old=entries)
                self.assertFalse((Path(self.temp.name) / "outside").exists())
        self.output = Path(self.temp.name) / "changed-migration"
        self.output.mkdir()
        changed = dict(self.entries, **{"migrations/0001.sql": b"changed ddl"})
        with self.assertRaisesRegex(rc.Failure, "historical_migration_changed"):
            self.run_sources(new=changed)

    def test_unfixed_revision_rejected_before_git(self):
        with mock.patch.object(rc, "P", "main"), mock.patch.object(rc, "run_command") as command:
            with self.assertRaisesRegex(rc.Failure, "revision_not_fixed"):
                rc.snapshot_sources(self.output, {})
            command.assert_not_called()

    def test_actual_source_symlink_rejected(self):
        target = self.root / "regular"
        target.write_bytes(b"fixed")
        try:
            (self.root / "alias").symlink_to(target)
        except OSError:
            # Windows without CreateSymbolicLink privilege cannot perform this
            # actual filesystem negative. Linux CI runs it, never a fake link.
            self.skipTest("host lacks filesystem symlink creation privilege; Linux CI must exercise this case")
        with self.assertRaisesRegex(rc.Failure, "source_symlink_rejected"):
            rc.file_manifest(self.root)


class _POSIXEnvironment:
    """Expose a Linux CLI gate without changing pathlib's host platform."""
    name = "posix"
    def __init__(self, values):
        self.environ = values
    def __getattr__(self, name):
        return getattr(os, name)


class CLIBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.output = Path(self.temp.name) / "new-run"
        previous = rc.RUN_DEADLINE
        self.addCleanup(setattr, rc, "RUN_DEADLINE", previous)

    def test_external_dsn_environment_and_unknown_cli_dsn_rejected_before_build(self):
        for key in ("LAIAI_DSN", "ARK_TEST_MYSQL_DSN", "A2C_DSN"):
            with self.subTest(key=key), mock.patch.object(rc, "os", _POSIXEnvironment({key: "synthetic-external-dsn"})), \
                    mock.patch.object(rc, "snapshot_sources") as source:
                with self.assertRaisesRegex(rc.Failure, "external_business_environment_rejected"):
                    rc.main(["--output", str(self.output), "--case", "N3-cancel-Ca"])
                source.assert_not_called()
                self.assertFalse(self.output.exists())
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as exit_info:
            rc.main(["--output", str(self.output), "--dsn", "synthetic-external-dsn"])
        self.assertEqual(exit_info.exception.code, 2)

    def test_existing_path_duplicate_case_and_mismatched_definition_cannot_run(self):
        with mock.patch.object(rc, "os", _POSIXEnvironment({})), mock.patch.object(rc, "snapshot_sources") as source:
            self.output.mkdir()
            with self.assertRaisesRegex(rc.Failure, "output_must_be_new"):
                rc.main(["--output", str(self.output), "--case", "N3-cancel-Ca"])
            with self.assertRaisesRegex(rc.Failure, "duplicate_selected_case"):
                rc.main(["--output", str(self.output), "--case", "N3-cancel-Ca", "--case", "N3-cancel-Ca"])
            source.assert_not_called()
        definition = next(item for item in rc.case_definitions() if item["id"] == "N3-cancel-Ca")
        invalid = (
            SimpleNamespace(id="unknown-case", definition=dict(definition, id="unknown-case")),
            SimpleNamespace(id=definition["id"], definition=dict(definition, action="delete")),
        )
        with mock.patch.object(rc, "scenario_n3") as scenario:
            for case in invalid:
                with self.subTest(case=case.id), self.assertRaisesRegex(rc.Failure, "scenario_definition_mismatch"):
                    rc.dispatch(case)
            scenario.assert_not_called()

    def test_go_build_environment_drops_overlay_and_business_secrets(self):
        values = {"PATH": "local-tools", "GOMODCACHE": "cache", "GOFLAGS": "-overlay=bad.json",
                  "GOWORK": "other.work", "GOENV": "custom", "LAIAI_DSN": "private", "A2C_TOKEN": "private"}
        with mock.patch.object(rc, "os", _POSIXEnvironment(values)):
            clean = rc.clean_environment()
        self.assertEqual(clean["GOENV"], "off")
        self.assertEqual(clean["GOTOOLCHAIN"], "local")
        self.assertEqual(clean["GOMODCACHE"], "cache")
        for key in ("GOFLAGS", "GOWORK", "LAIAI_DSN", "A2C_TOKEN"):
            self.assertNotIn(key, clean)

    def report_run(self, *, close_case=False, close_db=False, selected=True):
        db = SimpleNamespace(start=mock.Mock(), close=mock.Mock(side_effect=rc.Failure("cleanup") if close_db else None))
        case = SimpleNamespace(prepare=mock.Mock(), close=mock.Mock(side_effect=rc.Failure("case_cleanup_failed") if close_case else None))
        argv = ["--output", str(self.output)]
        if selected:
            argv += ["--case", "N3-cancel-Ca"]
        out = io.StringIO()
        with mock.patch.object(rc, "os", _POSIXEnvironment({})), \
                mock.patch.object(rc, "snapshot_sources", return_value={}), \
                mock.patch.object(rc, "build_binaries", return_value=({}, "go1.26.10")), \
                mock.patch.object(rc, "OwnedMySQL", return_value=db), \
                mock.patch.object(rc, "Case", return_value=case), \
                mock.patch.object(rc, "dispatch", side_effect=None if selected else rc.Failure("synthetic_scenario_failure")), \
                contextlib.redirect_stdout(out):
            exit_code = rc.main(argv)
        return exit_code, json.loads((self.output / "report.json").read_text()), out.getvalue(), db, case

    def test_partial_report_cannot_claim_39_pass_or_full_success_marker(self):
        code, report, output, db, case = self.report_run()
        self.assertEqual(code, 0)
        self.assertFalse(report["passed"])
        self.assertFalse(report["full_matrix"])
        self.assertTrue(report["partial_verified"])
        self.assertEqual(report["expected_cases"], 39)
        self.assertEqual([item["id"] for item in report["completed_cases"]], ["N3-cancel-Ca"])
        self.assertIn("A2C_PARTIAL_VERIFIED", output)
        self.assertNotIn("A2C_FULL_MATRIX_PASSED", output)
        db.close.assert_called_once_with()
        case.close.assert_called_once_with(success=True)

    def test_scenario_failure_prevents_full_pass(self):
        code, report, output, _, case = self.report_run(selected=False)
        self.assertEqual(code, 1)
        self.assertFalse(report["passed"])
        self.assertFalse(report["partial_verified"])
        self.assertTrue(report["full_matrix"])
        self.assertEqual(report["completed_cases"], [])
        self.assertEqual(report["error_code"], "synthetic_scenario_failure")
        self.assertNotIn("A2C_FULL_MATRIX_PASSED", output)
        case.close.assert_called_once_with(success=False)

    def test_case_cleanup_failure_prevents_case_and_run_pass(self):
        code, report, output, db, _ = self.report_run(close_case=True)
        self.assertEqual(code, 1)
        self.assertEqual(report["completed_cases"], [])
        self.assertFalse(report["partial_verified"])
        self.assertFalse(report["passed"])
        self.assertEqual(report["error_code"], "case_cleanup_failed")
        self.assertNotIn("A2C_CASE_VERIFIED", output)
        db.close.assert_called_once_with()

    def test_database_cleanup_failure_overrides_successful_case(self):
        code, report, output, _, _ = self.report_run(close_db=True)
        self.assertEqual(code, 1)
        self.assertEqual(len(report["completed_cases"]), 1)
        self.assertFalse(report["cleanup_passed"])
        self.assertFalse(report["partial_verified"])
        self.assertFalse(report["passed"])
        self.assertEqual(report["error_code"], "database_cleanup_failed")
        self.assertNotIn("A2C_PARTIAL_VERIFIED", output)

    def test_mysql_cleanup_requires_exact_owned_label_and_available_daemon(self):
        db = rc.OwnedMySQL(Path(self.temp.name), "0123456789abcdef", {})
        db.attempted = True
        db.env_file.write_text("synthetic-fixture-only")
        row = [{"Config": {"Labels": {rc.OWNER_LABEL: "other-owner"}}}]
        inspect = subprocess.CompletedProcess([], 0, json.dumps(row).encode(), b"")
        with mock.patch.object(rc.subprocess, "run", return_value=inspect), mock.patch.object(rc, "run_command") as command:
            with self.assertRaisesRegex(rc.Failure, "cleanup_owner_mismatch"):
                db.close()
            command.assert_not_called()
        self.assertFalse(db.removed)
        self.assertFalse(db.env_file.exists())
        missing = subprocess.CompletedProcess([], 1, b"", b"")
        with mock.patch.object(rc.subprocess, "run", return_value=missing), \
                mock.patch.object(rc, "run_command", side_effect=rc.Failure("daemon_unavailable")):
            with self.assertRaisesRegex(rc.Failure, "daemon_unavailable"):
                db.close()
        self.assertFalse(db.removed)


if __name__ == "__main__":
    unittest.main(verbosity=2)
