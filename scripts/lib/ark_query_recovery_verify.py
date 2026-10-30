"""Independent raw-evidence verifier for the four query-recovery handoffs."""
from __future__ import annotations

import collections
import datetime as dt
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import sys

PROTOCOL = 'query-recovery-handoff-v1'
PREFIX = 'QUERY_HANDOFF_EVENT '
OLD = 'a34a46c0b46bee63c9509224a1b6ebbf8e3fca8c'
TEST = 'TestMySQLQueryRecoveryBinaryHandoffProcess'
INSTANCES = {
    'P01': ('old-writer', 'new-recovery'),
    'P02': ('new-writer', 'old-consumer', 'new-replay'),
    'P03': ('new-writer', 'old-observer', 'new-guard'),
    'P04': ('new-writer', 'old-early', 'new-natural'),
}
CASES = tuple(INSTANCES)
COMMAND_EVENT = dict(seed='seeded', fault_json='fault_applied', schedule_fixture='scheduled',
                     recover='cycle', inspect='snapshot', direct_apply='direct_result')
EVENTS = set(COMMAND_EVENT.values()) | {'ready', 'command_done', 'drained'}
PRICE = dict(retail_usd_per_1m='8.00000000', supplier_cny_per_1m='46.00000000',
             fx_usd_per_cny='0.14000000', hold_usd='0.86400000', supplier_reserved_cny='4.96800000',
             effective_ratio='1.00000000', rule_version='query-handoff-v1', fx_version='query-handoff-v1')
SNAPSHOT_KEYS = {'database_utc', 'task_count', 'http', 'financial_sha256', 'encrypted_evidence_sha256',
                 'all_events_sha256', 'outbox_sha256', 'orgs', 'provider', 'tasks'}
TASK_KEYS = {'ordinal', 'slot', 'org_index', 'org_id', 'key_id', 'task_id', 'create_operation_id',
            'task_status', 'billing_state', 'task_sha256', 'state_sha256', 'operation_sha256',
            'event_count', 'event_sha256', 'outbox_count', 'journal_count', 'billing_snapshot_sha256',
            'input_sha256', 'request_body_sha256', 'receipt', 'money', 'price'}
RECEIPT_KEYS = {'attempt_id', 'sequence', 'task_row_version', 'outcome', 'http_status', 'payload_ref',
               'received_at', 'applied_at', 'due_at', 'recovery_version', 'error_code',
               'immutable_sha256', 'full_sha256'}
MONEY_KEYS = {'hold_status', 'hold_amount_usd', 'hold_captured_usd', 'task_cost_usd', 'billing_entry_count',
             'captured_usd', 'released_usd', 'supplier_cny', 'supplier_usd', 'quota_state',
             'quota_reserved_cny', 'quota_actual_cny'}
ZERO = '0.00000000'


def need(condition, code):
    if not condition:
        raise ValueError(code)


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            need(key not in result, 'duplicate_json_key')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError('nonfinite_json')))


def raw_file(path, limit=64 * 1024 * 1024):
    need(path.is_file() and not path.is_symlink() and path.stat().st_size <= limit, 'artifact_file')
    raw = path.read_bytes()
    need(len(raw) <= limit, 'artifact_size')
    return raw


def load(path):
    return strict_json(raw_file(path))


def keys(value, expected, code):
    need(isinstance(value, dict) and set(value) == set(expected), code)


def integer(value, minimum=0, maximum=1000000):
    need(type(value) is int and minimum <= value <= maximum, 'bounded_integer')
    return value


def uint(value, positive=True):
    need(isinstance(value, str) and re.fullmatch(r'0|[1-9][0-9]*', value) is not None, 'uint_string')
    result = int(value)
    need((result > 0 if positive else result >= 0) and result <= 2**64 - 1, 'uint_range')
    return result


def digest(value):
    need(isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None, 'sha256')


