"""Controller failure tests; synthetic proofs are not MySQL handoff evidence."""
import argparse
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import signal
import struct
import sys
import tarfile
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import ark_execution_input_compat as host
import ark_execution_input_verify as verify


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def proof(settled=False, action=False, fault=None, purged=False):
    # This independent literal fixture is deliberately not produced by the
    # matrix or verifier's calculation helpers.
    prints = {name: hashlib.sha256(name.encode()).hexdigest() for name in (
        'accounts', 'holds', 'journals', 'ledger_movements', 'ledger_operations',
        'ledger_events', 'billing_entries', 'quota_reservations', 'quota_buckets', 'key_usage')}
    if settled:
        prints = {key: hashlib.sha256((key + ':settled').encode()).hexdigest() for key in prints}
    prints['financial_all'] = digest(prints)
    return {
        'org_id': '11', 'key_id': '22', 'task_id': 'task-33', 'operation_id': 'op-action' if action else 'op-create',
        'price': {'retail_usd_per_1m': '8', 'supplier_cny_per_1m': '46', 'fx_usd_per_cny': '0.14',
                  'hold_usd': '0.86400000', 'supplier_reserved_cny': '4.96800000', 'effective_ratio': '1',
                  'rule_version': 'handoff-v1', 'fx_version': 'handoff-v1'},
        'finance': {'credit_usd': '100.00000000', 'available_usd': '99.99920000' if settled else '99.13600000',
                    'frozen_usd': '0.00000000' if settled else '0.86400000', 'hold_usd': '0.86400000',
                    'captured_usd': '0.00080000' if settled else '0.00000000',
                    'released_usd': '0.86320000' if settled else '0.00000000',
                    'hold_state': 'captured' if settled else 'active', 'capture_journal_count': int(settled),
                    'billing_entry_count': int(settled), 'ledger_drift_count': 0,
                    'quota_reservation_count': 7, 'quota_bucket_count': 7, 'release_movement_count': 0},
        'actual_amounts': {'supplier_cny': '0.00460000' if settled else '0.00000000',
                           'key_used_usd': '0.00080000' if settled else '0.86400000',
                           'provider_reserved_cny': '0.00000000' if settled else '4.96800000',
                           'provider_consumed_cny': '0.00460000' if settled else '0.00000000'},
        'financial_fingerprints': prints, 'financial_sha256': prints['financial_all'],
        'billing_snapshot_sha256': 'a' * 64, 'binding_sha256': 'b' * 64,
        'attempt_sha256': 'c' * 64, 'receipt_sha256': 'd' * 64,
        'input': {'identity_sha256': ('7' if fault == 'schema' else 'e') * 64, 'payload_ref': 'action' if action else 'create',
                  'schema': 99 if fault == 'schema' else (2 if action else 1),
                  'expires_at': '2026-10-26T00:00:30Z', 'capture_status': 'expired' if purged else 'staged',
                  'key_present': not purged, 'nonce_present': not purged, 'staging_count': 0 if purged else 1},
        'database_time': '2026-10-26T00:03:01Z' if purged else '2026-10-26T00:00:01Z',
        'runtime_version': 'future-runtime' if fault == 'runtime' else 'video-v1',
        'input_successful_reads': 1, 'attempt_count': 1, 'receipt_count': 1,
        'application_state': 'pending', 'applied_at': None,
        'operation_status': 'unknown', 'task_status': 'succeeded' if settled else 'queued',
        'billing_state': 'settled' if settled else 'held',
        'oracle': {'request_body_sha256': 'f' * 64},
    }


