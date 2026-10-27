"""Four S1 scenarios over direct real ELF children; no database mutation here."""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import queue
import re
import subprocess
import threading
import time

from ark_query_recovery_verify import (CASES, COMMAND_EVENT, EVENTS, INSTANCES, OLD, PREFIX, PROTOCOL,
    TEST, command_shape, http_counts, keys, load, need, raw_file, read_artifact, stable_snapshot,
    strict_json, utc, verify_case)


def write(path, value):
    with path.open('x', encoding='utf-8') as out:
        json.dump(value, out, sort_keys=True, indent=2)
        out.write('\n')
    path.chmod(0o600)


class Mock:
    def __init__(self, run, root, tokens):
        self.run, self.root, self.tokens = run, root, tokens
        self.lock, self.requests = threading.Lock(), []
        self.active_seed = None
        self.path = root / 'mock-requests.jsonl'
        self.path.touch(mode=0o600, exist_ok=False)
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def handle_method(self):
                self.connection.settimeout(5)
                case = self.headers.get('X-Query-Handoff-Case', '')
                instance = self.headers.get('X-Query-Handoff-Instance', '')
                ordinal_raw = self.headers.get('X-Query-Handoff-Ordinal', '')
                ordinal = int(ordinal_raw) if re.fullmatch(r'0|[1-9][0-9]?', ordinal_raw) else -1
                base = '/api/v3/contents/generations/tasks'
                target = 'qhandoff_' + run + '_' + case + '_' + str(ordinal)
                with owner.lock:
                    active = owner.active_seed
                valid = case in CASES and instance in INSTANCES.get(case, ()) and 0 <= ordinal < (42 if case == 'P01' else 2 if case == 'P04' else 1)
                valid = valid and self.headers.get('X-Query-Handoff-Run') == run and self.headers.get('Authorization') == 'Bearer ' + tokens.get(case, 'invalid')
                valid = valid and active is not None and active[0] == case and active[1] == instance and ordinal in active[2]
                valid = valid and ((self.command == 'POST' and self.path == base) or (self.command == 'GET' and self.path == base + '/' + target))
                length = self.headers.get('Content-Length', '0')
                valid = valid and length.isdecimal() and int(length) <= 1024 * 1024 and not self.headers.get('Transfer-Encoding')
                size = int(length) if length.isdecimal() and int(length) <= 1024 * 1024 else 0
                raw = self.rfile.read(size)
                valid = valid and len(raw) == size
                status = 200 if valid else 400
                # Record an invalid request too, but never attacker-controlled
                # headers, credential, payload or URL outside fixed vocabulary.
                record = dict(run_id=run, case_id=case if case in CASES else 'invalid',
                              instance=instance if re.fullmatch(r'(old|new)-[a-z0-9-]{1,32}', instance) else 'invalid',
                              ordinal=ordinal, method=self.command if self.command in ('POST', 'GET', 'DELETE') else 'other',
                              path=self.path if valid else 'invalid', status=status,
                              cmd_id=active[3] if valid else 0,
                              body_sha256=hashlib.sha256(raw).hexdigest(), body_bytes=len(raw))
                with owner.lock:
                    record['sequence'] = len(owner.requests)+1
                    owner.requests.append(record)
                    with owner.path.open('a', encoding='utf-8') as out:
                        out.write(json.dumps(record, sort_keys=True, separators=(',', ':'))+'\n')
                data = {'id': target, 'status': 'queued' if self.command == 'POST' else 'succeeded'} if valid else {'error': 'invalid synthetic request'}
                if valid and self.command == 'GET':
                    data['usage'] = {'completion_tokens': 100, 'total_tokens': 100}
                encoded = json.dumps(data, separators=(',', ':')).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            do_POST = handle_method
            do_GET = handle_method
            do_DELETE = handle_method

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = 'http://127.0.0.1:' + str(self.server.server_port)

    def snapshot(self, case):
        with self.lock:
            return [dict(r) for r in self.requests if r['case_id'] == case]

    def seed_window(self, case, instance, ordinals, command_id):
        with self.lock:
            need(self.active_seed is None, 'overlapping_seed')
            self.active_seed = (case, instance, set(ordinals), command_id)

    def end_seed(self):
        with self.lock:
            self.active_seed = None

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(3)
        need(not self.thread.is_alive(), 'mock_thread_live')


