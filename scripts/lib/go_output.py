"""Reassemble Go Output by package/test/execution; never join distinct rounds."""
from collections import defaultdict


class OutputLines:
    def __init__(self):
        self.rounds = defaultdict(int)
        self.buffers = {}
        self.lines = []

    def feed(self, event):
        package, test = event.get('Package'), event.get('Test')
        assert isinstance(package, str) and package
        identity = package, test
        action = event['Action']
        if action == 'run' and test:
            old = (*identity, self.rounds[identity])
            assert not self.buffers.get(old), 'Unfinished output from previous test execution'
            self.rounds[identity] += 1
        key = (*identity, self.rounds[identity])
        if action == 'output':
            text = event.get('Output')
            assert isinstance(text, str)
            buffer = self.buffers.get(key, '') + text
            assert len(buffer) <= 2_097_152, 'Unbounded single diagnostic output line'
            chunks = buffer.split('\n')
            self.buffers[key] = chunks.pop()
            for line in chunks:
                self.lines.append((key, line))
                assert 'DATA RACE' not in line, 'Race detected in reconstructed output'
        if action in ('pass', 'fail', 'skip'):
            assert not self.buffers.get(key), 'Terminal event has unfinished Output line'
        return key

    def finish(self):
        assert not any(self.buffers.values()), 'Truncated Output stream'
        return self.lines
