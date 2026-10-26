#!/usr/bin/env python3
"""Registry-mode coordinator tests with fixture processes and no real Docker/network."""
import copy
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import sys
import unittest

import test_release as base

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import artifact


def manifest():
    images = {r: {'repository': 'laiai/' + r, 'digest': 'sha256:' + d * 64}
              for r, d in (('core', '1'), ('caddy', '2'))}
    report = {'passed': True, 'commit': base.TARGET_SHA, 'images': copy.deepcopy(images),
              'paid_calls': 0, 'isolated_database': True, 'checks': {k: True for k in (
                  'health', 'frontend', 'migration_replay', 'schema_doctor', 'registration_login',
                  'balance', 'video_key_consent', 'key_revoke', 'tenant_isolation')}}
    return {'schema': 1, 'repository': artifact.REPOSITORY, 'commit': base.TARGET_SHA,
            'branch': 'main', 'tag': artifact.tag('main', base.TARGET_SHA),
            'platform': 'linux/amd64', 'images': images, 'acceptance': report,
            'acceptance_sha256': hashlib.sha256(json.dumps(report, sort_keys=True, separators=(',', ':')).encode()).hexdigest()}


@unittest.skipUnless(platform.system() == 'Linux' and hasattr(os, 'geteuid') and os.geteuid() == 0,
                     'CLI tests require a disposable root Linux environment')
