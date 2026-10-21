#!/usr/bin/env python3
"""Private release state. Never persist raw docker inspect/config or environment values."""
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import time


def fail(message):
    raise SystemExit(message)


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    target = Path(path)
    temporary = target.with_suffix(target.suffix + '.tmp')
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    temporary.chmod(0o600)
    temporary.replace(target)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def protected_keys(role, old):
    keys = {'mysql': ['MYSQL_ROOT_PASSWORD', 'MYSQL_DATABASE', 'MYSQL_USER', 'MYSQL_PASSWORD'],
            'redis': ['REDIS_PASSWORD'],
            'core': ['LAIAI_DSN', 'LAIAI_AUTH_SECRET', 'LAIAI_DATA_KEYS', 'LAIAI_DATA_KEY_ACTIVE',
                     'LAIAI_REDIS_ADDR', 'LAIAI_REDIS_PASSWORD'],
            'caddy': ['LAIAI_DOMAIN']}[role]
    # First installation may introduce this key; later releases must preserve it.
    if role == 'core' and old['env_hashes'].get('LAIAI_VIDEO_HMAC_SECRET') not in (
            None, hashlib.sha256(b'').hexdigest()):
        keys.append('LAIAI_VIDEO_HMAC_SECRET')
    return keys


def config_hash():
    keys = ['LIVE_DIR', 'STATE_DIR', 'SOURCE_REPOSITORY', 'COMPOSE_PROJECT', 'LIVE_ENV',
            'VIDEO_ENV', 'CADDYFILE', 'PUBLIC_ORIGIN', 'BACKUP_KEY_FILE', 'GO_IMAGE',
            'ALPINE_IMAGE', 'NODE_IMAGE', 'CADDY_BASE_IMAGE', 'MIN_FREE_MB',
            'MIN_AVAILABLE_MB', 'CI_CHECK_HOOK', 'CHECK_HOOK', 'HOOK_TIMEOUT']
    values = {key: os.environ.get(key, '') for key in keys}
    paths = [values[key] for key in ['LIVE_ENV', 'VIDEO_ENV', 'CADDYFILE', 'BACKUP_KEY_FILE',
                                    'CI_CHECK_HOOK', 'CHECK_HOOK'] if values[key]]
    paths += [str(p) for p in Path(os.environ['TOOL_DIR']).rglob('*') if p.is_file() and p.suffix in ('.sh', '.py', '.sql')]
    paths += [str(Path(values['LIVE_DIR']) / 'infra' / p)
              for p in ['docker-compose.yml', 'mysql/my.cnf', 'redis/redis.conf']]
    values['files'] = {p: hashlib.sha256(Path(p).read_bytes()).hexdigest() for p in paths}
    return digest(values)


def inspect_snapshot():
    services = {}
    for item in json.load(sys.stdin):
        role = item['Config']['Labels']['com.docker.compose.service']
        if role in services:
            fail('exactly one container per service required')
        services[role] = {
            'id': item['Id'], 'image': item['Image'],
            'revision': item['Config']['Labels'].get('org.opencontainers.image.revision', ''),
            'running': item['State']['Running'],
            'health': item['State'].get('Health', {}).get('Status', ''),
            'mounts': sorted([{'type': m['Type'], 'source': m.get('Name', m['Source']),
                               'destination': m['Destination'], 'rw': m['RW']}
                              for m in item['Mounts']], key=lambda m: m['destination']),
            'runtime_hash': digest({'config': item['Config'], 'host': item['HostConfig']}),
            'env_hashes': {e.split('=', 1)[0]: hashlib.sha256(e.split('=', 1)[1].encode()).hexdigest()
                           for e in item['Config'].get('Env', []) if '=' in e},
        }
    if set(services) != {'core', 'caddy', 'mysql', 'redis'}:
        fail('missing or unexpected service')
    for role, item in services.items():
        if role in ('core', 'caddy') and sys.argv[3:] == ['allow-stopped-apps']:
            continue
        if not item['running'] or (role != 'caddy' and item['health'] != 'healthy'):
            fail('unhealthy service: ' + role)
    write(sys.argv[2], services)


