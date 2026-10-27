"""Exact consumer selectors inside the existing ephemeral ark-mysql CI job.

Owns no database, container or credentials. Formal handoffs remain separate.
"""
from pathlib import Path
import argparse
import datetime
import hashlib
import json
import os
import re
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
LIB = ROOT / 'scripts/lib'
sys.path.insert(0, str(LIB))
from ark_s2_consumer_verify import verify


def load_catalog():
    return json.loads((LIB/'ark_s2_consumer_catalog.json').read_text(encoding='utf-8'))


def names(catalog, modes):
    return sorted({name for mode in modes for values in catalog['test_stages'][mode]['packages'].values() for name in values})


def pattern(values):
    assert values and all(re.fullmatch(r'Test[A-Za-z0-9_]+', n) for n in values)
    return '^('+'|'.join(sorted(set(values)))+')$'


def relocated(catalog, scope):
    video = names(catalog, ('s2_phase', 's2_generation', 's2_settlement'))
    # New dedicated binary adapters cannot pass from their ordinary skip paths.
    video += ['TestMySQLQueryPhaseBinaryHandoffProcess', 'TestMySQLQueryPhaseAdminHandoffProcess']
    if scope == 'video':
        return video
    if scope == 'race':
        return video + names(catalog, ('s2_initial',))
    assert scope == 'host'
    return names(catalog, ('s2_host',))


def execute(catalog, mode, output):
    stage = catalog['test_stages'][mode]
    if mode != 's2_pure' and not os.environ.get('ARK_TEST_MYSQL_DSN'):
        raise RuntimeError('Missing isolated MySQL test DSN; refusing skipped acceptance')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    assert re.fullmatch('[0-9a-f]{40}', head)
    subprocess.run(['git', 'diff', '--quiet', 'HEAD', '--'], cwd=ROOT, check=True)
    target = output / mode
    target.mkdir(parents=True, exist_ok=False)
    prefix = 'github.com/laiai/laiai-next/core/'
    assert all(p.startswith(prefix) for p in stage['packages'])
    argv = ['go', '-C', 'core', 'test', '-race', '-p', '1']
    argv += ['./'+p[len(prefix):] for p in stage['packages']]
    argv += ['-run', pattern(names(catalog, (mode,))), '-count='+str(stage['repeats']),
             '-timeout='+catalog['timeouts'][mode], '-json']
    record = {'stage': mode, 'commit': head, 'github_event_commit': os.environ.get('GITHUB_SHA'),
              'github_run_id': os.environ.get('GITHUB_RUN_ID'), 'github_run_attempt': os.environ.get('GITHUB_RUN_ATTEMPT'),
              'catalog_sha256': hashlib.sha256((LIB/'ark_s2_consumer_catalog.json').read_bytes()).hexdigest(),
              'argv': argv, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'passed': False, 'full_formal_acceptance': False}
    report = target/'result.json'
    report.write_text(json.dumps(record, indent=2)+'\n', encoding='utf-8')
    started = time.monotonic()
    try:
        raw = target/'go-test.jsonl'
        environment = os.environ.copy()
        if mode == 's2_pure':
            for key in ('LAIAI_DSN', 'LAIAI_TEST_MYSQL_DSN', 'ARK_TEST_MYSQL_DSN'):
                environment.pop(key, None)
        with raw.open('wb') as stream:
            result = subprocess.run(argv, cwd=ROOT, env=environment, stdout=stream, stderr=subprocess.STDOUT)
        record['exit_code'] = result.returncode
        record['raw_sha256'] = hashlib.sha256(raw.read_bytes()).hexdigest()
        if result.returncode:
            raise RuntimeError('Go tests failed; see retained JSONL')
        record['verification'] = verify(catalog, mode, raw.read_text(encoding='utf-8').splitlines())
        record['passed'] = True
    finally:
        record['elapsed_seconds'] = round(time.monotonic()-started, 3)
        record['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        report.write_text(json.dumps(record, indent=2)+'\n', encoding='utf-8')
    print(json.dumps(record, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--skip', choices=('video', 'race', 'host'))
    group.add_argument('--stage')
    args = parser.parse_args()
    catalog = load_catalog()
    if args.skip:
        print(pattern(relocated(catalog, args.skip)))
        return
    assert args.stage in catalog['test_stages']
    output = os.environ.get('ARK_S2_CONSUMER_REPORT_DIR')
    if not output:
        raise RuntimeError('ARK_S2_CONSUMER_REPORT_DIR is required')
    execute(catalog, args.stage, Path(output).resolve())


if __name__ == '__main__':
    main()
