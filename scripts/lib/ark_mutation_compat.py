#!/usr/bin/env python3
"""Fixed-binary mutation handoff. This runner never accepts a production DSN."""
from __future__ import annotations

import argparse
import base64
import datetime as dt
from decimal import Decimal
import hashlib
import io
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import time
import traceback

from ark_mutation_process import BinarySpec, MockArk, ProcessController, ProtocolError, read_verified_artifact

CA = '64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89'
P = '24da28583eddfaad151b9a4a054264a8275b61bb'
PROTOCOL = 'a2c-process-v1'
MYSQL_IMAGE = 'docker.io/library/mysql@sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084'
OWNER_LABEL = 'ai.laiai.ark-mutation-compat.run'
ROOT = Path(__file__).resolve().parents[2]
FINANCIAL_KEYS = ('accounts', 'holds', 'journals', 'ledger_movements', 'ledger_operations', 'ledger_events', 'billing_entries', 'quota_reservations', 'quota_buckets', 'key_usage', 'financial_all')
RUN_DEADLINE = None


class Failure(Exception):
    """Only fixed, non-sensitive codes may reach the terminal/report."""


def require(condition, code):
    if not condition:
        raise Failure(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value, *, exclusive=False):
    raw = (json.dumps(value, ensure_ascii=True, indent=2, sort_keys=True) + '\n').encode()
    flags = os.O_WRONLY | os.O_CREAT | (os.O_EXCL if exclusive else os.O_TRUNC)
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, 'wb') as f:
        f.write(raw)


def utc_now():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec='milliseconds').replace('+00:00', 'Z')


def parse_time(value):
    require(isinstance(value, str) and value.endswith('Z'), 'invalid_snapshot_time')
    try:
        parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    except (TypeError, ValueError):
        raise Failure('invalid_snapshot_time') from None
    require(parsed.tzinfo == dt.timezone.utc, 'invalid_snapshot_time')
    return parsed


def after_time(value):
    return (parse_time(value) + dt.timedelta(milliseconds=1)).isoformat(timespec='milliseconds').replace('+00:00', 'Z')


def case_definitions():
    cases = []
    def add(name, action, family, consumer='Ca', seed='Pon', **extra):
        cases.append(dict(id=name, action=action, family=family, consumer=consumer, seed=seed, **extra))
    for action in ('cancel', 'delete'):
        add(f'L1-{action}', action, 'L1', seed='Ca')
        for consumer in ('Poff', 'Pon'):
            add(f'L2-{action}-{consumer}', action, 'L2', consumer, 'Ca')
        for family in ('N1', 'N2', 'N3'):
            for consumer in ('Ca', 'Poff'):
                add(f'{family}-{action}-{consumer}', action, family, consumer)
        for family in ('R1', 'R2'):
            add(f'{family}-{action}-Ca', action, family)
        for family in ('M1', 'M2'):
            add(f'{family}-{action}', action, family)
        for repeat in (1, 2, 3):
            add(f'M3-{action}-{repeat}', action, 'M3')
    add('N1-wall-cancel-Ca', 'cancel', 'N1', wall=True)
    add('N3-delete-404-Ca', 'delete', 'N3', response='not_found404')
    add('N4-cancel-502-Ca', 'cancel', 'N4', response='upstream502')
    add('N4-delete-EOF-Poff', 'delete', 'N4', 'Poff', response='eof')
    add('Q1-cancel-succeeded-first', 'cancel', 'Q1', terminal='succeeded')
    add('Q1-cancel-cancelled-first', 'cancel', 'Q1', terminal='cancelled')
    add('N3-cancel-both-flags-Ca', 'cancel', 'N3', seed='Pboth', producer='Pboth')
    require(len(cases) == 39 and len({c['id'] for c in cases}) == 39, 'case_manifest_invalid')
    return cases