def compose_overlay():
    plan, images, target = read(sys.argv[2]), read(sys.argv[3]), sys.argv[4]
    rollback = sys.argv[5] == 'old'
    services = {}
    for role in ['mysql', 'redis', 'core', 'caddy']:
        image = plan['snapshot'][role]['image'] if rollback or role in ('mysql', 'redis') else images[role]
        services[role] = {'image': image, 'pull_policy': 'never'}
    services['caddy'].update({
        # Keep the existing mount target; /dev/stdin avoids Caddy's automatic
        # Caddyfile adapter selection based on that filename for this JSON file.
        'command': ['sh', '-ec', 'exec caddy run --config /dev/stdin < /etc/caddy/Caddyfile'],
        'volumes': [{'type': 'bind', 'source': str(Path(target).parent / 'config/active.json'),
                     'target': '/etc/caddy/Caddyfile', 'read_only': True}],
    })
    # Remove build contexts from the runtime model, including compose run where
    # --no-build is unavailable. !reset is supported by the required Compose v2.24.4+.
    lines = ['services:']
    for role, service in services.items():
        lines.append(f'  {role}:')
        if role in ('core', 'caddy'):
            lines.append('    build: !reset null')
        for key, value in service.items():
            lines.append(f'    {key}: ' + json.dumps(value))
    Path(target).write_text('\n'.join(lines) + '\n')
    Path(target).chmod(0o600)


def check_compose():
    # Compose config serializes literal dollar signs escaped for round-tripping.
    def runtime(value):
        if isinstance(value, str): return value.replace('$$', '$')
        if isinstance(value, list): return [runtime(v) for v in value]
        if isinstance(value, dict): return {k: runtime(v) for k, v in value.items()}
        return value
    plan = read(sys.argv[2]); config = runtime(json.load(sys.stdin))
    if config.get('name') != os.environ['COMPOSE_PROJECT']:
        fail('Compose project changed')
    for role in ['mysql', 'redis', 'core', 'caddy']:
        service = config['services'][role]
        if service.get('pull_policy') != 'never' or not re.fullmatch(r'sha256:[0-9a-f]{64}', service['image']):
            fail('unlocked image: ' + role)
        if service.get('build'):
            fail('runtime Compose still contains a build context: ' + role)
        old = plan['snapshot'][role]
        mounts = []
        for mount in service.get('volumes', []):
            source = mount['source']
            if mount['type'] == 'volume':
                source = config['volumes'][source]['name']
            mounts.append({'type': mount['type'], 'source': source, 'destination': mount['target'],
                           'rw': not mount.get('read_only', False)})
        # Only Caddy configuration is intentionally replaced; all data mounts stay exact.
        keep = lambda m: not (role == 'caddy' and m['destination'] == '/etc/caddy/Caddyfile')
        if sorted(filter(keep, mounts), key=lambda m: m['destination']) != list(filter(keep, old['mounts'])):
            fail('mount mapping changed: ' + role)
        env = service.get('environment', {})
        for key in protected_keys(role, old):
            value = '' if env.get(key) is None else str(env[key])
            if hashlib.sha256(value.encode()).hexdigest() != old['env_hashes'].get(key):
                fail('existing runtime configuration changed: ' + role + '/' + key)
        if role == 'core' and sys.argv[3] != 'old' and len(str(env.get('LAIAI_VIDEO_HMAC_SECRET') or '')) < 32:
            fail('stable LAIAI_VIDEO_HMAC_SECRET >=32 characters required')


def maintenance():
    value = read(sys.argv[2])
    servers = value.get('apps', {}).get('http', {}).get('servers', {})
    if not servers:
        fail('no Caddy HTTP server found')
    rule = {'match': [{'not': [{'path': ['/healthz']}]}], 'handle': [{
        'handler': 'static_response', 'status_code': 503, 'body': 'Maintenance in progress\n',
        'headers': {'Retry-After': ['60'], 'Cache-Control': ['no-store']}}], 'terminal': True}
    for server in servers.values():
        server['routes'] = [rule] + server.get('routes', [])
    write(sys.argv[3], value)