class Child:
    def __init__(self, matrix, case, instance):
        self.matrix, self.case, self.instance = matrix, case, instance
        self.role = instance.split('-')[0]
        self.command_id, self.sequence = 0, 0
        self.events, self.lines = queue.Queue(), []
        self.reader_error = None
        self.clean_exit, self.forced_stop = False, False
        self.artifacts = []
        directory = matrix.root / case
        directory.mkdir(mode=0o700, exist_ok=True)
        self.log = matrix.root / (case + '-' + instance + '.log')
        self.commands = matrix.root / (case + '-' + instance + '-commands.jsonl')
        self.commands.touch(mode=0o600, exist_ok=False)
        env = {k: os.environ[k] for k in ('PATH', 'HOME', 'TMPDIR', 'TZ', 'LANG', 'GOMAXPROCS', 'GOMEMLIMIT') if k in os.environ}
        env.update(ARK_QUERY_HANDOFF_PROTOCOL=PROTOCOL, ARK_QUERY_HANDOFF_RUN=matrix.run,
                   ARK_QUERY_HANDOFF_CASE=case, ARK_QUERY_HANDOFF_INSTANCE=instance,
                   ARK_QUERY_HANDOFF_ROLE=self.role, ARK_QUERY_HANDOFF_SOURCE=matrix.builds[self.role]['source'],
                   ARK_QUERY_HANDOFF_DIR=str(directory.resolve()), ARK_QUERY_HANDOFF_DSN=matrix.dsns[case],
                   ARK_QUERY_HANDOFF_MOCK_URL=matrix.mock.url, ARK_QUERY_HANDOFF_MOCK_TOKEN=matrix.tokens[case])
        self.process = subprocess.Popen([matrix.builds[self.role]['path'], '-test.run=^'+TEST+'$',
                                         '-test.count=1', '-test.v', '-test.timeout=10m'],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                        text=True, encoding='utf-8', errors='strict', env=env, bufsize=1)
        matrix.children.append(self)
        self.thread = threading.Thread(target=self.read, daemon=True)
        self.thread.start()
        ready = self.wait('ready', 0, 30)
        keys(ready['data'], ('schema', 'migration_max', 'has_persisted_consumer', 'has_dispatch_consumer', 'task_count'), 'ready_shape')
        need(ready['data']['schema'] == matrix.schemas[case] and ready['data']['migration_max'] == 135 and
             ready['data']['has_persisted_consumer'] is (self.role == 'new') and
             ready['data']['has_dispatch_consumer'] is (self.role == 'new'), 'ready_role')

    def read(self):
        try:
            total = 0
            with self.log.open('x', encoding='utf-8') as out:
                self.log.chmod(0o600)
                while True:
                    line = self.process.stdout.readline(65537)
                    if not line:
                        break
                    out.write(line); out.flush()
                    self.lines.append(line)
                    total += len(line)
                    need(len(line) <= 65536 and total <= 64*1024*1024, 'bounded_child_log')
                    need(not re.search(r'DATA RACE|--- (?:FAIL|SKIP):|^panic:', line), 'actual_go_failure')
                    if not line.startswith(PREFIX):
                        continue
                    event = strict_json(line[len(PREFIX):])
                    keys(event, ('protocol', 'run_id', 'case_id', 'instance', 'role', 'source', 'pid', 'seq', 'cmd_id', 'event', 'data'), 'event_shape')
                    need(event['protocol'] == PROTOCOL and event['run_id'] == self.matrix.run and event['case_id'] == self.case and
                         event['instance'] == self.instance and event['role'] == self.role and event['source'] == self.matrix.builds[self.role]['source'] and
                         event['pid'] == self.process.pid and event['event'] in EVENTS, 'child_identity')
                    need(type(event['seq']) is int and event['seq'] == self.sequence+1, 'child_sequence')
                    self.sequence += 1
                    self.events.put(event)
        except Exception:
            self.reader_error = 'child_output_invalid'
            self.events.put(None)

    def wait(self, kind, command_id, timeout=30):
        try:
            event = self.events.get(timeout=timeout)
        except queue.Empty:
            raise ValueError('child_event_timeout') from None
        need(self.reader_error is None and event is not None, 'child_reader_failure')
        need(event['event'] == kind and event['cmd_id'] == command_id, 'child_event_order')
        return event

    def command(self, name, **fields):
        self.command_id += 1
        command = dict(protocol=PROTOCOL, run_id=self.matrix.run, case_id=self.case,
                       instance=self.instance, cmd_id=self.command_id, command=name, **fields)
        command_shape(command, self.case, self.instance, self.matrix.run)
        encoded = json.dumps(command, separators=(',', ':'))+'\n'
        need(len(encoded.encode()) <= 4096, 'command_size')
        with self.commands.open('a', encoding='utf-8') as out:
            out.write(encoded)
        if name == 'seed':
            ordinals = range(42) if self.case == 'P01' else [1] if fields['slot'] == 'natural' else [0]
            self.matrix.mock.seed_window(self.case, self.instance, ordinals, self.command_id)
        before = self.matrix.mock.snapshot(self.case)
        try:
            self.process.stdin.write(encoded)
            self.process.stdin.flush()
            artifact = None
            if name != 'stop':
                event = self.wait(COMMAND_EVENT[name], self.command_id, 300 if name == 'seed' else 30)
                artifact = read_artifact(self.matrix.root / self.case, event)
                artifact['_command'] = command
                self.artifacts.append(artifact)
                self.matrix.artifacts[self.case].append(artifact)
            done = self.wait('command_done', self.command_id)
            need(done['data'] == dict(command=name, completed_commands=self.command_id), 'command_completion')
            after = self.matrix.mock.snapshot(self.case)
            need(name == 'seed' or before == after, 'nonseed_http')
            return artifact
        finally:
            if name == 'seed':
                self.matrix.mock.end_seed()

    def cycle(self):
        artifact = self.command('recover')
        details = artifact['details']
        need(details['entry'] == ('old_combined' if self.role == 'old' else 'new_persisted'), 'cycle_entry')
        need(all(code in ('query_decode', 'context_deadline') for code in details['error_classes']), 'unexpected_cycle_failure')
        http_counts(details['http_before']); http_counts(details['http_after'])
        need(details['http_before'] == details['http_after'], 'cycle_http')
        return artifact

    def stop(self):
        self.command('stop')
        self.process.stdin.close()
        drained = self.wait('drained', self.command_id, 20)
        need(drained['data'] == dict(completed_commands=self.command_id), 'drain_fact')
        need(self.process.wait(timeout=25) == 0, 'child_nonzero_exit')
        self.thread.join(3)
        need(not self.thread.is_alive() and self.reader_error is None and self.events.empty(), 'drain_reader')
        text = ''.join(self.lines)
        need(text.count('--- PASS: '+TEST) == 1 and '\nPASS\n' in text, 'drain_go_pass')
        self.clean_exit = True

    def close_failed(self):
        if self.process.poll() is None:
            self.forced_stop = True
            self.process.terminate()  # Direct unreaped Popen child, never a saved PID.
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=4)
        self.thread.join(3)

    def exit_fact(self):
        return dict(case_id=self.case, instance=self.instance, role=self.role, pid=self.process.pid,
                    source=self.matrix.builds[self.role]['source'], binary_sha256=self.matrix.builds[self.role]['sha256'],
                    exit_code=self.process.poll(), clean_exit=self.clean_exit, forced_stop=self.forced_stop)


