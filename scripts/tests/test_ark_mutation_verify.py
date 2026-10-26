"""Independent artifact verifier rejection tests, not real handoff evidence."""
from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    'a2c_artifact_verifier', Path(__file__).resolve().parents[1] / 'lib' / 'ark_mutation_verify.py')
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class ArtifactVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.case = Path(self.temp.name)
        self.process = self.case / 'processes' / 'consumer-ca'
        self.process.mkdir(parents=True)
        self.report = self.process / 'process.json'
        self.run_id = '0123456789abcdef'
        self.case_id = 'N3-cancel-Ca'
        self.binaries = {'Ca': 'a' * 64, 'P': 'b' * 64}
        self.value = dict(protocol='a2c-process-v1', run_id=self.run_id, case_id=self.case_id,
                          instance='consumer-ca', role='Ca', mode='worker', pid=123,
                          source_sha=verify.CA, binary_sha256=self.binaries['Ca'], returncode=0,
                          result='passed', test_run=True, test_pass=True, test_bad=False,
                          event_count=2, error_code=None)
        self.events = [dict(event=event, seq=index, run_id=self.run_id, case_id=self.case_id,
                            instance='consumer-ca', pid=123, role='Ca', data={})
                       for index, event in enumerate(('ready', 'stopped'), 1)]
        self.converted = [dict(Action=action, Test=verify.TEST) for action in ('run', 'pass')]
        self.write()

    def write(self):
        for name, data in {
            'stdout.log': b'synthetic unit fixture\n', 'stderr.log': b'',
            'test2json.stderr.log': b'',
            'test2json.jsonl': ''.join(json.dumps(row) + '\n' for row in self.converted).encode(),
            'events.jsonl': ''.join(json.dumps(row) + '\n' for row in self.events).encode(),
        }.items():
            (self.process / name).write_bytes(data)
        self.value['artifact_sha256'] = {
            path.name: hashlib.sha256(path.read_bytes()).hexdigest()
            for path in self.process.iterdir() if path.name != 'process.json'
        }
        self.report.write_text(json.dumps(self.value), encoding='utf-8')

    def read(self):
        return verify.process(self.case, self.report, self.case_id, self.run_id, self.binaries)

    def test_complete_normal_process(self):
        self.assertFalse(self.read())

    def test_actual_skip_cannot_be_hidden_by_summary(self):
        self.converted.insert(1, dict(Action='skip', Test=verify.TEST))
        self.write()
        with self.assertRaisesRegex(ValueError, 'actual_test_failed_or_skipped'):
            self.read()

    def test_summary_cannot_invent_actual_run(self):
        self.converted = [dict(Action='pass', Test=verify.TEST)]
        self.write()
        with self.assertRaisesRegex(ValueError, 'actual_test_run_missing'):
            self.read()

    def test_missing_pass_is_rejected(self):
        self.converted = [dict(Action='run', Test=verify.TEST)]
        self.write()
        with self.assertRaisesRegex(ValueError, 'actual_test_pass_missing'):
            self.read()

    def test_different_binary_is_rejected(self):
        self.value['binary_sha256'] = 'c' * 64
        self.write()
        with self.assertRaisesRegex(ValueError, 'process_binary_mismatch'):
            self.read()

    def test_modified_log_is_rejected(self):
        (self.process / 'stdout.log').write_bytes(b'changed after report')
        with self.assertRaisesRegex(ValueError, 'process_artifact_hash_mismatch'):
            self.read()

    def test_nonzero_exit_cannot_claim_pass(self):
        self.value['returncode'] = 1
        self.write()
        with self.assertRaisesRegex(ValueError, 'normal_process_not_passed'):
            self.read()

    def test_expected_kill_cannot_claim_actual_pass(self):
        self.value.update(returncode=-9, result='expected_crash_verified', test_pass=False)
        self.write()
        with self.assertRaisesRegex(ValueError, 'crash_claimed_test_pass'):
            self.read()

    def test_process_identity_is_not_transferable(self):
        self.events[-1]['case_id'] = 'N3-delete-Ca'
        self.write()
        with self.assertRaisesRegex(ValueError, 'process_event_mismatch'):
            self.read()

    def test_path_escape_is_rejected(self):
        for value in ('../process.json', '/tmp/process.json', 'x\\process.json', 'C:/process.json'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                verify.child(self.case, value)


if __name__ == '__main__':
    unittest.main()
