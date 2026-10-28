"""Static selector and synthetic verifier tests; no Go/DB/remote execution."""
from pathlib import Path
import copy
import importlib.util
import json
import os
import re
import subprocess
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[2]
C = HERE
OUT = HERE
BASH = 'bash'
spec = importlib.util.spec_from_file_location('consumer_ci', OUT/'scripts/ark-s2-consumer-ci.py')
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)
CATALOG = ci.load_catalog()


def events(mode):
    stage = CATALOG['test_stages'][mode]
    result = []
    for package, tops in stage['packages'].items():
        for _ in range(stage['repeats']):
            for top in tops:
                tests = [top] + [top+'/'+c for c in stage['exact_children'][package][top]]
                for test in tests:
                    result += [{'Action': a, 'Package': package, 'Test': test} for a in ('run', 'pass')]
        result.append({'Action': 'pass', 'Package': package})
    return result


def verify(mode, values):
    return ci.verify(CATALOG, mode, [json.dumps(e) for e in values])


class Tests(unittest.TestCase):
    def test_all_seven_exact_catalogs_match_current_source(self):
        for mode, stage in CATALOG['test_stages'].items():
            regex = re.compile(ci.pattern(ci.names(CATALOG, (mode,))))
            for pkg, expected in stage['packages'].items():
                directory = C/'core'/pkg.split('/core/')[1]
                tests = {n for path in directory.glob('*_test.go') for n in re.findall(r'^func (Test\w+)\(t \*testing.T\)', path.read_text(encoding='utf-8'), re.M)}
                self.assertEqual(set(expected), {n for n in tests if regex.fullmatch(n)}, (mode, pkg))
                self.assertTrue(all('HandoffProcess' not in n for n in expected))

    def test_relocated_sets_cover_exactly_new_tests_and_formal_exclusions(self):
        formal = {'TestMySQLQueryPhaseBinaryHandoffProcess', 'TestMySQLQueryPhaseAdminHandoffProcess'}
        video = set(ci.names(CATALOG, ('s2_phase','s2_generation','s2_settlement')))
        initial = set(ci.names(CATALOG, ('s2_initial',)))
        host = set(ci.names(CATALOG, ('s2_host',)))
        self.assertEqual(set(ci.relocated(CATALOG, 'video'))-formal, video)
        self.assertEqual(set(ci.relocated(CATALOG, 'race'))-formal, video|initial)
        self.assertEqual(set(ci.relocated(CATALOG, 'host')), host)
        for scope in ('video','race','host'):
            regex = re.compile(ci.pattern(ci.relocated(CATALOG, scope)))
            for name in ci.relocated(CATALOG, scope):
                self.assertIsNotNone(regex.fullmatch(name))
                self.assertIsNone(regex.fullmatch(name+'Future'))
                self.assertIsNone(regex.fullmatch('Prefix'+name))

    def test_original_jobs_and_budgets_preserved(self):
        old = (C/'.github/workflows/ci.yml').read_text(encoding='utf-8')
        new = (OUT/'.github/workflows/ci.yml').read_text(encoding='utf-8')
        jobs = lambda text: re.findall(r'^  ([a-z][a-z0-9-]+):$', text.split('\njobs:\n',1)[1], re.M)
        expected = ['ci','web','ark-mysql','ark-mock-baseline','ark-runtime-compat','ark-mutation-compat','ark-execution-input-compat','ark-query-recovery-compat','release-tools','redline']
        self.assertEqual(expected, [j for j in jobs(new) if j != 'exact-head-ci'])
        self.assertEqual(jobs(new).count('exact-head-ci'), 1)
        self.assertIn('python3 scripts/tests/test_ark_s2_consumer_ci.py', new)
        self.assertNotIn('-skip \"$(python3', new)
        for scope in ('video', 'race', 'host'):
            self.assertIn('skip_pattern=$(python3 scripts/ark-s2-consumer-ci.py --skip '+scope+')\n', new)
        for command in ["-count=1 -timeout=10m", "-count=1 -timeout=20m", "-count=2 -timeout=6m -v"]:
            self.assertIn(command, new)
        self.assertEqual(CATALOG['timeouts'], {'s2_pure':'10m','s2_migrations':'15m','s2_initial':'15m','s2_phase':'10m','s2_generation':'25m','s2_settlement':'25m','s2_host':'5m'})
        self.assertIn("'^TestMySQLVideoQueryRecoveryMigrationPreservesAndReplays$' -count=2 -timeout=5m -v", new)

    def test_new_migration_exact_two_and_guard_exact_thirteen(self):
        stage = CATALOG['test_stages']['s2_migrations']
        tops = ci.names(CATALOG, ('s2_migrations',))
        self.assertEqual(tops, ['TestMySQLVideoQuerySettlementMigrationPreservesAndReplays', 'TestMySQLVideoQuerySettlementShapeMigrationGuard'])
        children = next(iter(stage['exact_children'].values()))['TestMySQLVideoQuerySettlementShapeMigrationGuard']
        self.assertEqual(len(children), 13)
        self.assertEqual(stage['repeats'], 2)

    def test_synthetic_complete_accepts_all(self):
        for mode in CATALOG['test_stages']:
            self.assertTrue(verify(mode, events(mode))['passed'])

    def test_skip_fail_race_missing_child_duplicate_reject(self):
        mode = 's2_migrations'
        base = events(mode)
        cases = []
        for action in ('skip', 'fail', 'build-fail'):
            values = copy.deepcopy(base)
            values[1]['Action'] = action
            cases.append(values)
        cases += [base[:-1], base+base[:2]]
        missing = copy.deepcopy(base)
        child = next(v['Test'] for v in missing if '/' in v.get('Test',''))
        cases.append([v for v in missing if v.get('Test') != child])
        bad = copy.deepcopy(base)
        bad.insert(1, {'Action':'output','Package':base[0]['Package'],'Test':base[0]['Test'],'Output':'WARNING: DATA RACE\n'})
        cases.append(bad)
        for values in cases:
            with self.assertRaises(AssertionError):
                verify(mode, values)

    def test_missing_database_refuses_before_any_process(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(ci.subprocess, 'check_output') as command:
            with self.assertRaises(RuntimeError):
                ci.execute(CATALOG, 's2_migrations', HERE/'never-created')
            command.assert_not_called()

    def test_selector_failure_cannot_be_hidden_by_go_success(self):
        for scope in ('video', 'race', 'host'):
            failed = 'python3() { return 23; }; go() { printf UNEXPECTED_GO_EXECUTED; return 0; };\n'
            failed += 'skip_pattern=$(python3 scripts/ark-s2-consumer-ci.py --skip '+scope+')\n'
            failed += 'go -skip "$skip_pattern"\n'
            result = subprocess.run([BASH, '-e', '-c', failed], capture_output=True, text=True)
            self.assertEqual(result.returncode, 23)
            self.assertNotIn('UNEXPECTED_GO_EXECUTED', result.stdout)


if __name__ == '__main__':
    unittest.main(verbosity=2)
