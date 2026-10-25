#!/usr/bin/env python3
"""Portable, offline tests for the staging-to-production release contract."""
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ARTIFACT = Path(__file__).resolve().parents[1] / 'artifact.py'
spec = importlib.util.spec_from_file_location('artifact', ARTIFACT)
artifact = importlib.util.module_from_spec(spec)
spec.loader.exec_module(artifact)
COMMIT = 'd7fc49949c44b7e0dbfbf95384dafd15df13dadf'
OTHER_COMMIT = COMMIT[:12] + '0' * 28
CORE_DIGEST = 'sha256:' + 'a' * 64
CADDY_DIGEST = 'sha256:' + 'b' * 64
LOCAL_ID = 'sha256:' + 'c' * 64


def hash_report(value):
    value['acceptance_sha256'] = hashlib.sha256(json.dumps(
        value['acceptance'], sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return value


def manifest():
    images = {
        'core': {'repository': 'laiai/core', 'digest': CORE_DIGEST},
        'caddy': {'repository': 'laiai/caddy', 'digest': CADDY_DIGEST},
    }
    return hash_report({
        'schema': 1, 'repository': artifact.REPOSITORY,
        'branch': 'main', 'commit': COMMIT, 'tag': artifact.tag('main', COMMIT),
        'platform': 'linux/amd64', 'images': images,
        'acceptance': {
            'passed': True, 'commit': COMMIT, 'images': copy.deepcopy(images),
            'paid_calls': 0, 'isolated_database': True,
            'checks': {name: True for name in artifact.CHECKS},
        },
    })


def inspected():
    return [{
        'Os': 'linux', 'Architecture': 'amd64', 'Id': LOCAL_ID,
        'Config': {'Labels': {'org.opencontainers.image.revision': COMMIT}},
        'RepoDigests': ['127.0.0.1:15000/laiai/core@' + CORE_DIGEST],
    }]


class ArtifactTests(unittest.TestCase):
    def test_valid_pinned_main_and_digest_reference(self):
        value = manifest()
        self.assertIs(artifact.validate(value, COMMIT, production=True), value)
        self.assertEqual(artifact.reference(value, '127.0.0.1:15000', 'core'),
                         '127.0.0.1:15000/laiai/core@' + CORE_DIGEST)
        self.assertEqual(artifact.reference(value, '[::1]:15000', 'caddy'),
                         '[::1]:15000/laiai/caddy@' + CADDY_DIGEST)

    def test_branch_normalization_does_not_conflate_distinct_branches(self):
        self.assertNotEqual(artifact.tag('feature/a', COMMIT), artifact.tag('feature-a', COMMIT))
        self.assertNotEqual(artifact.tag('Main', COMMIT), artifact.tag('main', COMMIT))
        self.assertLessEqual(len(artifact.tag('x' * 181, COMMIT)), 128)

    def test_short_tag_is_not_accepted_as_commit_identity(self):
        # Publish must reject a pre-existing tag for different bytes, including a
        # short-SHA collision. An unchanged image may be reused without a push.
        # The contract and image inspection still demand the complete commit.
        self.assertEqual(artifact.tag('main', COMMIT), artifact.tag('main', OTHER_COMMIT))
        with self.assertRaisesRegex(ValueError, 'pinned main'):
            artifact.validate(manifest(), OTHER_COMMIT, production=True)
        with self.assertRaisesRegex(ValueError, 'wrong image revision'):
            artifact.inspect_image(inspected(), OTHER_COMMIT, inspected()[0]['RepoDigests'][0])

    def test_staging_branch_cannot_be_promoted_as_main(self):
        value = manifest()
        value.update(branch='codex/feature', tag=artifact.tag('codex/feature', COMMIT))
        artifact.validate(value, COMMIT)
        with self.assertRaisesRegex(ValueError, 'main artifact'):
            artifact.validate(value, COMMIT, production=True)

    def test_malformed_types_are_rejected_without_attribute_errors(self):
        for raw in (None, [], '', 1, True):
            with self.subTest(root=raw), self.assertRaises(ValueError):
                artifact.validate(raw)
        paths = (
            ('branch',), ('commit',), ('images',), ('images', 'core'),
            ('images', 'core', 'digest'), ('acceptance',), ('acceptance', 'checks'),
        )
        for path in paths:
            for malformed in (None, [], 42, True):
                value = manifest()
                parent = value
                for key in path[:-1]:
                    parent = parent[key]
                parent[path[-1]] = malformed
                with self.subTest(path=path, value=malformed), self.assertRaises(ValueError):
                    artifact.validate(value, COMMIT, production=True)

    def test_schema_and_paid_call_types_are_not_coerced(self):
        for schema in (True, 1.0, '1', 2):
            value = manifest()
            value['schema'] = schema
            with self.subTest(schema=schema), self.assertRaises(ValueError):
                artifact.validate(value)
        for count in (False, 0.0, '0', -1, 1):
            value = manifest()
            value['acceptance']['paid_calls'] = count
            hash_report(value)
            with self.subTest(count=count), self.assertRaises(ValueError):
                artifact.validate(value)

    def test_both_distinct_application_images_and_fixed_repositories_required(self):
        cases = []
        value = manifest(); del value['images']['caddy']; cases.append(value)
        value = manifest(); value['images']['mysql'] = value['images']['core']; cases.append(value)
        value = manifest(); value['images']['caddy']['repository'] = 'laiai/core'; cases.append(value)
        value = manifest(); value['images']['caddy']['digest'] = CORE_DIGEST; cases.append(value)
        value = manifest(); value['images']['core']['digest'] = 'latest'; cases.append(value)
        for value in cases:
            value['acceptance']['images'] = copy.deepcopy(value['images'])
            hash_report(value)
            with self.subTest(images=value['images']), self.assertRaises(ValueError):
                artifact.validate(value)

    def test_release_image_substitution_does_not_match_tested_images(self):
        value = manifest()
        value['images']['core']['digest'] = 'sha256:' + 'd' * 64
        with self.assertRaisesRegex(ValueError, 'images differ'):
            artifact.validate(value)
        value = manifest()
        value['acceptance']['commit'] = OTHER_COMMIT
        hash_report(value)
        with self.assertRaisesRegex(ValueError, 'exact-commit'):
            artifact.validate(value)

    def test_acceptance_body_hash_and_every_check_must_match(self):
        value = manifest()
        value['acceptance']['evidence_note'] = 'modified after acceptance'
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            artifact.validate(value)
        for key in artifact.CHECKS + ('additional_check',):
            value = manifest()
            value['acceptance']['checks'][key] = False
            hash_report(value)
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'incomplete'):
                artifact.validate(value)
        value = manifest()
        del value['acceptance']['checks']['tenant_isolation']
        hash_report(value)
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            artifact.validate(value)

    def test_invalid_branch_commit_platform_and_registry_inputs(self):
        for branch in ('', '../main', 'a b', '-main', 'a\nmain'):
            with self.subTest(branch=branch), self.assertRaises(ValueError):
                artifact.tag(branch, COMMIT)
        for commit in ('abc', 'g' * 40, COMMIT.upper(), None):
            with self.subTest(commit=commit), self.assertRaises(ValueError):
                artifact.tag('main', commit)
        value = manifest(); value['platform'] = 'linux/arm64'
        with self.assertRaisesRegex(ValueError, 'platform'):
            artifact.validate(value)
        for registry in ('https://zot.example', 'user:pass@zot.example', 'zot.example/a',
                         'zot.example:0', 'zot.example:65536', 'a..example', '-bad',
                         'zot.example\n', '::1', '[:::]:5000', None):
            with self.subTest(registry=registry), self.assertRaises(ValueError):
                artifact.reference(manifest(), registry, 'core')
        with self.assertRaises(ValueError):
            artifact.reference(manifest(), 'localhost:5000', 'mysql')

    def test_inspection_validates_digest_revision_and_platform(self):
        reference = inspected()[0]['RepoDigests'][0]
        self.assertEqual(artifact.inspect_image(inspected(), COMMIT, reference), LOCAL_ID)
        mutations = (
            ('Os', 'windows'), ('Architecture', 'arm64'), ('Id', 'latest'),
            ('RepoDigests', [reference.replace('laiai/core', 'other/core')]),
            ('RepoDigests', reference), ('Config', None),
            ('Config', {'Labels': None}), ('Config', {'Labels': {}}),
        )
        for key, replacement in mutations:
            value = inspected(); value[0][key] = replacement
            with self.subTest(key=key, value=replacement), self.assertRaises(ValueError):
                artifact.inspect_image(value, COMMIT, reference)
        for value in (None, {}, [], [None], inspected() * 2):
            with self.subTest(value=value), self.assertRaises(ValueError):
                artifact.inspect_image(value, COMMIT, reference)

    def test_ambiguous_json_is_rejected(self):
        for raw in ('{"schema":1,"schema":1}', '{"acceptance":{"paid_calls":0,"paid_calls":1}}',
                    '{"unexpected":NaN}', '{"unexpected":Infinity}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                artifact.read_json(io.StringIO(raw))

    def test_cli_null_has_safe_nonzero_error_and_valid_cli_emits_only_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'artifact.json'
            path.write_text('null', encoding='utf-8')
            result = subprocess.run([sys.executable, str(ARTIFACT), 'validate', str(path), COMMIT],
                                    text=True, capture_output=True, check=False)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, '')
            self.assertNotIn('Traceback', result.stderr)
            path.write_text(json.dumps(manifest()), encoding='utf-8')
            result = subprocess.run([sys.executable, str(ARTIFACT), 'validate', str(path), COMMIT],
                                    text=True, capture_output=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), COMMIT)


if __name__ == '__main__':
    unittest.main()