def family(case):
    events = []
    writer = 'writer-new' if case.startswith('N03') else 'writer-old'
    action = case in ('N02', 'N03-action') or case.startswith('N04-schema')
    seed = proof()
    seed['attempt_count'] = seed['receipt_count'] = 0

    def event(name, data, instance=writer, command=1):
        events.append({'event': name, 'data': copy.deepcopy(data), 'instance': instance, 'cmd_id': command})

    event('seeded', seed)
    persisted = proof(action=action)
    if case != 'N03-create':
        producer = 'sender-new' if case == 'N01' else writer
        event('receipt_committed', persisted, producer, 2)
        event('cycle_completed', {'error_class': 'cancelled', 'barrier_reached': True, 'proof': persisted}, producer, 2)
    fault = 'schema' if case.startswith('N04-schema') else 'runtime' if case.startswith('N04-runtime') else None
    if fault:
        injected = proof(action=action, fault=fault)
        event('unknown_injected', {'kind': fault, 'before': persisted, 'after': injected})
        persisted = injected
    if case == 'N03-action':
        event('action_read', {'payload_ref': 'action', 'bytes': 23}, 'first-old')
    purge = case in ('N01', 'N02') or case.startswith('N04')
    if purge:
        before = copy.deepcopy(persisted)
        before['database_time'] = '2026-10-26T00:03:01Z'
        persisted = proof(action=action, fault=fault, purged=True)
        purger = 'recovery-new' if case == 'N01' else 'first-new' if case == 'N02' else 'purger-new'
        event('purged', {'before': before, 'after': persisted}, purger)
    if fault:
        first = 'first-new' if case.endswith('-new') else 'first-old'
        for command in (5, 6):
            event('cycle_completed', {'error_class': 'none', 'barrier_reached': False, 'proof': persisted}, first, command)
    terminal = case in ('N01', 'N03-create')
    final = proof(terminal, action=action, fault=fault, purged=purge)
    if not fault:
        final['application_state'], final['applied_at'] = 'applied', '2026-10-26T00:03:02Z'
    instance = {'N01': 'recovery-new', 'N03-create': 'sender-old', 'N02': 'first-new', 'N03-action': 'first-old'}.get(case)
    if instance is None:
        instance = 'second-old' if case.endswith('-new') else 'second-new'
    for command in (7, 8):
        event('cycle_completed', {'error_class': 'none', 'barrier_reached': False, 'proof': final}, instance, command)
    return {'case_id': case, 'status': 'passed', 'seed': seed, 'final': copy.deepcopy(final)}, events


def raw_evidence(root):
    """Write actual verifier input format, not its own expected-result output."""
    matrix = root / 'matrix'
    matrix.mkdir()
    builds = {'old': {'source': host.OLD, 'sha256': 'a' * 64},
              'new': {'source': 'b' * 40, 'sha256': 'c' * 64}}
    (root / 'build-manifest.json').write_text(json.dumps(builds))
    exits, mock_calls = [], []
    run = '0123456789abcdef'
    for case, instances in verify.INSTANCES.items():
        result, facts = family(case)
        for instance in instances:
            role, pid = instance.split('-')[-1], 1000 + len(exits)
            row = {'case_id': case, 'instance': instance, 'role': role, 'source_sha': builds[role]['source'],
                   'binary_sha256': builds[role]['sha256'], 'pid': pid, 'exit_code': 0,
                   'forced_stop': False, 'clean_exit': True}
            exits.append(row)
            commands, events, mapped = [], [], {}

            def command(kind, **fields):
                ident = len(commands) + 1
                commands.append({'protocol': 'execution-clock-handoff-v1', 'run_id': run, 'case_id': case,
                                 'instance': instance, 'cmd_id': ident, 'command': kind, **fields})
                return ident

            def event(name, data, cmd=0):
                events.append({'protocol': 'execution-clock-handoff-v1', 'run_id': run, 'case_id': case,
                               'instance': instance, 'role': role, 'source_sha': builds[role]['source'],
                               'pid': pid, 'seq': len(events) + 1, 'cmd_id': cmd, 'event': name, 'data': data})

            producer = instance.startswith(('writer-', 'sender-'))
            event('ready', {'compiled_source': builds[role]['source'], 'old_source': host.OLD,
                            'create_producer': producer, 'mutation_producer': producer})
            for fact in (x for x in facts if x['instance'] == instance):
                kind = {'seeded': 'seed', 'receipt_committed': 'run_once', 'cycle_completed': 'run_once',
                        'purged': 'purge', 'unknown_injected': 'unknown', 'action_read': 'read_action'}[fact['event']]
                key = (kind, fact['cmd_id'])
                if key not in mapped:
                    extra = {'barrier': 'receipt_committed'} if fact['event'] == 'receipt_committed' else {}
                    mapped[key] = command(kind, **extra)
                event(fact['event'], fact['data'], mapped[key])
                if fact['event'] == 'receipt_committed':
                    cancel = command('cancel_active', owner_cmd_id=mapped[key])
                    event('cancel_requested', {'owner_cmd_id': mapped[key]}, cancel)
            event('stop_ack', {'active_work': False}, command('stop'))
            event('drained', {'active_work': False, 'pools_closed': True})
            text = '\n'.join('B1B_EVENT ' + json.dumps(e) for e in events)
            text += '\n--- PASS: TestMySQLExecutionInputBinaryHandoffProcess (1.00s)\nPASS\n'
            (matrix / (case + '-' + instance + '.log')).write_text(text)
            (matrix / (case + '-' + instance + '-commands.jsonl')).write_text('\n'.join(map(json.dumps, commands)) + '\n')
        calls = []
        methods = ['POST']
        if case in ('N01', 'N03-create'):
            methods.append('GET')
        if case in ('N02', 'N03-action') or case.startswith('N04-schema'):
            methods.append('DELETE')
        for method in methods:
            call = {'run_id': run, 'case_id': case, 'instance': instances[0], 'method': method,
                    'path': '/api/v3/contents/generations/tasks' + ('' if method == 'POST' else '/b1b-' + case.lower()),
                    'body_sha256': 'f' * 64, 'body_bytes': 10, 'sequence': len(mock_calls) + 1}
            mock_calls.append(call)
            calls.append(call)
        result['http'] = calls
        (matrix / (case + '-result.json')).write_text(json.dumps(result))
    (matrix / 'mock-requests.jsonl').write_text('\n'.join(map(json.dumps, mock_calls)) + '\n')
    (matrix / 'child-exits.json').write_text(json.dumps(exits))
    (matrix / 'matrix-passed.json').write_text(json.dumps({'status': 'passed', 'run_id': run, 'old_source': builds['old']['source'],
                                                          'new_source': builds['new']['source'], 'families': 4, 'cases': 8,
                                                          'cases_passed': list(verify.INSTANCES)}))


