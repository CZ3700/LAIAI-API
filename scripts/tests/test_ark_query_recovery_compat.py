"""Finite host-tool tests. Synthetic evidence is never binary/MySQL acceptance."""
import copy
from decimal import Decimal
import hashlib
import http.client
import json
from pathlib import Path
import sys
sys.dont_write_bytecode = True
import tempfile
import unittest
from unittest import mock as patching

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import ark_query_recovery_compat as host
import ark_query_recovery_matrix as matrix
import ark_query_recovery_verify as audit

RUN = '0123456789abcdef'
NEW = '1' * 40
PRICE = dict(retail_usd_per_1m='8.00000000', supplier_cny_per_1m='46.00000000',
             fx_usd_per_cny='0.14000000', hold_usd='0.86400000', supplier_reserved_cny='4.96800000',
             effective_ratio='1.00000000', rule_version='query-handoff-v1', fx_version='query-handoff-v1')


def hashed(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()


def timestamp(second):
    return '2026-10-26T00:00:%02dZ' % second


def task(ordinal, case, settled=False, review=False, version=0, bad=False, error='', due=40):
    org_index = ordinal % 2 if case == 'P01' else 0
    receipt = dict(attempt_id='query-'+str(ordinal), sequence='1', task_row_version='3',
                   outcome='accepted', http_status=200, payload_ref='payload-'+str(ordinal),
                   received_at='2026-10-26T00:00:01.%03dZ' % ordinal,
                   applied_at=timestamp(12) if settled else None,
                   due_at=None if review else timestamp(due), recovery_version=str(version),
                   error_code=error, immutable_sha256=hashed(['receipt', ordinal, bad]))
    receipt['full_sha256'] = hashed(receipt)
    amounts = dict(hold_status='captured' if settled else 'active', hold_amount_usd='0.86400000',
                   hold_captured_usd='0.00080000' if settled else '0.00000000',
                   task_cost_usd='0.00080000' if settled else '0.00000000', billing_entry_count=int(settled),
                   captured_usd='0.00080000' if settled else '0.00000000', released_usd='0.86320000' if settled else '0.00000000',
                   supplier_cny='0.00460000' if settled else '0.00000000', supplier_usd='0.00064400' if settled else '0.00000000',
                   quota_state='consumed' if settled else 'active', quota_reserved_cny='4.96800000',
                   quota_actual_cny='0.00460000' if settled else '0.00000000')
    row = dict(ordinal=ordinal, slot='natural' if ordinal == 1 and case == 'P04' else 'primary',
               org_index=org_index, org_id=str(org_index+1), key_id=str(org_index+11), task_id='task-'+str(ordinal),
               create_operation_id='create-'+str(ordinal), task_status='succeeded' if settled else 'queued',
               billing_state='settled' if settled else 'reserved', event_count=3+int(settled),
               outbox_count=3+int(settled), journal_count=int(settled), receipt=receipt, money=amounts, price=dict(PRICE))
    for key in ('task_sha256', 'state_sha256', 'event_sha256'):
        row[key] = hashed([key, ordinal, settled])
    for key in ('operation_sha256', 'billing_snapshot_sha256', 'input_sha256', 'request_body_sha256'):
        row[key] = hashed([key, ordinal])
    return row


def snapshot(rows, second=10):
    rows = copy.deepcopy(rows)
    orgs = []
    for index in (0, 1):
        chosen = [r for r in rows if r['org_index'] == index]
        settled = sum(r['billing_state'] == 'settled' for r in chosen)
        held = len(chosen)-settled
        posted = Decimal('100')-settled*Decimal('.0008')
        frozen = held*Decimal('.864')
        orgs.append(dict(org_id=str(index+1), key_id=str(index+11), posted_usd=format(posted, '.8f'),
                         held_usd=format(frozen, '.8f'), available_usd=format(posted-frozen, '.8f'),
                         key_used_usd=format(frozen+settled*Decimal('.0008'), '.8f')))
    settled = sum(r['billing_state'] == 'settled' for r in rows)
    provider = dict(reserved_cny=format((len(rows)-settled)*Decimal('4.968'), '.8f'),
                    consumed_cny=format(settled*Decimal('.0046'), '.8f'))
    return dict(database_utc=timestamp(second), task_count=len(rows), http=dict(post=0, get=0, delete=0, other=0),
                financial_sha256=hashed([orgs, provider, [r['money'] for r in rows]]),
                encrypted_evidence_sha256=hashed(['encrypted', len(rows)]),
                all_events_sha256=hashed([r['event_sha256'] for r in rows]),
                outbox_sha256=hashed([r['outbox_count'] for r in rows]), orgs=orgs, provider=provider, tasks=rows)


def scenario(case):
    artifacts = []
    commands = {}
    def emit(instance, name, rows, second=10, batch=None, **fields):
        cid = commands.get(instance, 0)+1
        commands[instance] = cid
        cmd = dict(protocol=audit.PROTOCOL, run_id=RUN, case_id=case, instance=instance,
                   cmd_id=cid, command=name, **fields)
        snap = snapshot(rows, second)
        kind = audit.COMMAND_EVENT[name]
        details = {}
        if name == 'seed':
            ids = list(range(42)) if case == 'P01' else [1] if fields['slot'] == 'natural' else [0]
            details = dict(group=fields['group'], slot=fields['slot'], ordinals=ids, post_delta=len(ids),
                           get_delta=len(ids), event_sql_failures=len(ids), receipt_insert_omits_progress=True,
                           financial_rollback_verified=True)
        elif name == 'recover':
            ids = [] if batch is None else batch
            details = dict(entry='old_combined' if instance.startswith('old-') else 'new_persisted',
                           error_classes=[], query_batches=[dict(count=len(ids), attempt_ids=ids)],
                           http_before=dict(snap['http']), http_after=dict(snap['http']))
        elif name == 'fault_json':
            old = artifacts[-1]['snapshot']['tasks']
            details = dict(ordinals=fields['ordinals'], allowed_columns=['observation_json'],
                           changes=[dict(ordinal=i, attempt_id=rows[i]['receipt']['attempt_id'],
                                         before_immutable_sha256=old[i]['receipt']['immutable_sha256'],
                                         after_immutable_sha256=rows[i]['receipt']['immutable_sha256']) for i in fields['ordinals']],
                           financial_before_sha256=snap['financial_sha256'])
        elif name == 'schedule_fixture':
            details = dict(mode=fields['mode'], slot=fields['slot'], ordinals=list(range(len(rows))),
                           allowed_columns=['recovery_due_at'], financial_before_sha256=snap['financial_sha256'])
        elif name == 'direct_apply':
            details = dict(barrier_rejection_observed=True, current_receipt_read=True, updates_observed=0,
                           error_classes=['unclassified_plain_error'], http_before=dict(snap['http']), http_after=dict(snap['http']))
        artifacts.append(dict(protocol=audit.PROTOCOL, run_id=RUN, case_id=case, instance=instance,
                              role=instance.split('-')[0], source=audit.OLD if instance.startswith('old-') else NEW,
                              cmd_id=cid, kind=kind, details=details, snapshot=snap, _command=cmd))
    group = dict(P01='bad40_good2', P02='one_good', P03='one_bad', P04='one_temporary')[case]
    writer = audit.INSTANCES[case][0]
    rows = [task(i, case) for i in range(42 if case == 'P01' else 1)]
    emit(writer, 'seed', rows, group=group, slot='primary')
    if case in ('P01', 'P03'):
        ids = list(range(40)) if case == 'P01' else [0]
        rows = [task(i, case, bad=i in ids) for i in range(len(rows))]
        emit(writer, 'fault_json', rows, ordinals=ids)
        rows = [task(i, case, bad=i in ids, due=0) for i in range(len(rows))]
        emit(writer, 'schedule_fixture', rows, mode='due_now', slot='primary')
    if case == 'P01':
        bad = ['query-'+str(i) for i in range(40)]
        emit(writer, 'recover', rows, batch=bad); emit(writer, 'recover', rows, batch=bad)
        rows = [task(i, case, settled=i >= 40, review=i < 40, version=1, bad=i < 40,
                     error='query_observation_invalid' if i < 40 else '') for i in range(42)]
        emit('new-recovery', 'recover', rows); emit('new-recovery', 'recover', rows)
    elif case == 'P02':
        rows = [task(0, case, settled=True)]
        emit('old-consumer', 'recover', rows)
        emit('new-replay', 'recover', rows); emit('new-replay', 'recover', rows)
    elif case == 'P03':
        rows = [task(0, case, review=True, version=1, bad=True, error='query_observation_invalid')]
        emit(writer, 'recover', rows)
        rows = [task(0, case, review=True, version=1, bad=True, error='query_apply_failed')]
        emit('old-observer', 'recover', rows); emit('old-observer', 'recover', rows)
        emit('new-guard', 'recover', rows); emit('new-guard', 'direct_apply', rows)
    else:
        emit(writer, 'recover', rows)
        rows = [task(0, case, settled=True)]
        emit('old-early', 'recover', rows, second=12)
        emit('new-natural', 'recover', rows, second=13)
        rows.append(task(1, case, due=50))
        emit('new-natural', 'seed', rows, second=20, group=group, slot='natural')
        emit('new-natural', 'recover', rows, second=21)
        emit('new-natural', 'inspect', rows, second=50)
        rows[1] = task(1, case, settled=True, version=1, due=50)
        emit('new-natural', 'recover', rows, second=50)
        emit('new-natural', 'recover', rows, second=51)
    return artifacts


def render(evidence, cases):
    root = evidence / 'matrix'
    root.mkdir()
    builds = dict(old=dict(source=audit.OLD, sha256='a'*64), new=dict(source=NEW, sha256='b'*64))
    def put(path, value):
        path.write_text(json.dumps(value, sort_keys=True), encoding='utf-8')
    put(evidence / 'build-manifest.json', builds)
    exits, mock = [], []
    for case, artifacts in cases.items():
        directory = root / case
        directory.mkdir()
        for instance in audit.INSTANCES[case]:
            role = instance.split('-')[0]
            pid = len(exits)+100
            exits.append(dict(case_id=case, instance=instance, role=role, pid=pid,
                              source=builds[role]['source'], binary_sha256=builds[role]['sha256'],
                              exit_code=0, clean_exit=True, forced_stop=False))
            events, commands = [], []
            def event(kind, cid, data):
                events.append(dict(protocol=audit.PROTOCOL, run_id=RUN, case_id=case, instance=instance,
                                   role=role, source=builds[role]['source'], pid=pid,
                                   seq=len(events)+1, cmd_id=cid, event=kind, data=data))
            event('ready', 0, dict(schema='ark_test_qhandoff_'+case[-1]*24, migration_max=136,
                                  has_persisted_consumer=role == 'new', has_dispatch_consumer=role == 'new', task_count=0))
            for art in [a for a in artifacts if a['instance'] == instance]:
                commands.append(art['_command'])
                raw = json.dumps({k:v for k,v in art.items() if k != '_command'}, sort_keys=True).encode()
                filename = instance+'-%04d-' % art['cmd_id']+art['kind']+'.json'
                (directory / filename).write_bytes(raw)
                event(art['kind'], art['cmd_id'], dict(artifact=filename, sha256=hashlib.sha256(raw).hexdigest()))
                event('command_done', art['cmd_id'], dict(command=art['_command']['command'], completed_commands=art['cmd_id']))
                if art['kind'] == 'seeded':
                    for ordinal in art['details']['ordinals']:
                        for method in ('POST', 'GET'):
                            base = '/api/v3/contents/generations/tasks'
                            path = base if method == 'POST' else base+'/qhandoff_'+RUN+'_'+case+'_'+str(ordinal)
                            mock.append(dict(sequence=len(mock)+1, run_id=RUN, case_id=case, instance=instance,
                                             cmd_id=art['cmd_id'], ordinal=ordinal, method=method, path=path, status=200,
                                             body_sha256=art['snapshot']['tasks'][ordinal]['request_body_sha256'] if method == 'POST' else hashlib.sha256(b'').hexdigest(),
                                             body_bytes=100 if method == 'POST' else 0))
            cid = len(commands)+1
            commands.append(dict(protocol=audit.PROTOCOL, run_id=RUN, case_id=case, instance=instance, cmd_id=cid, command='stop'))
            event('command_done', cid, dict(command='stop', completed_commands=cid))
            event('drained', cid, dict(completed_commands=cid))
            (root / (case+'-'+instance+'-commands.jsonl')).write_text(''.join(json.dumps(c)+'\n' for c in commands), encoding='utf-8')
            (root / (case+'-'+instance+'.log')).write_text(''.join(audit.PREFIX+json.dumps(e)+'\n' for e in events)+'--- PASS: '+audit.TEST+' (0.01s)\nPASS\n', encoding='utf-8', newline='\n')
        put(root / (case+'-result.json'), dict(status='passed', case_id=case, seed=artifacts[0]['snapshot'],
            final=artifacts[-1]['snapshot'], http=[c for c in mock if c['case_id'] == case]))
    put(root / 'child-exits.json', exits)
    (root / 'mock-requests.jsonl').write_text(''.join(json.dumps(c)+'\n' for c in mock), encoding='utf-8')
    put(root / 'matrix-passed.json', dict(status='passed', run_id=RUN, cases_passed=list(audit.CASES), cases=4,
        actual_processes=11, old_source=audit.OLD, new_source=NEW))


class QueryRecoveryHostTests(unittest.TestCase):
    def data(self):
        return {case: scenario(case) for case in audit.CASES}

    def audit_data(self, data):
        with tempfile.TemporaryDirectory() as directory:
            render(Path(directory), data)
            return audit.verify(directory)

    def test_complete_four_synthetic_cases(self):
        result = self.audit_data(self.data())
        self.assertEqual((result['cases'], result['actual_processes'], result['raw_http_requests']), (4, 11, 92))

    def test_financial_original_price_and_immutable_tampering_rejected(self):
        for case, key in [('P01', 'receipt'), ('P02', 'operation_sha256'), ('P03', 'input_sha256'), ('P04', 'billing_snapshot_sha256'), ('P02', 'price'), ('P04', 'money')]:
            with self.subTest(case=case, field=key):
                data = self.data()
                # Modify both replay proofs AND the rendered result. Neither
                # their mutual equality nor their hashes establish provenance.
                for art in data[case][-2:]:
                    row = art['snapshot']['tasks'][0]
                    if key == 'receipt': row[key]['immutable_sha256'] = '9'*64
                    elif key == 'price': row[key]['retail_usd_per_1m'] = '9.00000000'
                    elif key == 'money': row[key]['supplier_cny'] = '0.00000000'
                    else: row[key] = '9'*64
                with self.assertRaises(ValueError): self.audit_data(data)

    def test_held_and_settled_key_usage_have_distinct_literal_oracles(self):
        for settled in (False, True):
            snap = snapshot([task(0, 'P02', settled=settled)])
            audit.check_snapshot(snap)
            snap['orgs'][0]['key_used_usd'] = '0.86400000' if settled else '0.00080000'
            with self.assertRaisesRegex(ValueError, 'literal_org_money'): audit.check_snapshot(snap)

    def test_null_review_and_natural_expiry_are_not_success_markers(self):
        for mode in ('old_window', 'null', 'early_natural', 'ciphertext', 'new_http', 'settlement_event'):
            with self.subTest(mode=mode):
                data = self.data()
                if mode == 'old_window': data['P01'][3]['details']['query_batches'][0]['attempt_ids'][0] = 'healthy'
                elif mode == 'null': data['P03'][-1]['snapshot']['tasks'][0]['receipt']['due_at'] = timestamp(40)
                elif mode == 'early_natural': data['P04'][-2]['snapshot']['database_utc'] = timestamp(49)
                elif mode == 'ciphertext': data['P02'][-1]['snapshot']['encrypted_evidence_sha256'] = '9'*64
                elif mode == 'new_http': data['P02'][-1]['details']['http_after']['get'] = 1
                else:
                    for art in data['P02'][1:]:
                        art['snapshot']['tasks'][0]['event_count'] += 1
                        art['snapshot']['tasks'][0]['outbox_count'] += 1
                with self.assertRaises(ValueError): self.audit_data(data)

    def test_fixture_financial_and_nonallowed_columns_bind_previous_raw_proof(self):
        for mode in ('coordinated_money', 'task', 'state', 'event', 'receipt_version'):
            with self.subTest(mode=mode):
                data = self.data()
                art = data['P01'][1]
                if mode == 'coordinated_money':
                    art['snapshot']['financial_sha256'] = art['details']['financial_before_sha256'] = '9'*64
                elif mode == 'receipt_version': art['snapshot']['tasks'][0]['receipt']['recovery_version'] = '1'
                else: art['snapshot']['tasks'][0][mode+'_sha256'] = '9'*64
                with self.assertRaises(ValueError): self.audit_data(data)

    def test_raw_lifecycle_and_mock_command_tampering_rejected(self):
        for mode in ('run', 'source', 'stop_ack', 'forced', 'partial', 'result', 'mock_command', 'unknown_case', 'go_fail', 'same_binary', 'three_cases', 'missing_done', 'schema_before_expand', 'schema_prior_134', 'schema_prior_135', 'schema_future'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); render(root, self.data()); mat = root/'matrix'
                if mode in ('run', 'source', 'stop_ack', 'go_fail', 'missing_done', 'schema_before_expand', 'schema_prior_134', 'schema_prior_135', 'schema_future'):
                    path = mat/'P02-old-consumer.log'
                    raw = path.read_text()
                    if mode == 'run': raw = raw.replace(RUN, 'f'*16)
                    elif mode == 'source': raw = raw.replace(audit.OLD, NEW)
                    elif mode == 'stop_ack': raw = raw.replace('"event": "drained"', '"event": "command_done"')
                    elif mode == 'schema_before_expand': raw = raw.replace('"migration_max": 136', '"migration_max": 133')
                    elif mode == 'schema_prior_134': raw = raw.replace('"migration_max": 136', '"migration_max": 134')
                    elif mode == 'schema_prior_135': raw = raw.replace('"migration_max": 136', '"migration_max": 135')
                    elif mode == 'schema_future': raw = raw.replace('"migration_max": 136', '"migration_max": 137')
                    elif mode == 'missing_done': raw = '\n'.join(line for line in raw.splitlines() if '"event": "command_done"' not in line)+'\n'
                    else: raw += '    --- FAIL: hidden (0.01s)\n'
                    path.write_text(raw, newline='\n')
                elif mode == 'forced':
                    path = mat/'child-exits.json'; value = json.loads(path.read_text()); value[0]['forced_stop'] = True; path.write_text(json.dumps(value))
                elif mode == 'partial': (mat/'matrix-failed.json').write_text('{}')
                elif mode == 'result':
                    path = mat/'P02-result.json'; value = json.loads(path.read_text()); value['final']['financial_sha256'] = '9'*64; path.write_text(json.dumps(value))
                elif mode == 'same_binary':
                    path = root/'build-manifest.json'; value = json.loads(path.read_text()); value['new']['sha256'] = value['old']['sha256']; path.write_text(json.dumps(value))
                elif mode == 'three_cases':
                    path = mat/'matrix-passed.json'; value = json.loads(path.read_text()); value['cases_passed'].pop(); path.write_text(json.dumps(value))
                else:
                    path = mat/'mock-requests.jsonl'; records = [json.loads(s) for s in path.read_text().splitlines()]
                    if mode == 'mock_command': records[-1]['cmd_id'] += 1
                    else: records[-1]['case_id'] = 'P05'
                    path.write_text(''.join(json.dumps(r)+'\n' for r in records))
                    # Also alter result.http: the independent verifier must
                    # reject a coordinated change to both representations.
                    result = mat/'P04-result.json'; value = json.loads(result.read_text()); value['http'] = [r for r in records if r['case_id'] == 'P04']; result.write_text(json.dumps(value))
                with self.assertRaises(ValueError): audit.verify(root)

    def test_mock_accepts_only_active_seed_and_records_actual_body(self):
        with tempfile.TemporaryDirectory() as directory:
            mock = matrix.Mock(RUN, Path(directory), {c:'synthetic-token' for c in audit.CASES})
            try:
                mock.seed_window('P02', 'new-writer', [0], 7)
                connection = http.client.HTTPConnection('127.0.0.1', mock.server.server_port, timeout=3)
                headers = {'X-Query-Handoff-Run':RUN, 'X-Query-Handoff-Case':'P02', 'X-Query-Handoff-Instance':'new-writer', 'X-Query-Handoff-Ordinal':'0', 'Authorization':'Bearer synthetic-token'}
                body = b'{"model":"synthetic"}'
                connection.request('POST', '/api/v3/contents/generations/tasks', body=body, headers=headers)
                response = connection.getresponse(); self.assertEqual(response.status, 200); response.read()
                mock.end_seed()
                connection.request('GET', '/api/v3/contents/generations/tasks/qhandoff_'+RUN+'_P02_0', headers=headers)
                response = connection.getresponse(); self.assertEqual(response.status, 400); response.read(); connection.close()
                self.assertEqual(mock.requests[0]['cmd_id'], 7)
                self.assertEqual(mock.requests[0]['body_sha256'], hashlib.sha256(body).hexdigest())
                self.assertEqual(mock.requests[1]['cmd_id'], 0)
            finally: mock.close()

    def test_private_bridge_and_no_host_port(self):
        row = dict(HostConfig=dict(PortBindings={}, NetworkMode='bridge'),
                   NetworkSettings=dict(Ports={'3306/tcp':None}, Networks={'bridge':dict(NetworkID='a'*64, IPAddress='172.17.0.2')}))
        self.assertEqual(host.bridge_address(row)[0], '172.17.0.2')
        for address in ('127.0.0.1', '8.8.8.8', '169.254.1.1', '0.0.0.0', '::1'):
            bad = copy.deepcopy(row); bad['NetworkSettings']['Networks']['bridge']['IPAddress'] = address
            with self.assertRaises(ValueError): host.bridge_address(bad)
        row['HostConfig']['PortBindings'] = {'3306/tcp':[{'HostPort':'3306'}]}
        with self.assertRaises(ValueError): host.bridge_address(row)

    def test_compile_adapter_and_inherited_business_env_fail_closed(self):
        self.assertEqual(host.link_flags('host'), [])
        static = host.link_flags('docker', NEW)
        self.assertIn('-linkmode=external', static[1]); self.assertIn('-extldflags=-static', static[1])
        self.assertIn('queryHandoffCompiledSource='+NEW, static[1])
        self.assertEqual(host.clean_environment({'PATH':'safe'})['CGO_ENABLED'], '1')
        for key in ('LAIAI_DSN', 'ARK_TEST_MYSQL_DSN', 'ARK_QUERY_HANDOFF_DSN', 'QUERY_HANDOFF_DSNS'):
            with self.assertRaises(ValueError): host.clean_environment({key:'forbidden'})

    def test_mysql_initialization_retries_only_actual_command_status(self):
        runner = object.__new__(host.Runner)
        with patching.patch.object(host.time, 'sleep') as sleep:
            runner.sql = patching.Mock(side_effect=[ValueError('command_failed'), ValueError('command_failed'), b'1\n'])
            runner.wait_database()
            self.assertEqual((runner.sql.call_count, sleep.call_count), (3, 2))
            for error in (ValueError('owned_identity'), ValueError('database_network_changed'), RuntimeError('command_interrupted')):
                runner.sql = patching.Mock(side_effect=error); sleep.reset_mock()
                with self.assertRaises(type(error)): runner.wait_database()
                self.assertEqual(runner.sql.call_count, 1); sleep.assert_not_called()
            runner.sql = patching.Mock(return_value=b'2\n')
            with self.assertRaisesRegex(ValueError, 'mysql_ready'): runner.wait_database()
            runner.sql = patching.Mock(side_effect=ValueError('command_failed'))
            with self.assertRaisesRegex(RuntimeError, 'mysql_ready_timeout'): runner.wait_database()
            self.assertEqual(runner.sql.call_count, 120)

    def test_export_publication_is_complete_or_failed_and_private_seed_is_excluded(self):
        for failure in ('none', 'copy', 'publish', 'cancel', 'publish_cancel'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                runner = object.__new__(host.Runner)
                runner.root = Path(directory); runner.evidence = runner.root/'evidence'; runner.evidence.mkdir()
                runner.entries, runner.secrets, runner.export_files = [], ['synthetic-sensitive-token'], set()
                runner.cancelled = False
                runner.report = dict(status='running', run_id=RUN)
                runner.integrity = lambda: None
                private = runner.evidence/'matrix'/'P02'; private.mkdir(parents=True)
                (private/'seed-manifest.json').write_text('private synthetic key')
                raw = runner.artifact('evidence/sample.log'); raw.write_text('synthetic-sensitive-token')
                original_redact = host.redact_export
                def redact(value, secrets):
                    if failure == 'copy': raise ValueError('synthetic_export_failure')
                    if failure == 'cancel': runner.cancelled = True
                    return original_redact(value, secrets)
                original_rename = Path.rename
                def rename(path, target):
                    if failure == 'publish': raise OSError('synthetic_publish_failure')
                    result = original_rename(path, target)
                    if failure == 'publish_cancel' and Path(target).name == 'export': runner.cancelled = True
                    return result
                with patching.patch.object(host, 'redact_export', side_effect=redact), patching.patch.object(Path, 'rename', rename):
                    self.assertEqual(runner.finish(True), failure == 'none')
                report = json.loads((runner.root/'report.json').read_text())
                self.assertEqual(report['status'], 'passed' if failure == 'none' else 'failed')
                self.assertEqual(raw.read_text(), 'synthetic-sensitive-token')
                if failure in ('copy', 'publish', 'publish_cancel'):
                    self.assertFalse((runner.root/'export').exists())
                    self.assertTrue((runner.root/'export.pending').is_dir())
                else:
                    output = runner.root/'export'
                    self.assertFalse((output/'evidence/matrix/P02/seed-manifest.json').exists())
                    self.assertNotIn('synthetic-sensitive-token', (output/'evidence/sample.log').read_text())
                    manifest = json.loads((output/'evidence-manifest.json').read_text())
                    self.assertEqual(json.loads((output/'report.json').read_text()), report)
                    self.assertEqual(manifest['report.json']['original_sha256'], hashlib.sha256((runner.root/'report.json').read_bytes()).hexdigest())
                    for path, item in manifest.items():
                        self.assertEqual(item['export_sha256'], hashlib.sha256((output/path).read_bytes()).hexdigest())

    def test_strict_json_rejects_duplicate_and_nonfinite(self):
        for raw in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}'):
            with self.assertRaises(ValueError): audit.strict_json(raw)


if __name__ == '__main__':
    unittest.main()
