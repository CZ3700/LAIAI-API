"""Four committed S1 query handoffs. Never accepts a supplied database address."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import sys
sys.dont_write_bytecode = True
import tarfile
import time

from ark_execution_input_compat import checked_archive, elf_linkage, redact_export
from ark_mutation_ci import OwnedProcess
from ark_query_recovery_verify import CASES, OLD, TEST, need, strict_json, verify

ROOT = Path(__file__).resolve().parents[2]
GO = 'go version go1.26.10 linux/amd64'
TOOL_IMAGE = 'sha256:e5b54d386ea5dad8908135b5b2d65e1604f990e9628526125e9a01a36043c6ad'
MYSQL = 'docker.io/library/mysql@sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084'
DOCKER = ('docker', '--host', 'unix:///var/run/docker.sock')
LABEL = 'ai.laiai.query-recovery-handoff.run'
HARNESS = ('core/internal/video/query_recovery_handoff_test.go',)
CONTROLS = (
    'scripts/ark-query-recovery-compat.sh',
    'scripts/lib/ark_query_recovery_compat.py',
    'scripts/lib/ark_query_recovery_matrix.py',
    'scripts/lib/ark_query_recovery_verify.py',
    'scripts/tests/test_ark_query_recovery_compat.py',
    # Imported primitives and their transitive imports are bound as source too.
    'scripts/lib/ark_execution_input_compat.py',
    'scripts/lib/ark_execution_input_verify.py',
    'scripts/lib/ark_mutation_ci.py',
)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def write(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + '\n', encoding='utf-8', newline='\n')
    path.chmod(0o600)


def clean_environment(inherited):
    need(not any(k.startswith(('LAIAI_', 'ARK_TEST_', 'ARK_QUERY_HANDOFF_', 'QUERY_HANDOFF_', 'B1B_', 'A2C_')) for k in inherited), 'external_business_environment')
    env = {k: inherited[k] for k in ('PATH', 'HOME', 'TMPDIR', 'GOPROXY', 'GOMODCACHE', 'GOCACHE') if k in inherited}
    env.update(GOENV='off', GOTOOLCHAIN='local', GOMAXPROCS='3', GOMEMLIMIT='4GiB',
               CGO_ENABLED='1', GOOS='linux', GOARCH='amd64', TZ='UTC', LANG='C.UTF-8',
               PYTHONDONTWRITEBYTECODE='1')
    return env


def link_flags(compiler, source=None):
    flags = ['-linkmode=external', '-extldflags=-static'] if compiler == 'docker' else []
    if source:
        flags += ['-X', 'github.com/laiai/laiai-next/core/internal/video.queryHandoffCompiledSource=' + source]
    return ['-ldflags', ' '.join(flags)] if flags else []


def bridge_address(row):
    network = row['NetworkSettings']
    need(not row['HostConfig'].get('PortBindings') and not any(network.get('Ports', {}).values()), 'database_host_port')
    need(set(network['Networks']) == {'bridge'} and row['HostConfig']['NetworkMode'] == 'bridge', 'database_network')
    bridge = network['Networks']['bridge']
    need(re.fullmatch('[0-9a-f]{64}', bridge['NetworkID']) is not None, 'database_network_identity')
    address = ipaddress.ip_address(bridge['IPAddress'])
    need(address.version == 4 and address.is_private and not address.is_loopback and
         not address.is_multicast and not address.is_unspecified and not address.is_link_local, 'database_private_address')
    return str(address), bridge['NetworkID']


class Runner:
    def __init__(self, args):
        self.args, self.env = args, clean_environment(os.environ)
        self.root = Path(args.output).absolute()
        need(not self.root.exists() and not self.root.is_symlink() and self.root.resolve() == self.root and
             not ROOT.is_relative_to(self.root), 'new_output_required')
        self.root.mkdir(parents=True, mode=0o700)
        self.evidence = self.root / 'evidence'
        self.evidence.mkdir(mode=0o700)
        self.run = secrets.token_hex(8)
        self.deadline = time.monotonic() + 2400
        self.entries, self.secrets, self.sources = [], [], {}
        self.db = None
        self.cancelled = False
        self.export_files = set()
        self.report = dict(status='running', run_id=self.run, old_source=OLD,
                           new_source=None, compiler=args.compiler, official_calls=0,
                           started_at=dt.datetime.now(dt.timezone.utc).isoformat())

    def artifact(self, relative):
        path = self.root / relative
        need(path.parent.is_relative_to(self.root), 'artifact_path')
        path.parent.mkdir(parents=True, exist_ok=True)
        self.export_files.add(relative)
        return path

    def command(self, argv, log=None, env=None, cwd=None, timeout=120, stdin=None, allowed=(0,)):
        remaining = self.deadline - time.monotonic()
        need(remaining > 0, 'total_deadline')
        process = subprocess.Popen(argv, env=self.env if env is None else env, cwd=cwd,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True)
        try:
            out, err = process.communicate(stdin, timeout=min(timeout, remaining))
        except BaseException:
            # This direct unreaped Popen child still owns its PID/session.
            for sig in (signal.SIGTERM, signal.SIGKILL):
                try:
                    os.killpg(process.pid, sig)
                except ProcessLookupError:
                    pass
                try:
                    out, err = process.communicate(timeout=5)
                    break
                except subprocess.TimeoutExpired:
                    continue
            else:
                out, err = b'', b'command cleanup incomplete\n'
            if log:
                self.artifact('evidence/' + log).write_bytes(out + err)
            raise RuntimeError('command_interrupted') from None
        if log:
            self.artifact('evidence/' + log).write_bytes(out + err)
        need(process.returncode in allowed, 'command_failed')
        return out

    def source(self):
        head = self.command(['git', '-C', str(ROOT), 'rev-parse', 'HEAD']).decode().strip()
        need(re.fullmatch('[0-9a-f]{40}', head) is not None and head != OLD, 'new_commit')
        self.report['new_source'] = head
        need(not self.command(['git', '-C', str(ROOT), 'status', '--porcelain', '--untracked-files=no']).strip(), 'dirty_source')
        self.command(['git', '-C', str(ROOT), 'merge-base', '--is-ancestor', OLD, head])
        archives = {}
        for role, commit in (('old', OLD), ('new', head)):
            raw = self.command(['git', '-C', str(ROOT), 'archive', '--format=tar', commit], timeout=60)
            self.artifact(role + '-original.tar').write_bytes(raw)
            archives[role] = checked_archive(raw)
        new_files = archives['new'][0]
        for name in CONTROLS + HARNESS:
            need(name in new_files and (ROOT / name).read_bytes() == new_files[name], 'uncommitted_control_or_harness')
        common = {name: new_files[name] for name in HARNESS}
        old_migrations = {p: b for p, b in archives['old'][0].items() if p.startswith('migrations/')}
        need(all(new_files.get(p) == b for p, b in old_migrations.items()), 'historical_migration_changed')
        for role, commit in (('old', OLD), ('new', head)):
            files, modes = archives[role]
            original = {p: sha(b) for p, b in files.items()}
            for name, raw in common.items():
                need(name not in files or files[name] == raw, 'harness_collision')
                files[name] = raw
            directory = self.root / (role + '-source')
            directory.mkdir()
            for name, raw in files.items():
                target = directory / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(raw)
                target.chmod(modes.get(name, 0o644))
            manifest = {p: sha(b) for p, b in files.items()}
            migrations = sum(bool(re.fullmatch(r'migrations/\d{4}_[^/]+\.sql', p)) for p in files)
            need(migrations == (132 if role == 'old' else 136), 'migration_head')
            self.sources[role] = dict(source=commit, path=str(directory), files=manifest,
                                      original_sha256=original, migration_count=migrations,
                                      common_harness_sha256={p: sha(b) for p, b in common.items()})
            write(self.artifact(role + '-source-manifest.json'), manifest)
        write(self.artifact('sources.json'), self.sources)

    def compiler(self, argv, role, log):
        source = Path(self.sources[role]['path'])
        if self.args.compiler == 'host':
            return self.command(argv, log, cwd=source / 'core', timeout=600)
        cidfile = self.root / ('compiler-' + str(len(self.entries)) + '.cid')
        name = 'laiai-qrec-compile-' + self.run + '-' + str(len(self.entries))
        entry = dict(name=name, image=TOOL_IMAGE, cidfile=cidfile)
        self.entries.append(entry)
        args = [arg.replace(str(self.root), '/work') for arg in argv]
        docker = [*DOCKER, 'run', '--name', name, '--cidfile', str(cidfile), '--label', LABEL + '=' + self.run,
                  '--cpus=3', '--memory=6g', '--memory-swap=8g', '--network=none',
                  '-e', 'GOENV=off', '-e', 'GOTOOLCHAIN=local', '-e', 'CGO_ENABLED=1',
                  '-e', 'GOMAXPROCS=3', '-e', 'GOMEMLIMIT=4GiB',
                  '-v', str(self.root) + ':/work',
                  '-v', str(self.root / 'old-source') + ':/work/old-source:ro',
                  '-v', str(self.root / 'new-source') + ':/work/new-source:ro',
                  '-v', 'laiai-ci-gomod:/go/pkg/mod', '-v', 'laiai-ci-gocache:/root/.cache/go-build',
                  '-w', '/work/' + role + '-source/core', '--entrypoint', args[0], TOOL_IMAGE, *args[1:]]
        return self.command(docker, log, timeout=600)

    def build(self):
        if self.args.compiler == 'docker':
            need(self.command([*DOCKER, 'image', 'inspect', TOOL_IMAGE, '--format', '{{.Id}}']).decode().strip() == TOOL_IMAGE, 'compiler_image')
            for volume in ('laiai-ci-gomod', 'laiai-ci-gocache'):
                need(self.command([*DOCKER, 'volume', 'inspect', volume, '--format', '{{.Name}}']).decode().strip() == volume, 'existing_compiler_cache')
        self.builds, self.tools = {}, {}
        (self.root / 'bin').mkdir()
        for role in ('old', 'new'):
            need(self.compiler(['go', 'version'], role, role + '-go-version.log').decode().strip() == GO, 'go_version')
            source = self.sources[role]['source']
            binary = self.artifact('bin/' + role + '-video.test')
            self.compiler(['go', 'test', '-race', '-c', '-o', str(binary), *link_flags(self.args.compiler, source), './internal/video'], role, role + '-build.log')
            self.builds[role] = dict(path=str(binary), source=source, sha256=sha(binary.read_bytes()))
            products = {'video': binary}
            if role == 'new':
                for tool in ('migrate', 'schema-doctor'):
                    path = self.artifact('bin/new-' + tool)
                    self.compiler(['go', 'build', *link_flags(self.args.compiler), '-o', str(path), './cmd/' + tool], role, tool + '-build.log')
                    products[tool] = path
                    self.tools[tool] = str(path)
            for tool, path in products.items():
                self.compiler(['go', 'version', '-m', str(path)], role, role + '-' + tool + '-build-info.log')
                facts = elf_linkage(path.read_bytes())
                write(self.artifact('evidence/' + role + '-' + tool + '-elf.json'), dict(facts, sha256=sha(path.read_bytes())))
                self.command(['file', '--brief', str(path)], role + '-' + tool + '-file.log')
                if self.args.compiler == 'docker':
                    need(facts['interpreter'] is None and facts['needed_entries'] == 0, 'dynamic_docker_elf')
                static = facts['interpreter'] is None and facts['needed_entries'] == 0
                self.command(['ldd', str(path)], role + '-' + tool + '-ldd.log', allowed=(0, 1) if static else (0,))
                args = [str(path), '-test.list=^' + TEST + '$'] if tool == 'video' else [str(path), '-h']
                output = self.command(args, role + '-' + tool + '-entrypoint.log')
                if tool == 'video':
                    need(output.decode().splitlines() == [TEST], 'test_entrypoint')
        need(self.builds['old']['sha256'] != self.builds['new']['sha256'], 'identical_binary')
        write(self.artifact('evidence/build-manifest.json'), self.builds)
        write(self.artifact('evidence/build-artifacts.json'), {p.name: dict(bytes=p.stat().st_size, sha256=sha(p.read_bytes())) for p in (self.root / 'bin').iterdir()})
        self.integrity()

    def owned(self, entry):
        if not entry['cidfile'].exists():
            return None
        cid = entry['cidfile'].read_text().strip()
        need(re.fullmatch('[0-9a-f]{64}', cid) is not None, 'owned_cid')
        observed = subprocess.run([*DOCKER, 'inspect', cid], env=self.env, capture_output=True, timeout=15, check=True)
        row = strict_json(observed.stdout)[0]
        need(row['Id'] == cid and row['Name'] == '/' + entry['name'] and row['Image'] == entry['image'] and
             row['Config']['Labels'].get(LABEL) == self.run, 'owned_identity')
        if entry.get('database'):
            if row['State']['Running']:
                address, network = bridge_address(row)
            else:
                need(not row['HostConfig'].get('PortBindings') and row['HostConfig']['NetworkMode'] == 'bridge' and
                     set(row['NetworkSettings']['Networks']) == {'bridge'}, 'stopped_database_network')
                address = entry['address']
                network = row['NetworkSettings']['Networks']['bridge']['NetworkID']
            need(address == entry['address'] and network == entry['network'], 'database_network_changed')
            mounts = row['Mounts']
            need(len(mounts) == 1 and mounts[0]['Type'] == 'volume' and mounts[0]['Name'] == entry['volume'] and
                 mounts[0]['Destination'] == '/var/lib/mysql', 'database_volume')
        return row

    def sql(self, statement):
        need(self.db is not None, 'database_not_owned')
        self.owned(self.db)
        return self.command([*DOCKER, 'exec', '-i', '-e', 'MYSQL_PWD', self.db['cidfile'].read_text().strip(),
                             'mysql', '-uroot', '--batch', '--skip-column-names'],
                            env=dict(self.env, MYSQL_PWD=self.password), stdin=statement.encode(), timeout=15)

    def database(self):
        # Both environments pre-stage this exact image. No fallback pull or port.
        image = self.command([*DOCKER, 'image', 'inspect', MYSQL, '--format', '{{.Id}}']).decode().strip()
        need(re.fullmatch(r'sha256:[0-9a-f]{64}', image) is not None, 'mysql_image')
        self.password = secrets.token_hex(24)
        self.secrets.append(self.password)
        volume = 'laiai-qrec-data-' + self.run
        need(not self.command([*DOCKER, 'volume', 'inspect', volume, '--format', '{{.Name}}'], allowed=(0, 1)).strip(), 'existing_database_volume')
        self.command([*DOCKER, 'volume', 'create', '--label', LABEL + '=' + self.run, volume])
        volume_fact = strict_json(self.command([*DOCKER, 'volume', 'inspect', volume]))[0]
        need(volume_fact['Name'] == volume and volume_fact['Driver'] == 'local' and
             volume_fact.get('Labels', {}).get(LABEL) == self.run, 'database_volume_owner')
        entry = dict(name='laiai-qrec-mysql-' + self.run, image=image, cidfile=self.root / 'database.cid')
        self.entries.append(entry)
        self.db = entry
        self.command([*DOCKER, 'run', '-d', '--name', entry['name'], '--cidfile', str(entry['cidfile']),
                      '--label', LABEL + '=' + self.run, '--network=bridge', '--cpus=1', '--memory=1536m',
                      '--memory-swap=2g', '-e', 'MYSQL_ROOT_PASSWORD', '-e', 'MYSQL_ROOT_HOST=%',
                      '-v', volume + ':/var/lib/mysql', MYSQL, '--innodb-buffer-pool-size=268435456',
                      '--max-connections=80', '--skip-log-bin'], 'mysql-start.log',
                     env=dict(self.env, MYSQL_ROOT_PASSWORD=self.password))
        row = self.owned(entry)
        address, network = bridge_address(row)
        entry.update(database=True, address=address, network=network, volume=volume)
        self.owned(entry)
        write(self.artifact('evidence/mysql-identity.json'), dict(id=row['Id'], name=row['Name'], image=image,
              requested=MYSQL, owner=self.run, bridge_address=address, network=network, volume=volume, host_ports=[]))
        self.wait_database()
        self.schemas = {case: 'ark_test_qhandoff_' + secrets.token_hex(12) for case in CASES}
        write(self.artifact('schemas.json'), self.schemas)
        self.dsns = {}
        for case, name in self.schemas.items():
            self.sql('CREATE DATABASE `' + name + '` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;')
            dsn = 'root:' + self.password + '@tcp(' + address + ':3306)/' + name + '?parseTime=true&loc=UTC'
            self.dsns[case] = dsn
            self.secrets.append(dsn)
            env = dict(self.env, LAIAI_DSN=dsn)
            directory = str(Path(self.sources['new']['path']) / 'migrations')
            for repeat in (1, 2):
                self.command([self.tools['migrate'], '-dir', directory], case + '-migration-' + str(repeat) + '.log', env=env)
            output = self.command([self.tools['schema-doctor'], '-dir', directory], case + '-doctor.log', env=env)
            need(b'OK: schema at head (136 applied,' in output, 'schema_doctor')

    def wait_database(self):
        for _ in range(120):
            try:
                need(self.sql('SELECT 1;').strip() == b'1', 'mysql_ready')
                return
            except ValueError as exc:
                # mysql exits nonzero while this owned server initializes.
                # Identity, protocol and cancellation failures are not retries.
                if str(exc) != 'command_failed':
                    raise
                time.sleep(1)
        raise RuntimeError('mysql_ready_timeout')

    def matrix(self):
        tokens = {case: 'synthetic-' + secrets.token_hex(24) for case in CASES}
        self.secrets.extend(tokens.values())
        env = dict(self.env, QUERY_HANDOFF_DSNS=json.dumps(self.dsns), QUERY_HANDOFF_TOKENS=json.dumps(tokens))
        log = self.artifact('evidence/matrix-controller.log')
        process = OwnedProcess([sys.executable, str(ROOT / 'scripts/lib/ark_query_recovery_matrix.py'),
                                '--run-id', self.run, '--build-manifest', str(self.evidence / 'build-manifest.json'),
                                '--evidence', str(self.evidence / 'matrix')], env, log)
        try:
            need(process.proc.wait(timeout=max(1, self.deadline - time.monotonic())) == 0, 'matrix_exit')
        finally:
            process.stop()
        write(self.artifact('evidence/matrix-verified.json'), verify(self.evidence))

    def integrity(self):
        facts = {}
        for role, source in self.sources.items():
            directory = Path(source['path'])
            need(not any(p.is_symlink() for p in directory.rglob('*')), 'source_symlink')
            actual = {p.relative_to(directory).as_posix(): sha(p.read_bytes()) for p in directory.rglob('*') if p.is_file()}
            facts[role] = dict(expected=source['files'], actual=actual, unchanged=actual == source['files'])
        write(self.artifact('evidence/source-integrity.json'), facts)
        need(len(facts) == 2 and all(v['unchanged'] for v in facts.values()), 'source_changed')

    def finish(self, passed):
        clean = True
        for index, entry in enumerate(self.entries):
            try:
                row = self.owned(entry)
                if row is None:
                    clean = False
                    continue
                if row['State']['Running']:
                    subprocess.run([*DOCKER, 'stop', '--time=30', row['Id']], env=self.env, capture_output=True, timeout=45, check=True)
                    row = self.owned(entry)
                state = {k: row['State'][k] for k in ('Status', 'Running', 'ExitCode', 'OOMKilled', 'StartedAt', 'FinishedAt')}
                write(self.artifact('evidence/container-' + str(index) + '-final.json'), dict(id=row['Id'], name=row['Name'], image=row['Image'], owner=self.run, state=state, mounts=row['Mounts']))
                clean = clean and state['Status'] == 'exited' and not state['Running'] and state['ExitCode'] == 0 and not state['OOMKilled']
            except Exception:
                clean = False
        try:
            self.integrity()
        except Exception:
            passed = False
        for role in ('old', 'new'):
            directory = self.root / (role + '-source')
            if directory.exists():
                try:
                    with tarfile.open(self.artifact(role + '-runtime.tar'), 'w') as archive:
                        archive.add(directory, arcname=role + '-source', recursive=True)
                except Exception:
                    passed = False
                    write(self.artifact('evidence/' + role + '-runtime-unknown.json'), dict(status='unknown', reason='archive_unavailable'))
        self.report.update(status='finalizing',
                           containers_clean=clean, finished_at=dt.datetime.now(dt.timezone.utc).isoformat(), deletions=[])
        write(self.artifact('report.json'), self.report)
        final_report = dict(self.report, status='passed' if passed and clean and not self.cancelled else 'failed')
        try:
            self.export(final_report)
            self.report = final_report
        except Exception:
            # An incomplete whitelist export is never published. The original
            # evidence and export.pending remain available for failure triage.
            self.report.update(status='failed', export_status='incomplete')
            write(self.root / 'report.json', self.report)
        return self.report['status'] == 'passed'

    def export(self, final_report):
        output = self.root / 'export.pending'
        output.mkdir(mode=0o700)
        manifest = {}
        # Explicit matrix filenames only. In particular seed-manifest, DSN,
        # env and arbitrary runtime files are never uploaded by discovery.
        matrix = self.evidence / 'matrix'
        if matrix.exists():
            for path in matrix.rglob('*'):
                relative = path.relative_to(matrix).as_posix()
                allowed = bool(re.fullmatch(r'(matrix-(?:passed|failed)\.json|child-exits\.json|mock-requests\.jsonl|P0[1-4]-result\.json|P0[1-4]-(?:old|new)-[a-z0-9-]+(?:-commands\.jsonl|\.log)|P0[1-4]/(?:old|new)-[a-z0-9-]+-\d{4}-(?:seeded|fault_applied|scheduled|cycle|snapshot|direct_result)\.json)', relative))
                if allowed and path.is_file():
                    self.export_files.add(path.relative_to(self.root).as_posix())
        for relative in sorted(self.export_files):
            if relative == 'report.json':
                continue
            path = self.root / relative
            if not path.exists():
                continue  # A failed phase may never have created its output.
            need(path.is_file() and not path.is_symlink(), 'export_nonregular')
            raw = path.read_bytes()
            binary = relative.endswith('.tar') or relative.startswith('bin/')
            safe = raw if binary else redact_export(raw, self.secrets)
            target = output / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(safe)
            target.chmod(0o600)
            manifest[relative] = dict(original_sha256=sha(raw), export_sha256=sha(safe), bytes=len(safe), redacted=safe != raw)
        raw = (json.dumps(final_report, sort_keys=True, indent=2) + '\n').encode()
        if self.cancelled:
            final_report.update(status='failed', safe_error='host_signal')
            raw = (json.dumps(final_report, sort_keys=True, indent=2) + '\n').encode()
        safe = redact_export(raw, self.secrets)
        (output / 'report.json').write_bytes(safe)
        (output / 'report.json').chmod(0o600)
        manifest['report.json'] = dict(original_sha256=sha(raw), export_sha256=sha(safe), bytes=len(safe), redacted=safe != raw)
        write(output / 'evidence-manifest.json', manifest)
        # All potentially failing copies are complete before the final report
        # and export become visible. A failed rename is caught by finish(),
        # which restores the original overall report to failed.
        write(self.root / 'report.json', final_report)
        need(not self.cancelled or final_report['status'] == 'failed', 'export_cancelled')
        output.rename(self.root / 'export')
        # A signal delivered by the publication syscall still invalidates the
        # overall run. Withdraw the complete but cancelled candidate instead
        # of rewriting several published success files piecemeal.
        if self.cancelled and final_report['status'] != 'failed':
            (self.root / 'export').rename(output)
            raise ValueError('export_cancelled')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True)
    parser.add_argument('--compiler', choices=('host', 'docker'), default='host')
    args = parser.parse_args()
    need(sys.platform == 'linux', 'linux_required')
    runner, passed = None, False

    def interrupted(*_):
        if runner is not None:
            runner.cancelled = True
        raise RuntimeError('host_signal')

    def cleanup_interrupted(*_):
        # Finish bounded ownership cleanup, but never turn a cancelled export
        # into a successful run merely because SIGTERM was caught late.
        if runner is not None:
            runner.cancelled = True

    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        runner = Runner(args)
        for phase in ('source', 'build', 'database', 'matrix'):
            runner.report['phase'] = phase
            write(runner.artifact('progress.json'), runner.report)
            print('QUERY_HANDOFF_HOST_STAGE ' + phase, flush=True)
            getattr(runner, phase)()
        passed = True
    except Exception as exc:
        if runner is not None:
            code = str(exc)
            runner.report['safe_error'] = code if re.fullmatch('[a-zA-Z0-9_]+', code) else 'host_failure'
        print('Query handoff failed; retain the private run directory.', file=sys.stderr)
    finally:
        signal.signal(signal.SIGTERM, cleanup_interrupted)
        signal.signal(signal.SIGINT, cleanup_interrupted)
        if runner is not None:
            try:
                passed = runner.finish(passed)
            except Exception:
                passed = False
                print('Incomplete finalization; retain all private evidence.', file=sys.stderr)
    raise SystemExit(0 if passed else 1)


if __name__ == '__main__':
    main()