def github_ci():
    runs = [r for r in json.load(sys.stdin).get('workflow_runs', [])
            if r.get('head_sha') == sys.argv[2] and r.get('head_branch') == 'main'
            and r.get('event') == 'push' and r.get('path') == '.github/workflows/ci.yml']
    if not runs:
        fail('no main CI run for the exact commit')
    run = max(runs, key=lambda r: r['id'])
    if run['status'] != 'completed' or run['conclusion'] != 'success':
        fail('latest main CI is not successful')
    print(run['html_url'])


def approval(create):
    plan_path, stage = Path(sys.argv[2]), sys.argv[3]
    plan = read(plan_path)
    if stage not in ('before-maintenance', 'before-open', 'before-rollback'):
        fail('invalid approval stage')
    directory = plan_path.parent / 'approvals'
    record = directory / (stage + '.json')
    if create:
        report = Path(sys.argv[4])
        if stage == 'before-open' and not (plan_path.parent / 'deployed.ok').exists():
            fail('acceptance evidence must be recorded after the candidate is deployed')
        if stage == 'before-rollback' and not (plan_path.parent / 'migration-started.ok').exists():
            fail('no post-migration compatibility approval is needed yet')
        if not report.is_absolute() or not report.is_file() or report.is_symlink() or report.stat().st_size < 20:
            fail('a substantive existing acceptance report is required')
        directory.mkdir(mode=0o700, exist_ok=True)
        write(record, {'commit': plan['commit'], 'stage': stage, 'created_at': int(time.time()),
                       'evidence_file': str(report), 'evidence_sha256': hashlib.sha256(report.read_bytes()).hexdigest()})
    else:
        value = read(record)
        age = time.time() - value['created_at']
        report = Path(value['evidence_file'])
        if value['commit'] != plan['commit'] or value['stage'] != stage or not 0 <= age <= 86400:
            fail('approval is expired or belongs to another commit/stage')
        if report.is_symlink() or not report.is_file() or hashlib.sha256(report.read_bytes()).hexdigest() != value['evidence_sha256']:
            fail('approval evidence changed')


def running():
    plan, actual, images = read(sys.argv[2]), read(sys.argv[3]), read(sys.argv[4])
    for role in ('core', 'caddy', 'mysql', 'redis'):
        old, current = plan['snapshot'][role], actual[role]
        expected = images[role] if role in images else old['image']
        if current['image'] != expected:
            fail('unexpected running image: ' + role)
        if role in ('mysql', 'redis') and old != current:
            fail('data service/container/configuration changed: ' + role)
        keep = lambda m: not (role == 'caddy' and m['destination'] == '/etc/caddy/Caddyfile')
        if list(filter(keep, old['mounts'])) != list(filter(keep, current['mounts'])):
            fail('running data mounts changed: ' + role)
        for key in protected_keys(role, old):
            if old['env_hashes'].get(key) != current['env_hashes'].get(key):
                fail('running protected configuration changed: ' + role + '/' + key)


