#!/usr/bin/env python3
"""Staging boundaries and publication safety; no network or Docker required."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

RELEASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(RELEASE))
spec = importlib.util.spec_from_file_location('stage_under_test', RELEASE / 'stage.py')
stage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stage)


class StageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.s = object.__new__(stage.Stage)
        self.s.root = self.root
        self.s.private = self.root
        self.s.source = self.root
        self.s.cfg = {'branch': 'main', 'mysql_image': 'mysql@sha256:' + 'a' * 64,
                      'redis_image': 'redis@sha256:' + 'b' * 64}
        self.s.commit = '1' * 40
        self.s.tag = stage.artifact.tag('main', self.s.commit)
        self.s.project = 'laiai-stage-' + self.s.commit[:12]
        self.s.port = 18080
        self.s.fingerprint = 'f' * 64
        self.s.log = self.root / 'commands.log'
        self.s.result_path = self.root / 'acceptance.local.json'
        self.s.compose_path = self.root / 'compose.json'
        self.s.deploy_path = self.root / 'deployment.json'
        self.s.authority = '127.0.0.1:5000'
        self.ids = {'core': 'sha256:' + 'c' * 64, 'caddy': 'sha256:' + 'd' * 64}
        self.deployed = {'images': self.ids, 'compose_sha256': 'e' * 64}
        self.containers = {'core': 'core-id', 'caddy': 'caddy-id', 'mysql': 'mysql-id', 'redis': 'redis-id'}
        self.report = {'schema': 1, 'passed': True, 'commit': self.s.commit, 'tag': self.s.tag,
            'local_images': self.ids, 'containers': self.containers,
            'config_sha256': self.s.fingerprint, 'compose_sha256': 'e' * 64,
            'paid_calls': 0, 'isolated_database': True,
            'checks': {key: True for key in stage.artifact.CHECKS}}
        self.s.source_check = Mock()
        self.s.running = Mock(return_value=(self.deployed, self.containers))
        self.s.registry_auth = Mock(return_value='PRIVATE_CREDENTIAL')
        self.s.manifest = Mock()

    def save_report(self, value=None):
        stage.write(self.s.result_path, value or self.report)

    def test_http_only_loopback_and_no_credentials_or_paths(self):
        for value in ('http://177.3.41.117:5000', 'http://user:secret@127.0.0.1:5000',
                      'https://example.org/path', 'ftp://127.0.0.1:5000', 'https://example.org?x=1'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                stage.registry_url(value)
        self.assertEqual(stage.registry_url('http://127.0.0.1:5000'), 'http://127.0.0.1:5000')
        self.assertEqual(stage.registry_url('https://registry.example.org'), 'https://registry.example.org')

    def test_redirect_is_rejected_instead_of_forwarding_auth(self):
        with self.assertRaisesRegex(ValueError, 'redirect'):
            stage.NoRedirect().redirect_request(None, None, 302, '', {}, 'http://outside.invalid')

    def test_inspection_never_logs_container_secrets(self):
        with patch.object(stage, 'call', return_value=b'[{"Config":{"Env":["PASSWORD=secret"]}}]') as run:
            value = self.s.inspect('core-id')
        self.assertEqual(value[0]['Config']['Env'], ['PASSWORD=secret'])
        self.assertNotIn('log', run.call_args.kwargs)

    def test_compose_is_private_and_no_production_storage_or_public_port(self):
        config = self.s.make_compose(self.ids)
        self.assertEqual(config['networks'], {'isolated': {'internal': True}, 'ingress': {}})
        self.assertEqual(config['services']['caddy']['ports'], ['127.0.0.1:18080:80'])
        self.assertEqual(config['services']['core']['image'], self.ids['core'])
        for role, service in config['services'].items():
            self.assertNotIn('container_name', service)
            self.assertNotIn('build', service)
            self.assertEqual(service['networks'], ['isolated', 'ingress'] if role == 'caddy' else ['isolated'])
        self.assertEqual(config['volumes']['mysql-data'], {})
        env = config['services']['core']['environment']
        self.assertEqual(len(env['LAIAI_ADMIN_TOKEN']), 64)
        self.assertNotEqual(env['LAIAI_ADMIN_TOKEN'], env['LAIAI_AUTH_SECRET'])
        self.assertNotEqual(env['LAIAI_AUTH_SECRET'], env['LAIAI_VIDEO_HMAC_SECRET'])
        second = self.s.make_compose(self.ids)
        self.assertNotEqual(env['LAIAI_AUTH_SECRET'], second['services']['core']['environment']['LAIAI_AUTH_SECRET'])
        redis = config['services']['redis']
        self.assertNotIn(redis['environment']['REDISCLI_AUTH'], json.dumps(redis['entrypoint']))

    def test_changed_running_image_refuses_before_registry_contact(self):
        self.save_report()
        self.deployed['images'] = {**self.ids, 'core': 'sha256:' + '9' * 64}
        with self.assertRaisesRegex(ValueError, 'acceptance'):
            self.s.publish()
        self.s.registry_auth.assert_not_called()

    def test_preparation_refuses_even_started_or_failed_acceptance(self):
        self.save_report({'passed': False, 'status': 'running'})
        with self.assertRaisesRegex(ValueError, 'after acceptance started'):
            self.s.prepare_tests()
        self.s.running.assert_not_called()

    def test_ingress_repair_refuses_passed_acceptance(self):
        self.save_report()
        with self.assertRaisesRegex(ValueError, 'passed or ambiguous'):
            self.s.repair_ingress()
        self.s.running.assert_not_called()

    def test_legacy_admin_preparation_then_ingress_repair_has_no_deadlock(self):
        compose = self.s.make_compose(self.ids)
        del compose['networks']['ingress']
        compose['services']['caddy']['networks'] = ['isolated']
        del compose['services']['core']['environment']['LAIAI_ADMIN_TOKEN']
        stage.write(self.s.compose_path, compose)
        self.s.compose = Mock(return_value=b'')
        self.s.wait_http_health = Mock()
        prepared = {**self.containers, 'core': 'prepared-core'}
        self.s.running.side_effect = [(self.deployed, self.containers), (self.deployed, prepared)]
        self.s.prepare_tests()
        self.assertFalse(self.s.result_path.exists())
        self.assertTrue(all(c.kwargs.get('legacy_ingress') is True for c in self.s.running.call_args_list))
        self.s.running.side_effect = [(self.deployed, prepared),
            (self.deployed, {**prepared, 'caddy': 'replacement-caddy'})]
        self.s.repair_ingress()
        changed = json.loads(self.s.compose_path.read_text())
        self.assertTrue(changed['services']['core']['environment']['LAIAI_ADMIN_TOKEN'])
        self.assertEqual(changed['services']['caddy']['networks'], ['isolated', 'ingress'])
        self.assertFalse(json.loads(self.s.result_path.read_text())['passed'])

    def test_ingress_repair_archives_failed_evidence_and_only_recreates_caddy(self):
        failed = {'passed': False, 'commit': self.s.commit, 'status': 'running'}
        self.save_report(failed)
        compose = self.s.make_compose(self.ids)
        del compose['networks']['ingress']
        compose['services']['caddy']['networks'] = ['isolated']
        stage.write(self.s.compose_path, compose)
        self.s.compose = Mock(return_value=b'')
        self.s.wait_http_health = Mock()
        self.s.running.side_effect = [(self.deployed, self.containers),
            (self.deployed, {**self.containers, 'caddy': 'replacement-caddy'})]
        self.s.repair_ingress()
        archived = list(self.root.glob('acceptance.failed-*.json'))
        self.assertEqual(len(archived), 1)
        self.assertEqual(json.loads(archived[0].read_text()), failed)
        current = json.loads(self.s.result_path.read_text())
        self.assertFalse(current['passed'])
        self.assertEqual(current['status'], 'configuration_changed_full_retest_required')
        after = json.loads(self.s.compose_path.read_text())
        self.assertEqual(after['services']['caddy']['networks'], ['isolated', 'ingress'])
        self.assertEqual(after['services']['core'], compose['services']['core'])
        self.assertEqual(after['services']['mysql'], compose['services']['mysql'])
        self.assertEqual(after['services']['redis'], compose['services']['redis'])
        self.assertEqual(self.s.compose.call_args.args[-1], 'caddy')
        self.s.wait_http_health.assert_called_once()

    def test_failed_or_partial_acceptance_cannot_publish(self):
        for mutate in ('failed', 'missing_check', 'wrong_commit', 'paid'):
            report = copy.deepcopy(self.report)
            if mutate == 'failed':
                report['passed'] = False
            elif mutate == 'missing_check':
                del report['checks']['tenant_isolation']
            elif mutate == 'wrong_commit':
                report['commit'] = '9' * 40
            else:
                report['paid_calls'] = 1
            self.save_report(report)
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                self.s.publish()
        self.s.registry_auth.assert_not_called()

    def test_different_existing_tag_refuses_all_pushes(self):
        self.save_report()
        self.s.manifest.side_effect = [None, ('sha256:' + 'a' * 64, {'config': {'digest': 'sha256:' + '8' * 64}})]
        with patch.object(stage, 'call') as run, self.assertRaisesRegex(ValueError, 'different image'):
            self.s.publish()
        run.assert_not_called()
        self.assertFalse((self.root / 'artifact.json').exists())

    def test_existing_identical_tag_is_verified_without_overwrite(self):
        self.save_report()
        digests = {'core': 'sha256:' + 'a' * 64, 'caddy': 'sha256:' + 'b' * 64}
        def manifest(role, reference, auth, missing=False):
            return digests[role], {'config': {'digest': self.ids[role]}}
        self.s.manifest.side_effect = manifest
        def inspect(reference):
            role = 'core' if '/core@' in reference else 'caddy'
            return [{'Os': 'linux', 'Architecture': 'amd64', 'Id': self.ids[role], 'RepoDigests': [reference],
                     'Config': {'Labels': {'org.opencontainers.image.revision': self.s.commit}}}]
        self.s.inspect = Mock(side_effect=inspect)
        with patch.object(stage, 'call', return_value=b'') as run:
            self.s.publish()
        self.assertTrue(all(args.args[0][1] == 'pull' for args in run.call_args_list))
        artifact = json.loads((self.root / 'artifact.json').read_text())
        stage.artifact.validate(artifact, self.s.commit, production=True)
        self.assertEqual(artifact['images']['core']['digest'], digests['core'])

    def test_container_replaced_during_publication_does_not_emit_artifact(self):
        self.save_report()
        self.s.running.side_effect = [(self.deployed, self.containers),
            (self.deployed, {**self.containers, 'core': 'replacement-core'})]
        digests = {'core': 'sha256:' + 'a' * 64, 'caddy': 'sha256:' + 'b' * 64}
        self.s.manifest.side_effect = lambda role, *a, **kw: (digests[role], {'config': {'digest': self.ids[role]}})
        def inspect(reference):
            role = 'core' if '/core@' in reference else 'caddy'
            return [{'Os': 'linux', 'Architecture': 'amd64', 'Id': self.ids[role], 'RepoDigests': [reference],
                     'Config': {'Labels': {'org.opencontainers.image.revision': self.s.commit}}}]
        self.s.inspect = Mock(side_effect=inspect)
        with patch.object(stage, 'call', return_value=b''), self.assertRaisesRegex(ValueError, 'changed during publication'):
            self.s.publish()
        self.assertFalse((self.root / 'artifact.json').exists())


if __name__ == '__main__':
    unittest.main()
