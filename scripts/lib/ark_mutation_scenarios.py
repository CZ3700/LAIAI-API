"""The 33 non-N3 fixed-binary handoffs; no SQL or business-row mutation.

Only the main runner owns Case, resource cleanup, and source provenance. This
module issues its bounded public process commands and independently checks the
resulting read-only snapshots. Unit tests of this module are orchestration
checks, never evidence that a real binary/MySQL handoff passed.
"""
from __future__ import annotations

import datetime as dt
import time


def _definitions():
    result = {}
    for action in ('cancel', 'delete'):
        result['L1-' + action] = ('L1', action, 'Ca')
        for consumer in ('Poff', 'Pon'):
            result[f'L2-{action}-{consumer}'] = ('L2', action, consumer)
        for family in ('N1', 'N2'):
            for consumer in ('Ca', 'Poff'):
                result[f'{family}-{action}-{consumer}'] = (family, action, consumer)
        for family in ('R1', 'R2', 'M1', 'M2'):
            name = f'{family}-{action}' + ('-Ca' if family.startswith('R') else '')
            result[name] = (family, action, 'Ca')
        for repeat in (1, 2, 3):
            result[f'M3-{action}-{repeat}'] = ('M3', action, 'Ca')
    result['N1-wall-cancel-Ca'] = ('N1', 'cancel', 'Ca')
    result['N4-cancel-502-Ca'] = ('N4', 'cancel', 'Ca')
    result['N4-delete-EOF-Poff'] = ('N4', 'delete', 'Poff')
    for terminal in ('succeeded', 'cancelled'):
        result['Q1-cancel-' + terminal + '-first'] = ('Q1', 'cancel', 'Ca')
    return result


SCENARIO_DEFINITIONS = _definitions()
SCENARIO_IDS = frozenset(SCENARIO_DEFINITIONS)
_HELPERS = frozenset(('require', 'after_time', 'parse_time', 'validate_finance', 'same_finance', 'write_json'))


def same_response_evidence(before, after, require):
    """Application progress may advance; the registered evidence may not."""
    fields = ('receipt_id', 'attempt_id', 'response_payload_ref', 'outcome', 'http_status',
              'response_received_count', 'evidence_hmac_sha256')
    require(before['inbox']['count'] == after['inbox']['count'] == 1
            and before['mutation_attempt'] == after['mutation_attempt']
            and all(before['inbox'][field] == after['inbox'][field] for field in fields),
            'response_evidence_replaced')


