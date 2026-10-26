#!/usr/bin/env python3
"""Independent post-run verification. No business calls, SQL, or repairs."""
from __future__ import annotations
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile

CA = '64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89'
P = '24da28583eddfaad151b9a4a054264a8275b61bb'
TEST = 'TestMySQLMutationBinaryHandoffProcess'
EXPECTED = frozenset('''
L1-cancel L1-delete
L2-cancel-Poff L2-cancel-Pon L2-delete-Poff L2-delete-Pon
N1-cancel-Ca N1-cancel-Poff N1-delete-Ca N1-delete-Poff N1-wall-cancel-Ca
N2-cancel-Ca N2-cancel-Poff N2-delete-Ca N2-delete-Poff
N3-cancel-Ca N3-cancel-Poff N3-delete-Ca N3-delete-Poff N3-delete-404-Ca N3-cancel-both-flags-Ca
N4-cancel-502-Ca N4-delete-EOF-Poff
R1-cancel-Ca R1-delete-Ca R2-cancel-Ca R2-delete-Ca
M1-cancel M1-delete M2-cancel M2-delete
M3-cancel-1 M3-cancel-2 M3-cancel-3 M3-delete-1 M3-delete-2 M3-delete-3
Q1-cancel-succeeded-first Q1-cancel-cancelled-first
'''.split())

def require(value, code):
    if not value:
        raise ValueError(code)

def sha(path):
    require(path.is_file() and not path.is_symlink(), 'artifact_not_regular')
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def read(path):
    require(path.is_file() and not path.is_symlink(), 'json_artifact_not_regular')
    require(path.stat().st_size <= 8 * 1024 * 1024, 'json_artifact_oversize')
    return json.loads(path.read_text(encoding='utf-8'))

def child(root, name):
    require(isinstance(name, str), 'invalid_relative_path')
    relative = PurePosixPath(name)
    require(not relative.is_absolute() and relative.parts and '..' not in relative.parts
            and '\\' not in name and ':' not in name, 'unsafe_relative_path')
    path = root.joinpath(*relative.parts)
    require(path.resolve().is_relative_to(root.resolve()), 'artifact_path_escape')
    current = root
    for part in relative.parts:
        current /= part
        require(not current.is_symlink(), 'artifact_symlink_rejected')
    return path

def tree_manifest(root):
    result = {}
    for path in root.rglob('*'):
        require(not path.is_symlink(), 'source_symlink_rejected')
        if path.is_file():
            result[path.relative_to(root).as_posix()] = sha(path)
    return result

def sources(root, report, overlay):
    harness = 'core/internal/video/mutation_handoff_test.go'
    binaries = {}
    for role, revision in (('Ca', CA), ('P', P)):
        item = report['sources'][role]
        require(item['sha'] == revision, 'source_revision_changed')
        archive_path = root / (role + '-source.tar')
        require(sha(archive_path) == item['archive_sha256'], 'archive_hash_mismatch')
        archive_manifest = {}
        with tarfile.open(archive_path, 'r:') as archive:
            for member in archive.getmembers():
                relative = PurePosixPath(member.name)
                require(not relative.is_absolute() and '..' not in relative.parts and '\\' not in member.name,
                        'archive_path_invalid')
                require(member.isdir() or member.isfile(), 'archive_entry_invalid')
                if member.isfile():
                    require(member.name not in archive_manifest, 'archive_duplicate_file')
                    archive_manifest[member.name] = hashlib.sha256(archive.extractfile(member).read()).hexdigest()
        before = read(root / (role + '-source-manifest.json'))
        after = read(root / (role + '-overlay-manifest.json'))
        require(before == archive_manifest, 'source_manifest_not_archive')
        require(hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest() == item['source_manifest_sha256'],
                'source_manifest_hash_mismatch')
        require(hashlib.sha256(json.dumps(after, sort_keys=True).encode()).hexdigest() == item['overlay_manifest_sha256'],
                'overlay_manifest_hash_mismatch')
        changed = {name for name in before.keys() | after.keys() if before.get(name) != after.get(name)}
        require(changed == {harness} and after[harness] == overlay[harness], 'unexpected_source_overlay')
        require(tree_manifest(root / (role + '-source')) == after, 'source_changed_during_build')
        for name in ('test', 'migrate', 'schema-doctor'):
            filename = role + ('-video.test' if name == 'test' else '-' + name)
            require(sha(root / 'bin' / filename) == item[name + '_sha256'], 'binary_hash_mismatch')
        binaries[role] = item['test_sha256']
    historic = read(root / 'historical-migrations.json')
    before_ca = read(root / 'Ca-source-manifest.json')
    before_p = read(root / 'P-source-manifest.json')
    require(historic == {name: value for name, value in before_ca.items() if name.startswith('migrations/')},
            'historical_migration_inventory_changed')
    require(all(before_p.get(name) == value for name, value in historic.items()), 'historical_migration_bytes_changed')
    return binaries

