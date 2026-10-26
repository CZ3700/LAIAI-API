#!/usr/bin/env python3
"""Bounded CI host and safe metadata export; never accepts a database address."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
LABEL = 'ai.laiai.ark-mutation-compat.run'
HEX16 = re.compile(r'[0-9a-f]{16}')
HEX64 = re.compile(r'[0-9a-f]{64}')
MAX_FILE = 32 * 1024 * 1024
MAX_EXPORT = 256 * 1024 * 1024
STOP = False
CASE_IDS = frozenset('''L1-cancel L1-delete L2-cancel-Poff L2-cancel-Pon L2-delete-Poff L2-delete-Pon
N1-cancel-Ca N1-cancel-Poff N1-delete-Ca N1-delete-Poff N1-wall-cancel-Ca
N2-cancel-Ca N2-cancel-Poff N2-delete-Ca N2-delete-Poff N3-cancel-Ca N3-cancel-Poff
N3-delete-Ca N3-delete-Poff N3-delete-404-Ca N3-cancel-both-flags-Ca
N4-cancel-502-Ca N4-delete-EOF-Poff R1-cancel-Ca R1-delete-Ca R2-cancel-Ca R2-delete-Ca
M1-cancel M1-delete M2-cancel M2-delete M3-cancel-1 M3-cancel-2 M3-cancel-3
M3-delete-1 M3-delete-2 M3-delete-3 Q1-cancel-succeeded-first Q1-cancel-cancelled-first'''.split())


class CIError(Exception):
    """A fixed non-sensitive code, never an arbitrary subprocess diagnostic."""


def require(value, code):
    if not value:
        raise CIError(code)


def private_json(path, value):
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write('\n')
    path.chmod(0o600)


def safe_read(path, limit=MAX_FILE):
    require(path.is_file() and not path.is_symlink(), 'artifact_not_regular')
    require(path.stat().st_size <= limit, 'artifact_too_large')
    data = path.read_bytes()
    require(len(data) <= limit, 'artifact_too_large')
    return data


def new_output(path):
    path = path.absolute()
    require(not path.exists() and not path.is_symlink() and path.resolve() == path, 'output_not_fresh')
    path.mkdir(mode=0o700, parents=True)
    return path


def clean_env():
    require(not any(k.startswith(('LAIAI_', 'ARK_TEST_', 'A2C_')) for k in os.environ), 'external_business_environment')
    env = {k: os.environ[k] for k in ('PATH', 'HOME', 'TMPDIR', 'GOPROXY', 'GOMODCACHE', 'GOCACHE', 'GOMAXPROCS', 'GOMEMLIMIT') if k in os.environ}
    env.update(GOTOOLCHAIN='local', GOENV='off', TZ='UTC', LANG='C.UTF-8')
    return env


def proc_fact(pid):
    """Linux stat: only ownership facts, never cmdline or environment."""
    try:
        path = Path('/proc') / str(pid)
        fields = (path / 'stat').read_text().rsplit(')', 1)[1].split()
        return dict(pid=pid, state=fields[0], group=int(fields[2]), session=int(fields[3]),
                    start=int(fields[19]), uid=path.stat().st_uid)
    except (FileNotFoundError, ProcessLookupError):
        return None
    except (OSError, ValueError, IndexError):
        raise CIError('process_identity_unavailable') from None


class OwnedProcess:
    def __init__(self, argv, env, log):
        self.log = log
        self.stream = log.open('xb')
        try:
            self.proc = subprocess.Popen(argv, cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
                                         stdout=self.stream, stderr=subprocess.STDOUT, start_new_session=True)
        except Exception:
            self.stream.close()
            raise CIError('process_start_failed') from None
        try:
            self.identity = proc_fact(self.proc.pid)
            require(self.identity and self.identity['session'] == self.proc.pid and self.identity['group'] == self.proc.pid,
                    'process_identity_missing')
        except Exception:
            # Popen still owns this unreaped child, so this direct PID cannot be
            # reused. Without stat identity we cannot claim or kill descendants.
            try:
                self.proc.terminate()
                try:
                    self.proc.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    self.proc.kill()
                    self.proc.wait(timeout=2)
            except Exception:
                raise CIError('process_cleanup_failed') from None
            finally:
                try:
                    self.stream.close()
                except Exception:
                    raise CIError('process_cleanup_failed') from None
            raise CIError('process_identity_missing') from None

    def members(self):
        leader = proc_fact(self.proc.pid)
        if leader:
            require(leader['start'] == self.identity['start'], 'process_identity_changed')
        members = []
        for entry in Path('/proc').iterdir():
            if not entry.name.isdecimal():
                continue
            fact = proc_fact(int(entry.name))
            if fact and fact['session'] == self.proc.pid and fact['state'] != 'Z':
                require(fact['uid'] == self.identity['uid'] and fact['start'] >= self.identity['start'],
                        'process_session_not_owned')
                members.append(fact)
        return members

    def stop(self):
        # All children inherit the new session. Do not use ps-name matching or
        # kill a historical PID. A Linux SID cannot be reused while members live.
        try:
            for sig, seconds in ((signal.SIGTERM, 8), (signal.SIGKILL, 4)):
                members = self.members()
                if not members:
                    break
                for fact in members:
                    current = proc_fact(fact['pid'])
                    if current and current['start'] == fact['start'] and current['session'] == self.proc.pid:
                        try:
                            os.kill(fact['pid'], sig)
                        except ProcessLookupError:
                            pass
                until = time.monotonic() + seconds
                while self.members() and time.monotonic() < until:
                    self.proc.poll()
                    time.sleep(0.05)
            self.proc.poll()
            require(not self.members(), 'process_cleanup_failed')
        except Exception:
            raise CIError('process_cleanup_failed') from None
        finally:
            try:
                self.stream.close()
            except Exception:
                raise CIError('process_cleanup_failed') from None


class DatabaseStopper:
    """Independent cleanup deadline; only stop, never delete a failure volume."""
    def __init__(self, run, env):
        self.run, self.env = run, env
        self.run_id = None
        self.cid = None
        self.stopped = False

    def command(self, argv):
        try:
            return subprocess.run(['docker', *argv], env=self.env, stdin=subprocess.DEVNULL,
                                  capture_output=True, timeout=30, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise CIError('cleanup_docker_unavailable') from None

    def observe_run(self):
        for name in ('report.json', 'progress.json', 'build-evidence.json'):
            path = self.run / name
            if not path.exists():
                continue
            try:
                value = json.loads(safe_read(path, 8 << 20)).get('run_id')
            except (ValueError, AttributeError):
                continue  # A progress file may currently be being replaced.
            require(isinstance(value, str) and HEX16.fullmatch(value), 'cleanup_run_identity_invalid')
            require(self.run_id is None or self.run_id == value, 'cleanup_run_identity_changed')
            self.run_id = value
            return

    def inspect(self):
        if not self.run_id:
            return None
        name = 'laiai-a2c-' + self.run_id
        template = '{{.Id}} {{index .Config.Labels "' + LABEL + '"}} {{.State.Running}}'
        result = self.command(['inspect', '--format', template, name])
        if result.returncode:
            names = self.command(['ps', '-a', '--format', '{{.Names}}'])
            require(names.returncode == 0, 'cleanup_docker_unavailable')
            require(name not in names.stdout.decode('ascii').splitlines(), 'cleanup_container_unknown')
            return None
        fields = result.stdout.decode('ascii').strip().split()
        require(len(fields) == 3 and HEX64.fullmatch(fields[0]) and fields[1] == self.run_id
                and fields[2] in ('true', 'false'), 'cleanup_owner_mismatch')
        require(self.cid is None or self.cid == fields[0], 'cleanup_container_replaced')
        self.cid = fields[0]
        return fields[2] == 'true'

    def observe(self):
        self.observe_run()
        return self.inspect()

    def finish(self):
        running = self.observe()
        if running is None:
            return True
        if running:
            # Pin the command to the observed CID, not the mutable name.
            result = self.command(['stop', '--time', '15', self.cid])
            require(result.returncode == 0 and self.inspect() is False, 'cleanup_stop_failed')
            self.stopped = True
        return False  # Retained container means normal cleanup did not complete.


TOP = {'ci-report.json', 'tool-tests.json', 'overlay-manifest.json', 'entry-manifest.json'}
RUN_JSON = {'report.json', 'progress.json', 'build-evidence.json', 'mysql-image.json',
            'historical-migrations.json', 'Ca-source-manifest.json', 'P-source-manifest.json',
            'Ca-overlay-manifest.json', 'P-overlay-manifest.json'}
CASE_JSON = {'seed.json', 'seed-baseline.json', 'mock-final.json', 'snapshots.json',
             'competition.json', 'wall-lease-wait.json', 'database-retry-wait.json'}
PROCESS_FILES = {'process.json', 'events.jsonl', 'test2json.jsonl', 'stdout.log', 'stderr.log', 'test2json.stderr.log'}
FORBIDDEN_KEYS = {'password', 'secret', 'dsn', 'authorization', 'credential_enc', 'credential_cipher',
                  'access_key', 'secret_key', 'session_token', 'auth_secret', 'data_keys', 'hmac_secret'}


def export_allowed(relative):
    p = relative.parts
    if len(p) == 1:
        return p[0] in TOP
    if len(p) == 2 and p[0] == 'run':
        return p[1] in RUN_JSON or re.fullmatch(r'(Ca|P)-(test|migrate|schema-doctor)-build-info\.log', p[1]) is not None
    if len(p) < 4 or p[:2] != ('run', 'cases') or p[2] not in CASE_IDS:
        return False
    if len(p) == 4:
        return p[3] in CASE_JSON
    if len(p) == 5 and p[3] in ('snapshots', 'barriers'):
        return re.fullmatch(r'[A-Za-z0-9_-]+\.json', p[4]) is not None
    return (len(p) == 6 and p[3] == 'processes' and re.fullmatch(r'[a-z0-9-]{1,80}', p[4])
            and p[5] in PROCESS_FILES)


def check_metadata(value):
    if isinstance(value, dict):
        require(not any(str(k).lower() in FORBIDDEN_KEYS for k in value), 'export_secret_field')
        for item in value.values():
            check_metadata(item)
    elif isinstance(value, list):
        for item in value:
            check_metadata(item)


def safe_export(root):
    staging, target = root / '.export-pending', root / 'export'
    require(not staging.exists() and not target.exists(), 'export_not_fresh')
    staging.mkdir(mode=0o700)
    manifest, total = {}, 0
    for path in sorted(root.rglob('*')):
        relative = path.relative_to(root)
        if not export_allowed(relative):
            continue
        current = root
        for part in relative.parts:
            current /= part
            require(not current.is_symlink(), 'export_symlink_rejected')
        data = safe_read(path)
        total += len(data)
        require(total <= MAX_EXPORT and len(manifest) < 10000, 'export_limit')
        lowered = data.lower()
        require(not any(marker in lowered for marker in (b'@tcp(', b'bearer ', b'-----begin private key', b'mysql_root_password=')),
                'export_sensitive_content')
        if path.suffix == '.json':
            check_metadata(json.loads(data))
        elif path.suffix == '.jsonl':
            for line in data.splitlines():
                check_metadata(json.loads(line))
        destination = staging / relative
        destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with destination.open('xb') as stream:
            stream.write(data)
        manifest[relative.as_posix()] = hashlib.sha256(data).hexdigest()
    require('ci-report.json' in manifest, 'export_report_missing')
    private_json(staging / 'export-manifest.json', dict(evidence_kind='metadata_only', files=manifest))
    staging.rename(target)


def publish_failure_summary(root, report):
    """Reconcile an already published export after a late cancellation."""
    local = root / 'ci-report.json'
    require(local.is_file() and not local.is_symlink(), 'failure_report_not_owned')
    local.unlink()
    private_json(local, report)
    target = root / 'export'
    if not target.exists():
        target.mkdir(mode=0o700)
    require(target.is_dir() and not target.is_symlink(), 'failure_export_not_owned')
    exported = target / 'ci-report.json'
    if exported.exists():
        require(exported.is_file() and not exported.is_symlink(), 'failure_report_not_owned')
        exported.unlink()
    private_json(exported, report)
    manifest = target / 'export-manifest.json'
    if manifest.exists():
        value = json.loads(safe_read(manifest))
        require(value.get('evidence_kind') == 'metadata_only' and isinstance(value.get('files'), dict)
                and 'ci-report.json' in value['files'], 'failure_manifest_invalid')
        value['files']['ci-report.json'] = hashlib.sha256(safe_read(exported)).hexdigest()
        manifest.unlink()
        private_json(manifest, value)


def run_phase(argv, env, root, phase, deadline, db=None):
    owned = OwnedProcess(argv, env, root / (phase + '.private.log'))
    next_observe = 0.0
    try:
        while owned.proc.poll() is None:
            require(not STOP, 'ci_interrupted')
            require(time.monotonic() < deadline, 'ci_timeout')
            require(owned.log.stat().st_size <= 64 << 20, 'ci_output_limit')
            if db and time.monotonic() >= next_observe:
                db.observe()
                next_observe = time.monotonic() + 2
            time.sleep(0.1)
        require(owned.proc.returncode == 0, phase + '_failed')
        require(not owned.members(), 'orphan_processes')
    finally:
        owned.stop()


def main(argv=None):
    global STOP
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--timeout-seconds', type=int, default=3900)
    args = parser.parse_args(argv)
    require(sys.platform == 'linux', 'linux_required')
    require(120 <= args.timeout_seconds <= 7200, 'invalid_timeout')
    env = clean_env()
    root = new_output(args.output)
    os.umask(0o077)
    STOP = False
    def interrupted(_signum, _frame):
        global STOP
        STOP = True
    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    db = DatabaseStopper(root / 'run', env)
    report = dict(contract='a2c-ci-v1', passed=False, incomplete=True, cleanup_passed=False, process_cleanup_passed=True,
                  evidence_kind='full_binary_matrix_required', error_code=None)
    deadline = time.monotonic() + args.timeout_seconds
    try:
        tracked = ['scripts/lib/ark_mutation_ci.py', 'scripts/lib/ark_mutation_verify.py',
                   'scripts/tests/run_ark_mutation_tools.py', 'scripts/lib/ark_mutation_compat.py',
                   'scripts/lib/ark_mutation_process.py', 'scripts/lib/ark_mutation_scenarios.py',
                   'core/internal/video/mutation_handoff_test.go']
        hashes = {name: hashlib.sha256(safe_read(ROOT / name)).hexdigest() for name in tracked}
        private_json(root / 'entry-manifest.json', hashes)
        private_json(root / 'overlay-manifest.json', {'core/internal/video/mutation_handoff_test.go': hashes[tracked[-1]]})
        run_phase(['sh', 'scripts/check-go-toolchain.sh'], env, root, 'toolchain', deadline)
        run_phase([sys.executable, 'scripts/tests/run_ark_mutation_tools.py', '--output', str(root / 'tool-tests.json')], env, root, 'tools', deadline)
        run_phase([sys.executable, 'scripts/lib/ark_mutation_compat.py', '--output', str(root / 'run'),
                   '--timeout-seconds', str(max(60, min(3600, args.timeout_seconds - 60)))], env, root, 'matrix', deadline, db)
        run_phase([sys.executable, 'scripts/lib/ark_mutation_verify.py', str(root / 'run'), str(root / 'overlay-manifest.json')],
                  env, root, 'verification', deadline)
        require(not STOP, 'ci_interrupted')
        report['passed'], report['incomplete'] = True, False
    except CIError as error:
        report['error_code'] = str(error)
        if str(error) in ('process_cleanup_failed', 'process_identity_missing', 'process_identity_changed', 'process_session_not_owned'):
            report['process_cleanup_passed'] = False
    except Exception:
        report['error_code'] = 'unexpected_ci_failure'
    finally:
        try:
            report['cleanup_passed'] = db.finish() and report['process_cleanup_passed']
            require(report['cleanup_passed'], 'database_retained_after_failure')
        except Exception:
            report['error_code'] = report['error_code'] or 'ci_cleanup_failed'
        if STOP:
            report['error_code'] = report['error_code'] or 'ci_interrupted'
        report['passed'] = report['passed'] and report['cleanup_passed'] and report['error_code'] is None
        report['incomplete'] = not report['passed']
        private_json(root / 'ci-report.json', report)
        try:
            safe_export(root)
            require(not STOP, 'ci_interrupted')
        except Exception:
            report['passed'], report['incomplete'] = False, True
            report['error_code'] = 'ci_interrupted' if STOP else 'evidence_export_failed'
            # A signal may arrive after the atomic export rename. Reconcile both
            # summaries and the manifest, not only an unpublished staging tree.
            publish_failure_summary(root, report)
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    if STOP and report['passed']:
        report['passed'], report['incomplete'], report['error_code'] = False, True, 'ci_interrupted'
        publish_failure_summary(root, report)
    print('A2C_CI_FULL_VERIFIED' if report['passed'] else 'A2C_CI_FAILED ' + str(report['error_code']), flush=True)
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except CIError as error:
        print('A2C_CI_FAILED ' + str(error), file=sys.stderr)
        raise SystemExit(1)
