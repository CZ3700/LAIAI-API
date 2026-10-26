#!/usr/bin/env python3
"""Run the mutation runner's Linux unit suites; these are not binary handoff proof."""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys
import unittest

EXPECTED = {
    'test_ark_mutation_process.py': 18,
    'test_ark_mutation_compat.py': 20,
    'test_ark_mutation_scenarios.py': 16,
}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args(argv)
    if os.name != 'posix':
        parser.error('Linux is required; skipped platform checks cannot pass this gate.')
    output = args.output.absolute()
    if output.exists() or output.is_symlink() or output.resolve() != output:
        parser.error('Output must be a new file without symlink ancestors.')
    output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    suite = unittest.TestSuite()
    inventory = {}
    for name, expected in EXPECTED.items():
        spec = importlib.util.spec_from_file_location(name[:-3], Path(__file__).parent / name)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        tests = unittest.defaultTestLoader.loadTestsFromModule(module)
        inventory[name] = tests.countTestCases()
        if inventory[name] != expected:
            raise SystemExit('Tool-test inventory changed: ' + name)
        suite.addTests(tests)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    passed = (result.wasSuccessful() and not result.skipped and not result.expectedFailures
              and not result.unexpectedSuccesses and result.testsRun == sum(EXPECTED.values()))
    report = dict(inventory=inventory, executed=result.testsRun, failures=len(result.failures),
                  errors=len(result.errors), skipped=len(result.skipped), passed=bool(passed),
                  evidence_kind='tool_unit_tests_only')
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, 'w', encoding='utf-8') as stream:
        json.dump(report, stream, indent=2)
        stream.write('\n')
    if passed:
        print('A2C_TOOL_TESTS_VERIFIED', result.testsRun, flush=True)
    return 0 if passed else 1


if __name__ == '__main__':
    raise SystemExit(main())
