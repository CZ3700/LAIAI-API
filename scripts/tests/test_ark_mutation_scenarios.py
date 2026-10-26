"""Offline orchestration/oracle rejection checks, not binary/MySQL evidence."""
from __future__ import annotations

import ast
import copy
import datetime as dt
import importlib.util
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / 'lib' / 'ark_mutation_scenarios.py'
spec = importlib.util.spec_from_file_location('ark_mutation_scenarios', SOURCE)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

NOW = '2026-10-26T12:00:00.000Z'
LEASE = '2026-10-26T12:03:00.000Z'


class Rejected(Exception):
    pass


def require(condition, code):
    if not condition:
        raise Rejected(code)


def parse_time(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def after_time(value):
    return (parse_time(value) + dt.timedelta(milliseconds=1)).isoformat(timespec='milliseconds').replace('+00:00', 'Z')


def same_finance(a, b):
    require(a['fingerprints']['financial_all'] == b['fingerprints']['financial_all'], 'finance_changed')


def snapshot():
    return dict(clock=NOW, db_time=NOW,
                operation=dict(status='in_flight', error_code='', attempt_id='att_unit', lease_until=LEASE,
                               version='2', receipt_ref='', receipt_applied_at=None),
                task=dict(status='queued', deleted_at=None, next_poll_at='2026-10-26T12:00:10.000Z'),
                mutation_attempt=dict(count=1), inbox=dict(count=0),
                legacy=dict(receipt_present=False, attempt_count=0, receipt_ref='', receipt_applied_at=None),
                action_input=dict(capture_status='staged', payload_ref='vpl_unit', identity_sha256='identity',
                                  expires_at='2026-10-27T12:00:00.000Z', wrapped_key_present=True,
                                  nonce_present=True, staging_count=1),
                audits=dict(execution_read_count=1), fingerprints=dict(financial_all='funds', action_application_events='events'))


def helpers(writes=None):
    return dict(require=require, after_time=after_time, parse_time=parse_time, same_finance=same_finance,
                validate_finance=lambda value, **kw: None,
                write_json=lambda path, value, **kw: (writes if writes is not None else []).append((path, value)))


class CaseStub:
    def __init__(self, name='N1-cancel-Ca'):
        family, action, consumer = sc.SCENARIO_DEFINITIONS[name]
        self.id, self.action = name, action
        self.definition = dict(family=family, consumer=consumer, wall=name == 'N1-wall-cancel-Ca')
        self.baseline = snapshot()
        self.dir = Path('/synthetic-case')
        self.inspections = []
        self.clock = NOW
        self.status = dict(delete=0)
        self.mock = SimpleNamespace(status=lambda: self.status)
        self.advances = []
        self.deadline_checks = 0

    def registered(self, value, receipts=0):
        require(value['mutation_attempt']['count'] == 1 and value['inbox']['count'] == receipts, 'registration_count')
        require(not value['legacy']['receipt_present'] and value['legacy']['attempt_count'] == 0, 'mixed_protocol')

    def check_deadline(self):
        self.deadline_checks += 1

    def advance(self, target):
        self.advances.append(target)
        self.clock = target


class ScenarioOracleTests(unittest.TestCase):
    def test_literal_inventory_is_exactly_33(self):
        # Independent contract literals, not production case_definitions.
        expected = '''
L1-cancel L1-delete
L2-cancel-Poff L2-cancel-Pon L2-delete-Poff L2-delete-Pon
N1-cancel-Ca N1-cancel-Poff N1-delete-Ca N1-delete-Poff N1-wall-cancel-Ca
N2-cancel-Ca N2-cancel-Poff N2-delete-Ca N2-delete-Poff
N4-cancel-502-Ca N4-delete-EOF-Poff
R1-cancel-Ca R1-delete-Ca R2-cancel-Ca R2-delete-Ca
M1-cancel M1-delete M2-cancel M2-delete
M3-cancel-1 M3-cancel-2 M3-cancel-3 M3-delete-1 M3-delete-2 M3-delete-3
Q1-cancel-succeeded-first Q1-cancel-cancelled-first
'''.split()
        self.assertEqual(len(expected), 33)
        self.assertEqual(sc.SCENARIO_IDS, frozenset(expected))

    def test_all_cases_dispatch_the_exact_family(self):
        for name in sc.SCENARIO_IDS:
            with self.subTest(name=name):
                case = CaseStub(name)
                with patch.object(sc.Scenarios, case.definition['family']) as action:
                    sc.scenario_dispatch(case, helpers())
                    action.assert_called_once_with()

    def test_unknown_ids_wrong_roles_and_clock_fail_before_dispatch(self):
        for field, bad in (('id', 'N3-cancel-Ca'), ('action', 'delete')):
            case = CaseStub()
            setattr(case, field, bad)
            with self.assertRaises(Rejected):
                sc.scenario_dispatch(case, helpers())
        for field, bad in (('consumer', 'Pon'), ('family', 'N2'), ('wall', True)):
            case = CaseStub()
            case.definition[field] = bad
            with self.assertRaises(Rejected):
                sc.scenario_dispatch(case, helpers())

    def test_unanswered_requires_zero_receipts_no_delete_and_frozen_finance(self):
        case = CaseStub()
        runner = sc.Scenarios(case, helpers())
        good = snapshot()
        good['operation'].update(status='unknown', error_code='submission_uncertain')
        runner.unknown_without_response(good, 0)
        corruptions = [
            ('operation', 'status', 'confirmed'), ('operation', 'error_code', ''),
            ('operation', 'receipt_ref', 'vpl_unexpected'), ('operation', 'receipt_applied_at', NOW),
            ('inbox', 'count', 1), ('task', 'status', 'cancelled'), ('task', 'deleted_at', NOW),
            ('legacy', 'receipt_present', True), ('fingerprints', 'financial_all', 'refunded'),
        ]
        for section, key, value in corruptions:
            with self.subTest(section=section, key=key):
                invalid = copy.deepcopy(good)
                invalid[section][key] = value
                with self.assertRaises(Rejected):
                    runner.unknown_without_response(invalid, 0)
        case.status['delete'] = 1
        with self.assertRaisesRegex(Rejected, 'delete_count'):
            runner.unknown_without_response(good, 0)

    def test_no_response_delete_cannot_be_soft_deleted(self):
        case = CaseStub('N2-delete-Poff')
        case.status['delete'] = 1
        value = snapshot()
        value['task']['status'] = 'succeeded'
        value['operation'].update(status='unknown', error_code='submission_uncertain')
        runner = sc.Scenarios(case, helpers())
        runner.unknown_without_response(value, 1)
        value['task']['deleted_at'] = NOW
        with self.assertRaises(Rejected):
            runner.unknown_without_response(value, 1)

    def test_legacy_exactly_one_receipt_and_no_action_input(self):
        value = snapshot()
        value.update(mutation_attempt=dict(count=0), action_input=None)
        value['legacy'].update(receipt_present=True, attempt_count=1)
        runner = sc.Scenarios(CaseStub('L2-cancel-Poff'), helpers())
        runner.legacy_only(value, receipt=True)
        for count in (0, 2):
            invalid = copy.deepcopy(value)
            invalid['legacy']['attempt_count'] = count
            with self.assertRaises(Rejected):
                runner.legacy_only(invalid, receipt=True)
        value['action_input'] = {'payload_ref': 'unexpected'}
        with self.assertRaises(Rejected):
            runner.legacy_only(value, receipt=True)

    def test_live_claim_is_not_only_a_financial_comparison(self):
        runner = sc.Scenarios(CaseStub(), helpers())
        before = snapshot()
        after = copy.deepcopy(before)
        after['operation']['version'] = '3'
        with self.assertRaisesRegex(Rejected, 'live_claim_changed'):
            runner.unchanged(before, after)

    def test_application_progress_does_not_allow_evidence_replacement(self):
        before = snapshot()
        before['inbox'] = dict(count=1, receipt_id='receipt-one', attempt_id='attempt-one',
                               response_payload_ref='payload-one', outcome='accepted', http_status=204,
                               response_received_count=1, evidence_hmac_sha256='sha', application_state='pending')
        after = copy.deepcopy(before)
        after['inbox'].update(application_state='applied', applied_at=NOW)
        sc.same_response_evidence(before, after, require)
        for field in ('receipt_id', 'attempt_id', 'response_payload_ref', 'outcome', 'http_status',
                      'response_received_count', 'evidence_hmac_sha256'):
            with self.subTest(field=field):
                wrong = copy.deepcopy(after)
                wrong['inbox'][field] = 'replaced'
                with self.assertRaisesRegex(Rejected, 'response_evidence_replaced'):
                    sc.same_response_evidence(before, wrong, require)

    def test_logical_lease_uses_actual_persisted_deadline(self):
        case = CaseStub()
        runner = sc.Scenarios(case, helpers())
        value = snapshot()
        value['operation']['lease_until'] = '2026-10-26T14:27:19.321Z'
        runner.advance_lease(value)
        self.assertEqual(case.advances, ['2026-10-26T14:27:19.322Z'])

    def test_database_due_wait_reads_db_time_without_advancing_service(self):
        case = CaseStub('Q1-cancel-cancelled-first')
        pending = snapshot()
        pending['inbox'] = dict(count=1, next_attempt_at='2026-10-26T12:00:30.000Z')
        values = []
        for db_time in ('2026-10-26T12:00:29.999Z', '2026-10-26T12:00:30.000Z'):
            value = copy.deepcopy(pending)
            value['clock'] = '2030-01-01T00:00:00.000Z'  # Cannot satisfy the DB deadline.
            value['db_time'] = db_time
            values.append(value)
        case.inspect = lambda label: values.pop(0)
        writes = []
        with patch.object(sc.time, 'sleep'), patch.object(sc.time, 'monotonic', side_effect=[0, 0, 0, 1, 1, 2]):
            sc.Scenarios(case, helpers(writes)).wait_database_due(pending)
        self.assertEqual(case.advances, [])
        self.assertEqual(len(values), 0)
        self.assertEqual(writes[0][1]['observed_db_time'], '2026-10-26T12:00:30.000Z')
        self.assertIs(writes[0][1]['logical_due_advance'], False)

    def test_database_wait_is_bounded_and_checks_immutable_pending(self):
        case = CaseStub('Q1-cancel-cancelled-first')
        pending = snapshot()
        pending['inbox'] = dict(count=1, next_attempt_at='2026-10-26T12:00:30.000Z')
        with patch.object(sc.time, 'monotonic', side_effect=[0, 41]):
            with self.assertRaisesRegex(Rejected, 'database_retry_due_timeout'):
                sc.Scenarios(case, helpers()).wait_database_due(pending)
        invalid = copy.deepcopy(pending)
        invalid['operation']['status'] = 'unknown'
        case.inspect = lambda label: invalid
        with patch.object(sc.time, 'sleep'), patch.object(sc.time, 'monotonic', return_value=0):
            with self.assertRaisesRegex(Rejected, 'live_claim_changed'):
                sc.Scenarios(case, helpers()).wait_database_due(pending)

    def test_wall_wait_has_no_logical_advance_and_records_actual_observation(self):
        case = CaseStub('N1-wall-cancel-Ca')
        value = snapshot()
        value['operation']['lease_until'] = '2020-01-01T00:00:00.000Z'
        case.inspect = lambda label: dict(clock=NOW, db_time=NOW)
        writes = []
        sc.Scenarios(case, helpers(writes)).advance_lease(value)
        self.assertEqual(case.advances, [])
        self.assertIs(writes[0][1]['logical_advance'], False)

    def test_wall_wait_times_out_instead_of_advancing_the_lease(self):
        case = CaseStub('N1-wall-cancel-Ca')
        instant = parse_time(NOW)
        class Clock(dt.datetime):
            @classmethod
            def now(cls, tz=None):
                return instant
        with patch.object(sc.dt, 'datetime', Clock), patch.object(sc.time, 'monotonic', side_effect=[0, 241]):
            with self.assertRaisesRegex(Rejected, 'wall_lease_timeout'):
                sc.Scenarios(case, helpers()).advance_lease(snapshot())
        self.assertEqual(case.advances, [])

    def test_module_cannot_execute_sql_or_spawn_unowned_processes(self):
        tree = ast.parse(SOURCE.read_text(encoding='utf-8'))
        imports = {node.names[0].name for node in ast.walk(tree) if isinstance(node, ast.Import)}
        self.assertEqual(imports, {'datetime', 'time'})
        forbidden = {'execute', 'executescript', 'sql', 'Popen', 'kill', 'run_command', 'system'}
        calls = {node.func.attr for node in ast.walk(tree) if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)}
        self.assertFalse(calls & forbidden)


