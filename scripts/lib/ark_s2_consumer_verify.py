"""Strict go test -json evidence: declared tests and exact catalog children."""
from collections import Counter
from pathlib import Path
import json
import sys
from go_output import OutputLines


def verify(metadata, mode, lines):
    stage = metadata['test_stages'][mode]
    expected = {(p, t) for p, names in stage['packages'].items() for t in names}
    repeats = stage['repeats']
    assert expected and repeats > 0
    run, passed, package_passes = Counter(), Counter(), Counter()
    output = OutputLines()
    exact_children = stage.get('exact_children', {})
    exact_events = set(expected)
    leaves = set()
    for package, tops in exact_children.items():
        assert set(tops) <= set(stage['packages'][package])
        for top, children in tops.items():
            assert len(children) == len(set(children))
            full = {(package, top + '/' + child) for child in children}
            exact_events.update(full)
            leaves.update(full or {(package, top)})
    for line in lines:
        event = json.loads(line)
        action, test, package = event['Action'], event.get('Test'), event.get('Package')
        assert package in stage['packages'], 'Unexpected package'
        output.feed(event)
        assert 'DATA RACE' not in event.get('Output', ''), 'Race detected'
        assert action not in ('fail', 'skip', 'build-fail'), (action, package, test)
        if test:
            top = test.split('/')[0]
            assert (package, top) in expected, ('Unexpected top', test)
            if top in exact_children.get(package, {}):
                assert (package, test) in exact_events, ('Unexpected exact child', test)
            if action == 'run':
                run[(package, test)] += 1
            elif action == 'pass':
                passed[(package, test)] += 1
        elif action == 'pass':
            package_passes[package] += 1
    assert package_passes == Counter({p: 1 for p in stage['packages']}), 'Missing package completion'
    for key in exact_events:
        assert run[key] == passed[key] == repeats, ('Missing/repeated execution', key, run[key], passed[key])
    assert run == passed, 'Not all started tests passed'
    output.finish()
    assert all(n == repeats for n in run.values()), 'Unexpected subtest repetition'
    return {'stage': mode, 'top_level_tests': len(expected), 'top_level_executions': len(expected) * repeats,
            'subtest_executions': sum(n for (_, test), n in passed.items() if '/' in test),
            'exact_leaf_tests': len(leaves), 'exact_leaf_executions': len(leaves) * repeats,
            'failed': 0, 'skipped': 0, 'passed': True}


if __name__ == '__main__':
    metadata = json.loads(Path(sys.argv[1]).read_text(encoding='utf-8'))
    print(json.dumps(verify(metadata, sys.argv[2], Path(sys.argv[3]).read_text(encoding='utf-8').splitlines()), indent=2))
