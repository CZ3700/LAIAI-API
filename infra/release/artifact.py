#!/usr/bin/env python3
"""Registry release contract. Tags locate releases; digests identify tested bytes."""
import hashlib
import ipaddress
import json
import re
import sys
from pathlib import Path

REPOSITORY = 'https://github.com/CZ3700/LAIAI-API.git'
ROLES = ('core', 'caddy')
CHECKS = (
    'health', 'frontend', 'migration_replay', 'schema_doctor', 'registration_login',
    'balance', 'video_key_consent', 'key_revoke', 'tenant_isolation',
)
DIGEST = r'sha256:[0-9a-f]{64}'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def matches(pattern, value):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def object_value(value, message):
    require(isinstance(value, dict), message)
    return value


def read_json(source):
    """Reject ambiguous JSON rather than silently accepting the last duplicate key."""
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'duplicate JSON field')
            result[key] = value
        return result

    def constant(_):
        raise ValueError('non-finite JSON number')

    return json.load(source, object_pairs_hook=pairs, parse_constant=constant)


def tag(branch, commit):
    require(matches('[0-9a-f]{40}', commit), 'full commit required')
    require(matches(r'[A-Za-z0-9][A-Za-z0-9/_.-]{0,180}', branch) and '..' not in branch, 'invalid branch')
    slug = re.sub('[^a-z0-9_.-]', '-', branch.lower())[:60]
    if branch != 'main':
        slug += '-' + hashlib.sha256(branch.encode()).hexdigest()[:8]
    return slug + '-' + commit[:12]


def validate(value, commit=None, production=False):
    object_value(value, 'artifact must be an object')
    require(type(value.get('schema')) is int and value['schema'] == 1 and value.get('repository') == REPOSITORY, 'wrong artifact schema or repository')
    require(value.get('tag') == tag(value.get('branch', ''), value.get('commit', '')), 'tag/commit mismatch')
    require(commit is None or (matches('[0-9a-f]{40}', commit) and value['commit'] == commit), 'artifact does not match pinned main')
    require(not production or value['branch'] == 'main', 'production requires main artifact')
    require(value.get('platform') == 'linux/amd64', 'unsupported platform')
    images = object_value(value.get('images'), 'images must be an object')
    require(set(images) == set(ROLES), 'both application images required')
    for role in ROLES:
        entry = object_value(images[role], 'image entry must be an object')
        require(entry.get('repository') == 'laiai/' + role, 'unexpected image repository')
        require(matches(DIGEST, entry.get('digest')), 'immutable registry digest required')
    require(images['core']['digest'] != images['caddy']['digest'], 'application images must be distinct')
    report = object_value(value.get('acceptance'), 'acceptance must be an object')
    require(report.get('passed') is True and report.get('commit') == value['commit'], 'missing exact-commit acceptance')
    require(report.get('images') == value['images'], 'acceptance images differ from release')
    require(type(report.get('paid_calls')) is int and report['paid_calls'] == 0 and report.get('isolated_database') is True, 'invalid staging acceptance')
    checks = object_value(report.get('checks'), 'acceptance checks must be an object')
    require(set(CHECKS).issubset(checks) and all(v is True for v in checks.values()), 'incomplete staging acceptance')
    expected = hashlib.sha256(json.dumps(report, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    require(value.get('acceptance_sha256') == expected, 'acceptance hash mismatch')
    return value


def inspect_image(value, commit, reference):
    require(isinstance(value, list) and len(value) == 1, 'ambiguous image')
    require(matches('[0-9a-f]{40}', commit), 'full commit required')
    require(isinstance(reference, str) and '@' in reference and matches(DIGEST, reference.rsplit('@', 1)[1]), 'digest reference required')
    item = object_value(value[0], 'image inspection must be an object')
    require(item.get('Os') == 'linux' and item.get('Architecture') == 'amd64', 'wrong image platform')
    labels = object_value(object_value(item.get('Config'), 'image config must be an object').get('Labels'), 'image labels must be an object')
    require(labels.get('org.opencontainers.image.revision') == commit, 'wrong image revision')
    digests = item.get('RepoDigests')
    require(isinstance(digests, list) and all(isinstance(d, str) for d in digests) and reference in digests, 'pulled image digest not verified')
    require(matches(DIGEST, item.get('Id')), 'invalid local image identity')
    return item['Id']


def reference(value, registry, role):
    require(role in ROLES, 'unexpected image role')
    require(isinstance(registry, str), 'invalid registry authority')
    authority = re.fullmatch(r'(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9][A-Za-z0-9.-]*)(?::([0-9]{1,5}))?', registry)
    require(authority is not None, 'invalid registry authority')
    host, port = authority.groups()
    if host.startswith('['):
        try:
            ipaddress.IPv6Address(host[1:-1])
        except ValueError:
            raise ValueError('invalid registry authority') from None
    else:
        require(len(host) <= 253 and all(matches(r'[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?', label) for label in host.split('.')), 'invalid registry authority')
    require(port is None or 1 <= int(port) <= 65535, 'invalid registry port')
    return registry + '/' + value['images'][role]['repository'] + '@' + value['images'][role]['digest']


if __name__ == '__main__':
    try:
        command = sys.argv[1]
        if command == 'validate':
            with Path(sys.argv[2]).open(encoding='utf-8') as source:
                value = validate(read_json(source), sys.argv[3], production=True)
            print(value['commit'])
        elif command == 'reference':
            with Path(sys.argv[2]).open(encoding='utf-8') as source:
                value = validate(read_json(source), sys.argv[3], production=True)
            registry, role = sys.argv[4:6]
            print(reference(value, registry, role))
        elif command == 'inspect':
            print(inspect_image(read_json(sys.stdin), sys.argv[2], sys.argv[3]))
        else:
            raise ValueError('unknown artifact command')
    except (ValueError, KeyError, TypeError, IndexError, OSError) as error:
        print('Artifact validation failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
