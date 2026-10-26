"""CI host negative tests. These do not count as any binary handoff scenario."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('mutation_ci_under_test', Path(__file__).resolve().parents[1] / 'lib/ark_mutation_ci.py')
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)


class BoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def test_external_environment_rejected_before_output_or_process(self):
        for key in ('LAIAI_DSN', 'ARK_TEST_MYSQL_DSN', 'A2C_PROVIDER_TOKEN'):
            with self.subTest(key=key), mock.patch.dict(os.environ, {key: 'synthetic-only'}, clear=True), \
                    mock.patch.object(ci.sys, 'platform', 'linux'), mock.patch.object(ci, 'run_phase') as phase:
                with self.assertRaisesRegex(ci.CIError, 'external_business_environment'):
                    ci.main(['--output', str(self.root / 'out')])
                phase.assert_not_called()
                self.assertFalse((self.root / 'out').exists())

    def test_partial_or_external_dsn_options_cannot_enter_full_ci(self):
        for extra in (['--case', 'N3-cancel-Ca'], ['--dsn', 'synthetic-only']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                ci.main(['--output', str(self.root / 'out'), *extra])
            self.assertEqual(error.exception.code, 2)

    def test_output_must_be_new(self):
        with self.assertRaisesRegex(ci.CIError, 'output_not_fresh'):
            ci.new_output(self.root)

    def test_environment_drops_docker_remote_go_overlays_and_git_injection(self):
        with mock.patch.dict(os.environ, {'PATH': 'tools', 'DOCKER_HOST': 'tcp://remote', 'GOFLAGS': '-overlay=bad',
                                         'GOENV': 'custom', 'GOWORK': 'other', 'GIT_CONFIG_COUNT': '1'}, clear=True):
            env = ci.clean_env()
        self.assertEqual(env['GOENV'], 'off')
        self.assertEqual(env['GOTOOLCHAIN'], 'local')
        for name in ('DOCKER_HOST', 'GOFLAGS', 'GOWORK', 'GIT_CONFIG_COUNT'):
            self.assertNotIn(name, env)

    def test_export_excludes_secrets_databases_sources_and_binaries(self):
        ci.private_json(self.root / 'ci-report.json', {'passed': False, 'incomplete': True})
        (self.root / 'run').mkdir()
        ci.private_json(self.root / 'run/report.json', {'passed': False, 'error_code': 'fixed-error'})
        for name in ('mysql.env', 'raw-inspect.json', 'Ca-video.test', 'database.sql'):
            (self.root / 'run' / name).write_text('must-not-upload')
        (self.root / 'run/Ca-source').mkdir()
        (self.root / 'run/Ca-source/secret.json').write_text('must-not-upload')
        ci.safe_export(self.root)
        manifest = json.loads((self.root / 'export/export-manifest.json').read_text())['files']
        self.assertEqual(set(manifest), {'ci-report.json', 'run/report.json'})
        self.assertFalse((self.root / 'export/run/mysql.env').exists())

    def test_secret_fields_and_raw_dsn_prevent_publishing_partial_export(self):
        for content in ({'password': 'synthetic-only'}, {'nested': {'authorization': 'secret'}},
                        {'message': 'root:x@tcp(127.0.0.1:3306)/private'}):
            with self.subTest(content=content), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                ci.private_json(root / 'ci-report.json', content)
                with self.assertRaises(ci.CIError):
                    ci.safe_export(root)
                self.assertFalse((root / 'export').exists())

    def test_export_bounds_and_case_allowlist(self):
        ci.private_json(self.root / 'ci-report.json', {'passed': False})
        with mock.patch.object(ci, 'MAX_EXPORT', 1), self.assertRaisesRegex(ci.CIError, 'export_limit'):
            ci.safe_export(self.root)
        self.assertFalse((self.root / 'export').exists())
        self.assertFalse(ci.export_allowed(Path('run/cases/unknown/seed.json')))
        self.assertFalse(ci.export_allowed(Path('run/cases/N3-cancel-Ca/processes/../../mysql.env')))

    def test_actual_export_symlink_rejected(self):
        target = self.root / 'other.json'
        target.write_text('{}')
        try:
            (self.root / 'ci-report.json').symlink_to(target)
        except OSError:
            self.skipTest('host lacks symlink privilege; Linux CI must execute')
        with self.assertRaisesRegex(ci.CIError, 'export_symlink_rejected'):
            ci.safe_export(self.root)

    def test_failed_matrix_or_validator_never_reports_pass(self):
        for failed in ('matrix', 'verification'):
            output = self.root / failed
            phases = []
            def phase(argv, env, root, name, deadline, db=None):
                phases.append((name, argv))
                if name == failed:
                    raise ci.CIError(name + '_failed')
            with mock.patch.object(ci.sys, 'platform', 'linux'), mock.patch.object(ci, 'clean_env', return_value={}), \
                    mock.patch.object(ci, 'run_phase', side_effect=phase), mock.patch.object(ci.DatabaseStopper, 'finish', return_value=True), \
                    contextlib.redirect_stdout(io.StringIO()) as console:
                code = ci.main(['--output', str(output)])
            self.assertEqual(code, 1)
            report = json.loads((output / 'export/ci-report.json').read_text())
            self.assertFalse(report['passed'])
            self.assertTrue(report['incomplete'])
            self.assertNotIn('A2C_CI_FULL_VERIFIED', console.getvalue())
            for _, argv in phases:
                self.assertNotIn('--case', argv)

    def test_cleanup_failure_overrides_success_and_export_failure_is_honest(self):
        for export_fail in (False, True):
            output = self.root / str(export_fail)
            with mock.patch.object(ci.sys, 'platform', 'linux'), mock.patch.object(ci, 'clean_env', return_value={}), \
                    mock.patch.object(ci, 'run_phase'), mock.patch.object(ci.DatabaseStopper, 'finish', return_value=export_fail), \
                    contextlib.redirect_stdout(io.StringIO()):
                if export_fail:
                    with mock.patch.object(ci, 'safe_export', side_effect=ci.CIError('export_secret_field')):
                        code = ci.main(['--output', str(output)])
                else:
                    code = ci.main(['--output', str(output)])
            self.assertEqual(code, 1)
            for path in ('ci-report.json', 'export/ci-report.json'):
                self.assertFalse(json.loads((output / path).read_text())['passed'])

    def test_sigterm_during_export_cannot_publish_success_or_stale_manifest(self):
        previous_stop = ci.STOP
        self.addCleanup(setattr, ci, 'STOP', previous_stop)
        original = ci.safe_export
        for after_publish in (False, True):
            output = self.root / ('cancel-' + str(after_publish))
            def interrupt_export(root):
                if not after_publish:
                    signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
                original(root)
                if after_publish:
                    signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
            with mock.patch.object(ci.sys, 'platform', 'linux'), mock.patch.object(ci, 'clean_env', return_value={}), \
                    mock.patch.object(ci, 'run_phase'), mock.patch.object(ci.DatabaseStopper, 'finish', return_value=True), \
                    mock.patch.object(ci, 'safe_export', side_effect=interrupt_export), \
                    contextlib.redirect_stdout(io.StringIO()) as console:
                result = ci.main(['--output', str(output)])
            self.assertEqual(result, 1)
            self.assertNotIn('A2C_CI_FULL_VERIFIED', console.getvalue())
            local = json.loads((output / 'ci-report.json').read_text())
            exported = output / 'export/ci-report.json'
            self.assertEqual(local, json.loads(exported.read_text()))
            self.assertFalse(local['passed'])
            self.assertTrue(local['incomplete'])
            self.assertEqual(local['error_code'], 'ci_interrupted')
            manifest = json.loads((output / 'export/export-manifest.json').read_text())['files']
            self.assertEqual(manifest['ci-report.json'], hashlib.sha256(exported.read_bytes()).hexdigest())


class DatabaseOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.run_id = '0123456789abcdef'
        self.cid = 'a' * 64
        ci.private_json(self.root / 'build-evidence.json', {'run_id': self.run_id})
        self.db = ci.DatabaseStopper(self.root, {})

    def result(self, text='', code=0):
        return subprocess.CompletedProcess([], code, text.encode(), b'')

    def test_matching_running_container_stops_exact_cid_and_retains_volume(self):
        responses = [self.result(self.cid + ' ' + self.run_id + ' true'), self.result(),
                     self.result(self.cid + ' ' + self.run_id + ' false')]
        with mock.patch.object(self.db, 'command', side_effect=responses) as command:
            self.assertFalse(self.db.finish())
        self.assertTrue(self.db.stopped)
        self.assertEqual(command.call_args_list[1].args[0], ['stop', '--time', '15', self.cid])
        self.assertTrue(all('rm' not in call.args[0] for call in command.call_args_list))

    def test_wrong_label_or_replaced_id_is_never_stopped(self):
        for row in ('a' * 64 + ' ffffffffffffffff true', 'b' * 64 + ' ' + self.run_id + ' true'):
            self.db.cid = self.cid
            with mock.patch.object(self.db, 'command', return_value=self.result(row)) as command:
                with self.assertRaises(ci.CIError):
                    self.db.finish()
                self.assertEqual(command.call_count, 1)

    def test_daemon_failure_is_not_absence(self):
        with mock.patch.object(self.db, 'command', side_effect=[self.result(code=1), self.result(code=1)]):
            with self.assertRaisesRegex(ci.CIError, 'cleanup_docker_unavailable'):
                self.db.finish()

    def test_changed_run_identity_is_rejected(self):
        self.db.run_id = 'ffffffffffffffff'
        with mock.patch.object(self.db, 'command') as command:
            with self.assertRaisesRegex(ci.CIError, 'cleanup_run_identity_changed'):
                self.db.finish()
            command.assert_not_called()


class ProcessFailureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def test_popen_failure_closes_stream_and_does_not_leak_diagnostic(self):
        stream = io.BytesIO()
        with mock.patch.object(Path, 'open', return_value=stream), \
                mock.patch.object(ci.subprocess, 'Popen', side_effect=OSError('secret diagnostic')):
            with self.assertRaises(ci.CIError) as error:
                ci.OwnedProcess(['unused'], {}, self.root / 'process.log')
        self.assertEqual(str(error.exception), 'process_start_failed')
        self.assertTrue(stream.closed)

    def test_missing_identity_terminates_only_original_child_then_waits(self):
        stream = io.BytesIO()
        process = mock.Mock(pid=1234)
        process.wait.side_effect = [subprocess.TimeoutExpired('unused', 2), -9]
        with mock.patch.object(Path, 'open', return_value=stream), \
                mock.patch.object(ci.subprocess, 'Popen', return_value=process), \
                mock.patch.object(ci, 'proc_fact', return_value=None), mock.patch.object(ci.os, 'kill') as kill:
            with self.assertRaisesRegex(ci.CIError, '^process_identity_missing$'):
                ci.OwnedProcess(['unused'], {}, self.root / 'process.log')
        self.assertEqual(process.mock_calls, [mock.call.terminate(), mock.call.wait(timeout=2),
                                             mock.call.kill(), mock.call.wait(timeout=2)])
        kill.assert_not_called()  # No signal to an unverified historical/group PID.
        self.assertTrue(stream.closed)

    def test_failed_identity_cleanup_and_unknown_stop_error_fail_closed(self):
        for constructor in (True, False):
            with self.subTest(constructor=constructor):
                stream = io.BytesIO()
                process = mock.Mock(pid=1234)
                process.terminate.side_effect = OSError('must not be printed')
                if constructor:
                    with mock.patch.object(Path, 'open', return_value=stream), \
                            mock.patch.object(ci.subprocess, 'Popen', return_value=process), \
                            mock.patch.object(ci, 'proc_fact', side_effect=OSError('private')):
                        with self.assertRaisesRegex(ci.CIError, '^process_cleanup_failed$'):
                            ci.OwnedProcess(['unused'], {}, self.root / 'process.log')
                else:
                    owned = ci.OwnedProcess.__new__(ci.OwnedProcess)
                    owned.proc, owned.stream = process, stream
                    with mock.patch.object(owned, 'members', side_effect=OSError('private')):
                        with self.assertRaisesRegex(ci.CIError, '^process_cleanup_failed$'):
                            owned.stop()
                self.assertTrue(stream.closed)

    def test_unproved_process_cleanup_cannot_be_reported_success(self):
        for code in ('process_identity_missing', 'process_cleanup_failed'):
            output = self.root / code
            with mock.patch.object(ci.sys, 'platform', 'linux'), mock.patch.object(ci, 'clean_env', return_value={}), \
                    mock.patch.object(ci, 'run_phase', side_effect=ci.CIError(code)), \
                    mock.patch.object(ci.DatabaseStopper, 'finish', return_value=True), contextlib.redirect_stdout(io.StringIO()):
                result = ci.main(['--output', str(output)])
            report = json.loads((output / 'export/ci-report.json').read_text())
            self.assertEqual(result, 1)
            self.assertFalse(report['passed'])
            self.assertFalse(report['cleanup_passed'])
            self.assertFalse(report['process_cleanup_passed'])
            self.assertEqual(report['error_code'], code)


@unittest.skipUnless(sys.platform == 'linux', 'actual Linux process/session test; not a simulated handoff')
class LinuxLifecycleTests(unittest.TestCase):
    def test_timeout_drains_owned_parent_and_grandchild(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / 'spawn.py'
            script.write_text("import subprocess,sys,time\nfrom pathlib import Path\np=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'])\nPath(sys.argv[1]).write_text(str(p.pid))\ntime.sleep(60)\n")
            pid_file = root / 'child.pid'
            with self.assertRaisesRegex(ci.CIError, 'ci_timeout'):
                ci.run_phase([sys.executable, str(script), str(pid_file)], os.environ.copy(), root,
                             'owned', time.monotonic() + 2)
            self.assertTrue(pid_file.exists(), 'positive control: actual grandchild must have started')
            fact = ci.proc_fact(int(pid_file.read_text()))
            self.assertTrue(fact is None or fact['state'] == 'Z')


if __name__ == '__main__':
    if os.name == 'nt':
        # The Windows sandbox's system temporary root may deny directory rename.
        # This affects test fixtures only; production remains Linux-only.
        local_temp = ci.ROOT / '.cache' / 'ci-host-unit-temp'
        local_temp.mkdir(parents=True, exist_ok=True)
        tempfile.tempdir = str(local_temp)
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromModule(sys.modules[__name__]))
    # Local Windows may expose explicit platform skips; Linux CI never accepts
    # one. These tests remain separate from the fixed 54-tool suite and 39 cases.
    raise SystemExit(0 if result.wasSuccessful() and (sys.platform != 'linux' or not result.skipped) else 1)