def clean_environment():
    keep = ('PATH', 'HOME', 'TMPDIR', 'SYSTEMROOT', 'TEMP', 'TMP')
    env = {key: os.environ[key] for key in keep if key in os.environ}
    env.update(TZ='UTC', LANG='C.UTF-8', GOTOOLCHAIN='local', GOENV='off')
    # Build caches/proxy contain no model credentials and are only passed to Go.
    for name in ('GOPROXY', 'GOMODCACHE', 'GOCACHE', 'GOMAXPROCS', 'GOMEMLIMIT'):
        if name in os.environ:
            env[name] = os.environ[name]
    return env


def run_command(argv, *, cwd=None, env=None, timeout=120, output=None, input_bytes=None):
    if RUN_DEADLINE is not None:
        timeout = min(timeout, RUN_DEADLINE - time.monotonic())
        require(timeout > 0, 'run_deadline_exceeded')
    try:
        result = subprocess.run(argv, cwd=cwd, env=env, input=input_bytes,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    except (subprocess.TimeoutExpired, OSError):
        raise Failure('command_timeout_or_unavailable') from None
    if output:
        Path(output).write_bytes(result.stdout + result.stderr)
    require(result.returncode == 0, 'command_failed')
    return result.stdout


def file_manifest(path):
    result = {}
    for p in sorted(path.rglob('*')):
        require(not p.is_symlink(), 'source_symlink_rejected')
        if p.is_file():
            result[p.relative_to(path).as_posix()] = digest(p.read_bytes())
    return result


def snapshot_sources(run_dir, env):
    require(re.fullmatch(r'[0-9a-f]{40}', CA) and re.fullmatch(r'[0-9a-f]{40}', P), 'revision_not_fixed')
    for sha in (CA, P):
        run_command(['git', '-C', str(ROOT), 'cat-file', '-e', sha + '^{commit}'], env=env)
    run_command(['git', '-C', str(ROOT), 'merge-base', '--is-ancestor', CA, P], env=env)
    harness = ROOT / 'core/internal/video/mutation_handoff_test.go'
    require(harness.is_file() and not harness.is_symlink(), 'harness_missing')
    overlay = harness.read_bytes()
    results = {}
    for role, sha in (('Ca', CA), ('P', P)):
        archive = run_command(['git', '-C', str(ROOT), 'archive', '--format=tar', sha], env=env)
        (run_dir / (role + '-source.tar')).write_bytes(archive)
        source = run_dir / (role + '-source')
        source.mkdir(mode=0o700)
        with tarfile.open(fileobj=io.BytesIO(archive), mode='r:') as tar:
            for member in tar.getmembers():
                relative = Path(member.name)
                require(not relative.is_absolute() and '..' not in relative.parts and '.git' not in relative.parts,
                        'unsafe_archive_path')
                require(member.isfile() or member.isdir(), 'unsafe_archive_entry')
                target = source / relative
                if member.isdir():
                    target.mkdir(parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(tar.extractfile(member).read())
                    target.chmod(member.mode & 0o777)
        before = file_manifest(source)
        (source / 'core/internal/video/mutation_handoff_test.go').write_bytes(overlay)
        after = file_manifest(source)
        changed = {k for k in before.keys() | after.keys() if before.get(k) != after.get(k)}
        require(changed == {'core/internal/video/mutation_handoff_test.go'}, 'production_overlay_changed')
        write_json(run_dir / (role + '-source-manifest.json'), before)
        write_json(run_dir / (role + '-overlay-manifest.json'), after)
        results[role] = {'sha': sha, 'archive_sha256': digest(archive), 'source': str(source),
                         'source_manifest_sha256': digest(json.dumps(before, sort_keys=True).encode()),
                         'overlay_manifest_sha256': digest(json.dumps(after, sort_keys=True).encode())}
    old_migrations = {k: v for k, v in file_manifest(Path(results['Ca']['source'])).items() if k.startswith('migrations/')}
    new_migrations = file_manifest(Path(results['P']['source']))
    require(all(new_migrations.get(k) == v for k, v in old_migrations.items()), 'historical_migration_changed')
    write_json(run_dir / 'historical-migrations.json', old_migrations)
    return results


def build_binaries(run_dir, sources, env):
    run_command(['sh', str(ROOT / 'scripts/check-go-toolchain.sh')], env=env, output=run_dir / 'go-baseline.log')
    version = run_command(['go', 'version'], env=env).decode().strip()
    binaries = {}
    target = run_dir / 'bin'
    target.mkdir(mode=0o700)
    for role, source in sources.items():
        directory = Path(source['source']) / 'core'
        tests = target / (role + '-video.test')
        run_command(['go', 'test', '-c', '-o', str(tests), './internal/video'], cwd=directory,
                    env=env, timeout=600, output=run_dir / (role + '-test-build.log'))
        by_role = {'test': str(tests)}
        for command in ('migrate', 'schema-doctor'):
            path = target / (role + '-' + command)
            run_command(['go', 'build', '-o', str(path), './cmd/' + command], cwd=directory,
                        env=env, timeout=600, output=run_dir / (role + '-' + command + '-build.log'))
            by_role[command] = str(path)
        for name, path in by_role.items():
            source[name + '_sha256'] = digest(Path(path).read_bytes())
            run_command(['go', 'version', '-m', path], env=env, output=run_dir / (role + '-' + name + '-build-info.log'))
        binaries[role] = by_role
    return binaries, version


class OwnedMySQL:
    def __init__(self, run_dir, run_id, env):
        self.run_dir, self.run_id, self.env = run_dir, run_id, env
        self.name = 'laiai-a2c-' + run_id
        self.password = secrets.token_hex(24)
        self.attempted = False
        self.removed = False
        self.env_file = run_dir / 'mysql.env'
        self.port = None

    def start(self):
        fd = os.open(self.env_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f:
            f.write('MYSQL_ROOT_PASSWORD=' + self.password + '\nMYSQL_ROOT_HOST=%\n')
        self.attempted = True
        run_command(['docker', 'run', '-d', '--name', self.name, '--label', OWNER_LABEL + '=' + self.run_id,
                     '--cpus=2', '--memory=1536m', '--memory-swap=2g', '--env-file', str(self.env_file),
                     '-p', '127.0.0.1::3306', MYSQL_IMAGE, '--innodb-buffer-pool-size=268435456',
                     '--max-connections=80', '--skip-log-bin'], env=self.env, timeout=180)
        row = json.loads(run_command(['docker', 'inspect', self.name], env=self.env))[0]
        require(row['Config']['Labels'].get(OWNER_LABEL) == self.run_id, 'container_owner_mismatch')
        binding = row['NetworkSettings']['Ports']['3306/tcp']
        require(len(binding) == 1 and binding[0]['HostIp'] == '127.0.0.1', 'database_not_loopback')
        self.port = int(binding[0]['HostPort'])
        image = json.loads(run_command(['docker', 'image', 'inspect', row['Image']], env=self.env))[0]
        write_json(self.run_dir / 'mysql-image.json', {'requested': MYSQL_IMAGE, 'image_id': row['Image'], 'repo_digests': image.get('RepoDigests', [])})
        end = time.monotonic() + 120
        while time.monotonic() < end:
            try:
                self.sql('SELECT 1;')
                return
            except Failure:
                time.sleep(1)
        raise Failure('database_start_timeout')

    def sql(self, statement):
        env = dict(self.env, MYSQL_PWD=self.password)
        return run_command(['docker', 'exec', '-i', '-e', 'MYSQL_PWD', self.name, 'mysql', '-uroot', '--batch', '--skip-column-names'],
                           env=env, input_bytes=statement.encode(), timeout=15)

    def create_database(self, case_id):
        name = 'ark_test_action_' + self.run_id + '_' + digest(case_id.encode())[:12]
        require(re.fullmatch(r'ark_test_action_[a-f0-9]{16}_[a-f0-9]{12}', name), 'invalid_private_database')
        self.sql('CREATE DATABASE `' + name + '` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;')
        return name, 'root:' + self.password + '@tcp(127.0.0.1:' + str(self.port) + ')/' + name + '?parseTime=true&loc=UTC'

    def close(self):
        try:
            if self.attempted:
                result = subprocess.run(['docker', 'inspect', self.name], env=self.env, capture_output=True, timeout=20)
                if result.returncode == 0:
                    row = json.loads(result.stdout)[0]
                    require(row['Config']['Labels'].get(OWNER_LABEL) == self.run_id, 'cleanup_owner_mismatch')
                    run_command(['docker', 'rm', '-fv', self.name], env=self.env, timeout=45, output=self.run_dir / 'mysql-cleanup.log')
                else:
                    # An unavailable daemon must not be mistaken for an absent container.
                    names = run_command(['docker', 'ps', '-a', '--format', '{{.Names}}'], env=self.env).decode().splitlines()
                    require(self.name not in names, 'cleanup_container_unknown')
                self.removed = True
        finally:
            self.env_file.unlink(missing_ok=True)


def validate_finance(snapshot, *, terminal=None):
    finance = snapshot['finance']
    expected = {'credit_usd': Decimal('100'), 'available_usd': Decimal('99.136'), 'frozen_usd': Decimal('.864'),
                'captured_usd': Decimal('0'), 'released_usd': Decimal('0')}
    if terminal == 'succeeded':
        expected.update(available_usd=Decimal('99.9992'), frozen_usd=Decimal('0'), captured_usd=Decimal('.0008'), released_usd=Decimal('.8632'))
    elif terminal == 'cancelled':
        expected.update(available_usd=Decimal('100'), frozen_usd=Decimal('0'), captured_usd=Decimal('0'), released_usd=Decimal('.864'))
    for key, value in expected.items():
        actual = finance.get(key)
        require(isinstance(actual, str), 'finance_not_exact_string')
        try:
            require(Decimal(actual).is_finite() and Decimal(actual) == value, 'finance_oracle_mismatch')
        except ArithmeticError:
            raise Failure('finance_invalid_decimal') from None
    require(finance.get('ledger_drift_count') == 0, 'ledger_drift')
    for key in FINANCIAL_KEYS:
        require(re.fullmatch(r'[0-9a-f]{64}', snapshot['fingerprints'].get(key, '')) is not None, 'finance_fingerprint_missing')


def same_finance(before, after):
    require(all(before['fingerprints'][key] == after['fingerprints'][key] for key in FINANCIAL_KEYS), 'finance_fingerprint_changed')


def same_mutation_evidence(before, after):
    require(before['mutation_attempt'] == after['mutation_attempt'], 'mutation_registration_changed')
    immutable = ('count', 'receipt_id', 'attempt_id', 'response_payload_ref', 'outcome', 'http_status',
                 'response_received_count', 'evidence_hmac_sha256')
    require(all(before['inbox'][key] == after['inbox'][key] for key in immutable), 'immutable_response_changed')


class Case:
    """Own one private DB, mock, and exact child PIDs. Never repair business rows."""

    def __init__(self, definition, run_dir, run_id, database, binaries, sources, env):
        self.definition, self.run_id = definition, run_id
        self.id, self.action = definition['id'], definition['action']
        self.dir = run_dir / 'cases' / self.id
        self.dir.mkdir(mode=0o700, parents=True)
        self.database, self.binaries, self.sources, self.env = database, binaries, sources, env
        self.clock = utc_now()
        self.processes, self.process_clocks, self.inspections = [], {}, []
        self.mock = None
        self.inspector = None
        self.seed, self.baseline = None, None
        self.sequence = 0
        self.provider_token = secrets.token_hex(24)
        self.auth_secret, self.hmac_secret = secrets.token_hex(24), secrets.token_hex(24)
        self.data_key = 'a2c:' + base64.b64encode(secrets.token_bytes(32)).decode()
        self.db_name, self.dsn = database.create_database(self.id)
        self.go = Path(shutil.which('go', path=env.get('PATH')) or '').resolve()
        require(self.go.is_file(), 'go_unavailable')

    def check_deadline(self):
        require(RUN_DEADLINE is None or time.monotonic() < RUN_DEADLINE, 'run_deadline_exceeded')

    def prepare(self):
        self.check_deadline()
        migration_env = dict(self.env, LAIAI_DSN=self.dsn)
        for index, role in enumerate(('Ca', 'P', 'P')):
            migrations = str(Path(self.sources[role]['source']) / 'migrations')
            run_command([self.binaries[role]['migrate'], '-dir', migrations], env=migration_env,
                        output=self.dir / ('migration-' + str(index) + '-' + role + '.log'))
        run_command([self.binaries['P']['schema-doctor'], '-dir', str(Path(self.sources['P']['source']) / 'migrations')],
                    env=migration_env, output=self.dir / 'schema-doctor.log')
        self.mock = MockArk(self.run_id, self.id, self.provider_token, secrets.token_hex(24)).start()
        self.mock.set(['queued', 'succeeded'] if self.action == 'delete' else ['queued'], 'ack204')
        proc = self.start(self.definition['seed'], 'seed', 'seed')
        cmd = proc.send('seed')
        event = proc.wait_event('seeded', cmd)
        proc.wait_event('pending_intent', cmd)
        proc.wait_done(cmd)
        self.seed = read_verified_artifact(self.dir, event['data']['seed_file'], event['data']['seed_sha256'])
        require(self.seed['action'] == self.action, 'seed_action_mismatch')
        self.clock = self.seed['seed_finished_clock']
        proc.stop()
        self.mock.set(['queued'], self.definition.get('response', 'ack204'))
        self.baseline = self.inspect('baseline')
        validate_finance(self.baseline, terminal='succeeded' if self.action == 'delete' else None)
        require(self.baseline['fingerprints']['financial_all'] == self.seed['baseline_financial_sha256'], 'seed_financial_mismatch')
        require(self.baseline['operation']['status'] == 'pending' and self.baseline['mutation_attempt']['count'] == 0
                and self.baseline['inbox']['count'] == 0, 'seed_not_pending')
        require(self.mock.status()['delete'] == 0 and self.mock.status()['post'] == 1, 'seed_http_counts_invalid')

    def start(self, role, mode='worker', prefix='worker'):
        self.check_deadline()
        self.sequence += 1
        instance = prefix.lower() + '-' + role.lower() + '-' + str(self.sequence)
        source_role = 'Ca' if role == 'Ca' else 'P'
        spec = BinarySpec(Path(self.binaries[source_role]['test']), self.sources[source_role]['sha'],
                          self.sources[source_role]['test_sha256'])
        env = dict(A2C_PROTOCOL=PROTOCOL, A2C_RUN_ID=self.run_id, A2C_CASE_ID=self.id,
                   A2C_INSTANCE=instance, A2C_MODE=mode, A2C_BUILD_ROLE=role, A2C_SOURCE_SHA=spec.source_sha,
                   A2C_CASE_DIR=str(self.dir), A2C_CLOCK_MODE='wall' if self.definition.get('wall') else 'logical',
                   A2C_INITIAL_TIME=self.clock, A2C_MOCK_ORIGIN=self.mock.origin,
                   A2C_UPSTREAM_ID=self.mock.upstream_id, A2C_PROVIDER_TOKEN=self.provider_token,
                   LAIAI_DSN=self.dsn, LAIAI_AUTH_SECRET=self.auth_secret,
                   LAIAI_DATA_KEYS=self.data_key, LAIAI_DATA_KEY_ACTIVE='a2c', LAIAI_VIDEO_HMAC_SECRET=self.hmac_secret,
                   LAIAI_VIDEO_RESPONSE_INBOX='true' if role == 'Pboth' else 'false',
                   LAIAI_DB_MAX_OPEN_CONNS='8', LAIAI_DB_MAX_IDLE_CONNS='4')
        if role != 'Ca':
            env['LAIAI_VIDEO_MUTATION_RESPONSE_INBOX'] = 'false' if role == 'Poff' else 'true'
        proc = ProcessController(spec, self.go, self.dir, env)
        self.processes.append(proc)
        proc.start()
        self.process_clocks[proc.instance] = self.clock
        return proc

    def advance(self, to):
        self.check_deadline()
        require(not self.definition.get('wall'), 'wall_clock_advance_rejected')
        require(parse_time(to) >= parse_time(self.clock), 'clock_would_regress')
        self.clock = to

    def sync_clock(self, proc):
        if not self.definition.get('wall') and self.process_clocks[proc.instance] != self.clock:
            cmd = proc.send('advance_clock', to=self.clock)
            proc.wait_done(cmd)
            self.process_clocks[proc.instance] = self.clock

    def cycle(self, proc, barrier='none'):
        self.check_deadline()
        self.sync_clock(proc)
        cmd = proc.send('run_once', barrier=barrier)
        if barrier == 'none':
            proc.wait_done(cmd)
        else:
            proc.wait_event('barrier_reached', cmd)
        return cmd

    def release(self, proc, cmd, barrier):
        released = proc.send('release_barrier', barrier=barrier, owner_cmd_id=cmd)
        proc.wait_done(released)
        proc.wait_done(cmd)

    def inspect(self, label):
        self.check_deadline()
        if self.inspector is None:
            self.inspector = self.start('Ca', 'inspect', 'inspect')
        self.sync_clock(self.inspector)
        cmd = self.inspector.send('inspect', label=label)
        event = self.inspector.wait_event('inspected', cmd)
        self.inspector.wait_done(cmd)
        data = event['data']
        result = read_verified_artifact(self.dir, data['snapshot_file'], data['snapshot_sha256'])
        require(result['run_id'] == self.run_id and result['case_id'] == self.id, 'snapshot_identity_mismatch')
        if self.seed:
            require(result['ids']['operation_id'] == self.seed['operation_id'] and result['ids']['task_id'] == self.seed['task_id'],
                    'snapshot_resource_mismatch')
        self.inspections.append(dict(label=label, file=data['snapshot_file'], sha256=data['snapshot_sha256']))
        return result

    def registered(self, snapshot, receipts=0):
        attempt = snapshot['mutation_attempt']
        require(attempt['count'] == 1 and attempt['storage_protocol'] == 'inbox-actions-v1'
                and attempt['input_schema'] == 2 and attempt['observation_schema'] == 1 and attempt['delivery_schema'] == 0,
                'registered_action_contract_mismatch')
        require(attempt['action'] == self.seed['mutation_kind'] and attempt['target'] == self.mock.upstream_id,
                'registered_action_identity_mismatch')
        require(snapshot['inbox']['count'] == receipts, 'receipt_count_mismatch')
        require(snapshot['legacy']['attempt_count'] == 0 and not snapshot['legacy']['receipt_present'], 'mixed_storage_protocols')
        if receipts:
            require(snapshot['inbox']['attempt_id'] == attempt['id'] and snapshot['inbox']['response_received_count'] == 1,
                    'receipt_evidence_mismatch')

    def check_pending(self, snapshot, receipts):
        self.registered(snapshot, receipts)
        input_fact = snapshot['action_input']
        require(input_fact is not None and input_fact['capture_status'] == 'staged'
                and input_fact['wrapped_key_present'] is True and input_fact['nonce_present'] is True
                and input_fact['staging_count'] == 1, 'registered_input_not_durable')
        require(snapshot['operation']['status'] == 'in_flight' and snapshot['operation']['lease_until'] is not None,
                'live_claim_missing')
        require(parse_time(snapshot['operation']['lease_until']) > parse_time(snapshot['clock']), 'claim_already_expired')
        if receipts:
            require(snapshot['inbox']['application_state'] == 'pending' and snapshot['inbox']['applied_at'] is None,
                    'receipt_applied_before_handoff')
            require(snapshot['audits']['response_capture_status'] == ('staged' if self.seed['history_enabled'] else 'disabled')
                    and snapshot['audits']['response_staging_count'] == (1 if self.seed['history_enabled'] else 0),
                    'response_capture_policy_mismatch')
        same_finance(self.baseline, snapshot)

    def expected_action(self, snapshot, *, registered=True, terminal=None):
        if registered:
            self.registered(snapshot, 1)
            require(snapshot['inbox']['application_state'] == 'applied' and snapshot['inbox']['applied_at'] is not None,
                    'receipt_not_applied')
            require(terminal is not None or bool(snapshot['inbox']['applied_event_id']), 'action_event_missing')
        else:
            require(snapshot['mutation_attempt']['count'] == 0 and snapshot['inbox']['count'] == 0
                    and snapshot['legacy']['receipt_present'] and snapshot['legacy']['receipt_applied_at'] is not None,
                    'legacy_receipt_not_applied')
        if terminal is None:
            require(snapshot['operation']['status'] == ('unknown' if self.action == 'cancel' else 'confirmed'),
                    'action_outcome_mismatch')
            require(snapshot['task']['status'] == ('queued' if self.action == 'cancel' else 'succeeded'),
                    'action_task_status_mismatch')
            require((snapshot['task']['deleted_at'] is not None) == (self.action == 'delete'), 'delete_visibility_mismatch')
            same_finance(self.baseline, snapshot)
        else:
            require(snapshot['task']['status'] == terminal, 'terminal_task_mismatch')
            validate_finance(snapshot, terminal=terminal)
        require(self.mock.status()['delete'] == 1, 'delete_not_exactly_once')

    def replay(self, stable, *, registered=True, terminal=None, count=1):
        for role in ('Ca', 'Poff'):
            proc = self.start(role, prefix='replay')
            for number in (1, 2):
                self.cycle(proc)
                current = self.inspect('replay-' + role.lower() + '-' + str(number))
                same_finance(stable, current)
                require(current['fingerprints']['action_application_events'] == stable['fingerprints']['action_application_events'],
                        'action_events_changed_on_replay')
                require(current['operation']['status'] == stable['operation']['status'], 'operation_changed_on_replay')
                require(current['mutation_attempt'] == stable['mutation_attempt'] and current['inbox'] == stable['inbox']
                        and current['legacy'] == stable['legacy'], 'receipt_changed_on_replay')
                require(self.mock.status()['delete'] == count, 'delete_repeated_on_replay')
            proc.stop()

    def close(self, *, success):
        errors = []
        for proc in reversed(self.processes):
            try:
                if success and proc.returncode is None:
                    proc.stop()
                proc.close()
            except (ProtocolError, Failure, OSError):
                errors.append('process_cleanup_failed')
                proc.abort_owned()
        if self.mock:
            try:
                write_json(self.dir / 'mock-final.json', self.mock.status())
                self.mock.close()
            except (ProtocolError, OSError):
                errors.append('mock_cleanup_failed')
        write_json(self.dir / 'snapshots.json', self.inspections)
        require(not errors, 'case_cleanup_failed')


def scenario_n3(case):
    producer = case.start(case.definition.get('producer', 'Pon'), prefix='sender')
    cmd = case.cycle(producer, 'inbox_durable_before_apply')
    durable = case.inspect('durable-before-kill')
    case.check_pending(durable, 1)
    require(case.mock.status()['delete'] == 1, 'durable_receipt_without_delete')
    producer.kill_expected('inbox_durable_before_apply', cmd)
    consumer = case.start(case.definition['consumer'], prefix='first')
    case.cycle(consumer)
    before_expiry = case.inspect('before-lease-expiry')
    case.check_pending(before_expiry, 1)
    require(before_expiry['inbox'] == durable['inbox'], 'receipt_changed_while_claim_live')
    case.advance(after_time(durable['operation']['lease_until']))
    case.cycle(consumer)
    applied = case.inspect('first-consumer-applied')
    same_mutation_evidence(durable, applied)
    case.expected_action(applied)
    consumer.stop()
    case.replay(applied)


def dispatch(case):
    definitions = {item['id']: item for item in case_definitions()}
    require(case.id in definitions and case.definition == definitions[case.id], 'scenario_definition_mismatch')
    if case.definition['family'] == 'N3':
        scenario_n3(case)
    else:
        from ark_mutation_scenarios import scenario_dispatch
        scenario_dispatch(case, dict(require=require, after_time=after_time, parse_time=parse_time,
                                     validate_finance=validate_finance, same_finance=same_finance, write_json=write_json))


def main(argv=None):
    global RUN_DEADLINE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--case', action='append', choices=[c['id'] for c in case_definitions()], dest='selected')
    parser.add_argument('--timeout-seconds', type=int, default=3600)
    args = parser.parse_args(argv)
    require(os.name == 'posix', 'linux_runner_required')
    require(60 <= args.timeout_seconds <= 7200, 'invalid_run_timeout')
    require(not any(k.startswith(('LAIAI_', 'ARK_TEST_', 'A2C_')) for k in os.environ), 'external_business_environment_rejected')
    require(args.selected is None or len(args.selected) == len(set(args.selected)), 'duplicate_selected_case')
    RUN_DEADLINE = time.monotonic() + args.timeout_seconds
    os.umask(0o077)
    output = args.output.absolute()
    require(not output.exists() and not output.is_symlink(), 'output_must_be_new')
    output.mkdir(mode=0o700, parents=True)
    require(output.resolve() == output, 'output_symlink_rejected')
    run_id, env = secrets.token_hex(8), clean_environment()
    definitions = case_definitions()
    selected = definitions if args.selected is None else [c for c in definitions if c['id'] in args.selected]
    report = dict(protocol=PROTOCOL, run_id=run_id, started_at=utc_now(), revisions={'Ca': CA, 'P': P},
                  full_matrix=args.selected is None, expected_cases=39, selected_cases=[c['id'] for c in selected],
                  passed=False, completed_cases=[], cleanup_passed=False, error_code=None)
    database = OwnedMySQL(output, run_id, env)
    try:
        sources = snapshot_sources(output, env)
        binaries, version = build_binaries(output, sources, env)
        report.update(sources=sources, go_version=version)
        write_json(output / 'build-evidence.json', report)
        database.start()
        for definition in selected:
            case = None
            success = False
            started = time.monotonic()
            try:
                case = Case(definition, output, run_id, database, binaries, sources, env)
                case.prepare()
                dispatch(case)
                success = True
            finally:
                if case:
                    case.close(success=success)
            item = dict(id=definition['id'], passed=True, duration_seconds=round(time.monotonic() - started, 3))
            report['completed_cases'].append(item)
            write_json(output / 'progress.json', report)
            print('A2C_CASE_VERIFIED ' + definition['id'], flush=True)
    except (Failure, ProtocolError) as exc:
        report['error_code'] = exc.code if isinstance(exc, ProtocolError) else str(exc)
    except Exception as exc:
        report['error_code'] = 'unexpected_runner_failure'
        # Keep locations for diagnosis without exposing exception text, locals,
        # credentials, SQL or source lines.
        report['failure_locations'] = [dict(file=Path(frame.filename).name, line=frame.lineno, function=frame.name)
                                       for frame in traceback.extract_tb(exc.__traceback__)]
    finally:
        try:
            database.close()
            report['cleanup_passed'] = True
        except Exception:
            report['error_code'] = report['error_code'] or 'database_cleanup_failed'
        report['finished_at'] = utc_now()
        report['partial_verified'] = bool(report['completed_cases']) and len(report['completed_cases']) == len(selected) and report['error_code'] is None and report['cleanup_passed']
        report['passed'] = report['partial_verified'] and args.selected is None and len(report['completed_cases']) == 39
        write_json(output / 'report.json', report)
    print('A2C_FULL_MATRIX_PASSED' if report['passed'] else ('A2C_PARTIAL_VERIFIED' if report['partial_verified'] else 'A2C_FAILED ' + str(report['error_code'])), flush=True)
    return 0 if report['passed'] or report['partial_verified'] else 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Failure as error:
        print('A2C_FAILED ' + str(error), file=sys.stderr)
        sys.exit(1)
