#!/usr/bin/env python3
"""Pure release guard tests; no Docker daemon or production data."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


HELPER = Path(__file__).resolve().parents[1] / "carryover_0153.py"
SPEC = importlib.util.spec_from_file_location("carryover_0153", HELPER)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class Carryover0153Tests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="laiai-carryover-0153-")
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_duplicate_callback_blocks_manifest_before_migration(self):
        target = self.root / "columns.json"
        with patch.object(MODULE, "mysql_rows", return_value=["1"]):
            with self.assertRaisesRegex(MODULE.GuardError, "duplicate callback hints"):
                MODULE.prepare("fixture", target)
        self.assertFalse(target.exists())

    def test_manifest_requires_only_historical_columns(self):
        target = self.root / "columns.json"
        names = {"video_operations": ["id", "status"],
                 "video_callback_hints": ["id", "task_id", "body_excerpt"]}
        with patch.object(MODULE, "mysql_rows", side_effect=lambda _cid, sql:
                          ["0"] if "duplicated" in sql else names["video_operations"]
                          if "video_operations" in sql else names["video_callback_hints"]):
            MODULE.prepare("fixture", target)
        self.assertEqual(MODULE.load_manifest(target), names)
        if os.name != "nt":
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(MODULE.GuardError):
            MODULE.prepare("fixture", target)
        target.write_text(json.dumps({"version": 1, "columns": {
            **names, "video_operations": ["id", "next_dispatch_at"]}}))
        with self.assertRaises(MODULE.GuardError):
            MODULE.load_manifest(target)

    def test_projection_is_tagged_and_hex_encoded(self):
        query = MODULE.row_query("video_callback_hints", ["id", "body_excerpt"])
        self.assertIn("IF(`body_excerpt` IS NULL,'N;'", query)
        self.assertIn("HEX(CAST(`body_excerpt` AS BINARY))", query)
        self.assertTrue(query.endswith("ORDER BY `id`"))
        # The SQL representation distinguishes NULL, empty and arbitrary bytes.
        def cell(value):
            return "N;" if value is None else "V" + value.hex().upper() + ";"
        self.assertEqual(len({cell(None), cell(b""), cell(b"\x00\t\n"), cell(b"NULL")}), 4)

    def test_fingerprint_ignores_only_reviewed_new_columns(self):
        manifest = self.root / "columns.json"
        MODULE.save_private(manifest, {"version": 1, "columns": {
            "video_operations": ["id", "status"],
            "video_callback_hints": ["id", "body_excerpt"],
        }})
        state = {"expanded": False, "old_row": b"V31;V70656E64696E67;\n"}

        def column_names(_cid, table):
            old = MODULE.load_manifest(manifest)[table]
            return old + list(MODULE.CHANGED_TABLES[table]) if state["expanded"] else old

        def stream(command, digest):
            script = command[5]
            if "mysqldump" in script:
                digest.update(b"unchanged tables\n")
            elif "video_operations" in command[-1]:
                digest.update(state["old_row"])
            else:
                digest.update(b"V31;V63616C6C6261636B;\n")

        with patch.object(MODULE, "columns", side_effect=column_names), \
                patch.object(MODULE, "stream_hash", side_effect=stream):
            before = MODULE.fingerprint("fixture", manifest, "before")
            state["expanded"] = True
            self.assertEqual(before, MODULE.fingerprint("fixture", manifest, "after"))
            state["old_row"] = b"V31;V72756E6E696E67;\n"
            self.assertNotEqual(before, MODULE.fingerprint("fixture", manifest, "after"))
            state["expanded"] = False
            with self.assertRaisesRegex(MODULE.GuardError, "schema differs"):
                MODULE.fingerprint("fixture", manifest, "after")

    def test_new_feature_or_new_task_refuses_old_core(self):
        def zeros(_cid, sql):
            self.assertIn("video_user_task_limits", sql)
            self.assertIn("video_group_asset_projects", sql)
            self.assertIn("next_dispatch_at IS NOT NULL", sql)
            self.assertIn("provider = 'ark_official'", sql)
            self.assertIn("id > 10", sql)
            return ["\t".join("0" for _ in range(len(MODULE.FEATURE_SQL) + 1))]
        with patch.object(MODULE, "mysql_rows", side_effect=zeros):
            MODULE.require_unused("fixture", 10)
        with patch.object(MODULE, "mysql_rows", return_value=["\t".join(
                ["0"] * len(MODULE.FEATURE_SQL) + ["1"]) ]):
            with self.assertRaisesRegex(MODULE.GuardError, "new_video_tasks"):
                MODULE.require_unused("fixture", 10)
        with self.assertRaisesRegex(MODULE.GuardError, "invalid pre-start task"):
            MODULE.require_unused("fixture", "10; DELETE FROM tasks")

    def test_baseline_and_guard_use_database_time(self):
        target = self.root / "baseline.json"
        with patch.object(MODULE, "require_unused"), patch.object(MODULE, "mysql_rows",
                return_value=["10\t2026-11-10 06:00:00.123456"]), \
                patch.object(MODULE, "asset_id_digest", return_value="a" * 64):
            MODULE.baseline("fixture", target)
        self.assertEqual(json.loads(target.read_text())["task_max_id"], 10)
        with patch.object(MODULE, "require_unused") as unused:
            MODULE.guard("fixture", target)
        unused.assert_called_once_with("fixture", 10, {
            "video_asset_operations": "a" * 64, "video_assets": "a" * 64})

    def test_asset_identity_change_blocks_old_core(self):
        hashes = {"video_asset_operations": "a" * 64, "video_assets": "b" * 64}
        with patch.object(MODULE, "feature_counts", return_value={"new_video_tasks": 0}), \
                patch.object(MODULE, "asset_id_digest", side_effect=lambda _cid, table:
                             "c" * 64 if table == "video_assets" else "a" * 64):
            with self.assertRaisesRegex(MODULE.GuardError, "new_or_removed_video_assets"):
                MODULE.require_unused("fixture", 10, hashes)


if __name__ == "__main__":
    unittest.main()