def utc(value):
    need(isinstance(value, str) and re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z', value) is not None, 'utc_timestamp')
    return dt.datetime.fromisoformat(value[:-1] + '+00:00')


def money(value):
    need(isinstance(value, str) and re.fullmatch(r'(?:0|[1-9][0-9]*)\.[0-9]{8}', value) is not None, 'exact_money')
    return Decimal(value)


def fixed(value):
    return format(value, '.8f')


def http_counts(value):
    keys(value, ('post', 'get', 'delete', 'other'), 'http_shape')
    for count in value.values():
        integer(count)
    need(value['delete'] == value['other'] == 0, 'unexpected_http_method')


def check_snapshot(snapshot):
    keys(snapshot, SNAPSHOT_KEYS, 'snapshot_shape')
    utc(snapshot['database_utc'])
    integer(snapshot['task_count'], 1, 42)
    http_counts(snapshot['http'])
    for name in ('financial_sha256', 'encrypted_evidence_sha256', 'all_events_sha256', 'outbox_sha256'):
        digest(snapshot[name])
    rows = snapshot['tasks']
    need(isinstance(rows, list) and len(rows) == snapshot['task_count'], 'task_count')
    need([r['ordinal'] for r in rows] == list(range(len(rows))), 'task_ordinals')
    aggregates = collections.defaultdict(lambda: [0, 0])
    identities = {}
    for row in rows:
        keys(row, TASK_KEYS, 'task_shape')
        integer(row['ordinal'], 0, 41)
        integer(row['org_index'], 0, 1)
        need(row['slot'] in ('primary', 'natural'), 'task_slot')
        uint(row['org_id']); uint(row['key_id'])
        for name in ('task_id', 'create_operation_id'):
            need(isinstance(row[name], str) and 0 < len(row[name]) <= 128, 'task_identity')
        for name in ('task_sha256', 'state_sha256', 'operation_sha256', 'event_sha256', 'billing_snapshot_sha256', 'input_sha256', 'request_body_sha256'):
            digest(row[name])
        for name in ('event_count', 'outbox_count', 'journal_count'):
            integer(row[name])
        need(row['event_count'] == row['outbox_count'], 'event_outbox_count')
        need(row['price'] == PRICE, 'original_price_fx')
        receipt = row['receipt']
        keys(receipt, RECEIPT_KEYS, 'receipt_shape')
        for name in ('attempt_id', 'payload_ref'):
            need(isinstance(receipt[name], str) and 0 < len(receipt[name]) <= 128, 'receipt_identity')
        uint(receipt['sequence']); uint(receipt['task_row_version']); uint(receipt['recovery_version'], False)
        need(receipt['sequence'] == '1' and receipt['http_status'] == 200 and receipt['outcome'] == 'accepted', 'real_query_receipt')
        utc(receipt['received_at'])
        for name in ('applied_at', 'due_at'):
            if receipt[name] is not None:
                utc(receipt[name])
        digest(receipt['immutable_sha256']); digest(receipt['full_sha256'])
        need(receipt['error_code'] in ('', 'query_apply_failed', 'query_observation_invalid'), 'receipt_error_code')
        amounts = row['money']
        keys(amounts, MONEY_KEYS, 'money_shape')
        for name, value in amounts.items():
            if name not in ('hold_status', 'quota_state', 'billing_entry_count'):
                money(value)
        integer(amounts['billing_entry_count'], 0, 1)
        settled = row['billing_state'] == 'settled'
        need(row['journal_count'] == int(settled), 'single_hold_capture_journal')
        need(row['billing_state'] in ('reserved', 'settled'), 'financial_state')
        need(row['task_status'] == ('succeeded' if settled else 'queued'), 'task_status')
        expected = dict(hold_status='captured' if settled else 'active', hold_amount_usd='0.86400000',
                        hold_captured_usd='0.00080000' if settled else ZERO,
                        task_cost_usd='0.00080000' if settled else ZERO, billing_entry_count=int(settled),
                        captured_usd='0.00080000' if settled else ZERO,
                        released_usd='0.86320000' if settled else ZERO,
                        supplier_cny='0.00460000' if settled else ZERO,
                        supplier_usd='0.00064400' if settled else ZERO,
                        quota_state='consumed' if settled else 'active', quota_reserved_cny='4.96800000',
                        quota_actual_cny='0.00460000' if settled else ZERO)
        need(amounts == expected, 'literal_task_money')
        need((receipt['applied_at'] is not None) == settled, 'receipt_settlement_state')
        aggregates[row['org_id']][int(settled)] += 1
        prior = identities.setdefault(row['org_id'], (row['org_index'], row['key_id']))
        need(prior == (row['org_index'], row['key_id']), 'org_key_binding')
    need(len({r['task_id'] for r in rows}) == len(rows) and len({r['receipt']['attempt_id'] for r in rows}) == len(rows), 'duplicate_task_or_receipt')
    # The shared harness provisions two real tenants even in one-task cases.
    # The unused tenant remains a separately checked 100/0/0 positive control.
    need(isinstance(snapshot['orgs'], list) and len(snapshot['orgs']) == 2, 'org_count')
    seen = set()
    for index, org in enumerate(snapshot['orgs']):
        keys(org, ('org_id', 'key_id', 'posted_usd', 'held_usd', 'available_usd', 'key_used_usd'), 'org_money_shape')
        uint(org['org_id']); uint(org['key_id'])
        need(org['org_id'] not in seen, 'org_identity')
        if org['org_id'] in identities:
            need((index, org['key_id']) == identities[org['org_id']], 'org_key_index')
        seen.add(org['org_id'])
        held, settled = aggregates.get(org['org_id'], [0, 0])
        posted = Decimal('100') - settled * Decimal('0.0008')
        frozen = held * Decimal('0.864')
        expected = dict(posted_usd=fixed(posted), held_usd=fixed(frozen), available_usd=fixed(posted-frozen),
                        key_used_usd=fixed(frozen+settled*Decimal('0.0008')))
        need(all(org[k] == v for k, v in expected.items()), 'literal_org_money')
    need(set(aggregates).issubset(seen) and len({o['key_id'] for o in snapshot['orgs']}) == 2, 'missing_org')
    held = sum(pair[0] for pair in aggregates.values())
    settled = sum(pair[1] for pair in aggregates.values())
    need(snapshot['provider'] == dict(reserved_cny=fixed(held*Decimal('4.968')), consumed_cny=fixed(settled*Decimal('0.0046'))), 'literal_provider_money')


def stable_snapshot(left, right):
    # Independent processes have independent HTTP counters and observation time.
    return {k: v for k, v in left.items() if k not in ('database_utc', 'http')} == {k: v for k, v in right.items() if k not in ('database_utc', 'http')}


def command_shape(command, case, instance, run):
    common = {'protocol', 'run_id', 'case_id', 'instance', 'cmd_id', 'command'}
    name = command.get('command')
    extra = {'seed': {'group', 'slot'}, 'fault_json': {'ordinals'}, 'schedule_fixture': {'mode', 'slot'},
             'recover': set(), 'inspect': set(), 'direct_apply': set(), 'stop': set()}
    need(name in extra and set(command) == common | extra[name], 'command_shape')
    need(command['protocol'] == PROTOCOL and command['case_id'] == case and command['instance'] == instance and command['run_id'] == run, 'command_identity')
    integer(command['cmd_id'], 1)
    if name == 'seed':
        group = {'P01': 'bad40_good2', 'P02': 'one_good', 'P03': 'one_bad', 'P04': 'one_temporary'}[case]
        need(command['group'] == group and command['slot'] in ('primary', 'natural'), 'seed_spec')
        need(command['slot'] != 'natural' or (case == 'P04' and instance == 'new-natural'), 'natural_owner')
        need(command['slot'] != 'primary' or instance == INSTANCES[case][0], 'primary_seed_owner')
    if name == 'fault_json':
        need(command['ordinals'] == (list(range(40)) if case == 'P01' else [0]) and case in ('P01', 'P03'), 'fault_ordinals')
    if name == 'schedule_fixture':
        need(command['mode'] in ('due_now', 'park_dispatch') and command['slot'] in ('primary', 'natural'), 'schedule_spec')
        need(command['mode'] != 'due_now' or (case in ('P01', 'P03') and command['slot'] == 'primary'), 'natural_due_edit')
    if name == 'direct_apply':
        need(case == 'P03' and instance == 'new-guard', 'direct_owner')


def read_artifact(case_dir, event):
    keys(event['data'], ('artifact', 'sha256'), 'artifact_reference')
    name = event['instance'] + '-' + format(event['cmd_id'], '04d') + '-' + event['event'] + '.json'
    need(event['data']['artifact'] == name, 'artifact_owner_name')
    raw = raw_file(case_dir / name, 4 * 1024 * 1024)
    need(hashlib.sha256(raw).hexdigest() == event['data']['sha256'], 'artifact_digest')
    value = strict_json(raw)
    keys(value, ('protocol', 'run_id', 'case_id', 'instance', 'role', 'source', 'cmd_id', 'kind', 'details', 'snapshot'), 'artifact_shape')
    for key in ('protocol', 'run_id', 'case_id', 'instance', 'role', 'source', 'cmd_id'):
        need(value[key] == event[key], 'artifact_identity')
    need(value['kind'] == event['event'], 'artifact_kind')
    check_snapshot(value['snapshot'])
    return value


def read_child(root, case, instance, run, builds, exit_row):
    role = instance.split('-')[0]
    need(exit_row['case_id'] == case and exit_row['instance'] == instance and exit_row['role'] == role and
         exit_row['source'] == builds[role]['source'] and exit_row['binary_sha256'] == builds[role]['sha256'], 'child_build')
    need(exit_row['exit_code'] == 0 and exit_row['clean_exit'] is True and exit_row['forced_stop'] is False, 'child_exit')
    text = raw_file(root / (case + '-' + instance + '.log')).decode('utf-8', errors='strict')
    need(not re.search(r'DATA RACE|--- (?:FAIL|SKIP):|^panic:', text, re.M), 'go_failure')
    need(text.count('--- PASS: ' + TEST) == 1 and '\nPASS\n' in text, 'actual_go_pass')
    events = [strict_json(line[len(PREFIX):]) for line in text.splitlines() if line.startswith(PREFIX)]
    commands = [strict_json(line) for line in raw_file(root / (case + '-' + instance + '-commands.jsonl')).splitlines()]
    need(commands and [c['cmd_id'] for c in commands] == list(range(1, len(commands)+1)), 'command_sequence')
    need(events and [e['seq'] for e in events] == list(range(1, len(events)+1)), 'event_sequence')
    for event in events:
        keys(event, ('protocol', 'run_id', 'case_id', 'instance', 'role', 'source', 'pid', 'seq', 'cmd_id', 'event', 'data'), 'event_shape')
        need(event['protocol'] == PROTOCOL and event['run_id'] == run and event['case_id'] == case and
             event['instance'] == instance and event['role'] == role and event['source'] == builds[role]['source'] and
             event['pid'] == exit_row['pid'] and event['event'] in EVENTS, 'event_identity')
    ready = events[0]
    need(ready['event'] == 'ready' and ready['cmd_id'] == 0 and sum(e['event'] == 'ready' for e in events) == 1, 'ready_event')
    keys(ready['data'], ('schema', 'migration_max', 'has_persisted_consumer', 'has_dispatch_consumer', 'task_count'), 'ready_shape')
    need(re.fullmatch(r'ark_test_qhandoff_[0-9a-f]{24}', ready['data']['schema']) is not None and
         ready['data']['migration_max'] == 136 and ready['data']['has_persisted_consumer'] is (role == 'new') and
         ready['data']['has_dispatch_consumer'] is (role == 'new'), 'real_role_interface')
    integer(ready['data']['task_count'], 0, 42)
    artifacts = []
    need(commands[-1]['command'] == 'stop' and sum(c['command'] == 'stop' for c in commands) == 1, 'stop_once')
    expected_event_order = [('ready', 0)]
    for command in commands:
        command_shape(command, case, instance, run)
        name, cid = command['command'], command['cmd_id']
        owned = [event for event in events if event['cmd_id'] == cid]
        expected = ['command_done', 'drained'] if name == 'stop' else [COMMAND_EVENT[name], 'command_done']
        need([e['event'] for e in owned] == expected, 'command_event_ownership')
        done = owned[-2] if name == 'stop' else owned[-1]
        need(done['data'] == dict(command=name, completed_commands=cid), 'command_done')
        if name == 'stop':
            need(owned[-1]['data'] == dict(completed_commands=cid), 'actual_drain')
        else:
            artifact = read_artifact(root / case, owned[0])
            artifact['_command'] = command
            artifacts.append(artifact)
        expected_event_order.extend((kind, cid) for kind in expected)
    need([(e['event'], e['cmd_id']) for e in events] == expected_event_order, 'event_order_or_unowned')
    return artifacts, ready['data']['schema']


def details_shape(artifact):
    kind, details, command = artifact['kind'], artifact['details'], artifact['_command']
    if kind == 'seeded':
        keys(details, ('group', 'slot', 'ordinals', 'post_delta', 'get_delta', 'event_sql_failures', 'receipt_insert_omits_progress', 'financial_rollback_verified'), 'seed_details')
        count = 42 if artifact['case_id'] == 'P01' else 1
        ordinals = list(range(42)) if count == 42 else ([1] if command['slot'] == 'natural' else [0])
        need(details['group'] == command['group'] and details['slot'] == command['slot'] and details['ordinals'] == ordinals, 'seed_command')
        need(details['post_delta'] == details['get_delta'] == count and details['event_sql_failures'] >= count and
             details['receipt_insert_omits_progress'] is True and details['financial_rollback_verified'] is True, 'real_seed_rollback')
    elif kind == 'cycle':
        keys(details, ('entry', 'error_classes', 'query_batches', 'http_before', 'http_after'), 'cycle_details')
        need(details['entry'] == ('old_combined' if artifact['role'] == 'old' else 'new_persisted'), 'real_entrypoint')
        need(isinstance(details['error_classes'], list) and all(x in ('query_decode', 'context_deadline') for x in details['error_classes']), 'unexpected_cycle_error')
        need(artifact['role'] != 'old' or not details['error_classes'], 'old_return_semantics')
        need(len(details['query_batches']) <= 1, 'production_query_round')
        for batch in details['query_batches']:
            keys(batch, ('count', 'attempt_ids'), 'batch_shape')
            integer(batch['count'], 0, 40)
            need(len(batch['attempt_ids']) == batch['count'] and len(set(batch['attempt_ids'])) == batch['count'], 'bounded_real_batch')
        http_counts(details['http_before']); http_counts(details['http_after'])
        need(details['http_before'] == details['http_after'], 'recovery_http')
    elif kind == 'fault_applied':
        keys(details, ('ordinals', 'allowed_columns', 'changes', 'financial_before_sha256'), 'fault_details')
        need(details['ordinals'] == command['ordinals'] and details['allowed_columns'] == ['observation_json'], 'fault_columns')
        need([c['ordinal'] for c in details['changes']] == details['ordinals'], 'fault_changes')
        for change in details['changes']:
            keys(change, ('ordinal', 'attempt_id', 'before_immutable_sha256', 'after_immutable_sha256'), 'fault_change_shape')
    elif kind == 'scheduled':
        keys(details, ('mode', 'slot', 'ordinals', 'allowed_columns', 'financial_before_sha256'), 'schedule_details')
        need(details['mode'] == command['mode'] and details['slot'] == command['slot'], 'schedule_command')
        expected = ['recovery_due_at'] if command['mode'] == 'due_now' else ['video_task_states.next_poll_at', 'video_provider_rates.next_allowed_at']
        need(details['allowed_columns'] == expected, 'schedule_columns')
    elif kind == 'snapshot':
        need(details == {}, 'snapshot_details')
    elif kind == 'direct_result':
        keys(details, ('barrier_rejection_observed', 'current_receipt_read', 'updates_observed', 'error_classes', 'http_before', 'http_after'), 'direct_details')
        need(details['barrier_rejection_observed'] is True and details['current_receipt_read'] is True and details['updates_observed'] == 0 and
             details['error_classes'] == ['unclassified_plain_error'], 'current_direct_barrier')
        http_counts(details['http_before']); http_counts(details['http_after'])
        need(details['http_before'] == details['http_after'], 'direct_http')


def evidence_chain(artifacts):
    prior, first = {}, {}
    previous = None
    for artifact in artifacts:
        details_shape(artifact)
        snapshot = artifact['snapshot']
        kind = artifact['kind']
        changed = {c['ordinal']: c for c in artifact['details']['changes']} if kind == 'fault_applied' else {}
        for row in snapshot['tasks']:
            ordinal = row['ordinal']
            if ordinal not in prior:
                need(kind == 'seeded' and ordinal in artifact['details']['ordinals'], 'task_without_real_seed')
                need(row['billing_state'] == 'reserved' and row['receipt']['applied_at'] is None and row['receipt']['due_at'] is not None, 'seed_not_pending')
                first[ordinal] = row
            else:
                old = prior[ordinal]
                for key in ('slot', 'org_index', 'org_id', 'key_id', 'task_id', 'create_operation_id', 'operation_sha256', 'billing_snapshot_sha256', 'input_sha256', 'request_body_sha256', 'price'):
                    need(row[key] == old[key], 'durable_task_identity')
                for key in RECEIPT_KEYS - {'applied_at', 'due_at', 'recovery_version', 'error_code', 'full_sha256', 'immutable_sha256'}:
                    need(row['receipt'][key] == old['receipt'][key], 'durable_receipt_field')
                before, after = old['receipt']['immutable_sha256'], row['receipt']['immutable_sha256']
                if ordinal in changed:
                    change = changed[ordinal]
                    need(change['attempt_id'] == row['receipt']['attempt_id'] and change['before_immutable_sha256'] == before and
                         change['after_immutable_sha256'] == after and before != after, 'declared_fault_chain')
                else:
                    need(after == before, 'durable_receipt_chain')
                need(old['billing_state'] != 'settled' or row['billing_state'] == 'settled', 'settlement_reversed')
                if old['billing_state'] == 'reserved' and row['billing_state'] == 'settled':
                    need(row['event_count'] == old['event_count']+1 and row['outbox_count'] == old['outbox_count']+1,
                         'single_settlement_event')
                need(uint(row['receipt']['recovery_version'], False) >= uint(old['receipt']['recovery_version'], False), 'version_reversed')
            prior[ordinal] = row
        if kind in ('fault_applied', 'scheduled'):
            need(previous is not None and artifact['details']['financial_before_sha256'] ==
                 previous['financial_sha256'] == snapshot['financial_sha256'], 'fixture_financial_write')
            for key in ('task_count', 'orgs', 'provider', 'encrypted_evidence_sha256', 'all_events_sha256', 'outbox_sha256'):
                need(snapshot[key] == previous[key], 'fixture_changed_durable_state')
            selected = set(artifact['details']['ordinals'])
            need(selected == {r['ordinal'] for r in snapshot['tasks'] if kind == 'fault_applied' and r['ordinal'] in changed or
                             kind == 'scheduled' and r['slot'] == artifact['_command']['slot']}, 'fixture_selected_tasks')
            for before, after in zip(previous['tasks'], snapshot['tasks']):
                allowed = set()
                if after['ordinal'] in selected:
                    if kind == 'fault_applied': allowed = {'immutable_sha256', 'full_sha256'}
                    elif artifact['_command']['mode'] == 'due_now': allowed = {'due_at', 'full_sha256'}
                before_receipt = {k:v for k,v in before['receipt'].items() if k not in allowed}
                after_receipt = {k:v for k,v in after['receipt'].items() if k not in allowed}
                need(before_receipt == after_receipt, 'fixture_receipt_columns')
                omit = {'receipt'}
                if kind == 'scheduled' and artifact['_command']['mode'] == 'park_dispatch' and after['ordinal'] in selected:
                    omit.add('state_sha256')
                need({k:v for k,v in before.items() if k not in omit} == {k:v for k,v in after.items() if k not in omit}, 'fixture_task_columns')
        previous = snapshot
    return first, prior


def verify_case(case, result, artifacts, calls):
    need(artifacts, 'missing_case_evidence')
    seeds = [a for a in artifacts if a['kind'] == 'seeded']
    need(len(seeds) == (2 if case == 'P04' else 1), 'seed_count')
    need(result['seed'] == seeds[0]['snapshot'] and result['final'] == artifacts[-1]['snapshot'], 'result_not_raw')
    first, final = evidence_chain(artifacts)
    count = 42 if case == 'P01' else 2 if case == 'P04' else 1
    need(len(final) == count, 'case_task_count')
    # Raw Mock evidence is owned by actual seed commands, never a summary count.
    seed_owners = {(a['instance'], a['cmd_id'], o) for a in seeds for o in a['details']['ordinals']}
    for ordinal in range(count):
        real = [c for c in calls if c['ordinal'] == ordinal]
        need([c['method'] for c in real] == ['POST', 'GET'], 'post_get_once')
        need(real[0]['body_sha256'] == first[ordinal]['request_body_sha256'], 'original_request_body')
        need(all((c['instance'], c['cmd_id'], ordinal) in seed_owners for c in real), 'http_outside_seed_owner')
    for call in calls:
        keys(call, ('run_id', 'case_id', 'instance', 'cmd_id', 'ordinal', 'method', 'path', 'status',
                    'body_sha256', 'body_bytes', 'sequence'), 'mock_record_shape')
        integer(call['cmd_id'], 1)
        integer(call['body_bytes'], 0, 1024*1024)
        digest(call['body_sha256'])
        integer(call['ordinal'], 0, count-1)
        need(call['status'] == 200, 'mock_response_status')
        base = '/api/v3/contents/generations/tasks'
        target = 'qhandoff_' + call['run_id'] + '_' + case + '_' + str(call['ordinal'])
        need(call['path'] == base + ('' if call['method'] == 'POST' else '/' + target), 'mock_target')
    cycles = [a for a in artifacts if a['kind'] == 'cycle']
    need(cycles, 'missing_real_cycle')
    for a in artifacts:
        if a['kind'] != 'seeded':
            # Encrypted payloads/audit rows cannot change during recovery/faults.
            latest = max((s for s in seeds if artifacts.index(s) <= artifacts.index(a)), key=lambda s: artifacts.index(s))
            need(a['snapshot']['encrypted_evidence_sha256'] == latest['snapshot']['encrypted_evidence_sha256'], 'recovery_read_or_changed_ciphertext')
    final_snapshot = artifacts[-1]['snapshot']
    if case == 'P01':
        old = [a for a in cycles if a['role'] == 'old']
        new = [a for a in cycles if a['role'] == 'new']
        need(len(old) == 2 and 2 <= len(new) <= 11, 'p01_round_count')
        bad_ids = {first[i]['receipt']['attempt_id'] for i in range(40)}
        for a in old:
            batches = a['details']['query_batches']
            need(len(batches) == 1 and batches[0]['count'] == 40 and set(batches[0]['attempt_ids']) == bad_ids, 'old_bad_window')
            need(all(r['billing_state'] == 'reserved' and r['receipt']['applied_at'] is None for r in a['snapshot']['tasks']), 'old_progressed_healthy')
            need(a['snapshot']['financial_sha256'] == seeds[0]['snapshot']['financial_sha256'], 'old_changed_money')
        need(max(utc(first[i]['receipt']['received_at']) for i in range(40)) < min(utc(first[i]['receipt']['received_at']) for i in (40, 41)), 'old_received_order')
        for i, row in final.items():
            need(row['org_index'] == i % 2, 'p01_org_distribution')
            need(row['receipt']['recovery_version'] == str(uint(first[i]['receipt']['recovery_version'], False)+1), 'single_committed_progress')
            if i < 40:
                need(row['receipt']['due_at'] is None and row['receipt']['applied_at'] is None and row['receipt']['error_code'] == 'query_observation_invalid', 'p01_review')
                need(row['money'] == first[i]['money'] and row['task_sha256'] == first[i]['task_sha256'] and row['event_sha256'] == first[i]['event_sha256'], 'review_changed_task_money')
            else:
                need(row['billing_state'] == 'settled' and row['event_count'] == first[i]['event_count']+1, 'p01_settle_once')
        need(stable_snapshot(new[-2]['snapshot'], new[-1]['snapshot']), 'p01_replay')
    elif case == 'P02':
        old = [a for a in cycles if a['role'] == 'old']
        new = [a for a in cycles if a['instance'] == 'new-replay']
        need(len(old) == 1 and len(new) == 2, 'p02_cycles')
        row = old[0]['snapshot']['tasks'][0]
        need(row['billing_state'] == 'settled' and row['receipt']['recovery_version'] == first[0]['receipt']['recovery_version'], 'old_writer_version')
        need(stable_snapshot(old[0]['snapshot'], new[0]['snapshot']) and stable_snapshot(new[0]['snapshot'], new[1]['snapshot']), 'p02_replay')
    elif case == 'P03':
        initial = [a for a in cycles if a['instance'] == 'new-writer']
        old = [a for a in cycles if a['role'] == 'old']
        guarded = [a for a in cycles if a['instance'] == 'new-guard']
        direct = [a for a in artifacts if a['kind'] == 'direct_result']
        need(1 <= len(initial) <= 10 and len(old) == 2 and len(guarded) == 1 and len(direct) == 1, 'p03_cycles')
        reviewed = initial[-1]['snapshot']['tasks'][0]
        need(reviewed['receipt']['due_at'] is None and reviewed['receipt']['error_code'] == 'query_observation_invalid' and
             reviewed['receipt']['recovery_version'] == str(uint(first[0]['receipt']['recovery_version'], False)+1), 'new_isolation')
        for a in old:
            row = a['snapshot']['tasks'][0]
            need(row['receipt']['due_at'] is None and row['receipt']['recovery_version'] == reviewed['receipt']['recovery_version'] and
                 row['receipt']['error_code'] == 'query_apply_failed' and row['receipt']['applied_at'] is None, 'old_null_preservation')
            need(a['snapshot']['financial_sha256'] == seeds[0]['snapshot']['financial_sha256'], 'old_review_money')
        need(not any(b['count'] for b in guarded[0]['details']['query_batches']), 'new_selected_review')
        need(stable_snapshot(old[-1]['snapshot'], guarded[0]['snapshot']) and stable_snapshot(guarded[0]['snapshot'], direct[0]['snapshot']), 'direct_changed_review')
    else:
        primary, natural = seeds
        future = [a for a in cycles if a['instance'] == 'new-writer']
        early = [a for a in cycles if a['instance'] == 'old-early']
        need(len(future) == 1 and len(early) == 1 and natural['instance'] == 'new-natural', 'p04_process_phases')
        due = utc(primary['snapshot']['tasks'][0]['receipt']['due_at'])
        need(utc(primary['snapshot']['database_utc']) < due and utc(future[0]['snapshot']['database_utc']) < due and
             not any(b['count'] for b in future[0]['details']['query_batches']) and stable_snapshot(primary['snapshot'], future[0]['snapshot']), 'future_new_positive')
        need(early[0]['snapshot']['tasks'][0]['billing_state'] == 'settled' and utc(early[0]['snapshot']['database_utc']) < due, 'old_not_actually_early')
        second = natural['snapshot']['tasks'][1]
        natural_due = utc(second['receipt']['due_at'])
        need(second['slot'] == 'natural' and utc(natural['snapshot']['database_utc']) < natural_due, 'natural_future')
        after = artifacts[artifacts.index(natural)+1:]
        pending = [a for a in after if a['kind'] == 'cycle' and a['snapshot']['tasks'][1]['receipt']['applied_at'] is None]
        settled = [a for a in after if a['kind'] == 'cycle' and a['snapshot']['tasks'][1]['receipt']['applied_at'] is not None]
        need(pending and len(settled) == 2, 'natural_wait_and_replay')
        for a in pending:
            need(utc(a['snapshot']['database_utc']) < natural_due and not any(b['count'] for b in a['details']['query_batches']) and
                 stable_snapshot(natural['snapshot'], a['snapshot']), 'natural_future_attempt')
        need(utc(settled[0]['snapshot']['database_utc']) >= natural_due and
             (utc(settled[0]['snapshot']['database_utc'])-utc(natural['snapshot']['database_utc'])).total_seconds() <= 45, 'natural_database_expiry')
        need(stable_snapshot(settled[0]['snapshot'], settled[1]['snapshot']), 'natural_replay')
        need(all(r['billing_state'] == 'settled' for r in final.values()), 'p04_two_settled')
    return final_snapshot


def verify(evidence):
    evidence = Path(evidence)
    root = evidence / 'matrix'
    builds, summary = load(evidence / 'build-manifest.json'), load(root / 'matrix-passed.json')
    need(not (root / 'matrix-failed.json').exists(), 'failure_marker')
    keys(builds, ('old', 'new'), 'build_roles')
    need(builds['old']['source'] == OLD and re.fullmatch('[0-9a-f]{40}', builds['new']['source']) is not None and builds['new']['source'] != OLD, 'real_sources')
    need(builds['old']['sha256'] != builds['new']['sha256'], 'same_binary')
    run = summary['run_id']
    need(re.fullmatch('[0-9a-f]{16}', run) is not None and summary['status'] == 'passed' and
         summary['cases_passed'] == list(CASES) and summary['cases'] == 4 and summary['actual_processes'] == 11 and
         summary['old_source'] == OLD and summary['new_source'] == builds['new']['source'], 'complete_summary')
    exits = load(root / 'child-exits.json')
    need(len(exits) == 11, 'eleven_actual_processes')
    by_child = {(r['case_id'], r['instance']): r for r in exits}
    need(set(by_child) == {(case, instance) for case, instances in INSTANCES.items() for instance in instances}, 'child_set')
    mock = [strict_json(line) for line in raw_file(root / 'mock-requests.jsonl').splitlines()]
    need([r['sequence'] for r in mock] == list(range(1, len(mock)+1)), 'mock_sequence')
    need(len(mock) == 92 and all(r['run_id'] == run and r['case_id'] in CASES and r['instance'] in INSTANCES[r['case_id']] for r in mock), 'mock_full_scope')
    schemas = set()
    artifacts_total = 0
    for case, instances in INSTANCES.items():
        artifacts, schema = [], None
        for instance in instances:
            values, observed = read_child(root, case, instance, run, builds, by_child[(case, instance)])
            need(schema is None or schema == observed, 'case_schema_changed')
            schema = observed
            artifacts.extend(values)
        need(schema not in schemas, 'shared_case_schema')
        schemas.add(schema)
        result = load(root / (case + '-result.json'))
        keys(result, ('status', 'case_id', 'seed', 'final', 'http'), 'result_shape')
        calls = [r for r in mock if r['case_id'] == case]
        need(result['status'] == 'passed' and result['case_id'] == case and result['http'] == calls, 'case_summary')
        verify_case(case, result, artifacts, calls)
        artifacts_total += len(artifacts)
    return dict(status='passed', cases=4, actual_processes=11, raw_artifacts=artifacts_total,
                raw_http_requests=92, run_id=run, old_source=OLD, new_source=builds['new']['source'])


if __name__ == '__main__':
    need(len(sys.argv) == 2, 'evidence_argument')
    print(json.dumps(verify(Path(sys.argv[1])), sort_keys=True))