class Scenarios:
    def __init__(self, case, helpers):
        self.case = case
        self.require = helpers['require']
        self.require(set(helpers) == _HELPERS, 'scenario_helpers_invalid')
        self.after_time = helpers['after_time']
        self.parse_time = helpers['parse_time']
        self.validate_finance = helpers['validate_finance']
        self.same_finance = helpers['same_finance']
        self.write_json = helpers['write_json']
        definition = SCENARIO_DEFINITIONS.get(case.id)
        self.require(definition is not None, 'scenario_not_implemented')
        self.require(definition == (case.definition['family'], case.action, case.definition['consumer']),
                     'scenario_definition_mismatch')
        self.require(bool(case.definition.get('wall')) == (case.id == 'N1-wall-cancel-Ca'),
                     'scenario_clock_mismatch')

    def record(self, name, value):
        self.write_json(self.case.dir / (name + '.json'), value, exclusive=True)

    def http_count(self, expected):
        self.require(self.case.mock.status()['delete'] == expected, 'delete_count_mismatch')

    def unchanged(self, before, after):
        self.same_finance(before, after)
        self.require(before['operation'] == after['operation']
                     and before['mutation_attempt'] == after['mutation_attempt']
                     and before['inbox'] == after['inbox']
                     and before['legacy'] == after['legacy'], 'live_claim_changed')

    def legacy_only(self, snapshot, *, receipt):
        self.require(snapshot['mutation_attempt']['count'] == 0 and snapshot['inbox']['count'] == 0
                     and snapshot['action_input'] is None, 'unexpected_mutation_registration')
        self.require(snapshot['legacy']['receipt_present'] is receipt
                     and snapshot['legacy']['attempt_count'] == (1 if receipt else 0),
                     'legacy_evidence_mismatch')

    def unknown_without_response(self, snapshot, expected_http):
        case = self.case
        case.registered(snapshot, 0)
        self.require(snapshot['operation']['status'] == 'unknown'
                     and snapshot['operation']['error_code'] == 'submission_uncertain'
                     and snapshot['operation']['receipt_ref'] == ''
                     and snapshot['operation']['receipt_applied_at'] is None,
                     'unanswered_attempt_not_unknown')
        self.require(snapshot['task']['status'] == ('queued' if case.action == 'cancel' else 'succeeded')
                     and snapshot['task']['deleted_at'] is None, 'unanswered_action_changed_task')
        self.same_finance(case.baseline, snapshot)
        self.http_count(expected_http)

    def advance_lease(self, snapshot):
        case = self.case
        target = self.after_time(snapshot['operation']['lease_until'])
        if not case.definition.get('wall'):
            case.advance(target)
            return
        started = time.monotonic()
        deadline = started + 240
        while dt.datetime.now(dt.timezone.utc) < self.parse_time(target):
            case.check_deadline()
            self.require(time.monotonic() < deadline, 'wall_lease_timeout')
            time.sleep(min(1, max(0.001, (self.parse_time(target) - dt.datetime.now(dt.timezone.utc)).total_seconds())))
        observed = case.inspect('wall-lease-expired')
        self.require(self.parse_time(observed['clock']) > self.parse_time(snapshot['operation']['lease_until']),
                     'wall_lease_not_expired')
        self.record('wall-lease-wait', dict(start_clock=snapshot['clock'], lease_until=snapshot['operation']['lease_until'],
                                         observed_clock=observed['clock'], observed_db_time=observed['db_time'],
                                         elapsed_seconds=round(time.monotonic() - started, 3), logical_advance=False))

    def recover_unanswered(self, durable, expected_http):
        case = self.case
        consumer = case.start(case.definition['consumer'], prefix='first')
        case.cycle(consumer)
        before_expiry = case.inspect('before-lease-expiry')
        case.check_pending(before_expiry, 0)
        self.unchanged(durable, before_expiry)
        self.http_count(expected_http)
        self.advance_lease(durable)
        case.cycle(consumer)
        unknown = case.inspect('after-lease-unknown')
        self.unknown_without_response(unknown, expected_http)
        consumer.stop()
        case.replay(unknown, count=expected_http)

    def L1(self):
        case = self.case
        self.require(case.seed['creation_role'] == 'Ca', 'legacy_intent_seed_required')
        producer = case.start('Pon', prefix='new-sender')
        case.cycle(producer)
        applied = case.inspect('new-sender-applied')
        case.expected_action(applied)
        producer.stop()
        case.replay(applied)

    def L2(self):
        case = self.case
        self.require(case.seed['creation_role'] == 'Ca', 'legacy_intent_seed_required')
        producer = case.start('Ca', prefix='legacy-sender')
        cmd = case.cycle(producer, 'legacy_receipt_durable')
        durable = case.inspect('legacy-durable-before-kill')
        self.legacy_only(durable, receipt=True)
        self.require(durable['legacy']['receipt_applied_at'] is None
                     and durable['operation']['receipt_applied_at'] is None, 'legacy_applied_before_handoff')
        self.same_finance(case.baseline, durable)
        self.http_count(1)
        producer.kill_expected('legacy_receipt_durable', cmd)
        consumer = case.start(case.definition['consumer'], prefix='first')
        case.cycle(consumer)
        applied = case.inspect('legacy-first-consumer-applied')
        self.legacy_only(applied, receipt=True)
        self.require(applied['legacy']['receipt_ref'] == durable['legacy']['receipt_ref'], 'legacy_receipt_replaced')
        case.expected_action(applied, registered=False)
        consumer.stop()
        case.replay(applied, registered=False)

    def N1(self):
        case = self.case
        producer = case.start('Pon', prefix='sender')
        cmd = case.cycle(producer, 'registered_before_http')
        durable = case.inspect('registered-before-kill')
        case.check_pending(durable, 0)
        self.http_count(0)
        producer.kill_expected('registered_before_http', cmd)
        self.recover_unanswered(durable, 0)

    def N2(self):
        case = self.case
        case.mock.set(['queued'], 'hold')
        producer = case.start('Pon', prefix='sender')
        cmd = producer.send('run_once', barrier='none')
        producer.wait_event('run_started', cmd)
        request = case.mock.wait_delete(producer.instance)
        durable = case.inspect('delete-seen-no-response')
        case.check_pending(durable, 0)
        self.http_count(1)
        evidence = case.inspections[-1]
        producer.kill_after_mock_hold(case.mock, request['request_seq'], evidence['file'], evidence['sha256'])
        case.mock.drop_waiting(request['request_seq'], producer)
        self.recover_unanswered(durable, 1)

    def N4(self):
        case = self.case
        expected_http = 502 if case.action == 'cancel' else 0
        producer = case.start('Pon', prefix='sender')
        case.cycle(producer)
        unknown = case.inspect('unknown-response-applied')
        case.registered(unknown, 1)
        self.require(unknown['inbox']['outcome'] == 'unknown' and unknown['inbox']['http_status'] == expected_http
                     and unknown['inbox']['application_state'] == 'applied' and unknown['inbox']['applied_at'] is not None
                     and bool(unknown['inbox']['applied_event_id']), 'unknown_response_not_applied')
        self.require(unknown['operation']['status'] == 'unknown' and unknown['task']['deleted_at'] is None
                     and unknown['task']['status'] == ('queued' if case.action == 'cancel' else 'succeeded'),
                     'unknown_response_changed_task')
        self.same_finance(case.baseline, unknown)
        self.http_count(1)
        producer.stop()
        first = case.start(case.definition['consumer'], prefix='first')
        case.cycle(first)
        recovered = case.inspect('unknown-first-consumer')
        self.unchanged(unknown, recovered)
        self.require(unknown['fingerprints']['action_application_events'] == recovered['fingerprints']['action_application_events'],
                     'unknown_response_duplicated_event')
        self.http_count(1)
        first.stop()
        case.replay(recovered)

    def recovery_history(self, *, purge):
        case = self.case
        self.require(case.seed['history_enabled'] is purge, 'history_fixture_mismatch')
        producer = case.start('Pon', prefix='sender')
        cmd = case.cycle(producer, 'inbox_durable_before_apply')
        durable = case.inspect('history-durable-before-kill')
        case.check_pending(durable, 1)
        self.http_count(1)
        self.require(durable['audits']['execution_read_count'] > 0, 'execution_read_positive_control_missing')
        producer.kill_expected('inbox_durable_before_apply', cmd)
        consumer = case.start('Ca', prefix='first')
        case.cycle(consumer)
        before_expiry = case.inspect('history-before-lease-expiry')
        case.check_pending(before_expiry, 1)
        self.unchanged(durable, before_expiry)
        if purge:
            expiry = max((durable['action_input']['expires_at'], durable['operation']['lease_until']), key=self.parse_time)
            case.advance(self.after_time(expiry))
            case.sync_clock(consumer)
            command = consumer.send('purge_expired')
            event = consumer.wait_event('purged', command)
            consumer.wait_done(command)
            cleared = case.inspect('exact-input-purged')
            identity = durable['action_input']['identity_sha256']
            facts = event['data']
            self.require(facts['input_payload_ref'] == durable['action_input']['payload_ref']
                         and facts['purged_count'] > 0
                         and facts['before_identity_sha256'] == facts['after_identity_sha256'] == identity,
                         'purge_identity_mismatch')
            self.require(cleared['action_input']['identity_sha256'] == identity
                         and cleared['action_input']['capture_status'] == 'expired'
                         and cleared['action_input']['wrapped_key_present'] is False
                         and cleared['action_input']['nonce_present'] is False
                         and cleared['action_input']['staging_count'] == 0, 'exact_input_not_purged')
            self.same_finance(durable, cleared)
            self.require(cleared['inbox'] == durable['inbox'] and cleared['mutation_attempt'] == durable['mutation_attempt'],
                         'purge_modified_response_evidence')
        else:
            self.advance_lease(durable)
        case.cycle(consumer)
        applied = case.inspect('history-first-consumer-applied')
        case.expected_action(applied)
        same_response_evidence(durable, applied, self.require)
        self.require(applied['action_input']['identity_sha256'] == durable['action_input']['identity_sha256']
                     and applied['audits']['execution_read_count'] == durable['audits']['execution_read_count'],
                     'recovery_decrypted_or_changed_action_input')
        if not purge:
            self.require(applied['audits']['response_capture_status'] == 'disabled'
                         and applied['audits']['response_staging_count'] == 0, 'disabled_history_was_captured')
        consumer.stop()
        case.replay(applied)
        final = case.inspect('history-final-audit')
        self.require(final['audits']['execution_read_count'] == durable['audits']['execution_read_count'],
                     'replay_decrypted_action_input')

    def R1(self):
        self.recovery_history(purge=False)

    def R2(self):
        self.recovery_history(purge=True)

    def M1(self):
        case = self.case
        producer = case.start('Pon', prefix='new-sender')
        cmd = case.cycle(producer, 'registered_before_http')
        durable = case.inspect('new-claim-before-competitor')
        case.check_pending(durable, 0)
        self.http_count(0)
        competitor = case.start('Ca', prefix='legacy-competitor')
        case.cycle(competitor)
        after = case.inspect('new-claim-after-competitor')
        case.check_pending(after, 0)
        self.unchanged(durable, after)
        self.http_count(0)
        competitor.stop()
        case.release(producer, cmd, 'registered_before_http')
        applied = case.inspect('new-winner-applied')
        case.expected_action(applied)
        producer.stop()
        case.replay(applied)

    def M2(self):
        case = self.case
        producer = case.start('Ca', prefix='legacy-sender')
        cmd = case.cycle(producer, 'legacy_claim_before_http')
        durable = case.inspect('legacy-claim-before-competitor')
        self.legacy_only(durable, receipt=False)
        self.require(durable['operation']['status'] == 'in_flight'
                     and bool(durable['operation']['attempt_id'])
                     and self.parse_time(durable['operation']['lease_until']) > self.parse_time(durable['clock']),
                     'legacy_claim_not_durable')
        self.same_finance(case.baseline, durable)
        self.http_count(0)
        competitor = case.start('Pon', prefix='new-competitor')
        case.cycle(competitor)
        after = case.inspect('legacy-claim-after-competitor')
        self.legacy_only(after, receipt=False)
        self.unchanged(durable, after)
        self.http_count(0)
        competitor.stop()
        case.release(producer, cmd, 'legacy_claim_before_http')
        applied = case.inspect('legacy-winner-applied')
        self.legacy_only(applied, receipt=True)
        case.expected_action(applied, registered=False)
        producer.stop()
        case.replay(applied, registered=False)

    def M3(self):
        case = self.case
        old = case.start('Ca', prefix='legacy-contender')
        new = case.start('Pon', prefix='new-contender')
        old_cmd = case.cycle(old, 'start')
        new_cmd = case.cycle(new, 'start')
        before = case.inspect('both-start-ready')
        self.unchanged(case.baseline, before)
        self.http_count(0)
        self.require(old.pid != new.pid, 'competition_pid_not_distinct')
        # Do not use Case.release: it waits for completion and would serialize
        # the two cycles, defeating the shared-pending competition.
        old_release = old.send('release_barrier', barrier='start', owner_cmd_id=old_cmd)
        new_release = new.send('release_barrier', barrier='start', owner_cmd_id=new_cmd)
        old.wait_done(old_release)
        new.wait_done(new_release)
        old.wait_done(old_cmd)
        new.wait_done(new_cmd)
        applied = case.inspect('competition-applied')
        new_won = applied['mutation_attempt']['count'] == 1
        if new_won:
            case.registered(applied, 1)
        else:
            self.legacy_only(applied, receipt=True)
        case.expected_action(applied, registered=new_won)
        self.record('competition', dict(legacy_pid=old.pid, new_pid=new.pid, legacy_instance=old.instance,
                                       new_instance=new.instance, both_start_ready=True, winner='Pon' if new_won else 'Ca'))
        old.stop()
        new.stop()
        case.replay(applied, registered=new_won)

    def wait_database_due(self, pending):
        case = self.case
        due = self.parse_time(pending['inbox']['next_attempt_at'])
        self.require(due > self.parse_time(pending['db_time']), 'claim_changed_retry_not_deferred')
        started = time.monotonic()
        deadline = started + 40
        latest = pending
        observation = 0
        while self.parse_time(latest['db_time']) < due:
            case.check_deadline()
            self.require(time.monotonic() < deadline, 'database_retry_due_timeout')
            time.sleep(min(0.5, max(0.001, deadline - time.monotonic())))
            observation += 1
            latest = case.inspect('database-due-' + str(observation))
            self.unchanged(pending, latest)
        self.record('database-retry-wait', dict(next_attempt_at=pending['inbox']['next_attempt_at'],
                                              initial_db_time=pending['db_time'], observed_db_time=latest['db_time'],
                                              elapsed_seconds=round(time.monotonic() - started, 3), logical_due_advance=False))

    def Q1(self):
        case = self.case
        terminal = case.definition['terminal']
        self.require(terminal in ('succeeded', 'cancelled')
                     and case.id == 'Q1-cancel-' + terminal + '-first', 'query_terminal_definition_mismatch')
        case.mock.set([terminal], 'hold')
        producer = case.start('Pon', prefix='sender')
        cmd = producer.send('run_once', barrier='none')
        producer.wait_event('run_started', cmd)
        request = case.mock.wait_delete(producer.instance)
        live = case.inspect('query-before-terminal')
        case.check_pending(live, 0)
        self.http_count(1)
        self.require(live['task']['next_poll_at'] is not None, 'query_due_missing')
        case.advance(self.after_time(live['task']['next_poll_at']))
        self.require(self.parse_time(case.clock) < self.parse_time(live['operation']['lease_until']),
                     'query_not_before_mutation_lease')
        consumer = case.start('Ca', prefix='terminal-query')
        previous_gets = case.mock.status()['get']
        case.cycle(consumer)
        terminal_snapshot = case.inspect('query-terminal-before-ack')
        case.registered(terminal_snapshot, 0)
        self.require(terminal_snapshot['task']['status'] == terminal and terminal_snapshot['task']['deleted_at'] is None,
                     'query_did_not_establish_terminal')
        self.validate_finance(terminal_snapshot, terminal=terminal)
        self.require(case.mock.status()['get'] == previous_gets + 1, 'query_terminal_not_once')
        expected_status = 'confirmed' if terminal == 'cancelled' else 'in_flight'
        self.require(terminal_snapshot['operation']['status'] == expected_status, 'query_operation_fact_mismatch')
        self.require(terminal_snapshot['finance']['capture_journal_count'] == (1 if terminal == 'succeeded' else 0)
                     and terminal_snapshot['finance']['release_movement_count'] == 1,
                     'query_financial_posting_count_mismatch')
        case.mock.release_delete(request['request_seq'], 'ack204')
        producer.wait_done(cmd)
        saved = case.inspect('late-ack-saved')
        case.registered(saved, 1)
        self.same_finance(terminal_snapshot, saved)
        self.require(saved['inbox']['outcome'] == 'accepted' and saved['inbox']['http_status'] == 204,
                     'late_ack_evidence_lost')
        if terminal == 'cancelled':
            self.require(saved['operation']['status'] == 'confirmed'
                         and saved['inbox']['application_state'] == 'pending'
                         and saved['inbox']['application_error_code'] == 'claim_changed'
                         and saved['inbox']['applied_at'] is None, 'late_ack_claim_change_not_preserved')
            self.wait_database_due(saved)
            case.cycle(consumer)
        applied = case.inspect('late-ack-applied')
        case.expected_action(applied, terminal=terminal)
        same_response_evidence(saved, applied, self.require)
        self.require(applied['operation']['status'] == ('confirmed' if terminal == 'cancelled' else 'unknown')
                     and applied['task']['deleted_at'] is None, 'late_ack_regressed_terminal')
        self.same_finance(terminal_snapshot, applied)
        self.require(case.mock.status()['get'] == previous_gets + 1, 'late_ack_queried_terminal_again')
        producer.stop()
        consumer.stop()
        case.replay(applied, terminal=terminal)


def scenario_dispatch(case, helpers):
    """Run exactly one whitelisted non-N3 case, or fail closed."""
    runner = Scenarios(case, helpers)
    getattr(runner, case.definition['family'])()