class CompetitionOrderingTests(unittest.TestCase):
    def test_both_releases_precede_any_completion_wait(self):
        case = CaseStub('M3-cancel-1')
        events = []
        processes = []
        class Process:
            def __init__(self, role):
                self.instance, self.pid = role, 10 + len(processes)
                processes.append(self)
            def send(self, command, **fields):
                events.append(('send', self.instance, command))
                return 2
            def wait_done(self, command):
                events.append(('wait', self.instance, command))
                require(sum(item[0] == 'send' for item in events) == 2, 'premature_competition_wait')
            def stop(self):
                events.append(('stop', self.instance))
        case.start = lambda role, **kw: Process(role)
        case.cycle = lambda process, barrier: 1
        before = copy.deepcopy(case.baseline)
        final = copy.deepcopy(before)
        final['inbox']['count'] = 1
        values = [before, final]
        case.inspect = lambda label: values.pop(0)
        case.expected_action = lambda *args, **kw: events.append(('expected', kw['registered']))
        case.replay = lambda *args, **kw: events.append(('replay', kw['registered']))
        # No HTTP during START. The independent outcome assertion is owned by
        # Case; this unit tests command ordering, not simulated billing.
        sc.Scenarios(case, helpers()).M3()
        self.assertEqual(events[:2], [('send', 'Ca', 'release_barrier'), ('send', 'Pon', 'release_barrier')])
        self.assertIn(('expected', True), events)
        self.assertIn(('replay', True), events)

    def test_n2_kills_owned_sender_before_dropping_socket(self):
        case = CaseStub('N2-cancel-Ca')
        events = []
        class Process:
            instance = 'sender-pon-unit'
            def send(self, command, **kw):
                return 1
            def wait_event(self, event, command):
                events.append('started')
            def kill_after_mock_hold(self, mock, seq, path, digest):
                require((seq, path, digest) == (7, 'snapshots/verified.json', 'hash'), 'wrong_kill_evidence')
                events.append('killed')
        process = Process()
        case.start = lambda *args, **kw: process
        case.mock.set = lambda *args: None
        case.mock.wait_delete = lambda instance: dict(request_seq=7)
        case.mock.drop_waiting = lambda seq, owner: events.append('dropped')
        case.status['delete'] = 1
        case.inspect = lambda label: snapshot()
        case.inspections = [dict(file='snapshots/verified.json', sha256='hash')]
        case.check_pending = lambda value, count: events.append('verified')
        runner = sc.Scenarios(case, helpers())
        with patch.object(runner, 'recover_unanswered') as recover:
            runner.N2()
            recover.assert_called_once()
        self.assertEqual(events, ['started', 'verified', 'killed', 'dropped'])


if __name__ == '__main__':
    unittest.main()