def process(case_dir, path, case_id, run_id, binaries):
    value = read(path)
    require(value['protocol'] == 'a2c-process-v1' and value['case_id'] == case_id and value['run_id'] == run_id,
            'process_identity_mismatch')
    require(value['instance'] == path.parent.name and type(value['pid']) is int and value['pid'] > 0,
            'process_owner_mismatch')
    role = 'Ca' if value['role'] == 'Ca' else 'P'
    require(value['role'] in ('Ca', 'Pon', 'Poff', 'Pboth') and value['source_sha'] == (CA if role == 'Ca' else P)
            and value['binary_sha256'] == binaries[role], 'process_binary_mismatch')
    require(value['error_code'] is None and value['test_run'] is True and value['test_bad'] is False,
            'process_failed_or_skipped')
    hashes = value['artifact_sha256']
    required = {'stdout.log', 'stderr.log', 'test2json.jsonl', 'test2json.stderr.log', 'events.jsonl'}
    require(set(hashes) == required, 'process_artifact_inventory')
    for name, expected in hashes.items():
        require(sha(child(path.parent, name)) == expected, 'process_artifact_hash_mismatch')
    events = [json.loads(line) for line in (path.parent / 'events.jsonl').read_text().splitlines()]
    require(len(events) == value['event_count'] and events and events[0]['event'] == 'ready', 'process_ready_missing')
    for index, event in enumerate(events, 1):
        require(event['seq'] == index and event['run_id'] == run_id and event['case_id'] == case_id
                and event['instance'] == value['instance'] and event['pid'] == value['pid']
                and event['role'] == value['role'] and event['event'] != 'error', 'process_event_mismatch')
        data = event['data']
        if event['event'] == 'barrier_reached':
            require(sha(child(case_dir, data['verification_file'])) == data['verification_sha256'], 'barrier_hash_mismatch')
    converted = [json.loads(line) for line in (path.parent / 'test2json.jsonl').read_text().splitlines()]
    require(any(row.get('Action') == 'run' and row.get('Test') == TEST for row in converted), 'actual_test_run_missing')
    require(not any(row.get('Action') in ('fail', 'skip') and row.get('Test') == TEST for row in converted),
            'actual_test_failed_or_skipped')
    if value['result'] == 'passed':
        require(value['returncode'] == 0 and value['test_pass'] is True, 'normal_process_not_passed')
        require(any(row.get('Action') == 'pass' and row.get('Test') == TEST for row in converted), 'actual_test_pass_missing')
        require(events[-1]['event'] == 'stopped', 'normal_process_stopped_missing')
        return False
    require(value['result'] == 'expected_crash_verified' and value['returncode'] == -9
            and value['test_pass'] is False, 'unexpected_process_exit')
    require(not any(row.get('Action') == 'pass' and row.get('Test') == TEST for row in converted), 'crash_claimed_test_pass')
    require(events[-1]['event'] != 'stopped', 'crash_was_normal_exit')
    return True