def rollback_guard():
    plan, actual, images = read(sys.argv[2]), read(sys.argv[3]), read(sys.argv[4])
    for role in ('core', 'caddy', 'mysql', 'redis'):
        old, current = plan['snapshot'][role], actual[role]
        if current['image'] not in (old['image'], images.get(role, old['image'])):
            fail('running service belongs to another release: ' + role)
        if role in ('mysql', 'redis') and old != current:
            fail('data service changed since this release: ' + role)
        keep = lambda m: not (role == 'caddy' and m['destination'] == '/etc/caddy/Caddyfile')
        if list(filter(keep, old['mounts'])) != list(filter(keep, current['mounts'])):
            fail('running data mounts changed: ' + role)
    configs = [digest(read(Path(sys.argv[2]).parent / 'config' / name))
               for name in ('original.json', 'maintenance.json')]
    current, old = actual['caddy'], plan['snapshot']['caddy']
    if current['running']:
        if current.get('active_config_hash') not in configs:
            fail('active Caddy configuration was changed outside this release')
        return
    # Offline recovery is limited to this release's known configuration bind, or
    # the unchanged original container. Do not treat an unavailable admin API on
    # a running/externally reconfigured Caddy as permission to replace it.
    mounts = [m for m in current['mounts'] if m['destination'] == '/etc/caddy/Caddyfile']
    active = Path(sys.argv[2]).parent / 'config' / 'active.json'
    expected = {'type': 'bind', 'source': str(active),
                'destination': '/etc/caddy/Caddyfile', 'rw': False}
    if mounts == [expected]:
        if active.is_symlink() or digest(read(active)) not in configs:
            fail('stopped Caddy configuration was changed outside this release')
        return
    if (current['id'] == old['id'] and current['runtime_hash'] == old['runtime_hash']
            and current['mounts'] == old['mounts'] and len(mounts) == 1
            and mounts[0]['type'] == 'bind' and not mounts[0]['rw']):
        source = Path(mounts[0]['source'])
        # verify_config already pins the operator's original Caddyfile bytes.
        if str(source) == os.environ.get('CADDYFILE'):
            return
        # A prior release may have been running a different saved JSON bind.
        if not source.is_symlink() and digest(read(source)) in configs:
            return
    fail('stopped Caddy is not an unchanged original or this release configuration')


def main():
    command = sys.argv[1]
    if command == 'inspect': inspect_snapshot()
    elif command == 'config-hash': print(config_hash())
    elif command == 'plan':
        snapshot = read(sys.argv[3]); sha = sys.argv[4]
        if not re.fullmatch('[0-9a-f]{40}', sha): fail('invalid main SHA')
        write(sys.argv[2], {'version': 1, 'commit': sha, 'config_hash': config_hash(), 'snapshot': snapshot})
    elif command == 'get':
        value = read(sys.argv[2])
        for key in sys.argv[3].split('.'): value = value[key]
        if not isinstance(value, (str, int)): fail('not a scalar')
        print(value)
    elif command == 'same':
        if read(sys.argv[2]) != read(sys.argv[3]): fail('snapshot changed; create a fresh plan')
    elif command == 'compose': compose_overlay()
    elif command == 'check-compose': check_compose()
    elif command == 'maintenance': maintenance()
    elif command == 'ci': github_ci()
    elif command == 'approve': approval(True)
    elif command == 'approval-check': approval(False)
    elif command == 'running': running()
    elif command == 'rollback-guard': rollback_guard()
    elif command == 'compose-version':
        version = re.fullmatch(r'v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?', sys.argv[2])
        if not version or tuple(map(int, version.groups())) < (2, 24, 4):
            fail('Docker Compose >=2.24.4 is required')
    elif command == 'caddy-hash':
        value = read(sys.argv[2])
        value['caddy']['active_config_hash'] = digest(json.load(sys.stdin))
        write(sys.argv[2], value)
    elif command == 'caddy-match':
        if read(sys.argv[2])['caddy']['active_config_hash'] != digest(read(sys.argv[3])):
            fail('active Caddy configuration does not match this release phase')
    elif command == 'schema':
        applied = set(Path(sys.argv[2]).read_text().splitlines())
        wanted = {p.stem for p in Path(sys.argv[3]).glob('[0-9][0-9][0-9][0-9]_*.sql')}
        unknown = applied - wanted
        if unknown: fail('database has migrations absent from this commit')
        if sys.argv[4] == 'exact' and applied != wanted: fail('pending migrations remain')
        print(f'applied={len(applied)} target={len(wanted)} pending={len(wanted - applied)}')
    elif command == 'images':
        images = {'core': sys.argv[3], 'caddy': sys.argv[4]}
        if not all(re.fullmatch(r'sha256:[0-9a-f]{64}', v) for v in images.values()): fail('invalid image ID')
        write(sys.argv[2], images)
    elif command == 'funds':
        before, after = read(sys.argv[2]), read(sys.argv[3])
        if before['funds_fingerprint'] != after['funds_fingerprint']:
            fail('funds facts changed while writes were stopped')
    else: fail('unknown state operation')


if __name__ == '__main__':
    try: main()
    except (KeyError, ValueError, OSError, TypeError): fail('invalid or incomplete private release state')