class FinancialEvidenceTests(unittest.TestCase):
    def test_reserved_key_budget_is_not_only_settled_spend(self):
        for settled, expected, wrong in ((False, '0.86400000', '0.00000000'),
                                          (True, '0.00080000', '0.86400000')):
            with self.subTest(settled=settled):
                value = proof(settled=settled)
                self.assertEqual(value['actual_amounts']['key_used_usd'], expected)
                verify.check_finance(value, settled)
                value['actual_amounts']['key_used_usd'] = wrong
                with self.assertRaisesRegex(ValueError, 'literal_supplier_quota_key'):
                    verify.check_finance(value, settled)

    def test_coordinated_raw_and_result_key_state_transition_rejected(self):
        # Mutate every raw proof and corresponding result together, so raw
        # equality alone cannot hide the wrong held/settled budget oracle.
        for case in host.CASES:
            states = (False, True) if case in ('N01', 'N03-create') else (False,)
            for settled in states:
                with self.subTest(case=case, settled=settled):
                    result, events = family(case)
                    touched = 0

                    def corrupt(value):
                        nonlocal touched
                        if isinstance(value, dict):
                            if 'actual_amounts' in value and (value.get('billing_state') == 'settled') == settled:
                                value['actual_amounts']['key_used_usd'] = '0.86400000' if settled else '0.00000000'
                                touched += 1
                            for child in value.values():
                                corrupt(child)
                        elif isinstance(value, list):
                            for child in value:
                                corrupt(child)

                    corrupt(result)
                    corrupt(events)
                    self.assertGreaterEqual(touched, 2)
                    with self.assertRaisesRegex(ValueError, 'literal_supplier_quota_key'):
                        verify.verify_case_proofs(case, result, events)

    def test_run_command_and_mock_boundary_tampering_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw_evidence(root)
            matrix = root / 'matrix'
            path = matrix / 'N01-writer-old.log'
            original = path.read_bytes()
            for change in ('run_id', 'protocol', 'event_name', 'event_command'):
                with self.subTest(change=change):
                    lines = original.decode().splitlines()
                    for index, line in enumerate(lines):
                        if not line.startswith('B1B_EVENT '):
                            continue
                        event = json.loads(line[10:])
                        if change == 'run_id':
                            event['run_id'] = 'ffffffffffffffff'
                        elif change == 'protocol':
                            event['protocol'] = 'different-v1'
                        elif event['event'] == 'seeded':
                            if change == 'event_name':
                                event['event'] = 'inspected'
                            else:
                                event['cmd_id'] = 2
                        lines[index] = 'B1B_EVENT ' + json.dumps(event)
                    path.write_text('\n'.join(lines) + '\n')
                    with self.assertRaises(ValueError):
                        verify.verify(root)
                    path.write_bytes(original)
            commands = matrix / 'N01-writer-old-commands.jsonl'
            original_commands = commands.read_bytes()
            data = [json.loads(line) for line in original_commands.decode().splitlines()]
            data[0]['command'] = 'inspect'
            commands.write_text('\n'.join(map(json.dumps, data)) + '\n')
            with self.assertRaisesRegex(ValueError, 'command_completion_ownership'):
                verify.verify(root)
            commands.write_bytes(original_commands)
            mock_path = matrix / 'mock-requests.jsonl'
            calls = [json.loads(line) for line in mock_path.read_text().splitlines()]
            rogue = dict(calls[0], case_id='unaccounted-case', sequence=len(calls) + 1)
            mock_path.write_text('\n'.join(map(json.dumps, calls + [rogue])) + '\n')
            with self.assertRaisesRegex(ValueError, 'mock_outside_run'):
                verify.verify(root)

    def test_complete_raw_stream_and_result_tampering(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw_evidence(root)
            result = verify.verify(root)
            self.assertEqual((result['cases'], result['actual_processes']), (8, 25))
            for case in host.CASES:
                path = root / 'matrix' / (case + '-result.json')
                original = path.read_bytes()
                for which in ('seed', 'final'):
                    with self.subTest(case=case, which=which):
                        data = json.loads(original)
                        data[which]['actual_amounts']['key_used_usd'] = '50.00000000'
                        path.write_text(json.dumps(data))
                        with self.assertRaisesRegex(ValueError, 'result_.*_not_raw'):
                            verify.verify(root)
                        path.write_bytes(original)
            exit_path = root / 'matrix/child-exits.json'
            exits = json.loads(exit_path.read_text())
            exits[0]['forced_stop'] = True
            exit_path.write_text(json.dumps(exits))
            with self.assertRaisesRegex(ValueError, 'child_exit'):
                verify.verify(root)

    def test_all_eight_family_proof_shapes_pass_independent_oracle(self):
        for case in host.CASES:
            with self.subTest(case=case):
                result, events = family(case)
                verify.verify_case_proofs(case, result, events)

    def test_result_seed_or_final_cannot_diverge_from_raw_event(self):
        for which in ('seed', 'final'):
            for field in ('finance', 'actual_amounts', 'financial_sha256', 'price', 'billing_snapshot_sha256'):
                with self.subTest(which=which, field=field):
                    result, events = family('N01')
                    result[which][field] = {'forged': '0'} if isinstance(result[which][field], dict) else '0' * 64
                    with self.assertRaisesRegex(ValueError, 'result_.*_not_raw'):
                        verify.verify_case_proofs('N01', result, events)

    def test_coordinated_raw_and_result_money_forgery_rejected(self):
        for case in host.CASES:
            for field, key, value in (('finance', 'captured_usd', '0.00000001'),
                                       ('actual_amounts', 'key_used_usd', '0.00000001'),
                                       ('actual_amounts', 'provider_reserved_cny', '1.00000000'),
                                       ('actual_amounts', 'supplier_cny', '1.00000000'),
                                       ('price', 'fx_usd_per_cny', '0.15')):
                with self.subTest(case=case, field=field, key=key):
                    result, events = family(case)
                    result['final'][field][key] = value
                    for event in events:
                        if event['event'] == 'cycle_completed' and event['data']['error_class'] == 'none':
                            event['data']['proof'][field][key] = value
                    with self.assertRaises(ValueError):
                        verify.verify_case_proofs(case, result, events)

    def test_full_fingerprint_and_replay_tamper_rejected(self):
        for case in host.CASES:
            for target in ('accounts', 'quota_buckets', 'key_usage', 'ledger_events'):
                with self.subTest(case=case, target=target):
                    result, events = family(case)
                    altered = events[-1]['data']['proof']
                    altered['financial_fingerprints'][target] = '0' * 64
                    # Recompute the aggregate too: equality to the original raw
                    # application, not merely a self-consistent hash, must fail.
                    parts = {k: v for k, v in altered['financial_fingerprints'].items() if k != 'financial_all'}
                    altered['financial_fingerprints']['financial_all'] = altered['financial_sha256'] = digest(parts)
                    if case not in ('N01', 'N03-create'):
                        result['final'] = copy.deepcopy(altered)
                    with self.assertRaises(ValueError):
                        verify.verify_case_proofs(case, result, events)

    def test_durable_writer_evidence_binds_both_final_cycles_and_result(self):
        for case in ('N01', 'N02', 'N04-schema-new', 'N04-runtime-old'):
            for field in ('attempt_sha256', 'receipt_sha256', 'operation_id'):
                with self.subTest(case=case, field=field):
                    result, events = family(case)
                    final_instance = events[-1]['instance']
                    result['final'][field] = '9' * 64
                    for event in events:
                        if event['event'] == 'cycle_completed' and event['instance'] == final_instance:
                            event['data']['proof'][field] = '9' * 64
                    with self.assertRaisesRegex(ValueError, 'durable_chain|operation_not_durable'):
                        verify.verify_case_proofs(case, result, events)

    def test_changing_purge_pair_and_final_does_not_rewrite_original_receipt(self):
        for case in ('N01', 'N02', 'N04-schema-new'):
            with self.subTest(case=case):
                result, events = family(case)
                for event in events:
                    if event['event'] == 'purged':
                        for side in ('before', 'after'):
                            event['data'][side]['receipt_sha256'] = '8' * 64
                    if event['event'] == 'cycle_completed' and event['data']['error_class'] == 'none':
                        event['data']['proof']['receipt_sha256'] = '8' * 64
                result['final']['receipt_sha256'] = '8' * 64
                with self.assertRaisesRegex(ValueError, 'durable_chain_receipt_sha256'):
                    verify.verify_case_proofs(case, result, events)

    def test_wrong_unknown_purge_commit_and_replay_evidence_rejected(self):
        for case, event_name, change in (
            ('N01', 'receipt_committed', lambda p: p.update(database_time='2026-10-26T00:03:01Z')),
            ('N02', 'purged', lambda p: p['after']['input'].update(staging_count=1)),
            ('N03-action', 'action_read', lambda p: p.update(bytes=0)),
            ('N04-schema-new', 'unknown_injected', lambda p: p['after']['input'].update(schema=2)),
            ('N04-runtime-old', 'unknown_injected', lambda p: p['after'].update(runtime_version='video-v1')),
        ):
            with self.subTest(case=case, event=event_name):
                result, events = family(case)
                change(next(e['data'] for e in events if e['event'] == event_name))
                with self.assertRaises(ValueError):
                    verify.verify_case_proofs(case, result, events)

    def test_duplicate_json_or_nonfinite_numbers_rejected(self):
        for text in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                verify.strict_json(text)


class HostBoundaries(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def runner(self):
        args = argparse.Namespace(output=str(self.root / 'run'), timeout_seconds=1200, compiler='host')
        with mock.patch.dict(os.environ, {'PATH': os.environ.get('PATH', '')}, clear=True):
            return host.Runner(args)

    def test_external_dsn_and_credentials_rejected_before_mkdir(self):
        for name in ('LAIAI_DSN', 'ARK_TEST_MYSQL_DSN', 'B1B_PROVIDER_TOKEN'):
            with self.subTest(name=name), mock.patch.dict(os.environ, {name: 'private'}, clear=True):
                with self.assertRaisesRegex(RuntimeError, 'inherited_business_environment'):
                    host.Runner(argparse.Namespace(output=str(self.root / 'bad'), timeout_seconds=1200, compiler='host'))
                self.assertFalse((self.root / 'bad').exists())

    def test_environment_cannot_select_remote_docker_or_git_or_go_overlay(self):
        env = host.clean_environment({'PATH': 'tool', 'DOCKER_HOST': 'tcp://remote', 'DOCKER_CONTEXT': 'other',
                                      'GIT_CONFIG_COUNT': '3', 'GOFLAGS': '-overlay=bad', 'GOWORK': 'bad', 'GOENV': 'bad'})
        self.assertEqual(env['GOENV'], 'off')
        self.assertEqual(env['GOTOOLCHAIN'], 'local')
        self.assertFalse(set(env) & {'DOCKER_HOST', 'DOCKER_CONTEXT', 'GIT_CONFIG_COUNT', 'GOFLAGS', 'GOWORK'})
        self.assertEqual(host.DOCKER, ('docker', '--host', 'unix:///var/run/docker.sock'))

    def test_archive_preserves_raw_bytes_and_rejects_escape_duplicate_or_link(self):
        for names, kind in ((('a.txt',), None), (('../escape',), None), (('/absolute',), None),
                             (('same', 'same'), None), (('link',), tarfile.SYMTYPE)):
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode='w') as archive:
                for name in names:
                    info = tarfile.TarInfo(name)
                    raw = b'original\r\n\xff'
                    info.size = len(raw)
                    if kind:
                        info.type, info.linkname = kind, 'elsewhere'
                    archive.addfile(info, io.BytesIO(raw))
            if names == ('a.txt',):
                self.assertEqual(host.checked_archive(stream.getvalue())[0], {'a.txt': b'original\r\n\xff'})
            else:
                with self.subTest(names=names), self.assertRaisesRegex(RuntimeError, 'archive_member'):
                    host.checked_archive(stream.getvalue())

    def test_no_container_stop_on_identity_mismatch(self):
        runner = self.runner()
        cidfile = runner.root / 'database.cid'
        cidfile.write_text('a' * 64)
        runner.db = {'name': 'expected', 'image': 'sha256:fixed', 'cidfile': cidfile}
        row = {'Id': 'a' * 64, 'Name': '/wrong', 'Image': 'sha256:fixed', 'Config': {'Labels': {host.LABEL: runner.run_id}}}
        with mock.patch.object(host.subprocess, 'check_output', return_value=json.dumps([row]).encode()), \
                mock.patch.object(host.subprocess, 'run') as action:
            self.assertFalse(runner.finish(False))
        action.assert_not_called()
        self.assertEqual(json.loads((runner.root / 'report.json').read_text())['status'], 'failed')
        self.assertTrue((runner.root / 'export/evidence-manifest.json').is_file())

    def test_compiler_mounts_readonly_sources_and_existing_named_caches(self):
        runner = self.runner()
        runner.args.compiler = 'docker'
        runner.sources = {role: {'path': str(runner.root / (role + '-source'))} for role in ('old', 'new')}
        calls = []

        def capture(argv, *_args, **_kwargs):
            calls.append(argv)
            Path(argv[argv.index('--cidfile') + 1]).write_text(str(len(calls)) * 64)
            return b'compiler-output'

        with mock.patch.object(runner, 'command', side_effect=capture):
            runner.compiler(['go', 'version'], 'old', 'old-version.log')
            runner.compiler(['go', 'version'], 'new', 'new-version.log')
        self.assertEqual(len(runner.compiler_containers), 2)
        self.assertNotEqual(runner.compiler_containers[0]['name'], runner.compiler_containers[1]['name'])
        for argv in calls:
            mounts = [argv[index + 1] for index, value in enumerate(argv) if value == '-v']
            self.assertIn(str(runner.root / 'old-source') + ':/work/old-source:ro', mounts)
            self.assertIn(str(runner.root / 'new-source') + ':/work/new-source:ro', mounts)
            self.assertIn('laiai-ci-gomod:/go/pkg/mod', mounts)
            self.assertIn('laiai-ci-gocache:/root/.cache/go-build', mounts)
            self.assertFalse(any('docker.sock' in mount for mount in mounts))
            self.assertIn(host.TOOL_IMAGE, argv)
            env = [argv[index + 1] for index, value in enumerate(argv) if value == '-e']
            self.assertIn('CGO_ENABLED=1', env)

    def test_failed_empty_or_partial_verification_still_exports_original(self):
        for data in (b'', b'{"status":', b'{"status":"failed"}'):
            with self.subTest(data=data):
                runner = self.runner()
                (runner.evidence / 'matrix-verified.json').write_bytes(data)
                self.assertFalse(runner.finish(False))
                self.assertEqual((runner.root / 'export/evidence/matrix-verified.json').read_bytes(), data)
                # A fresh independent run path for the next negative case.
                self.root = self.root / 'next'
                self.root.mkdir()

    def test_safe_export_redacts_only_text_and_records_original_hash(self):
        runner = self.runner()
        runner.secrets = ['synthetic-private']
        raw = b'error synthetic-private'
        (runner.evidence / 'failure.log').write_bytes(raw)
        (runner.root / 'old-original.tar').write_bytes(raw)
        runner.export()
        self.assertEqual((runner.root / 'export/old-original.tar').read_bytes(), raw)
        self.assertNotIn(b'synthetic-private', (runner.root / 'export/evidence/failure.log').read_bytes())
        manifest = json.loads((runner.root / 'export/evidence-manifest.json').read_text())
        self.assertEqual(manifest['evidence/failure.log']['original_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertTrue(manifest['evidence/failure.log']['redacted'])

    @unittest.skipUnless(sys.platform == 'linux', 'real POSIX process-group cleanup requires Linux')
    def test_command_timeout_kills_actual_owned_grandchild(self):
        runner = self.runner()
        marker = runner.root / 'child.pid'
        code = ('import subprocess,sys,time,pathlib;'
                'p=subprocess.Popen([sys.executable,"-c","import time;time.sleep(60)"]);'
                f'pathlib.Path({str(marker)!r}).write_text(str(p.pid));time.sleep(60)')
        with self.assertRaisesRegex(RuntimeError, 'command_interrupted'):
            runner.command([sys.executable, '-c', code], timeout=.4)
        pid = int(marker.read_text())
        # A zombie is already terminated and can only be reaped by PID 1.
        status = Path('/proc') / str(pid) / 'stat'
        self.assertTrue(not status.exists() or status.read_text().rsplit(')', 1)[1].split()[0] == 'Z')


def synthetic_elf(interpreter=None, needed=False, unterminated=False):
    """ELF layout fixture only; it is never passed off as executable evidence."""
    segments = []
    payload = bytearray(b'\x00' * 512)
    payload[:16] = b'\x7fELF\x02\x01\x01' + b'\x00' * 9
    struct.pack_into('<HHIQQQIHHHHHH', payload, 16, 2, 62, 1, 0, 64, 0, 0, 64, 56,
                     1 + int(interpreter is not None) + int(needed or unterminated), 0, 0, 0)
    segments.append((1, 5, 0, 0, 0, len(payload), len(payload), 4096))
    if interpreter is not None:
        raw = interpreter.encode() + b'\x00'
        payload[256:256 + len(raw)] = raw
        segments.append((3, 4, 256, 0, 0, len(raw), len(raw), 1))
    if needed or unterminated:
        struct.pack_into('<qQqQ', payload, 384, 1 if needed else 5, 0, 5 if unterminated else 0, 0)
        segments.append((2, 6, 384, 0, 0, 32, 32, 8))
    for index, entry in enumerate(segments):
        struct.pack_into('<IIQQQQQQ', payload, 64 + index * 56, *entry)
    return bytes(payload)


class StaticCompilerBoundaries(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def runner(self, label='run', compiler='docker'):
        args = argparse.Namespace(output=str(self.root / label), timeout_seconds=1200, compiler=compiler)
        with mock.patch.dict(os.environ, {'PATH': os.environ.get('PATH', '')}, clear=True):
            return host.Runner(args)

    def test_real_build_command_generation_preserves_race_and_host_adapter(self):
        for adapter in ('docker', 'host'):
            with self.subTest(adapter=adapter):
                runner = self.runner(adapter, adapter)
                runner.sources = {role: {'source': 'a' * 40 if role == 'old' else 'b' * 40,
                                         'path': str(runner.root / (role + '-source'))} for role in ('old', 'new')}
                builds, commands = [], []

                def compile_command(argv, role, log):
                    builds.append((list(argv), role))
                    if argv == ['go', 'version']:
                        return (host.GO + '\n').encode()
                    if '-o' in argv:
                        Path(argv[argv.index('-o') + 1]).write_bytes(synthetic_elf() + role.encode())
                    return b''

                def command(argv, *_args, **kwargs):
                    commands.append((list(argv), kwargs))
                    if argv == ['go', 'version']:
                        return (host.GO + '\n').encode()
                    if 'inspect' in argv:
                        return ((host.TOOL_IMAGE if 'image' in argv else argv[-3]) + '\n').encode()
                    if any(value.startswith('-test.list=') for value in argv):
                        return b'TestMySQLExecutionInputBinaryHandoffProcess\n'
                    return b''

                with mock.patch.object(runner, 'compiler', side_effect=compile_command), \
                        mock.patch.object(runner, 'command', side_effect=command), \
                        mock.patch.object(runner, 'source_integrity'):
                    runner.build()
                compiled = [(argv, role) for argv, role in builds if '-o' in argv]
                self.assertEqual(len(compiled), 6)
                for argv, role in compiled:
                    is_test = argv[1] == 'test'
                    if is_test:
                        self.assertIn('-race', argv)
                        self.assertIn('-c', argv)
                    expected = []
                    if adapter == 'docker':
                        expected += ['-linkmode=external', '-extldflags=-static']
                    if is_test:
                        expected += ['-X', 'github.com/laiai/laiai-next/core/internal/video.executionHandoffCompiledSource=' + runner.sources[role]['source']]
                    if expected:
                        self.assertEqual(argv[argv.index('-ldflags') + 1], ' '.join(expected))
                    else:
                        self.assertNotIn('-ldflags', argv)
                ldd = [(argv, kwargs) for argv, kwargs in commands if argv[0] == 'ldd']
                self.assertEqual(len(ldd), 6 if adapter == 'docker' else 2)
                self.assertEqual(sum('-h' in argv for argv, _ in commands), 4 if adapter == 'docker' else 0)
                if adapter == 'host':
                    self.assertTrue(all('allowed_returncodes' not in kwargs for _, kwargs in ldd))

    def test_independent_elf_structure_rejects_malformed_and_detects_dependencies(self):
        static = synthetic_elf()
        self.assertEqual(host.elf_linkage(static)['needed_entries'], 0)
        self.assertIsNone(host.elf_linkage(static)['interpreter'])
        self.assertEqual(host.elf_linkage(synthetic_elf('/lib/ld-musl-x86_64.so.1'))['interpreter'], '/lib/ld-musl-x86_64.so.1')
        self.assertEqual(host.elf_linkage(synthetic_elf(needed=True))['needed_entries'], 1)
        mutations = {'truncated': static[:63], 'wrong_magic': b'nope' + static[4:],
                     'elf32': static[:4] + b'\x01' + static[5:],
                     'big_endian': static[:5] + b'\x02' + static[6:],
                     'unterminated_dynamic': synthetic_elf(unterminated=True)}
        for name, offset, fmt, value in [('wrong_machine', 18, '<H', 3), ('ph_overflow', 32, '<Q', 4096),
                                         ('short_phdr', 54, '<H', 48), ('missing_phdr', 56, '<H', 0),
                                         ('segment_overflow', 96, '<Q', 1024)]:
            raw = bytearray(static)
            struct.pack_into(fmt, raw, offset, value)
            mutations[name] = bytes(raw)
        for name, raw in mutations.items():
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                host.elf_linkage(raw)

    def test_dynamic_dependency_is_rejected_before_ldd_or_actual_start(self):
        for name, raw in [('interpreter', synthetic_elf('/lib/ld-musl-x86_64.so.1')),
                          ('needed_without_interpreter', synthetic_elf(needed=True))]:
            with self.subTest(name=name):
                runner = self.runner(name)
                binary = runner.root / 'video.test'
                binary.write_bytes(raw)
                with mock.patch.object(runner, 'command', return_value=b'statically linked') as command:
                    with self.assertRaisesRegex(RuntimeError, 'host_elf_dynamic_dependency'):
                        runner.check_static_host_binary(str(binary), 'old', 'video')
                self.assertEqual(command.call_count, 1)
                self.assertEqual(command.call_args.args[0][0], 'file')
                # A misleading text tool cannot override independent ELF facts.
                self.assertTrue((runner.evidence / 'old-video-host-elf.json').exists())

    def test_static_check_requires_actual_entrypoint_and_tools_start(self):
        runner = self.runner()
        binary = runner.root / 'video.test'
        binary.write_bytes(synthetic_elf())
        calls = []

        def command(argv, *args, **kwargs):
            calls.append((argv, kwargs))
            if any(v.startswith('-test.list=') for v in argv):
                return b'TestMySQLExecutionInputBinaryHandoffProcess\n'
            return b'not a dynamic executable\n'

        with mock.patch.object(runner, 'command', side_effect=command):
            runner.check_static_host_binary(str(binary), 'old', 'video')
            runner.check_static_host_binary(str(binary), 'old', 'migrate')
            runner.check_static_host_binary(str(binary), 'old', 'schema-doctor')
        self.assertEqual([argv[-1] for argv, _ in calls if argv[0] == str(binary)],
                         ['-test.list=^TestMySQLExecutionInputBinaryHandoffProcess$', '-h', '-h'])
        for argv, kwargs in calls:
            if argv[0] == 'ldd':
                self.assertEqual(kwargs['allowed_returncodes'], (0, 1))
                self.assertIsNotNone(kwargs['exit_record'])
        with mock.patch.object(runner, 'command', return_value=b'wrong test\n'):
            with self.assertRaisesRegex(RuntimeError, 'host_elf_entrypoint'):
                runner.check_static_host_binary(str(binary), 'new', 'video')
        def failed_start(argv, *args, **kwargs):
            if argv[0] == str(binary):
                raise RuntimeError('actual_start_failed')
            return b''
        with mock.patch.object(runner, 'command', side_effect=failed_start):
            with self.assertRaisesRegex(RuntimeError, 'actual_start_failed'):
                runner.check_static_host_binary(str(binary), 'new', 'video')

    def test_real_subprocess_exit_policy_is_narrow_and_logged(self):
        runner = self.runner()
        for rc in (0, 1, 2):
            log = runner.evidence / ('exit-' + str(rc) + '.log')
            record = runner.evidence / ('exit-' + str(rc) + '.json')
            argv = [sys.executable, '-c', 'import sys; print("local-process-control"); sys.exit(' + str(rc) + ')']
            if rc <= 1:
                runner.command(argv, log, allowed_returncodes=(0, 1), exit_record=record)
            else:
                with self.assertRaisesRegex(RuntimeError, 'command_failed'):
                    runner.command(argv, log, allowed_returncodes=(0, 1), exit_record=record)
            self.assertEqual(json.loads(record.read_text())['exit_code'], rc)
            self.assertIn(b'local-process-control', log.read_bytes())
        with self.assertRaisesRegex(RuntimeError, 'command_failed'):
            runner.command([sys.executable, '-c', 'raise SystemExit(1)'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