class Matrix:
    def __init__(self, args):
        self.run = args.run_id
        need(re.fullmatch('[0-9a-f]{16}', self.run) is not None, 'run_id')
        self.root = Path(args.evidence).absolute()
        need(not self.root.exists() and self.root.resolve() == self.root, 'new_matrix_output')
        self.root.mkdir(parents=True, mode=0o700)
        self.builds = load(Path(args.build_manifest))
        keys(self.builds, ('old', 'new'), 'build_roles')
        need(self.builds['old']['source'] == OLD and self.builds['new']['source'] != OLD and
             re.fullmatch('[0-9a-f]{40}', self.builds['new']['source']) is not None, 'fixed_sources')
        for build in self.builds.values():
            path = Path(build['path'])
            need(path.is_absolute() and not path.is_symlink() and hashlib.sha256(raw_file(path, 512*1024*1024)).hexdigest() == build['sha256'], 'binary_digest')
        self.dsns = strict_json(os.environ['QUERY_HANDOFF_DSNS'])
        self.tokens = strict_json(os.environ['QUERY_HANDOFF_TOKENS'])
        keys(self.dsns, CASES, 'case_dsns'); keys(self.tokens, CASES, 'case_tokens')
        self.schemas = {}
        for case, dsn in self.dsns.items():
            matched = re.fullmatch(r'root:[0-9a-f]{48}@tcp\(([0-9.]+):3306\)/(ark_test_qhandoff_[0-9a-f]{24})\?parseTime=true&loc=UTC', dsn)
            need(matched is not None, 'private_dsn_shape')
            self.schemas[case] = matched.group(2)
            need(re.fullmatch(r'synthetic-[0-9a-f]{48}', self.tokens[case]) is not None, 'synthetic_token')
        need(len(set(self.schemas.values())) == 4, 'distinct_schemas')
        self.children, self.artifacts, self.passed = [], {c: [] for c in CASES}, []
        self.mock = Mock(self.run, self.root, self.tokens)

    def child(self, case, instance):
        need(instance in INSTANCES[case] and not any(c.case == case and c.instance == instance for c in self.children), 'unique_child')
        need(all(c.clean_exit for c in self.children), 'previous_child_not_drained')
        return Child(self, case, instance)

    def run_case(self, case):
        print('QUERY_HANDOFF_CASE '+case+' start', flush=True)
        writer = self.child(case, INSTANCES[case][0])
        group = dict(P01='bad40_good2', P02='one_good', P03='one_bad', P04='one_temporary')[case]
        seed = writer.command('seed', group=group, slot='primary')
        if case in ('P01', 'P03'):
            writer.command('fault_json', ordinals=list(range(40)) if case == 'P01' else [0])
            writer.command('schedule_fixture', mode='due_now', slot='primary')
        if case == 'P01':
            writer.cycle(); writer.cycle(); writer.stop()
            consumer = self.child(case, 'new-recovery')
            for _ in range(10):
                final = consumer.cycle()
                tasks = final['snapshot']['tasks']
                complete = all(r['receipt']['due_at'] is None and r['receipt']['applied_at'] is None for r in tasks[:40]) and all(r['billing_state'] == 'settled' for r in tasks[40:])
                if complete:
                    break
            else:
                raise ValueError('bounded_bad_queue_incomplete')
            final = consumer.cycle(); consumer.stop()
        elif case == 'P02':
            writer.stop()
            old = self.child(case, 'old-consumer'); old.cycle(); old.stop()
            consumer = self.child(case, 'new-replay'); consumer.cycle(); final = consumer.cycle(); consumer.stop()
        elif case == 'P03':
            for _ in range(10):
                review = writer.cycle()
                if review['snapshot']['tasks'][0]['receipt']['due_at'] is None:
                    break
            else:
                raise ValueError('review_not_committed')
            writer.stop()
            old = self.child(case, 'old-observer'); old.cycle(); old.cycle(); old.stop()
            consumer = self.child(case, 'new-guard'); consumer.cycle(); final = consumer.command('direct_apply'); consumer.stop()
        else:
            due = utc(seed['snapshot']['tasks'][0]['receipt']['due_at'])
            future = writer.cycle()
            need(utc(future['snapshot']['database_utc']) < due and stable_snapshot(seed['snapshot'], future['snapshot']), 'future_precondition')
            writer.stop()
            old = self.child(case, 'old-early')
            early = old.cycle()
            need(early['snapshot']['tasks'][0]['billing_state'] == 'settled' and utc(early['snapshot']['database_utc']) < due, 'early_old_precondition')
            old.stop()
            consumer = self.child(case, 'new-natural')
            consumer.cycle()
            natural = consumer.command('seed', group='one_temporary', slot='natural')
            natural_due = utc(natural['snapshot']['tasks'][1]['receipt']['due_at'])
            before = consumer.cycle()
            need(utc(before['snapshot']['database_utc']) < natural_due and stable_snapshot(natural['snapshot'], before['snapshot']), 'natural_future_precondition')
            end = time.monotonic()+45
            while True:
                observed = consumer.command('inspect')
                if utc(observed['snapshot']['database_utc']) >= natural_due:
                    break
                need(time.monotonic() < end, 'natural_database_wait_timeout')
                time.sleep(.25)
            consumer.cycle(); final = consumer.cycle(); consumer.stop()
        calls = self.mock.snapshot(case)
        result = dict(status='passed', case_id=case, seed=seed['snapshot'], final=final['snapshot'], http=calls)
        # The separate CLI repeats this from original logs/artifact hashes.
        verify_case(case, result, self.artifacts[case], calls)
        write(self.root / (case+'-result.json'), result)
        self.passed.append(case)
        print('QUERY_HANDOFF_CASE '+case+' passed', flush=True)

    def finish(self, success):
        for child in self.children:
            child.close_failed()
        write(self.root / 'child-exits.json', [child.exit_fact() for child in self.children])
        self.mock.close()
        complete = success and self.passed == list(CASES) and len(self.children) == 11 and all(c.clean_exit for c in self.children)
        marker = 'matrix-passed.json' if complete else 'matrix-failed.json'
        write(self.root / marker, dict(status='passed' if complete else 'failed', run_id=self.run,
              cases=4, cases_passed=self.passed, actual_processes=len(self.children),
              old_source=OLD, new_source=self.builds['new']['source']))
        return complete


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-id', required=True)
    parser.add_argument('--build-manifest', required=True)
    parser.add_argument('--evidence', required=True)
    args = parser.parse_args()
    matrix, success = None, False
    try:
        matrix = Matrix(args)
        for case in CASES:
            matrix.run_case(case)
        success = True
    except Exception:
        print('Query handoff matrix failed; retain raw process evidence.', flush=True)
    finally:
        if matrix is not None:
            try:
                success = matrix.finish(success)
            except Exception:
                success = False
                print('Matrix finalization incomplete; retain private evidence.', flush=True)
    raise SystemExit(0 if success else 1)


if __name__ == '__main__':
    main()