class RegistryCLITests(unittest.TestCase):
    tearDown = base.CLITests.tearDown
    set_control = base.CLITests.set_control
    change = base.CLITests.change
    log = base.CLITests.log
    call = base.CLITests.call
    run_dir = base.CLITests.run_dir
    assert_no_stop_or_up = base.CLITests.assert_no_stop_or_up

    def setUp(self):
        base.CLITests.setUp(self)
        self.manifest = self.root / 'tested artifact.json'
        self.manifest.write_text(json.dumps(manifest()))
        self.manifest.chmod(0o600)
        with self.config.open('a') as f:
            f.write('\nIMAGE_SOURCE=registry\nREGISTRY_AUTHORITY=127.0.0.1:15000\n')
            f.write('ARTIFACT_MANIFEST=' + shlex.quote(str(self.manifest)) + '\n')
            # A pull-only host does not need any build base-image configuration.
            f.write('GO_IMAGE=\nALPINE_IMAGE=\nNODE_IMAGE=\nCADDY_BASE_IMAGE=\n')
        extra = r'''
    elif args[0]=='pull':
        reference=args[-1]
        if '--platform' not in args or args[args.index('--platform')+1]!='linux/amd64': fail()
        if '@sha256:' not in reference: fail()
        role='core' if '/laiai/core@' in reference else 'caddy'
        event('pull-'+role)
        if c.get('pull_fails')==role: fail()
        c.setdefault('pulled',{})[reference]=role; save()
    elif args[:2]==['image','inspect'] and '--format' not in args and '@sha256:' in args[-1]:
        ref=args[-1]; role=c.get('pulled',{}).get(ref)
        if not role: fail()
        data={'Id':c['new_images'][role], 'Os':'linux', 'Architecture':'amd64', 'RepoDigests':[ref],
              'Config':{'Labels':{'org.opencontainers.image.revision':c['target']}}}
        data.update(c.get('inspect_changes',{}))
        print(json.dumps([data]))
'''
        original = (self.bin / 'docker').read_text()
        original = original.replace("    elif args[:2]==['image','inspect']:\n", extra + "    elif args[:2]==['image','inspect']:\n")
        (self.bin / 'docker').write_text(original)

    def prepare(self, through='pull'):
        self.call('preflight')
        self.release_id = next((self.state_dir / 'releases').iterdir()).name
        if through in ('backup', 'pull'):
            self.call('backup')
        if through == 'pull':
            self.call('pull')

    def rewrite(self, update):
        value=json.loads(self.manifest.read_text())
        update(value)
        self.manifest.write_text(json.dumps(value))

    def test_registry_preflight_needs_no_base_images_and_pull_keeps_service_running(self):
        self.prepare()
        self.assertEqual(json.loads((self.run_dir() / 'images.json').read_text()), base.NEW_IMAGES)
        self.assertEqual(self.log().count('pull-core'), 1)
        self.assertEqual(self.log().count('pull-caddy'), 1)
        self.assertFalse(any(v.startswith('build-') for v in self.log()))
        self.assertTrue((self.run_dir() / 'pull.ok').exists())
        self.assertFalse((self.run_dir() / 'build.ok').exists())
        self.assertTrue((self.run_dir() / 'candidate-images.tar.sha256').exists())
        self.assert_no_stop_or_up()

    def test_build_command_is_forbidden_before_any_build_or_service_action(self):
        self.call('build', ok=False, error='Build is forbidden in registry mode')
        self.assertEqual(self.log(), [])

    def test_unaccepted_artifact_refused_before_backup_or_pull(self):
        self.rewrite(lambda m: m['acceptance'].update(passed=False))
        self.call('preflight', ok=False, error='acceptance')
        self.assertFalse(any(v.startswith('pull-') or v == 'backup' for v in self.log()))
        self.assert_no_stop_or_up()

    def test_non_main_artifact_is_rejected(self):
        self.rewrite(lambda m: m.update(branch='codex/feature', tag=artifact.tag('codex/feature', base.TARGET_SHA)))
        self.call('preflight', ok=False, error='production requires main')
        self.assert_no_stop_or_up()

    def test_other_commit_is_rejected(self):
        self.rewrite(lambda m: m.update(commit=base.NEXT_SHA, tag=artifact.tag('main', base.NEXT_SHA)))
        self.call('preflight', ok=False, error='artifact does not match pinned main')
        self.assert_no_stop_or_up()

    def test_manifest_mutation_after_preflight_blocks_pull(self):
        self.prepare('backup')
        self.rewrite(lambda m: m.update(extra='changed after preflight'))
        self.call('pull', ok=False, error='Configuration/tool/evidence hooks changed')
        self.assertNotIn('pull-core', self.log())
        self.assert_no_stop_or_up()

    def test_registry_address_mutation_after_preflight_blocks_pull(self):
        self.prepare('backup')
        with self.config.open('a') as f: f.write('REGISTRY_AUTHORITY=127.0.0.1:15001\n')
        self.call('pull', ok=False, error='Configuration/tool/evidence hooks changed')
        self.assertNotIn('pull-core', self.log())
        self.assert_no_stop_or_up()

    def test_docker_credentials_change_invalidates_preflight_without_printing_them(self):
        auth = self.root / 'docker auth'
        auth.mkdir()
        settings = auth / 'config.json'
        settings.write_text(json.dumps({'auths': {'127.0.0.1:15000': {'auth': base.SECRET}}}))
        settings.chmod(0o600)
        with self.config.open('a') as f:
            f.write('export DOCKER_CONFIG=' + shlex.quote(str(auth)) + '\n')
        self.prepare('backup')
        settings.write_text(json.dumps({'auths': {'127.0.0.1:15000': {'auth': base.SECRET + 'changed'}}}))
        self.call('pull', ok=False, error='Configuration/tool/evidence hooks changed')
        self.assertNotIn('pull-core', self.log())
        self.assert_no_stop_or_up()

    def test_registry_rollback_uses_recorded_local_images_without_pulling(self):
        self.prepare()
        self.call('deploy')
        before = len(self.log())
        self.call('rollback')
        after = self.log()[before:]
        self.assertIn('approval:before-rollback', after)
        self.assertFalse(any(v.startswith('pull-') or v.startswith('build-') for v in after))
        self.assertEqual(json.loads(self.control.read_text())['images'], base.OLD_IMAGES)
        self.assertTrue((self.run_dir() / 'rolled-back.ok').exists())

    def test_mutated_acceptance_images_is_rejected(self):
        self.rewrite(lambda m: m['images']['core'].update(digest='sha256:' + '3' * 64))
        self.call('preflight', ok=False, error='acceptance images differ')
        self.assert_no_stop_or_up()

    def test_wrong_image_platform_blocks_candidate_and_deploy(self):
        self.prepare('backup')
        self.change(inspect_changes={'Architecture': 'arm64'})
        self.call('pull', ok=False, error='wrong image platform')
        self.call('deploy', ok=False, error='Missing successful stage: pull')
        self.assert_no_stop_or_up()

    def test_wrong_image_revision_blocks_candidate(self):
        self.prepare('backup')
        self.change(inspect_changes={'Config': {'Labels': {'org.opencontainers.image.revision': base.NEXT_SHA}}})
        self.call('pull', ok=False, error='wrong image revision')
        self.assertFalse((self.run_dir() / 'pull.ok').exists())
        self.assert_no_stop_or_up()

    def test_unverified_digest_blocks_candidate(self):
        self.prepare('backup')
        self.change(inspect_changes={'RepoDigests': []})
        self.call('pull', ok=False, error='pulled image digest not verified')
        self.assertFalse((self.run_dir() / 'pull.ok').exists())
        self.assert_no_stop_or_up()

    def test_image_readability_gate_applies_to_registry_images(self):
        self.prepare('backup')
        self.change(image_read_fails=True)
        self.call('pull', ok=False, error='Core image is not readable/executable')
        self.assertFalse((self.run_dir() / 'pull.ok').exists())
        self.assert_no_stop_or_up()

    def test_ci_and_maintenance_approval_remain_required(self):
        self.prepare('backup')
        self.change(ci_fails=True)
        self.call('pull', ok=False, error='no verified successful CI')
        self.assertNotIn('pull-core', self.log())
        self.change(ci_fails=False)
        self.call('pull')
        self.change(deny_approvals=['before-maintenance'])
        self.call('deploy', ok=False, error='Missing/failed external evidence for before-maintenance')
        self.assert_no_stop_or_up()

    def test_release_automatically_pulls_and_preserves_backup_migration_gates(self):
        self.call('release')
        self.release_id = next((self.state_dir / 'releases').iterdir()).name
        events = self.log()
        for before, after in [('backup', 'pull-core'), ('pull-caddy', 'approval:before-maintenance'),
                              ('approval:before-maintenance', 'stop-core'), ('stop-core', 'final-backup'),
                              ('final-backup', 'migrate'), ('migrate', 'schema-doctor'),
                              ('data:post-backfill', 'up-core'), ('up-core', 'approval:before-open')]:
            self.assertLess(events.index(before), events.index(after))
        self.assertEqual(events.count('migrate'), 2)
        self.assertEqual(events.count('backfill-funds'), 2)
        self.assertFalse(any(v.startswith('build-') for v in events))
        self.assertTrue((self.run_dir() / 'open.ok').exists())
        self.call('pull', ok=False)
        self.assertEqual(self.log().count('pull-core'), 1)


if __name__ == '__main__':
    unittest.main()