def verify(root, overlay_path):
    root = root.resolve(strict=True)
    tools = read(root.parent / 'tool-tests.json')
    require(tools['inventory'] == {'test_ark_mutation_process.py': 18, 'test_ark_mutation_compat.py': 20,
                                  'test_ark_mutation_scenarios.py': 16}
            and tools['executed'] == 54 and tools['failures'] == tools['errors'] == tools['skipped'] == 0
            and tools['passed'] is True and tools['evidence_kind'] == 'tool_unit_tests_only', 'tool_tests_incomplete')
    report = read(root / 'report.json')
    require(len(EXPECTED) == 39 and report['revisions'] == {'Ca': CA, 'P': P}, 'fixed_revisions_mismatch')
    require(report['passed'] is True and report['full_matrix'] is True and report['partial_verified'] is True
            and report['cleanup_passed'] is True and report['error_code'] is None and report['expected_cases'] == 39,
            'full_report_not_passed')
    selected = report['selected_cases']
    completed = report['completed_cases']
    require(len(selected) == 39 and set(selected) == EXPECTED and len(completed) == 39
            and {item['id'] for item in completed} == EXPECTED and all(item['passed'] is True for item in completed),
            'full_case_set_mismatch')
    run_id = report['run_id']
    require(re.fullmatch('[0-9a-f]{16}', run_id) is not None, 'run_id_invalid')
    binaries = sources(root, report, read(overlay_path))
    cases_dir = root / 'cases'
    require({path.name for path in cases_dir.iterdir()} == EXPECTED, 'case_directories_mismatch')
    normal_count, crash_count = 0, 0
    for case_id in sorted(EXPECTED):
        case_dir = child(cases_dir, case_id)
        seed = read(case_dir / 'seed.json')
        require(seed['run_id'] == run_id and seed['case_id'] == case_id, 'seed_identity_mismatch')
        mock = read(case_dir / 'mock-final.json')
        expected_delete = 0 if case_id.startswith('N1-') else 1
        require(mock['run_id'] == run_id and mock['case_id'] == case_id and mock['post'] == 1
                and mock['delete'] == expected_delete and mock['get'] >= 1 and mock['pending_deletes'] == [],
                'mock_method_counts_invalid')
        require(len(mock['requests']) == mock['seq'] and mock['seq'] <= 100, 'mock_request_inventory')
        for index, request in enumerate(mock['requests'], 1):
            require(request['request_seq'] == index, 'mock_request_sequence')
        for method in ('post', 'get', 'delete'):
            require(sum(item[method] for item in mock['by_instance'].values()) == mock[method], 'mock_per_process_counts')
        snapshots = read(case_dir / 'snapshots.json')
        require(snapshots and snapshots[0]['label'] == 'baseline', 'baseline_snapshot_missing')
        for entry in snapshots:
            path = child(case_dir, entry['file'])
            require(sha(path) == entry['sha256'], 'snapshot_hash_mismatch')
            data = read(path)
            require(data['run_id'] == run_id and data['case_id'] == case_id and data['ids']['task_id'] == seed['task_id']
                    and data['ids']['operation_id'] == seed['operation_id'], 'snapshot_identity_mismatch')
        processes = list((case_dir / 'processes').glob('*/process.json'))
        require(len(processes) >= 2, 'case_process_evidence_missing')
        require(len(processes) == len(list((case_dir / 'processes').iterdir())), 'unreported_process')
        killed = sum(process(case_dir, path, case_id, run_id, binaries) for path in processes)
        expected_killed = 1 if case_id.split('-')[0] in ('L2', 'N1', 'N2', 'N3', 'R1', 'R2') else 0
        require(killed == expected_killed, 'case_crash_count_mismatch')
        crash_count += killed
        normal_count += len(processes) - killed
    return dict(cases=39, normal_processes=normal_count, expected_sigkill_processes=crash_count,
                source_and_binary_hashes_verified=True, full_matrix=True)

if __name__ == '__main__':
    try:
        require(len(sys.argv) == 3, 'invalid_verifier_arguments')
        result = verify(Path(sys.argv[1]), Path(sys.argv[2]))
        print('A2C_STRICT_REPORT_VERIFIED ' + json.dumps(result, sort_keys=True), flush=True)
    except Exception:
        # Never print unexpected artifact content, credentials or raw paths.
        print('A2C_STRICT_REPORT_REJECTED', file=sys.stderr)
        raise SystemExit(1)
